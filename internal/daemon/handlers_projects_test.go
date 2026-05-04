package daemon_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wesm/kata/internal/daemon"
	"github.com/wesm/kata/internal/db"
)

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	//nolint:gosec // git binary is fixed; args are test-supplied subcommand flags.
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	require.NoErrorf(t, err, "git %v: %s", args, out)
}

func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	d := openTestDB(t)
	srv := daemon.NewServer(daemon.ServerConfig{DB: d.db, StartedAt: d.now})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts
}

func postJSON(t *testing.T, ts *httptest.Server, path string, body any) (*http.Response, []byte) {
	t.Helper()
	js, err := json.Marshal(body)
	require.NoError(t, err)
	resp, err := http.Post(ts.URL+path, "application/json", bytes.NewReader(js))
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	bs, _ := io.ReadAll(resp.Body)
	return resp, bs
}

func TestResolve_FailsOutsideKataTomlAndWithoutAlias(t *testing.T) {
	ts := newTestServer(t)
	resp, bs := postJSON(t, ts, "/api/v1/projects/resolve", map[string]any{
		"start_path": t.TempDir(),
	})
	assert.Equal(t, 404, resp.StatusCode)
	assert.Contains(t, string(bs), "project_not_initialized")
}

func TestInit_FromGitRemoteCreatesProject(t *testing.T) {
	dir := t.TempDir()
	runGit(t, dir, "init", "--quiet")
	runGit(t, dir, "remote", "add", "origin", "https://github.com/wesm/kata.git")

	ts := newTestServer(t)
	resp, bs := postJSON(t, ts, "/api/v1/projects", map[string]any{
		"start_path": dir,
	})
	assert.Equal(t, 200, resp.StatusCode, string(bs))

	var body struct {
		Project struct {
			ID       int64
			Identity string
			Name     string
		} `json:"project"`
		Alias struct {
			AliasIdentity string `json:"alias_identity"`
			AliasKind     string `json:"alias_kind"`
		} `json:"alias"`
		WorkspaceRoot string `json:"workspace_root"`
		Created       bool   `json:"created"`
	}
	require.NoError(t, json.Unmarshal(bs, &body))
	assert.Equal(t, "github.com/wesm/kata", body.Project.Identity)
	assert.Equal(t, "kata", body.Project.Name)
	assert.True(t, body.Created)
	assert.Equal(t, "github.com/wesm/kata", body.Alias.AliasIdentity)

	// .kata.toml must have been written
	_, err := os.Stat(filepath.Join(dir, ".kata.toml"))
	assert.NoError(t, err)
}

func TestInit_FreshCloneFromExistingKataToml(t *testing.T) {
	// Simulate "git clone, kata init" on a repo that already had .kata.toml.
	dir := t.TempDir()
	runGit(t, dir, "init", "--quiet")
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".kata.toml"), //nolint:gosec // test fixture matches production .kata.toml mode
		[]byte(`version = 1

[project]
identity = "github.com/wesm/system"
name     = "system"
`), 0o644))

	ts := newTestServer(t)
	resp, bs := postJSON(t, ts, "/api/v1/projects", map[string]any{
		"start_path": dir,
	})
	assert.Equal(t, 200, resp.StatusCode, string(bs))

	var body struct {
		Project struct {
			Identity string
		} `json:"project"`
		Created bool `json:"created"`
	}
	require.NoError(t, json.Unmarshal(bs, &body))
	assert.Equal(t, "github.com/wesm/system", body.Project.Identity)
	assert.True(t, body.Created)
}

func TestResolve_AfterInitSucceeds(t *testing.T) {
	dir := t.TempDir()
	runGit(t, dir, "init", "--quiet")
	runGit(t, dir, "remote", "add", "origin", "https://github.com/wesm/kata.git")
	ts := newTestServer(t)

	_, _ = postJSON(t, ts, "/api/v1/projects", map[string]any{"start_path": dir})

	resp, bs := postJSON(t, ts, "/api/v1/projects/resolve", map[string]any{"start_path": dir})
	assert.Equal(t, 200, resp.StatusCode, string(bs))
	assert.Contains(t, string(bs), `"identity":"github.com/wesm/kata"`)
}

func TestInit_AliasConflictWithoutReassign(t *testing.T) {
	dir := t.TempDir()
	runGit(t, dir, "init", "--quiet")
	runGit(t, dir, "remote", "add", "origin", "https://github.com/wesm/kata.git")
	ts := newTestServer(t)

	// First init binds the alias to "github.com/wesm/kata".
	_, _ = postJSON(t, ts, "/api/v1/projects", map[string]any{"start_path": dir})

	// .kata.toml now declares a different identity.
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".kata.toml"), //nolint:gosec // test fixture matches production .kata.toml mode
		[]byte(`version = 1

[project]
identity = "github.com/wesm/other"
name     = "other"
`), 0o644))

	// Re-init without --replace must fail.
	resp, bs := postJSON(t, ts, "/api/v1/projects", map[string]any{
		"start_path": dir,
	})
	assert.Equal(t, http.StatusConflict, resp.StatusCode)
	assert.Contains(t, string(bs), "project_alias_conflict")

	// With --reassign + --replace, succeeds and rewrites alias.
	resp2, bs2 := postJSON(t, ts, "/api/v1/projects", map[string]any{
		"start_path": dir,
		"replace":    true,
		"reassign":   true,
	})
	require.Equal(t, 200, resp2.StatusCode, string(bs2))
}

func TestResetCounter_EmptyProjectSucceeds(t *testing.T) {
	h, pid := bootstrapProject(t)
	ts := h.ts.(*httptest.Server)
	pidStr := strconv.FormatInt(pid, 10)

	resp, bs := postJSON(t, ts, "/api/v1/projects/"+pidStr+"/reset-counter",
		map[string]any{"to": 7})
	require.Equal(t, 200, resp.StatusCode, string(bs))

	var body struct {
		Project struct {
			ID              int64 `json:"id"`
			NextIssueNumber int64 `json:"next_issue_number"`
		} `json:"project"`
	}
	require.NoError(t, json.Unmarshal(bs, &body))
	assert.Equal(t, pid, body.Project.ID)
	assert.EqualValues(t, 7, body.Project.NextIssueNumber)

	// Counter actually moved: a fresh create allocates from the new value.
	resp2, bs2 := postJSON(t, ts, "/api/v1/projects/"+pidStr+"/issues",
		map[string]any{"actor": "agent", "title": "x"})
	require.Equal(t, 200, resp2.StatusCode, string(bs2))
	assert.Contains(t, string(bs2), `"number":7`)
}

func TestResetCounter_RefusesWhenIssuesExist(t *testing.T) {
	h, pid := bootstrapProject(t)
	ts := h.ts.(*httptest.Server)
	pidStr := strconv.FormatInt(pid, 10)

	requireOK(t, postWithHeader(t, ts, "/api/v1/projects/"+pidStr+"/issues",
		nil, map[string]any{"actor": "agent", "title": "x"}))

	resp, bs := postJSON(t, ts, "/api/v1/projects/"+pidStr+"/reset-counter",
		map[string]any{"to": 1})
	require.Equal(t, http.StatusConflict, resp.StatusCode, string(bs))
	assert.Contains(t, string(bs), `"project_has_issues"`)
	assert.Contains(t, string(bs), `"issue_count":1`)
}

func TestResetCounter_RejectsZeroOrNegative(t *testing.T) {
	h, pid := bootstrapProject(t)
	ts := h.ts.(*httptest.Server)
	pidStr := strconv.FormatInt(pid, 10)

	for _, to := range []int64{0, -5} {
		resp, bs := postJSON(t, ts, "/api/v1/projects/"+pidStr+"/reset-counter",
			map[string]any{"to": to})
		require.Equal(t, http.StatusBadRequest, resp.StatusCode, string(bs))
		assert.Contains(t, string(bs), `"validation"`)
	}
}

func TestResetCounter_ProjectNotFound(t *testing.T) {
	ts := newTestServer(t)
	resp, bs := postJSON(t, ts, "/api/v1/projects/9999/reset-counter",
		map[string]any{"to": 1})
	require.Equal(t, http.StatusNotFound, resp.StatusCode, string(bs))
	assert.Contains(t, string(bs), `"project_not_found"`)
}

func TestListProjectsAndShow(t *testing.T) {
	dir := t.TempDir()
	runGit(t, dir, "init", "--quiet")
	runGit(t, dir, "remote", "add", "origin", "https://github.com/wesm/x.git")
	ts := newTestServer(t)
	_, _ = postJSON(t, ts, "/api/v1/projects", map[string]any{"start_path": dir})

	resp, err := http.Get(ts.URL + "/api/v1/projects")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	bs, _ := io.ReadAll(resp.Body)
	require.Equal(t, 200, resp.StatusCode, string(bs))
	assert.Contains(t, string(bs), `"identity":"github.com/wesm/x"`)

	// pull project_id from the resolve flow then GET the show endpoint.
	_, rb := postJSON(t, ts, "/api/v1/projects/resolve", map[string]any{"start_path": dir})
	var rbody struct {
		Project struct{ ID int64 }
	}
	require.NoError(t, json.Unmarshal(rb, &rbody))
	resp2, err := http.Get(ts.URL + "/api/v1/projects/" + strconv.FormatInt(rbody.Project.ID, 10))
	require.NoError(t, err)
	defer func() { _ = resp2.Body.Close() }()
	body2, _ := io.ReadAll(resp2.Body)
	assert.Equal(t, 200, resp2.StatusCode)
	assert.Contains(t, string(body2), `"aliases":`)
}

// TestListProjects_DefaultShape pins the byte-level wire shape of
// GET /api/v1/projects. A future addition of a field to db.Project
// (e.g. an internal-only column) must not silently leak onto this
// response. Spec §7.2.
func TestListProjects_DefaultShape(t *testing.T) {
	h := openTestDB(t)
	_, err := h.db.CreateProject(t.Context(), "github.com/wesm/x", "x")
	require.NoError(t, err)
	srv := daemon.NewServer(daemon.ServerConfig{DB: h.db, StartedAt: h.now})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	body := getBody(t, ts, "/api/v1/projects")
	var parsed struct {
		Projects []map[string]any `json:"projects"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &parsed))
	require.Len(t, parsed.Projects, 1)
	p := parsed.Projects[0]

	for _, key := range []string{"id", "uid", "identity", "name", "created_at", "next_issue_number"} {
		_, ok := p[key]
		assert.True(t, ok, "missing key %q in projects[0]: %s", key, body)
	}
	_, hasStats := p["stats"]
	assert.False(t, hasStats, "stats must not appear in default response: %s", body)
	_, hasUpdated := p["updated_at"]
	assert.False(t, hasUpdated, "updated_at must not appear: %s", body)
	_, hasDeleted := p["deleted_at"]
	assert.False(t, hasDeleted, "deleted_at must omit on active project: %s", body)
}

func TestRenameProject_UpdatesNameAndKeepsIdentity(t *testing.T) {
	dir := t.TempDir()
	runGit(t, dir, "init", "--quiet")
	runGit(t, dir, "remote", "add", "origin", "https://github.com/wesm/kata.git")
	ts := newTestServer(t)
	_, _ = postJSON(t, ts, "/api/v1/projects", map[string]any{"start_path": dir})

	_, rb := postJSON(t, ts, "/api/v1/projects/resolve", map[string]any{"start_path": dir})
	var rbody struct {
		Project struct{ ID int64 }
	}
	require.NoError(t, json.Unmarshal(rb, &rbody))

	resp, bs := patchJSON(t, ts, "/api/v1/projects/"+strconv.FormatInt(rbody.Project.ID, 10), map[string]any{
		"name": "Kata Tracker",
	})
	require.Equal(t, 200, resp.StatusCode, string(bs))
	assert.Contains(t, string(bs), `"identity":"github.com/wesm/kata"`)
	assert.Contains(t, string(bs), `"name":"Kata Tracker"`)
	assert.Contains(t, string(bs), `"aliases":`)

	resp2, err := http.Get(ts.URL + "/api/v1/projects/" + strconv.FormatInt(rbody.Project.ID, 10))
	require.NoError(t, err)
	defer func() { _ = resp2.Body.Close() }()
	body2, _ := io.ReadAll(resp2.Body)
	assert.Equal(t, 200, resp2.StatusCode)
	assert.Contains(t, string(body2), `"name":"Kata Tracker"`)
}

func TestRenameProject_RejectsBlankName(t *testing.T) {
	dir := t.TempDir()
	runGit(t, dir, "init", "--quiet")
	runGit(t, dir, "remote", "add", "origin", "https://github.com/wesm/kata.git")
	ts := newTestServer(t)
	_, _ = postJSON(t, ts, "/api/v1/projects", map[string]any{"start_path": dir})

	_, rb := postJSON(t, ts, "/api/v1/projects/resolve", map[string]any{"start_path": dir})
	var rbody struct {
		Project struct{ ID int64 }
	}
	require.NoError(t, json.Unmarshal(rb, &rbody))

	resp, bs := patchJSON(t, ts, "/api/v1/projects/"+strconv.FormatInt(rbody.Project.ID, 10), map[string]any{
		"name": "   ",
	})
	assert.Equal(t, 400, resp.StatusCode)
	assert.Contains(t, string(bs), "name must be non-empty")
}

func TestRenameProject_MissingIs404(t *testing.T) {
	ts := newTestServer(t)
	resp, bs := patchJSON(t, ts, "/api/v1/projects/9999", map[string]any{
		"name": "Missing",
	})
	assert.Equal(t, 404, resp.StatusCode)
	assert.Contains(t, string(bs), "project_not_found")
}

func TestMergeProject_SourceMovesIntoSurvivingTarget(t *testing.T) {
	h := newServerWithGitWorkspace(t, "")
	store := h.DB()
	ctx := t.Context()
	kenn, err := store.CreateProject(ctx, "github.com/wesm/kenn", "kenn")
	require.NoError(t, err)
	steward, err := store.CreateProject(ctx, "github.com/wesm/steward", "steward")
	require.NoError(t, err)
	_, err = store.AttachAlias(ctx, kenn.ID, "github.com/wesm/kenn", "git", "/tmp/kenn")
	require.NoError(t, err)
	_, err = store.AttachAlias(ctx, steward.ID, "github.com/wesm/steward", "git", "/tmp/steward")
	require.NoError(t, err)
	_, _, err = store.CreateIssue(ctx, db.CreateIssueParams{
		ProjectID: kenn.ID, Title: "existing work", Author: "tester",
	})
	require.NoError(t, err)

	resp, bs := postJSON(t, h.ts.(*httptest.Server),
		"/api/v1/projects/"+strconv.FormatInt(steward.ID, 10)+"/merge",
		map[string]any{"source_project_id": kenn.ID})
	require.Equal(t, 200, resp.StatusCode, string(bs))
	assert.Contains(t, string(bs), `"identity":"github.com/wesm/steward"`)
	assert.Contains(t, string(bs), `"issues_moved":1`)
	assert.Contains(t, string(bs), `"next_issue_number":2`)

	issue, err := store.IssueByNumber(ctx, steward.ID, 1)
	require.NoError(t, err)
	assert.Equal(t, "existing work", issue.Title)
	_, err = store.ProjectByID(ctx, kenn.ID)
	assert.ErrorIs(t, err, db.ErrNotFound)
}

// TestRemoveProject_ArchivesAndDropsAliases pins #24's wire shape: DELETE
// /api/v1/projects/{id}?actor=tester archives the project and removes its
// aliases. List endpoint no longer surfaces the row; resolve against the
// archived identity returns project_archived (409).
func TestRemoveProject_ArchivesAndDropsAliases(t *testing.T) {
	h := newServerWithGitWorkspace(t, "")
	store := h.DB()
	ctx := t.Context()
	p, err := store.CreateProject(ctx, "github.com/wesm/proj-rm", "proj-rm")
	require.NoError(t, err)
	_, err = store.AttachAlias(ctx, p.ID, "github.com/wesm/proj-rm", "git", h.dir)
	require.NoError(t, err)

	req, err := http.NewRequest(http.MethodDelete,
		h.ts.(*httptest.Server).URL+"/api/v1/projects/"+strconv.FormatInt(p.ID, 10)+"?actor=tester", nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req) //nolint:gosec // test loopback
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	bs, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(bs))

	var body struct {
		Project struct {
			DeletedAt *string `json:"deleted_at"`
		} `json:"project"`
		Event struct {
			Type string `json:"type"`
		} `json:"event"`
	}
	require.NoError(t, json.Unmarshal(bs, &body), string(bs))
	require.NotNil(t, body.Project.DeletedAt)
	assert.Equal(t, "project.removed", body.Event.Type)

	listResp, err := http.Get(h.ts.(*httptest.Server).URL + "/api/v1/projects") //nolint:gosec // test loopback
	require.NoError(t, err)
	defer func() { _ = listResp.Body.Close() }()
	listBs, err := io.ReadAll(listResp.Body)
	require.NoError(t, err)
	assert.NotContains(t, string(listBs), "github.com/wesm/proj-rm",
		"archived project must not surface in /projects list")
}

// TestRemoveProject_RefusesWithOpenIssues pins the safety gate: the wire
// returns 409 project_has_open_issues when force is omitted.
func TestRemoveProject_RefusesWithOpenIssues(t *testing.T) {
	h := newServerWithGitWorkspace(t, "")
	store := h.DB()
	ctx := t.Context()
	p, err := store.CreateProject(ctx, "github.com/wesm/proj-busy", "proj-busy")
	require.NoError(t, err)
	_, _, err = store.CreateIssue(ctx, db.CreateIssueParams{
		ProjectID: p.ID, Title: "still open", Author: "tester",
	})
	require.NoError(t, err)

	req, err := http.NewRequest(http.MethodDelete,
		h.ts.(*httptest.Server).URL+"/api/v1/projects/"+strconv.FormatInt(p.ID, 10)+"?actor=tester", nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req) //nolint:gosec // test loopback
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	bs, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Equal(t, http.StatusConflict, resp.StatusCode, string(bs))
	assert.Contains(t, string(bs), "project_has_open_issues")
}

// TestRemoveProject_ForceOverridesOpenIssues pins ?force=true: with the
// flag, archival succeeds even with open issues.
func TestRemoveProject_ForceOverridesOpenIssues(t *testing.T) {
	h := newServerWithGitWorkspace(t, "")
	store := h.DB()
	ctx := t.Context()
	p, err := store.CreateProject(ctx, "github.com/wesm/proj-force-http", "proj-force-http")
	require.NoError(t, err)
	_, _, err = store.CreateIssue(ctx, db.CreateIssueParams{
		ProjectID: p.ID, Title: "still open", Author: "tester",
	})
	require.NoError(t, err)

	req, err := http.NewRequest(http.MethodDelete,
		h.ts.(*httptest.Server).URL+"/api/v1/projects/"+strconv.FormatInt(p.ID, 10)+
			"?actor=tester&force=true", nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req) //nolint:gosec // test loopback
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	bs, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode, string(bs))
}

// TestDetachProjectAlias_DropsOneAndEmitsEvent pins the alias-level wire:
// DELETE /api/v1/projects/{id}/aliases/{alias_id} drops one alias and
// emits project.alias_removed.
func TestDetachProjectAlias_DropsOneAndEmitsEvent(t *testing.T) {
	h := newServerWithGitWorkspace(t, "")
	store := h.DB()
	ctx := t.Context()
	p, err := store.CreateProject(ctx, "github.com/wesm/proj-alias-http", "proj-alias-http")
	require.NoError(t, err)
	a1, err := store.AttachAlias(ctx, p.ID, "github.com/wesm/proj-alias-http", "git", h.dir)
	require.NoError(t, err)
	a2, err := store.AttachAlias(ctx, p.ID, "local:///tmp/aliased", "local", "/tmp/aliased")
	require.NoError(t, err)

	req, err := http.NewRequest(http.MethodDelete,
		h.ts.(*httptest.Server).URL+"/api/v1/projects/"+strconv.FormatInt(p.ID, 10)+
			"/aliases/"+strconv.FormatInt(a2.ID, 10)+"?actor=tester", nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req) //nolint:gosec // test loopback
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	bs, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(bs))

	var body struct {
		Alias struct {
			ID int64 `json:"id"`
		} `json:"alias"`
		Event struct {
			Type string `json:"type"`
		} `json:"event"`
	}
	require.NoError(t, json.Unmarshal(bs, &body), string(bs))
	assert.Equal(t, a2.ID, body.Alias.ID)
	assert.Equal(t, "project.alias_removed", body.Event.Type)

	// The other alias remains and resolve still works.
	remaining, err := store.AliasByID(ctx, a1.ID)
	require.NoError(t, err)
	assert.Equal(t, a1.ID, remaining.ID)
}

// TestDetachProjectAlias_LastRefuses pins the safety gate at the wire:
// the only alias for a project rejects detach without ?force=true.
func TestDetachProjectAlias_LastRefuses(t *testing.T) {
	h := newServerWithGitWorkspace(t, "")
	store := h.DB()
	ctx := t.Context()
	p, err := store.CreateProject(ctx, "github.com/wesm/proj-only-http", "proj-only-http")
	require.NoError(t, err)
	a, err := store.AttachAlias(ctx, p.ID, "github.com/wesm/proj-only-http", "git", h.dir)
	require.NoError(t, err)

	req, err := http.NewRequest(http.MethodDelete,
		h.ts.(*httptest.Server).URL+"/api/v1/projects/"+strconv.FormatInt(p.ID, 10)+
			"/aliases/"+strconv.FormatInt(a.ID, 10)+"?actor=tester", nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req) //nolint:gosec // test loopback
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	bs, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Equal(t, http.StatusConflict, resp.StatusCode, string(bs))
	assert.Contains(t, string(bs), "alias_is_last")
}

// TestDetachProjectAlias_RejectsCrossProject pins that an alias_id from
// another project can't be dropped via this project's path. Returns 404 to
// avoid leaking the existence of the cross-project alias.
func TestDetachProjectAlias_RejectsCrossProject(t *testing.T) {
	h := newServerWithGitWorkspace(t, "")
	store := h.DB()
	ctx := t.Context()
	p1, err := store.CreateProject(ctx, "github.com/wesm/p1", "p1")
	require.NoError(t, err)
	p2, err := store.CreateProject(ctx, "github.com/wesm/p2", "p2")
	require.NoError(t, err)
	_, err = store.AttachAlias(ctx, p1.ID, "github.com/wesm/p1", "git", h.dir)
	require.NoError(t, err)
	a2, err := store.AttachAlias(ctx, p2.ID, "github.com/wesm/p2", "git", h.dir)
	require.NoError(t, err)

	req, err := http.NewRequest(http.MethodDelete,
		h.ts.(*httptest.Server).URL+"/api/v1/projects/"+strconv.FormatInt(p1.ID, 10)+
			"/aliases/"+strconv.FormatInt(a2.ID, 10)+"?actor=tester", nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req) //nolint:gosec // test loopback
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	bs, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Equal(t, http.StatusNotFound, resp.StatusCode, string(bs))
	assert.Contains(t, string(bs), "alias_not_found")
}

// TestRemoveProject_ArchivedIdentityRefusesReinit pins the user's clarifier
// in the design conversation: re-init against an archived identity returns
// project_archived (409) rather than silently resurrecting the project.
func TestRemoveProject_ArchivedIdentityRefusesReinit(t *testing.T) {
	h := newServerWithGitWorkspace(t, "https://github.com/wesm/proj-archive-reinit.git")
	store := h.DB()
	ctx := t.Context()
	// Init the project.
	resp, bs := postJSON(t, h.ts.(*httptest.Server), "/api/v1/projects",
		map[string]any{"start_path": h.dir})
	require.Equal(t, http.StatusOK, resp.StatusCode, string(bs))
	var initBody struct {
		Project struct {
			ID int64 `json:"id"`
		} `json:"project"`
	}
	require.NoError(t, json.Unmarshal(bs, &initBody))

	// Archive it.
	_, _, err := store.RemoveProject(ctx, db.RemoveProjectParams{
		ProjectID: initBody.Project.ID, Actor: "tester",
	})
	require.NoError(t, err)

	// Re-init from the same workspace must refuse.
	resp2, bs2 := postJSON(t, h.ts.(*httptest.Server), "/api/v1/projects",
		map[string]any{"start_path": h.dir})
	assert.Equal(t, http.StatusConflict, resp2.StatusCode, string(bs2))
	assert.Contains(t, string(bs2), "project_archived")
}

// TestListProjects_WithStatsIncludesAggregates pins the new wire
// contract: ?include=stats returns a stats triple per project. Spec §7.1.
func TestListProjects_WithStatsIncludesAggregates(t *testing.T) {
	h := openTestDB(t)
	ctx := t.Context()
	p, err := h.db.CreateProject(ctx, "github.com/wesm/x", "x")
	require.NoError(t, err)
	for i := 0; i < 3; i++ {
		_, _, err := h.db.CreateIssue(ctx, db.CreateIssueParams{
			ProjectID: p.ID, Title: "i", Author: "tester",
		})
		require.NoError(t, err)
	}
	srv := daemon.NewServer(daemon.ServerConfig{DB: h.db, StartedAt: h.now})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	body := getBody(t, ts, "/api/v1/projects?include=stats")
	var parsed struct {
		Projects []struct {
			ID    int64 `json:"id"`
			Stats *struct {
				Open        int     `json:"open"`
				Closed      int     `json:"closed"`
				LastEventAt *string `json:"last_event_at"`
			} `json:"stats"`
		} `json:"projects"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &parsed))
	require.Len(t, parsed.Projects, 1)
	require.NotNil(t, parsed.Projects[0].Stats, "stats present with ?include=stats")
	assert.Equal(t, 3, parsed.Projects[0].Stats.Open)
	assert.Equal(t, 0, parsed.Projects[0].Stats.Closed)
	require.NotNil(t, parsed.Projects[0].Stats.LastEventAt)
}

// TestListProjects_WithStatsHandlesEmptyProjects pins that a project
// with zero issues and zero events serializes Open=0, Closed=0,
// LastEventAt=null. Spec §7.1.
func TestListProjects_WithStatsHandlesEmptyProjects(t *testing.T) {
	h := openTestDB(t)
	_, err := h.db.CreateProject(t.Context(), "github.com/wesm/empty", "empty")
	require.NoError(t, err)
	srv := daemon.NewServer(daemon.ServerConfig{DB: h.db, StartedAt: h.now})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	body := getBody(t, ts, "/api/v1/projects?include=stats")
	var parsed struct {
		Projects []struct {
			// Pointer so a missing/null "stats" key would decode as nil
			// — without this, the omitempty path would let the test
			// pass even if the API stopped emitting stats for empty
			// projects. The require.NotNil below is the actual contract.
			Stats *struct {
				Open        int     `json:"open"`
				Closed      int     `json:"closed"`
				LastEventAt *string `json:"last_event_at"`
			} `json:"stats"`
		} `json:"projects"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &parsed))
	require.Len(t, parsed.Projects, 1)
	require.NotNil(t, parsed.Projects[0].Stats, "stats must be present even for empty projects")
	assert.Equal(t, 0, parsed.Projects[0].Stats.Open)
	assert.Equal(t, 0, parsed.Projects[0].Stats.Closed)
	assert.Nil(t, parsed.Projects[0].Stats.LastEventAt, "no events → null")
}

// TestListProjects_DefaultShapeUnchangedAfterStats pins that the no-query
// default response did not regress after Task 3 — backwards compat for
// kata projects list and any other consumer that doesn't opt in. Spec §7.1.
func TestListProjects_DefaultShapeUnchangedAfterStats(t *testing.T) {
	h := openTestDB(t)
	_, err := h.db.CreateProject(t.Context(), "github.com/wesm/x", "x")
	require.NoError(t, err)
	srv := daemon.NewServer(daemon.ServerConfig{DB: h.db, StartedAt: h.now})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	body := getBody(t, ts, "/api/v1/projects")
	var parsed struct {
		Projects []map[string]any `json:"projects"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &parsed))
	require.Len(t, parsed.Projects, 1)
	_, has := parsed.Projects[0]["stats"]
	assert.False(t, has, "stats must omit without ?include=stats")
}

func TestInit_MergedKataTomlIdentityResolvesToSurvivingProject(t *testing.T) {
	h := newServerWithGitWorkspace(t, "https://github.com/wesm/steward.git")
	store := h.DB()
	ctx := t.Context()
	kenn, err := store.CreateProject(ctx, "github.com/wesm/kenn", "kenn")
	require.NoError(t, err)
	steward, err := store.CreateProject(ctx, "github.com/wesm/steward", "steward")
	require.NoError(t, err)
	_, err = store.AttachAlias(ctx, kenn.ID, "github.com/wesm/kenn", "git", h.dir)
	require.NoError(t, err)
	_, err = store.AttachAlias(ctx, steward.ID, "github.com/wesm/steward", "git", h.dir)
	require.NoError(t, err)
	_, err = store.MergeProjects(ctx, db.MergeProjectsParams{
		SourceProjectID: kenn.ID,
		TargetProjectID: steward.ID,
	})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(h.dir, ".kata.toml"), //nolint:gosec // test fixture mirrors production .kata.toml mode
		[]byte(`version = 1

[project]
identity = "github.com/wesm/kenn"
name     = "kenn"
`), 0o644))

	resp, bs := postJSON(t, h.ts.(*httptest.Server), "/api/v1/projects", map[string]any{
		"start_path": h.dir,
	})
	require.Equal(t, 200, resp.StatusCode, string(bs))
	assert.Contains(t, string(bs), `"identity":"github.com/wesm/steward"`)

	cfgBytes, err := os.ReadFile(filepath.Join(h.dir, ".kata.toml"))
	require.NoError(t, err)
	assert.Contains(t, string(cfgBytes), `identity = "github.com/wesm/steward"`)
}
