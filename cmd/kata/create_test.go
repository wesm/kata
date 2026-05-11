package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wesm/kata/internal/config"
)

func TestCreate_PrintsIssueShortIDInQuietMode(t *testing.T) {
	env, dir := setupCLIEnv(t)
	out := runCLI(t, env, dir, "--quiet", "create", "first issue", "--body", "details")
	// Quiet mode emits the new issue's short_id as the only output.
	assert.NotEmpty(t, out)
	assert.NotContains(t, out, "\n", "quiet mode must emit a single line")
}

func TestCreate_WithInitialLabelsAndParent(t *testing.T) {
	env, dir := setupCLIEnv(t)
	pid := resolvePIDViaHTTP(t, env.URL, dir)
	parent := createIssue(t, env, pid, "parent-issue")
	blocker := createIssue(t, env, pid, "blocker")

	out := runCLI(t, env, dir, "create", "child",
		"--label", "bug", "--label", "needs-review",
		"--parent", parent,
		"--blocks", blocker,
		"--owner", "alice",
	)
	assert.Contains(t, out, "child")

	// Decode the created issue's short_id from the create response so we
	// can fetch and assert on its persisted state.
	type createResp struct {
		Issue struct {
			ShortID string `json:"short_id"`
		} `json:"issue"`
	}
	jsonOut := runCLI(t, env, dir, "--json", "create", "child2",
		"--label", "bug",
		"--parent", parent,
		"--blocks", blocker,
		"--owner", "alice",
	)
	var resp createResp
	require.NoError(t, json.Unmarshal([]byte(jsonOut), &resp))

	b := fetchIssueViaHTTP(t, env, pid, resp.Issue.ShortID)
	require.NotNil(t, b.Issue.Owner)
	assert.Equal(t, "alice", *b.Issue.Owner)

	gotLabels := make([]string, 0, len(b.Labels))
	for _, l := range b.Labels {
		gotLabels = append(gotLabels, l.Label)
	}
	assert.Contains(t, gotLabels, "bug")

	var sawParent, sawBlocks bool
	for _, l := range b.Links {
		switch l.Type {
		case "parent":
			if l.From.ShortID == resp.Issue.ShortID && l.To.ShortID == parent {
				sawParent = true
			}
		case "blocks":
			if l.From.ShortID == resp.Issue.ShortID && l.To.ShortID == blocker {
				sawBlocks = true
			}
		}
	}
	assert.True(t, sawParent, "parent link from new issue to parent must be persisted")
	assert.True(t, sawBlocks, "blocks link from new issue to blocker must be persisted")
}

// TestCreate_WithBlockedByAndRelated covers the new repeatable link flags
// added by the relationship-flag consolidation. `--blocked-by R` records
// "this new issue is blocked by R" — i.e. the link runs FROM R TO the new
// issue. `--related R` records the symmetric tie.
func TestCreate_WithBlockedByAndRelated(t *testing.T) {
	env, dir := setupCLIEnv(t)
	pid := resolvePIDViaHTTP(t, env.URL, dir)
	blocker := createIssue(t, env, pid, "blocker")
	peer := createIssue(t, env, pid, "peer")

	type createResp struct {
		Issue struct {
			ShortID string `json:"short_id"`
		} `json:"issue"`
	}
	out := runCLI(t, env, dir, "--json", "create", "child",
		"--blocked-by", blocker,
		"--related", peer,
	)
	var resp createResp
	require.NoError(t, json.Unmarshal([]byte(out), &resp))

	b := fetchIssueViaHTTP(t, env, pid, resp.Issue.ShortID)

	var sawBlockedBy, sawRelated bool
	for _, l := range b.Links {
		switch l.Type {
		case "blocks":
			if l.From.ShortID == blocker && l.To.ShortID == resp.Issue.ShortID {
				sawBlockedBy = true
			}
		case "related":
			if (l.From.ShortID == peer && l.To.ShortID == resp.Issue.ShortID) ||
				(l.From.ShortID == resp.Issue.ShortID && l.To.ShortID == peer) {
				sawRelated = true
			}
		}
	}
	assert.True(t, sawBlockedBy, "blocks link from blocker to new issue (blocked-by) must be persisted")
	assert.True(t, sawRelated, "related link between peer and new issue must be persisted")
}

func TestCreate_WithIdempotencyKeyReusesOnRepeat(t *testing.T) {
	env, dir := setupCLIEnv(t)

	// First call.
	first := runCLI(t, env, dir, "--quiet", "create",
		"first issue", "--idempotency-key", "K1")
	assert.NotEmpty(t, first)

	// Repeat with the same key + same fingerprint → reuse, same short_id.
	resetFlags(t)
	second := runCLI(t, env, dir, "--quiet", "create",
		"first issue", "--idempotency-key", "K1")
	assert.Equal(t, first, second, "same key + fingerprint must return existing issue short_id")
}

// TestCreate_IdempotentReuseHumanModeOmitsLinksSummary pins that a
// create whose Idempotency-Key matched a prior issue (changed=false)
// does NOT print a synthetic `links: +parent ...` summary in human
// mode — nothing was mutated on this call, so reporting "links
// applied" would mislead the operator.
func TestCreate_IdempotentReuseHumanModeOmitsLinksSummary(t *testing.T) {
	env, dir := setupCLIEnv(t)
	pid := resolvePIDViaHTTP(t, env.URL, dir)
	parent := createIssue(t, env, pid, "parent")

	// First create with a parent link.
	first := runCLI(t, env, dir, "create",
		"child", "--parent", parent, "--idempotency-key", "K2")
	assert.Contains(t, first, "links: +parent "+parent,
		"sanity: the original create echoes the link summary")

	// Second call with the same key → daemon returns the existing issue
	// with changed=false. The synthesized links summary must NOT print.
	resetFlags(t)
	second := runCLI(t, env, dir, "create",
		"child", "--parent", parent, "--idempotency-key", "K2")
	assert.NotContains(t, second, "links:",
		"idempotent reuse must not synthesize a links summary: %q", second)
}

func TestCreate_ForceNewBypassesLookalike(t *testing.T) {
	env, dir := setupCLIEnv(t)
	first := createIssueViaHTTP(t, env, dir, "fix login crash on Safari")

	// Without --force-new the daemon would 409 on look-alike. With it, a new
	// issue lands with a fresh short_id.
	second := runCLI(t, env, dir, "--quiet", "create",
		"fix login crash Safari", "--force-new")
	assert.NotEmpty(t, second)
	assert.NotEqual(t, first, second)
}

// TestResolveProjectID_PropagatesParseError guards against a malformed
// .kata.toml silently falling through to a start_path request. In
// remote-client mode the daemon cannot stat the client path, so the
// failure mode would be a confusing "stat: no such file" instead of
// the actual "broken .kata.toml" the user can fix. The fix-it error
// must surface client-side without ever calling the daemon.
func TestResolveProjectID_PropagatesParseError(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".kata.toml"), //nolint:gosec // test fixture mode matches production
		[]byte("not = valid = toml ==="), 0o644))

	var called atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		called.Add(1)
	}))
	t.Cleanup(srv.Close)

	_, err := resolveProjectID(context.Background(), srv.URL, dir)
	require.Error(t, err)
	assert.Zero(t, called.Load(), "client must reject parse errors before reaching the daemon")
}

// TestResolveProjectID_FallsBackOnMissingConfig confirms the missing
// case still works: when no .kata.toml exists, the request goes
// through with start_path so the daemon can resolve via its own
// filesystem walk (local-mode behavior).
func TestResolveProjectID_FallsBackOnMissingConfig(t *testing.T) {
	dir := t.TempDir() // no .kata.toml

	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bs, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(bs, &got)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"project":{"id":42}}`))
	}))
	t.Cleanup(srv.Close)

	id, err := resolveProjectID(context.Background(), srv.URL, dir)
	require.NoError(t, err)
	assert.EqualValues(t, 42, id)
	assert.Equal(t, dir, got["start_path"])
	_, hasName := got["name"]
	assert.False(t, hasName, "no .kata.toml means no project name in the request")
}

func TestResolveProjectID_UsesStartPathForWorkspaceConfig(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, config.WriteProjectConfig(dir, "stale-name"))

	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bs, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(bs, &got)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"project":{"id":42}}`))
	}))
	t.Cleanup(srv.Close)

	id, err := resolveProjectID(context.Background(), srv.URL, dir)
	require.NoError(t, err)
	assert.EqualValues(t, 42, id)
	assert.Equal(t, dir, got["start_path"])
	_, hasName := got["name"]
	assert.False(t, hasName, "ordinary workspace resolution must let daemon alias repair run")
}
