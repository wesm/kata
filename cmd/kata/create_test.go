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

func TestCreate_PrintsIssueNumberInQuietMode(t *testing.T) {
	env, dir := setupCLIEnv(t)
	out := runCLI(t, env, dir, "--quiet", "create", "first issue", "--body", "details")
	assert.Equal(t, "1", out)
}

func TestCreate_WithInitialLabelsAndParent(t *testing.T) {
	env, dir := setupCLIEnv(t)
	pid := resolvePIDViaHTTP(t, env.URL, dir)
	createIssue(t, env, pid, "parent-issue") // #1
	createIssue(t, env, pid, "blocker")      // #2

	out := runCLI(t, env, dir, "create", "child",
		"--label", "bug", "--label", "needs-review",
		"--parent", "1",
		"--blocks", "2",
		"--owner", "alice",
	)
	assert.Contains(t, out, "child")

	// Fetch the created issue (#3) and assert every initial-state flag was
	// actually persisted, not just echoed back in the create response.
	b := fetchIssueViaHTTP(t, env, pid, 3)
	require.NotNil(t, b.Issue.Owner)
	assert.Equal(t, "alice", *b.Issue.Owner)

	gotLabels := make([]string, 0, len(b.Labels))
	for _, l := range b.Labels {
		gotLabels = append(gotLabels, l.Label)
	}
	assert.ElementsMatch(t, []string{"bug", "needs-review"}, gotLabels)

	var sawParent, sawBlocks bool
	for _, l := range b.Links {
		switch l.Type {
		case "parent":
			if l.FromNumber == 3 && l.ToNumber == 1 {
				sawParent = true
			}
		case "blocks":
			if l.FromNumber == 3 && l.ToNumber == 2 {
				sawBlocks = true
			}
		}
	}
	assert.True(t, sawParent, "parent link from #3 to #1 must be persisted")
	assert.True(t, sawBlocks, "blocks link from #3 to #2 must be persisted")
}

// TestCreate_WithBlockedByAndRelated covers the new repeatable link flags
// added by the relationship-flag consolidation. `--blocked-by N` records
// "this new issue is blocked by N" — i.e. the link runs FROM N TO the new
// issue. `--related N` records the symmetric tie.
func TestCreate_WithBlockedByAndRelated(t *testing.T) {
	env, dir := setupCLIEnv(t)
	pid := resolvePIDViaHTTP(t, env.URL, dir)
	createIssue(t, env, pid, "blocker") // #1
	createIssue(t, env, pid, "peer")    // #2

	out := runCLI(t, env, dir, "create", "child",
		"--blocked-by", "1",
		"--related", "2",
	)
	assert.Contains(t, out, "child")

	b := fetchIssueViaHTTP(t, env, pid, 3)

	var sawBlockedBy, sawRelated bool
	for _, l := range b.Links {
		switch l.Type {
		case "blocks":
			if l.FromNumber == 1 && l.ToNumber == 3 {
				sawBlockedBy = true
			}
		case "related":
			// Related is canonical-ordered server-side; either ordering
			// is correct so long as the endpoints are 2 and 3.
			if (l.FromNumber == 2 && l.ToNumber == 3) || (l.FromNumber == 3 && l.ToNumber == 2) {
				sawRelated = true
			}
		}
	}
	assert.True(t, sawBlockedBy, "blocks link from #1 to #3 (i.e. blocked-by) must be persisted")
	assert.True(t, sawRelated, "related link between #2 and #3 must be persisted")
}

func TestCreate_WithIdempotencyKeyReusesOnRepeat(t *testing.T) {
	env, dir := setupCLIEnv(t)

	// First call.
	first := runCLI(t, env, dir, "--quiet", "create",
		"first issue", "--idempotency-key", "K1")
	assert.Equal(t, "1", first)

	// Repeat with the same key + same fingerprint → reuse, same number.
	resetFlags(t)
	second := runCLI(t, env, dir, "--quiet", "create",
		"first issue", "--idempotency-key", "K1")
	assert.Equal(t, "1", second, "same key + fingerprint must return existing issue number")
}

// TestCreate_IdempotentReuseHumanModeOmitsLinksSummary pins that a
// create whose Idempotency-Key matched a prior issue (changed=false)
// does NOT print a synthetic `links: +parent #X` summary in human
// mode — nothing was mutated on this call, so reporting "links
// applied" would mislead the operator. The original create's
// response was the source of truth for what landed.
func TestCreate_IdempotentReuseHumanModeOmitsLinksSummary(t *testing.T) {
	env, dir := setupCLIEnv(t)
	pid := resolvePIDViaHTTP(t, env.URL, dir)
	createIssue(t, env, pid, "parent")

	// First create with a parent link.
	first := runCLI(t, env, dir, "create",
		"child", "--parent", "1", "--idempotency-key", "K2")
	assert.Contains(t, first, "links: +parent #1",
		"sanity: the original create echoes the link summary")

	// Second call with the same key → daemon returns the existing issue
	// with changed=false. The synthesized links summary must NOT print.
	resetFlags(t)
	second := runCLI(t, env, dir, "create",
		"child", "--parent", "1", "--idempotency-key", "K2")
	assert.NotContains(t, second, "links:",
		"idempotent reuse must not synthesize a links summary: %q", second)
}

func TestCreate_ForceNewBypassesLookalike(t *testing.T) {
	env, dir := setupCLIEnv(t)
	createIssueViaHTTP(t, env, dir, "fix login crash on Safari")

	// Without --force-new the daemon would 409 on look-alike. With it, a new issue lands.
	out := runCLI(t, env, dir, "--quiet", "create",
		"fix login crash Safari", "--force-new")
	assert.Equal(t, "2", out)
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
