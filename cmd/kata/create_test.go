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
	_, hasIdentity := got["project_identity"]
	assert.False(t, hasIdentity, "no .kata.toml means no project_identity in the request")
}

// TestResolveProjectID_WithProjectFlag_ResolvesByName proves that when
// flags.Project is set, resolveProjectID skips the workspace-based
// /resolve flow and goes through the projects list selector. This is
// what enables `kata create --project <name>` to target a project
// other than the one bound to cwd.
func TestResolveProjectID_WithProjectFlag_ResolvesByName(t *testing.T) {
	resetFlags(t)
	flags.Project = "kata"

	var resolveCalled atomic.Int32
	srv := httptest.NewServer(projectsListHandler(t, &resolveCalled))
	t.Cleanup(srv.Close)

	id, err := resolveProjectID(context.Background(), srv.URL, t.TempDir())
	require.NoError(t, err)
	assert.EqualValues(t, 7, id)
	assert.Zero(t, resolveCalled.Load(), "selector path must not call /resolve")
}

// TestResolveProjectID_WithProjectFlag_AcceptsID confirms numeric
// selectors work alongside name selectors.
func TestResolveProjectID_WithProjectFlag_AcceptsID(t *testing.T) {
	resetFlags(t)
	flags.Project = "7"

	srv := httptest.NewServer(projectsListHandler(t, nil))
	t.Cleanup(srv.Close)

	id, err := resolveProjectID(context.Background(), srv.URL, t.TempDir())
	require.NoError(t, err)
	assert.EqualValues(t, 7, id)
}

// TestResolveProjectID_WithProjectFlag_NotFound surfaces a useful
// error when the selector matches nothing.
func TestResolveProjectID_WithProjectFlag_NotFound(t *testing.T) {
	resetFlags(t)
	flags.Project = "nonexistent"

	srv := httptest.NewServer(projectsListHandler(t, nil))
	t.Cleanup(srv.Close)

	_, err := resolveProjectID(context.Background(), srv.URL, t.TempDir())
	require.Error(t, err)
}

// projectsListHandler stubs the daemon endpoints loadProjectRefs touches:
// GET /api/v1/projects (the list) and GET /api/v1/projects/{id} (the
// per-project alias detail loadProjectRefs walks for selector matching).
// resolveSeen, when non-nil, is incremented if /api/v1/projects/resolve is
// hit — selector callers must never go through that endpoint.
func projectsListHandler(t *testing.T, resolveSeen *atomic.Int32) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/projects":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"projects":[{"id":7,"identity":"github.com/wesm/kata","name":"kata","next_issue_number":1}]}`))
		case "/api/v1/projects/7":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"project":{"id":7,"identity":"github.com/wesm/kata","name":"kata","next_issue_number":1},"aliases":[]}`))
		case "/api/v1/projects/resolve":
			if resolveSeen != nil {
				resolveSeen.Add(1)
			}
			http.Error(w, "selector path must not call /resolve", http.StatusInternalServerError)
		default:
			http.NotFound(w, r)
		}
	}
}

// TestCreate_WithProjectFlag_TargetsNamedProject is the integration
// proof that --project lets a user create issues in a project other
// than the one bound to cwd. Without --project, create falls back to
// workspace resolution and the issue would land in the default
// project.
func TestCreate_WithProjectFlag_TargetsNamedProject(t *testing.T) {
	env, dir := setupCLIEnv(t)
	otherDir := initBoundWorkspace(t, env.URL, "https://github.com/wesm/other.git")
	defaultPID := resolvePIDViaHTTP(t, env.URL, dir)
	otherPID := resolvePIDViaHTTP(t, env.URL, otherDir)

	out := runCLI(t, env, dir, "--quiet", "create", "--project", "other", "issue in other")
	assert.Equal(t, "1", out)

	defaultIssues := listIssueNumbersViaHTTP(t, env.URL, defaultPID)
	otherIssues := listIssueNumbersViaHTTP(t, env.URL, otherPID)
	assert.Empty(t, defaultIssues, "issue must not land in the workspace project")
	assert.Equal(t, []int64{1}, otherIssues, "issue must land in the project named by --project")
}
