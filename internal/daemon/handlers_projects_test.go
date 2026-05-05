package daemon_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wesm/kata/internal/daemon"
	"github.com/wesm/kata/internal/db"
	"github.com/wesm/kata/internal/testfix"
)

func TestResolve_FailsOutsideKataTomlAndWithoutAlias(t *testing.T) {
	ts := newTestServer(t)
	resp, bs := postJSON(t, ts, "/api/v1/projects/resolve", map[string]any{
		"start_path": t.TempDir(),
	})
	assertAPIError(t, resp.StatusCode, bs, 404, "project_not_initialized")
}

func TestInit_FromGitRemoteCreatesProject(t *testing.T) {
	h := newServerWithGitWorkspace(t, "https://github.com/wesm/kata.git")
	ts := h.ts.(*httptest.Server)
	resp, bs := postJSON(t, ts, "/api/v1/projects", map[string]any{
		"start_path": h.dir,
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
	_, err := os.Stat(filepath.Join(h.dir, ".kata.toml"))
	assert.NoError(t, err)
}

func TestInit_FreshCloneFromExistingKataToml(t *testing.T) {
	// Simulate "git clone, kata init" on a repo that already had .kata.toml.
	h := newServerWithGitWorkspace(t, "")
	testfix.WriteKataToml(t, h.dir, "github.com/wesm/system", "system")

	resp, bs := postJSON(t, h.ts.(*httptest.Server), "/api/v1/projects", map[string]any{
		"start_path": h.dir,
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
	h := newServerWithGitWorkspace(t, "https://github.com/wesm/kata.git")
	ts := h.ts.(*httptest.Server)

	_, _ = postJSON(t, ts, "/api/v1/projects", map[string]any{"start_path": h.dir})

	resp, bs := postJSON(t, ts, "/api/v1/projects/resolve", map[string]any{"start_path": h.dir})
	assert.Equal(t, 200, resp.StatusCode, string(bs))
	assert.Contains(t, string(bs), `"identity":"github.com/wesm/kata"`)
}

// TestResolve_ByProjectIdentity_PathFree verifies the remote-client
// resolution path: the daemon looks up the project by its committed
// identity without touching the filesystem. This is what lets a kata
// client on host B reach a project registered on host A's daemon.
func TestResolve_ByProjectIdentity_PathFree(t *testing.T) {
	dir := t.TempDir()
	testfix.RunGit(t, dir, "init", "--quiet")
	testfix.RunGit(t, dir, "remote", "add", "origin", "https://github.com/wesm/kata.git")
	ts := newTestServer(t)

	// Register the project (local-style init).
	_, _ = postJSON(t, ts, "/api/v1/projects", map[string]any{"start_path": dir})

	// Now resolve by identity only — no start_path. Note that the
	// identity we send doesn't refer to anything on the daemon's
	// filesystem; the lookup must be path-free.
	resp, bs := postJSON(t, ts, "/api/v1/projects/resolve", map[string]any{
		"project_identity": "github.com/wesm/kata",
	})
	assert.Equal(t, 200, resp.StatusCode, string(bs))
	assert.Contains(t, string(bs), `"identity":"github.com/wesm/kata"`)
}

// TestResolve_ByProjectIdentity_NotRegistered surfaces the right error
// when a remote client claims an identity the daemon doesn't know about.
func TestResolve_ByProjectIdentity_NotRegistered(t *testing.T) {
	ts := newTestServer(t)

	resp, bs := postJSON(t, ts, "/api/v1/projects/resolve", map[string]any{
		"project_identity": "github.com/never/registered",
	})
	assert.Equal(t, http.StatusNotFound, resp.StatusCode, string(bs))
	assert.Contains(t, string(bs), "project_not_initialized")
	assert.Contains(t, string(bs), "github.com/never/registered")
}

// TestResolve_NeitherFieldSet rejects a request that supplies neither
// project_identity nor start_path.
func TestResolve_NeitherFieldSet(t *testing.T) {
	ts := newTestServer(t)

	resp, bs := postJSON(t, ts, "/api/v1/projects/resolve", map[string]any{})
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode, string(bs))
	assert.Contains(t, string(bs), "project_identity")
	assert.Contains(t, string(bs), "start_path")
}

// TestResolve_IdentityWinsOverStartPath verifies precedence: when both
// project_identity and start_path are supplied, identity takes priority
// and the daemon never touches the (potentially nonexistent) path.
func TestResolve_IdentityWinsOverStartPath(t *testing.T) {
	dir := t.TempDir()
	testfix.RunGit(t, dir, "init", "--quiet")
	testfix.RunGit(t, dir, "remote", "add", "origin", "https://github.com/wesm/kata.git")
	ts := newTestServer(t)

	_, _ = postJSON(t, ts, "/api/v1/projects", map[string]any{"start_path": dir})

	// start_path is bogus and would not stat; identity must win.
	resp, bs := postJSON(t, ts, "/api/v1/projects/resolve", map[string]any{
		"project_identity": "github.com/wesm/kata",
		"start_path":       "/no/such/path/anywhere",
	})
	assert.Equal(t, 200, resp.StatusCode, string(bs))
	assert.Contains(t, string(bs), `"identity":"github.com/wesm/kata"`)
}

func TestInit_AliasConflictWithoutReassign(t *testing.T) {
	h := newServerWithGitWorkspace(t, "https://github.com/wesm/kata.git")
	ts := h.ts.(*httptest.Server)

	// First init binds the alias to "github.com/wesm/kata".
	_, _ = postJSON(t, ts, "/api/v1/projects", map[string]any{"start_path": h.dir})

	// .kata.toml now declares a different identity.
	testfix.WriteKataToml(t, h.dir, "github.com/wesm/other", "other")

	// Re-init without --replace must fail.
	resp, bs := postJSON(t, ts, "/api/v1/projects", map[string]any{
		"start_path": h.dir,
	})
	assertAPIError(t, resp.StatusCode, bs, http.StatusConflict, "project_alias_conflict")

	// With --reassign + --replace, succeeds and rewrites alias.
	resp2, bs2 := postJSON(t, ts, "/api/v1/projects", map[string]any{
		"start_path": h.dir,
		"replace":    true,
		"reassign":   true,
	})
	require.Equal(t, 200, resp2.StatusCode, string(bs2))
}

// TestInit_ByIdentity_PathFree verifies the remote-client init path:
// the daemon registers a project by client-derived identity without
// touching the filesystem. This is what lets a kata client on host B
// init a project against host A's daemon when host A cannot stat host
// B's workspace.
func TestInit_ByIdentity_PathFree(t *testing.T) {
	ts := newTestServer(t)

	resp, bs := postJSON(t, ts, "/api/v1/projects", map[string]any{
		"project_identity": "github.com/wesm/remote",
		"name":             "remote",
	})
	require.Equal(t, 200, resp.StatusCode, string(bs))

	var body struct {
		Project struct {
			Identity string
			Name     string
		} `json:"project"`
		WorkspaceRoot string `json:"workspace_root"`
		Created       bool   `json:"created"`
	}
	require.NoError(t, json.Unmarshal(bs, &body))
	assert.Equal(t, "github.com/wesm/remote", body.Project.Identity)
	assert.Equal(t, "remote", body.Project.Name)
	assert.True(t, body.Created)
	// Daemon never knew the client workspace path; response must
	// reflect that so the client doesn't write .gitignore in the
	// wrong place.
	assert.Empty(t, body.WorkspaceRoot)

	// Re-init by same identity is idempotent and reports created=false.
	resp2, bs2 := postJSON(t, ts, "/api/v1/projects", map[string]any{
		"project_identity": "github.com/wesm/remote",
	})
	require.Equal(t, 200, resp2.StatusCode, string(bs2))
	var body2 struct {
		Created bool `json:"created"`
	}
	require.NoError(t, json.Unmarshal(bs2, &body2))
	assert.False(t, body2.Created)
}

// TestInit_NeitherFieldSet rejects requests that supply neither
// project_identity nor start_path (mirrors the resolve contract so
// callers see a uniform validation message).
func TestInit_NeitherFieldSet(t *testing.T) {
	ts := newTestServer(t)

	resp, bs := postJSON(t, ts, "/api/v1/projects", map[string]any{})
	require.Equal(t, http.StatusBadRequest, resp.StatusCode, string(bs))
	assert.Contains(t, string(bs), "project_identity")
	assert.Contains(t, string(bs), "start_path")
}

// TestInit_ByIdentity_RejectsEmptyIdentity guards against an empty or
// whitespace-only project_identity slipping through into a project row.
func TestInit_ByIdentity_RejectsEmptyIdentity(t *testing.T) {
	ts := newTestServer(t)

	resp, bs := postJSON(t, ts, "/api/v1/projects", map[string]any{
		"project_identity": "   ",
	})
	require.Equal(t, http.StatusBadRequest, resp.StatusCode, string(bs))
	assert.Contains(t, string(bs), "project_identity")
}

// TestInit_ByIdentity_StrictIdentityLookup guards against an alias
// collision silently rebinding identity-only init to the wrong
// project. If "github.com/wesm/origin" is registered as an alias for
// project X (whose canonical identity is "github.com/wesm/override"),
// a path-free init that asserts project_identity="github.com/wesm/origin"
// must create a new project — not return the alias-bound override.
func TestInit_ByIdentity_StrictIdentityLookup(t *testing.T) {
	dir := t.TempDir()
	testfix.RunGit(t, dir, "init", "--quiet")
	testfix.RunGit(t, dir, "remote", "add", "origin", "https://github.com/wesm/origin.git")
	ts := newTestServer(t)

	// Path-based init with --project override: project.identity is
	// "github.com/wesm/override" but its alias derives from the git
	// remote → "github.com/wesm/origin".
	resp1, bs1 := postJSON(t, ts, "/api/v1/projects", map[string]any{
		"start_path":       dir,
		"project_identity": "github.com/wesm/override",
	})
	require.Equal(t, 200, resp1.StatusCode, string(bs1))

	// Identity-only init asserting "github.com/wesm/origin" as canonical.
	// Strict lookup must not return the override project.
	resp2, bs2 := postJSON(t, ts, "/api/v1/projects", map[string]any{
		"project_identity": "github.com/wesm/origin",
	})
	require.Equal(t, 200, resp2.StatusCode, string(bs2))

	var body struct {
		Project struct {
			Identity string
		} `json:"project"`
		Created bool `json:"created"`
	}
	require.NoError(t, json.Unmarshal(bs2, &body))
	assert.Equal(t, "github.com/wesm/origin", body.Project.Identity,
		"daemon must treat project_identity as canonical, not as an alias lookup")
	assert.True(t, body.Created)
}

// TestInit_ByIdentity_AttachesAliasWhenSupplied verifies the path-free
// init attaches an alias the client supplies. This preserves alias
// semantics (resolve-by-git-remote, conflict detection) for remote
// clients that can compute alias info locally.
func TestInit_ByIdentity_AttachesAliasWhenSupplied(t *testing.T) {
	ts := newTestServer(t)

	resp, bs := postJSON(t, ts, "/api/v1/projects", map[string]any{
		"project_identity": "github.com/wesm/foo",
		"name":             "foo",
		"alias": map[string]any{
			"identity":  "github.com/wesm/foo",
			"kind":      "git",
			"root_path": "/client/workspace",
		},
	})
	require.Equal(t, 200, resp.StatusCode, string(bs))

	var body struct {
		Alias struct {
			AliasIdentity string `json:"alias_identity"`
			AliasKind     string `json:"alias_kind"`
			RootPath      string `json:"root_path"`
		} `json:"alias"`
	}
	require.NoError(t, json.Unmarshal(bs, &body))
	assert.Equal(t, "github.com/wesm/foo", body.Alias.AliasIdentity)
	assert.Equal(t, "git", body.Alias.AliasKind)
	assert.Equal(t, "/client/workspace", body.Alias.RootPath)
}

// TestInit_ByIdentity_AliasConflictWithoutReassign returns
// project_alias_conflict when the supplied alias is already attached
// to a different project. Without reassign, the daemon must not
// silently move the alias.
func TestInit_ByIdentity_AliasConflictWithoutReassign(t *testing.T) {
	ts := newTestServer(t)

	_, _ = postJSON(t, ts, "/api/v1/projects", map[string]any{
		"project_identity": "github.com/wesm/a",
		"alias": map[string]any{
			"identity":  "shared",
			"kind":      "git",
			"root_path": "/work",
		},
	})

	resp, bs := postJSON(t, ts, "/api/v1/projects", map[string]any{
		"project_identity": "github.com/wesm/b",
		"alias": map[string]any{
			"identity":  "shared",
			"kind":      "git",
			"root_path": "/work",
		},
	})
	require.Equal(t, http.StatusConflict, resp.StatusCode, string(bs))
	assert.Contains(t, string(bs), "project_alias_conflict")
}

// TestInit_ByIdentity_ReassignMovesAlias asserts that reassign +
// alias metadata moves the alias from the old project to the new one
// — this is what `kata init --reassign` against a remote daemon
// should do.
func TestInit_ByIdentity_ReassignMovesAlias(t *testing.T) {
	ts := newTestServer(t)

	_, _ = postJSON(t, ts, "/api/v1/projects", map[string]any{
		"project_identity": "github.com/wesm/old",
		"alias": map[string]any{
			"identity":  "shared",
			"kind":      "git",
			"root_path": "/work",
		},
	})

	resp, bs := postJSON(t, ts, "/api/v1/projects", map[string]any{
		"project_identity": "github.com/wesm/new",
		"reassign":         true,
		"alias": map[string]any{
			"identity":  "shared",
			"kind":      "git",
			"root_path": "/work",
		},
	})
	require.Equal(t, 200, resp.StatusCode, string(bs))

	var body struct {
		Project struct {
			Identity string
		} `json:"project"`
		Alias struct {
			AliasIdentity string `json:"alias_identity"`
		} `json:"alias"`
	}
	require.NoError(t, json.Unmarshal(bs, &body))
	assert.Equal(t, "github.com/wesm/new", body.Project.Identity)
	assert.Equal(t, "shared", body.Alias.AliasIdentity)
}

// TestInit_ByIdentity_ReassignWithoutAliasErrors guards against the
// silent-success case where --reassign is requested but no alias
// metadata is supplied. With nothing to reassign, the daemon must
// reject rather than report success and leave the old binding intact.
func TestInit_ByIdentity_ReassignWithoutAliasErrors(t *testing.T) {
	ts := newTestServer(t)

	resp, bs := postJSON(t, ts, "/api/v1/projects", map[string]any{
		"project_identity": "github.com/wesm/foo",
		"reassign":         true,
	})
	require.Equal(t, http.StatusBadRequest, resp.StatusCode, string(bs))
	assert.Contains(t, string(bs), "reassign")
	assert.Contains(t, string(bs), "alias")
}

// TestInit_ByIdentity_AcceptsLocalAliasWithSpaces guards against
// rejecting valid local:// aliases derived from workspace paths
// that contain spaces (or other characters the project-identity
// charset rules disallow). Path-based init attaches such aliases
// without complaint; the path-free flow must do the same so users
// in workspaces like "/Users/me/My Project" aren't blocked.
func TestInit_ByIdentity_AcceptsLocalAliasWithSpaces(t *testing.T) {
	ts := newTestServer(t)

	resp, bs := postJSON(t, ts, "/api/v1/projects", map[string]any{
		"project_identity": "github.com/wesm/foo",
		"alias": map[string]any{
			"identity":  "local:///Users/me/My Project",
			"kind":      "local",
			"root_path": "/Users/me/My Project",
		},
	})
	require.Equal(t, 200, resp.StatusCode, string(bs))

	var body struct {
		Alias struct {
			AliasIdentity string `json:"alias_identity"`
			AliasKind     string `json:"alias_kind"`
			RootPath      string `json:"root_path"`
		} `json:"alias"`
	}
	require.NoError(t, json.Unmarshal(bs, &body))
	assert.Equal(t, "local:///Users/me/My Project", body.Alias.AliasIdentity)
	assert.Equal(t, "local", body.Alias.AliasKind)
	assert.Equal(t, "/Users/me/My Project", body.Alias.RootPath)
}

// TestInit_ByIdentity_RejectsInvalidAliasKind ensures the daemon
// rejects malformed alias metadata explicitly rather than relying on
// downstream code to misbehave on an unknown kind.
func TestInit_ByIdentity_RejectsInvalidAliasKind(t *testing.T) {
	ts := newTestServer(t)

	resp, bs := postJSON(t, ts, "/api/v1/projects", map[string]any{
		"project_identity": "github.com/wesm/foo",
		"alias": map[string]any{
			"identity":  "github.com/wesm/foo",
			"kind":      "bogus",
			"root_path": "/work",
		},
	})
	require.Equal(t, http.StatusBadRequest, resp.StatusCode, string(bs))
	assert.Contains(t, string(bs), "kind")
}

// TestInit_ByIdentity_RejectsEmptyAliasRootPath enforces that an
// alias attach has somewhere to root: empty root_path makes future
// path-anchored operations meaningless.
func TestInit_ByIdentity_RejectsEmptyAliasRootPath(t *testing.T) {
	ts := newTestServer(t)

	resp, bs := postJSON(t, ts, "/api/v1/projects", map[string]any{
		"project_identity": "github.com/wesm/foo",
		"alias": map[string]any{
			"identity":  "github.com/wesm/foo",
			"kind":      "git",
			"root_path": "",
		},
	})
	require.Equal(t, http.StatusBadRequest, resp.StatusCode, string(bs))
	assert.Contains(t, string(bs), "root_path")
}

// TestInit_ByIdentity_DefaultsName verifies the daemon falls back to
// the last identity segment when the client doesn't supply name. This
// matches the local-init contract so the two paths produce the same
// project rows.
func TestInit_ByIdentity_DefaultsName(t *testing.T) {
	ts := newTestServer(t)

	resp, bs := postJSON(t, ts, "/api/v1/projects", map[string]any{
		"project_identity": "github.com/wesm/auto-name",
	})
	require.Equal(t, 200, resp.StatusCode, string(bs))

	var body struct {
		Project struct {
			Name string
		} `json:"project"`
	}
	require.NoError(t, json.Unmarshal(bs, &body))
	assert.Equal(t, "auto-name", body.Project.Name)
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
	assertAPIError(t, resp.StatusCode, bs, http.StatusConflict, "project_has_issues")
	assert.Contains(t, string(bs), `"issue_count":1`)
}

func TestResetCounter_RejectsZeroOrNegative(t *testing.T) {
	h, pid := bootstrapProject(t)
	ts := h.ts.(*httptest.Server)
	pidStr := strconv.FormatInt(pid, 10)

	for _, to := range []int64{0, -5} {
		resp, bs := postJSON(t, ts, "/api/v1/projects/"+pidStr+"/reset-counter",
			map[string]any{"to": to})
		assertAPIError(t, resp.StatusCode, bs, http.StatusBadRequest, "validation")
	}
}

func TestResetCounter_ProjectNotFound(t *testing.T) {
	ts := newTestServer(t)
	resp, bs := postJSON(t, ts, "/api/v1/projects/9999/reset-counter",
		map[string]any{"to": 1})
	assertAPIError(t, resp.StatusCode, bs, http.StatusNotFound, "project_not_found")
}

func TestListProjectsAndShow(t *testing.T) {
	h := newServerWithGitWorkspace(t, "https://github.com/wesm/x.git")
	ts := h.ts.(*httptest.Server)
	_, _ = postJSON(t, ts, "/api/v1/projects", map[string]any{"start_path": h.dir})

	listBody := getBody(t, ts, "/api/v1/projects")
	assert.Contains(t, listBody, `"identity":"github.com/wesm/x"`)

	pid := resolveProjectID(t, ts, h.dir)
	showBody := getBody(t, ts, "/api/v1/projects/"+strconv.FormatInt(pid, 10))
	assert.Contains(t, showBody, `"aliases":`)
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
	h := newServerWithGitWorkspace(t, "https://github.com/wesm/kata.git")
	ts := h.ts.(*httptest.Server)
	_, _ = postJSON(t, ts, "/api/v1/projects", map[string]any{"start_path": h.dir})

	pid := resolveProjectID(t, ts, h.dir)
	pidStr := strconv.FormatInt(pid, 10)

	resp, bs := patchJSON(t, ts, "/api/v1/projects/"+pidStr, map[string]any{
		"name": "Kata Tracker",
	})
	require.Equal(t, 200, resp.StatusCode, string(bs))
	assert.Contains(t, string(bs), `"identity":"github.com/wesm/kata"`)
	assert.Contains(t, string(bs), `"name":"Kata Tracker"`)
	assert.Contains(t, string(bs), `"aliases":`)

	showBody := getBody(t, ts, "/api/v1/projects/"+pidStr)
	assert.Contains(t, showBody, `"name":"Kata Tracker"`)
}

func TestRenameProject_RejectsBlankName(t *testing.T) {
	h := newServerWithGitWorkspace(t, "https://github.com/wesm/kata.git")
	ts := h.ts.(*httptest.Server)
	_, _ = postJSON(t, ts, "/api/v1/projects", map[string]any{"start_path": h.dir})

	pid := resolveProjectID(t, ts, h.dir)

	resp, bs := patchJSON(t, ts, "/api/v1/projects/"+strconv.FormatInt(pid, 10), map[string]any{
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
	assertAPIError(t, resp.StatusCode, bs, 404, "project_not_found")
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
	ts := h.ts.(*httptest.Server)
	store := h.DB()
	ctx := t.Context()
	p, err := store.CreateProject(ctx, "github.com/wesm/proj-rm", "proj-rm")
	require.NoError(t, err)
	_, err = store.AttachAlias(ctx, p.ID, "github.com/wesm/proj-rm", "git", h.dir)
	require.NoError(t, err)

	resp, bs := doReq(t, ts, http.MethodDelete,
		"/api/v1/projects/"+strconv.FormatInt(p.ID, 10)+"?actor=tester", nil, nil)
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

	listBody := getBody(t, ts, "/api/v1/projects")
	assert.NotContains(t, listBody, "github.com/wesm/proj-rm",
		"archived project must not surface in /projects list")
}

// TestRemoveProject_RefusesWithOpenIssues pins the safety gate: the wire
// returns 409 project_has_open_issues when force is omitted.
func TestRemoveProject_RefusesWithOpenIssues(t *testing.T) {
	h := newServerWithGitWorkspace(t, "")
	ts := h.ts.(*httptest.Server)
	store := h.DB()
	ctx := t.Context()
	p, err := store.CreateProject(ctx, "github.com/wesm/proj-busy", "proj-busy")
	require.NoError(t, err)
	_, _, err = store.CreateIssue(ctx, db.CreateIssueParams{
		ProjectID: p.ID, Title: "still open", Author: "tester",
	})
	require.NoError(t, err)

	resp, bs := doReq(t, ts, http.MethodDelete,
		"/api/v1/projects/"+strconv.FormatInt(p.ID, 10)+"?actor=tester", nil, nil)
	assertAPIError(t, resp.StatusCode, bs, http.StatusConflict, "project_has_open_issues")
}

// TestRemoveProject_ForceOverridesOpenIssues pins ?force=true: with the
// flag, archival succeeds even with open issues.
func TestRemoveProject_ForceOverridesOpenIssues(t *testing.T) {
	h := newServerWithGitWorkspace(t, "")
	ts := h.ts.(*httptest.Server)
	store := h.DB()
	ctx := t.Context()
	p, err := store.CreateProject(ctx, "github.com/wesm/proj-force-http", "proj-force-http")
	require.NoError(t, err)
	_, _, err = store.CreateIssue(ctx, db.CreateIssueParams{
		ProjectID: p.ID, Title: "still open", Author: "tester",
	})
	require.NoError(t, err)

	resp, bs := doReq(t, ts, http.MethodDelete,
		"/api/v1/projects/"+strconv.FormatInt(p.ID, 10)+"?actor=tester&force=true", nil, nil)
	assert.Equal(t, http.StatusOK, resp.StatusCode, string(bs))
}

// TestDetachProjectAlias_DropsOneAndEmitsEvent pins the alias-level wire:
// DELETE /api/v1/projects/{id}/aliases/{alias_id} drops one alias and
// emits project.alias_removed.
func TestDetachProjectAlias_DropsOneAndEmitsEvent(t *testing.T) {
	h := newServerWithGitWorkspace(t, "")
	ts := h.ts.(*httptest.Server)
	store := h.DB()
	ctx := t.Context()
	p, err := store.CreateProject(ctx, "github.com/wesm/proj-alias-http", "proj-alias-http")
	require.NoError(t, err)
	a1, err := store.AttachAlias(ctx, p.ID, "github.com/wesm/proj-alias-http", "git", h.dir)
	require.NoError(t, err)
	a2, err := store.AttachAlias(ctx, p.ID, "local:///tmp/aliased", "local", "/tmp/aliased")
	require.NoError(t, err)

	resp, bs := doReq(t, ts, http.MethodDelete,
		"/api/v1/projects/"+strconv.FormatInt(p.ID, 10)+
			"/aliases/"+strconv.FormatInt(a2.ID, 10)+"?actor=tester", nil, nil)
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
	ts := h.ts.(*httptest.Server)
	store := h.DB()
	ctx := t.Context()
	p, err := store.CreateProject(ctx, "github.com/wesm/proj-only-http", "proj-only-http")
	require.NoError(t, err)
	a, err := store.AttachAlias(ctx, p.ID, "github.com/wesm/proj-only-http", "git", h.dir)
	require.NoError(t, err)

	resp, bs := doReq(t, ts, http.MethodDelete,
		"/api/v1/projects/"+strconv.FormatInt(p.ID, 10)+
			"/aliases/"+strconv.FormatInt(a.ID, 10)+"?actor=tester", nil, nil)
	assertAPIError(t, resp.StatusCode, bs, http.StatusConflict, "alias_is_last")
}

// TestDetachProjectAlias_RejectsCrossProject pins that an alias_id from
// another project can't be dropped via this project's path. Returns 404 to
// avoid leaking the existence of the cross-project alias.
func TestDetachProjectAlias_RejectsCrossProject(t *testing.T) {
	h := newServerWithGitWorkspace(t, "")
	ts := h.ts.(*httptest.Server)
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

	resp, bs := doReq(t, ts, http.MethodDelete,
		"/api/v1/projects/"+strconv.FormatInt(p1.ID, 10)+
			"/aliases/"+strconv.FormatInt(a2.ID, 10)+"?actor=tester", nil, nil)
	assertAPIError(t, resp.StatusCode, bs, http.StatusNotFound, "alias_not_found")
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
	testfix.WriteKataToml(t, h.dir, "github.com/wesm/kenn", "kenn")

	resp, bs := postJSON(t, h.ts.(*httptest.Server), "/api/v1/projects", map[string]any{
		"start_path": h.dir,
	})
	require.Equal(t, 200, resp.StatusCode, string(bs))
	assert.Contains(t, string(bs), `"identity":"github.com/wesm/steward"`)

	cfgBytes, err := os.ReadFile(filepath.Join(h.dir, ".kata.toml"))
	require.NoError(t, err)
	assert.Contains(t, string(cfgBytes), `identity = "github.com/wesm/steward"`)
}
