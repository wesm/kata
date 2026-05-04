package daemon_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wesm/kata/internal/db"
	"github.com/wesm/kata/internal/testenv"
	"github.com/wesm/kata/internal/uid"
)

// httptestServerHandle bundles a httptest.Server with the on-disk workspace
// directory the server was bootstrapped against. The ts field is typed as
// any to keep cross-helper imports loose; tests cast to *httptest.Server.
type httptestServerHandle struct {
	ts  any // *httptest.Server, but kept generic to avoid import cycles in helpers
	dir string
	db  *db.DB
}

// DB returns the *db.DB the test server is wired against, so a test can
// reach below the HTTP surface to set up state (e.g. soft-delete an issue
// before retrying create-with-idempotency-key).
func (h *httptestServerHandle) DB() *db.DB { return h.db }

// bootstrapProject spins up a fresh server + git workspace and runs `kata
// init` against it, returning the handle and the project rowid. Used as a
// shared setup for every issue handler test.
func bootstrapProject(t *testing.T) (*httptestServerHandle, int64) {
	t.Helper()
	h := newServerWithGitWorkspace(t, "https://github.com/wesm/kata.git")
	_, bs := postJSON(t, h.ts.(*httptest.Server), "/api/v1/projects", map[string]any{"start_path": h.dir})
	var resp struct{ Project struct{ ID int64 } }
	require.NoError(t, json.Unmarshal(bs, &resp))
	return h, resp.Project.ID
}

func TestIssues_CreateRoundtrip(t *testing.T) {
	h, projectID := bootstrapProject(t)
	resp, bs := postJSON(t, h.ts.(*httptest.Server),
		"/api/v1/projects/"+strconv.FormatInt(projectID, 10)+"/issues",
		map[string]any{"actor": "agent-1", "title": "first", "body": "details"})
	require.Equal(t, 200, resp.StatusCode, string(bs))

	var body struct {
		Issue struct {
			Number int64
			Title  string
			Status string
		}
		Event struct{ Type string }
	}
	require.NoError(t, json.Unmarshal(bs, &body))
	assert.EqualValues(t, 1, body.Issue.Number)
	assert.Equal(t, "first", body.Issue.Title)
	assert.Equal(t, "open", body.Issue.Status)
	assert.Equal(t, "issue.created", body.Event.Type)
}

func TestIssues_UIDWireShapeAndLookup(t *testing.T) {
	h, projectID := bootstrapProject(t)
	ts := h.ts.(*httptest.Server)
	resp, bs := postJSON(t, ts,
		"/api/v1/projects/"+strconv.FormatInt(projectID, 10)+"/issues",
		map[string]any{"actor": "agent-1", "title": "uid issue"})
	require.Equal(t, 200, resp.StatusCode, string(bs))

	var created struct {
		Issue struct {
			UID        string `json:"uid"`
			ProjectUID string `json:"project_uid"`
			Number     int64  `json:"number"`
		} `json:"issue"`
		Event struct {
			ProjectUID string  `json:"project_uid"`
			IssueUID   *string `json:"issue_uid"`
		} `json:"event"`
	}
	require.NoError(t, json.Unmarshal(bs, &created))
	assert.True(t, uid.Valid(created.Issue.UID))
	assert.True(t, uid.Valid(created.Issue.ProjectUID))
	assert.Equal(t, created.Issue.ProjectUID, created.Event.ProjectUID)
	require.NotNil(t, created.Event.IssueUID)
	assert.Equal(t, created.Issue.UID, *created.Event.IssueUID)

	respList, err := http.Get(ts.URL + "/api/v1/projects/" + strconv.FormatInt(projectID, 10) + "/issues")
	require.NoError(t, err)
	defer func() { _ = respList.Body.Close() }()
	listBS, err := io.ReadAll(respList.Body)
	require.NoError(t, err)
	assert.Contains(t, string(listBS), `"uid":"`+created.Issue.UID+`"`)
	assert.Contains(t, string(listBS), `"project_uid":"`+created.Issue.ProjectUID+`"`)

	respByUID, err := http.Get(ts.URL + "/api/v1/issues/" + created.Issue.UID)
	require.NoError(t, err)
	defer func() { _ = respByUID.Body.Close() }()
	byUIDBS, err := io.ReadAll(respByUID.Body)
	require.NoError(t, err)
	require.Equal(t, 200, respByUID.StatusCode, string(byUIDBS))
	assert.Contains(t, string(byUIDBS), `"number":`+strconv.FormatInt(created.Issue.Number, 10))
	assert.Contains(t, string(byUIDBS), `"uid":"`+created.Issue.UID+`"`)

	respBad, err := http.Get(ts.URL + "/api/v1/issues/not-a-ulid")
	require.NoError(t, err)
	defer func() { _ = respBad.Body.Close() }()
	badBS, err := io.ReadAll(respBad.Body)
	require.NoError(t, err)
	assert.Equal(t, 400, respBad.StatusCode, string(badBS))
	assert.Contains(t, string(badBS), `"code":"validation"`)
}

func TestIssues_ListAndShow(t *testing.T) {
	h, pid := bootstrapProject(t)
	for _, title := range []string{"a", "b"} {
		_, _ = postJSON(t, h.ts.(*httptest.Server),
			"/api/v1/projects/"+strconv.FormatInt(pid, 10)+"/issues",
			map[string]any{"actor": "x", "title": title})
	}

	resp, err := http.Get(h.ts.(*httptest.Server).URL +
		"/api/v1/projects/" + strconv.FormatInt(pid, 10) + "/issues?status=open")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	bs, _ := io.ReadAll(resp.Body)
	assert.Equal(t, 200, resp.StatusCode)
	assert.Contains(t, string(bs), `"title":"a"`)
	assert.Contains(t, string(bs), `"title":"b"`)

	resp2, err := http.Get(h.ts.(*httptest.Server).URL +
		"/api/v1/projects/" + strconv.FormatInt(pid, 10) + "/issues/1")
	require.NoError(t, err)
	defer func() { _ = resp2.Body.Close() }()
	bs2, _ := io.ReadAll(resp2.Body)
	assert.Equal(t, 200, resp2.StatusCode)
	assert.Contains(t, string(bs2), `"comments":`)
}

func TestIssues_ListMissingProjectIs404(t *testing.T) {
	h, _ := bootstrapProject(t)
	resp, err := http.Get(h.ts.(*httptest.Server).URL + "/api/v1/projects/9999/issues")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	bs, _ := io.ReadAll(resp.Body)
	assert.Equal(t, 404, resp.StatusCode, string(bs))
	assert.Contains(t, string(bs), `"code":"project_not_found"`)
}

func TestIssues_PatchEditTitleAndBody(t *testing.T) {
	h, pid := bootstrapProject(t)
	_, _ = postJSON(t, h.ts.(*httptest.Server),
		"/api/v1/projects/"+strconv.FormatInt(pid, 10)+"/issues",
		map[string]any{"actor": "x", "title": "old"})

	resp, bs := patchJSON(t, h.ts.(*httptest.Server),
		"/api/v1/projects/"+strconv.FormatInt(pid, 10)+"/issues/1",
		map[string]any{"actor": "x", "title": "new"})
	require.Equal(t, 200, resp.StatusCode, string(bs))
	assert.Contains(t, string(bs), `"title":"new"`)
}

func TestCreateIssue_BlankActorIs400(t *testing.T) {
	h, pid := bootstrapProject(t)
	resp, bs := postJSON(t, h.ts.(*httptest.Server),
		"/api/v1/projects/"+strconv.FormatInt(pid, 10)+"/issues",
		map[string]any{"actor": "   ", "title": "x"})
	assert.Equal(t, 400, resp.StatusCode, string(bs))
	assert.Contains(t, string(bs), `"code":"validation"`)
}

func TestEditIssue_BlankActorIs400(t *testing.T) {
	h, pid := bootstrapProject(t)
	_, _ = postJSON(t, h.ts.(*httptest.Server),
		"/api/v1/projects/"+strconv.FormatInt(pid, 10)+"/issues",
		map[string]any{"actor": "x", "title": "old"})

	resp, bs := patchJSON(t, h.ts.(*httptest.Server),
		"/api/v1/projects/"+strconv.FormatInt(pid, 10)+"/issues/1",
		map[string]any{"actor": "   ", "title": "new"})
	assert.Equal(t, 400, resp.StatusCode, string(bs))
	assert.Contains(t, string(bs), `"code":"validation"`)
}

func TestCreateIssue_WithInitialState(t *testing.T) {
	env := testenv.New(t)
	pid, parent, _ := setupTwoIssues(t, env)

	body, _ := json.Marshal(map[string]any{
		"actor":  "tester",
		"title":  "child",
		"owner":  "alice",
		"labels": []string{"bug", "needs-review"},
		"links":  []map[string]any{{"type": "parent", "to_number": parent}},
	})
	resp, err := env.HTTP.Post(
		env.URL+"/api/v1/projects/"+strconv.FormatInt(pid, 10)+"/issues",
		"application/json", bytes.NewReader(body))
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, 200, resp.StatusCode)
	var out struct {
		Issue struct {
			Number int64   `json:"number"`
			Owner  *string `json:"owner"`
		} `json:"issue"`
		Event struct {
			Type    string `json:"type"`
			Payload string `json:"payload"`
		} `json:"event"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	require.NotNil(t, out.Issue.Owner)
	assert.Equal(t, "alice", *out.Issue.Owner)
	assert.Equal(t, "issue.created", out.Event.Type)
	assert.Contains(t, out.Event.Payload, `"labels":["bug","needs-review"]`)
	assert.Contains(t, out.Event.Payload, `"owner":"alice"`)
	assert.Contains(t, out.Event.Payload, `"type":"parent"`)
}

func TestCreateIssue_InitialLinkToMissingTargetIs404(t *testing.T) {
	env := testenv.New(t)
	pid := initWorkspaceViaHTTP(t, env, "https://github.com/wesm/kata.git")
	body, _ := json.Marshal(map[string]any{
		"actor": "tester", "title": "child",
		"links": []map[string]any{{"type": "parent", "to_number": 99}},
	})
	resp, err := env.HTTP.Post(
		env.URL+"/api/v1/projects/"+strconv.FormatInt(pid, 10)+"/issues",
		"application/json", bytes.NewReader(body))
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	assert.Equal(t, 404, resp.StatusCode)
}

// TestCreateIssue_RejectsArchivedProject pins that creating an issue
// against an archived project returns 404 (matching the "archived
// projects are gone" semantic) rather than a 500. The DB-layer
// CreateIssue gates writes with deleted_at IS NULL and returns
// ErrNotFound; the handler must surface that as project_not_found
// after also rejecting in the preflight ProjectByID check.
func TestCreateIssue_RejectsArchivedProject(t *testing.T) {
	h, projectID := bootstrapProject(t)
	_, _, err := h.DB().RemoveProject(t.Context(), db.RemoveProjectParams{
		ProjectID: projectID, Actor: "tester",
	})
	require.NoError(t, err)

	resp, bs := postJSON(t, h.ts.(*httptest.Server),
		"/api/v1/projects/"+strconv.FormatInt(projectID, 10)+"/issues",
		map[string]any{"actor": "agent-1", "title": "should fail", "body": "details"})
	assert.Equal(t, http.StatusNotFound, resp.StatusCode, string(bs))
	assert.Contains(t, string(bs), `"code":"project_not_found"`)
}

func TestCreateIssue_InvalidLabelIs400(t *testing.T) {
	env := testenv.New(t)
	pid := initWorkspaceViaHTTP(t, env, "https://github.com/wesm/kata.git")
	body, _ := json.Marshal(map[string]any{
		"actor": "tester", "title": "x",
		"labels": []string{"BadCase"},
	})
	resp, err := env.HTTP.Post(
		env.URL+"/api/v1/projects/"+strconv.FormatInt(pid, 10)+"/issues",
		"application/json", bytes.NewReader(body))
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	assert.Equal(t, 400, resp.StatusCode)
}

// TestCreateIssue_InitialSelfLinkIs400 verifies that an initial link whose
// to_number equals the issue being created (which lands as #1 in a fresh
// project) surfaces as a 400 validation error, not a 500. The DB layer
// catches this via ErrSelfLink and the handler must map it.
func TestCreateIssue_InitialSelfLinkIs400(t *testing.T) {
	env := testenv.New(t)
	pid := initWorkspaceViaHTTP(t, env, "https://github.com/wesm/kata.git")
	body, _ := json.Marshal(map[string]any{
		"actor": "tester", "title": "self",
		"links": []map[string]any{{"type": "parent", "to_number": 1}},
	})
	resp, err := env.HTTP.Post(
		env.URL+"/api/v1/projects/"+strconv.FormatInt(pid, 10)+"/issues",
		"application/json", bytes.NewReader(body))
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	assert.Equal(t, 400, resp.StatusCode)
}

// TestCreate_IdempotencyReuse_SameFingerprint verifies that a second create
// with the same Idempotency-Key + same body returns the reuse envelope: no
// fresh event, the original_event populated, changed=false, reused=true.
func TestCreate_IdempotencyReuse_SameFingerprint(t *testing.T) {
	h, pid := bootstrapProject(t)
	ts := h.ts.(*httptest.Server)
	path := "/api/v1/projects/" + strconv.FormatInt(pid, 10) + "/issues"
	body := map[string]any{"actor": "agent-1", "title": "fix login crash", "body": "stack trace here"}

	first := postWithHeader(t, ts, path, map[string]string{"Idempotency-Key": "K1"}, body)
	requireOK(t, first)
	var firstOut struct {
		Issue struct {
			Number     int64  `json:"number"`
			UID        string `json:"uid"`
			ProjectUID string `json:"project_uid"`
		} `json:"issue"`
		Event struct {
			ID         int64   `json:"id"`
			ProjectUID string  `json:"project_uid"`
			IssueUID   *string `json:"issue_uid"`
		} `json:"event"`
	}
	require.NoError(t, json.Unmarshal(first.body, &firstOut))
	require.NotZero(t, firstOut.Event.ID)

	second := postWithHeader(t, ts, path, map[string]string{"Idempotency-Key": "K1"}, body)
	requireOK(t, second)
	var secondOut struct {
		Issue struct {
			Number int64 `json:"number"`
		} `json:"issue"`
		Event         *struct{ ID int64 } `json:"event"`
		OriginalEvent *struct {
			ID         int64   `json:"id"`
			ProjectUID string  `json:"project_uid"`
			IssueUID   *string `json:"issue_uid"`
		} `json:"original_event"`
		Changed bool `json:"changed"`
		Reused  bool `json:"reused"`
	}
	require.NoError(t, json.Unmarshal(second.body, &secondOut))
	assert.Equal(t, firstOut.Issue.Number, secondOut.Issue.Number)
	assert.Nil(t, secondOut.Event, "reuse must omit fresh event")
	require.NotNil(t, secondOut.OriginalEvent, "reuse must populate original_event")
	assert.Equal(t, firstOut.Event.ID, secondOut.OriginalEvent.ID)
	assert.Equal(t, firstOut.Issue.ProjectUID, secondOut.OriginalEvent.ProjectUID)
	require.NotNil(t, secondOut.OriginalEvent.IssueUID)
	assert.Equal(t, firstOut.Issue.UID, *secondOut.OriginalEvent.IssueUID)
	assert.False(t, secondOut.Changed)
	assert.True(t, secondOut.Reused)
}

// TestCreate_IdempotencyMismatch verifies that reusing the same Idempotency-Key
// with a different fingerprint (different title) is a 409 idempotency_mismatch.
func TestCreate_IdempotencyMismatch(t *testing.T) {
	h, pid := bootstrapProject(t)
	ts := h.ts.(*httptest.Server)
	path := "/api/v1/projects/" + strconv.FormatInt(pid, 10) + "/issues"

	first := postWithHeader(t, ts, path, map[string]string{"Idempotency-Key": "K1"},
		map[string]any{"actor": "agent-1", "title": "first title", "body": "first body"})
	requireOK(t, first)

	second := postWithHeader(t, ts, path, map[string]string{"Idempotency-Key": "K1"},
		map[string]any{"actor": "agent-1", "title": "different title", "body": "different body"})
	require.Equal(t, 409, second.status, string(second.body))
	assert.Contains(t, string(second.body), `"code":"idempotency_mismatch"`)
	// Decode the response and pin the original_issue_number echo so a future
	// regression that drops the data field surfaces immediately. The wire
	// envelope is {status, error: {code, message, data: {...}}}.
	var errBody struct {
		Error struct {
			Data struct {
				OriginalIssueNumber int64 `json:"original_issue_number"`
			} `json:"data"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal(second.body, &errBody), string(second.body))
	assert.EqualValues(t, 1, errBody.Error.Data.OriginalIssueNumber,
		"mismatch payload must echo the original issue's number")
}

// TestCreate_IdempotencyDeletedIs409 verifies the §3.6 deleted-issue branch:
// when the idempotent-matched issue has been soft-deleted, retrying with the
// same key yields 409 idempotency_deleted with a restore hint.
func TestCreate_IdempotencyDeletedIs409(t *testing.T) {
	h, pid := bootstrapProject(t)
	ts := h.ts.(*httptest.Server)
	path := "/api/v1/projects/" + strconv.FormatInt(pid, 10) + "/issues"

	first := postWithHeader(t, ts, path, map[string]string{"Idempotency-Key": "K-DEL"},
		map[string]any{"actor": "agent-1", "title": "soft delete me", "body": "details"})
	requireOK(t, first)
	var firstResp struct {
		Issue struct{ ID int64 } `json:"issue"`
	}
	require.NoError(t, json.Unmarshal(first.body, &firstResp))

	// Soft-delete the issue at the DB layer (Task 5 ships SoftDeleteIssue;
	// the daemon wraps it in Task 10. We can call DB directly because the
	// test server shares the same *db.DB as the handler.)
	_, _, _, err := h.DB().SoftDeleteIssue(t.Context(), firstResp.Issue.ID, "agent-1")
	require.NoError(t, err)

	second := postWithHeader(t, ts, path, map[string]string{"Idempotency-Key": "K-DEL"},
		map[string]any{"actor": "agent-1", "title": "soft delete me", "body": "details"})
	require.Equal(t, 409, second.status, string(second.body))
	assert.Contains(t, string(second.body), `"code":"idempotency_deleted"`)
	assert.Contains(t, string(second.body), `kata restore 1`,
		"hint must point at the restore command")
}

// TestCreate_LookalikeSoftBlock verifies that a near-identical second create
// (same title+body) without Idempotency-Key and without force_new is rejected
// as 409 duplicate_candidates.
func TestCreate_LookalikeSoftBlock(t *testing.T) {
	h, pid := bootstrapProject(t)
	ts := h.ts.(*httptest.Server)
	path := "/api/v1/projects/" + strconv.FormatInt(pid, 10) + "/issues"
	body := map[string]any{"actor": "agent-1", "title": "fix login crash", "body": "stack trace here"}

	first := postWithHeader(t, ts, path, nil, body)
	requireOK(t, first)

	second := postWithHeader(t, ts, path, nil, body)
	require.Equal(t, 409, second.status, string(second.body))
	assert.Contains(t, string(second.body), `"code":"duplicate_candidates"`)
	assert.Contains(t, string(second.body), `"candidates"`)
}

// TestCreate_ForceNewBypassesLookalike verifies that force_new=true on a body
// that would otherwise trip the look-alike check creates a new issue (200).
func TestCreate_ForceNewBypassesLookalike(t *testing.T) {
	h, pid := bootstrapProject(t)
	ts := h.ts.(*httptest.Server)
	path := "/api/v1/projects/" + strconv.FormatInt(pid, 10) + "/issues"

	first := postWithHeader(t, ts, path, nil,
		map[string]any{"actor": "agent-1", "title": "fix login crash", "body": "stack trace here"})
	requireOK(t, first)

	second := postWithHeader(t, ts, path, nil,
		map[string]any{"actor": "agent-1", "title": "fix login crash", "body": "stack trace here", "force_new": true})
	requireOK(t, second)
	var out struct {
		Issue struct{ Number int64 } `json:"issue"`
	}
	require.NoError(t, json.Unmarshal(second.body, &out))
	assert.EqualValues(t, 2, out.Issue.Number, "force_new must yield a new issue, not reuse")
}

// TestCreate_IdempotencyWinsOverForceNew verifies the spec §3.7 ordering: an
// idempotent match returns reuse even when force_new=true is also set.
func TestCreate_IdempotencyWinsOverForceNew(t *testing.T) {
	h, pid := bootstrapProject(t)
	ts := h.ts.(*httptest.Server)
	path := "/api/v1/projects/" + strconv.FormatInt(pid, 10) + "/issues"
	body := map[string]any{"actor": "agent-1", "title": "fix login crash", "body": "stack trace here"}

	first := postWithHeader(t, ts, path, map[string]string{"Idempotency-Key": "K1"}, body)
	requireOK(t, first)
	var firstOut struct {
		Issue struct{ Number int64 } `json:"issue"`
	}
	require.NoError(t, json.Unmarshal(first.body, &firstOut))

	bodyForceNew := map[string]any{
		"actor": "agent-1", "title": "fix login crash", "body": "stack trace here", "force_new": true,
	}
	second := postWithHeader(t, ts, path, map[string]string{"Idempotency-Key": "K1"}, bodyForceNew)
	requireOK(t, second)
	var secondOut struct {
		Issue  struct{ Number int64 } `json:"issue"`
		Reused bool                   `json:"reused"`
	}
	require.NoError(t, json.Unmarshal(second.body, &secondOut))
	assert.Equal(t, firstOut.Issue.Number, secondOut.Issue.Number, "idempotency wins: same number returned")
	assert.True(t, secondOut.Reused)
}

// TestListIssues_HydratesLabels verifies the Plan 8 commit 5b
// contract: GET /api/v1/projects/{id}/issues returns each issue with
// its attached labels (sorted alphabetically), so the TUI list view
// can render label chips without an extra fetch per row.
func TestListIssues_HydratesLabels(t *testing.T) {
	env := testenv.New(t)
	pid := initWorkspaceViaHTTP(t, env, "https://github.com/wesm/kata.git")
	first := createIssueViaHTTP(t, env, pid, "first")
	second := createIssueViaHTTP(t, env, pid, "second")
	postLabel(t, env, pid, first, "prio-1")
	postLabel(t, env, pid, first, "bug")
	postLabel(t, env, pid, second, "enhancement")

	resp, err := env.HTTP.Get(env.URL +
		"/api/v1/projects/" + strconv.FormatInt(pid, 10) + "/issues")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, 200, resp.StatusCode)
	var out struct {
		Issues []struct {
			Number int64    `json:"number"`
			Labels []string `json:"labels"`
		} `json:"issues"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	require.Len(t, out.Issues, 2)
	byNumber := map[int64][]string{}
	for _, iss := range out.Issues {
		byNumber[iss.Number] = iss.Labels
	}
	assert.Equal(t, []string{"bug", "prio-1"}, byNumber[first])
	assert.Equal(t, []string{"enhancement"}, byNumber[second])
}

func TestListIssues_IncludesHierarchyMetadata(t *testing.T) {
	env := testenv.New(t)
	pid := initWorkspaceViaHTTP(t, env, "https://github.com/wesm/kata.git")
	parent := createIssueViaHTTP(t, env, pid, "parent")
	child := createIssueViaHTTP(t, env, pid, "child")
	postLink(t, env, pid, child, "parent", parent)

	resp, err := env.HTTP.Get(env.URL +
		"/api/v1/projects/" + strconv.FormatInt(pid, 10) + "/issues")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, 200, resp.StatusCode)
	var out struct {
		Issues []struct {
			Number       int64  `json:"number"`
			ParentNumber *int64 `json:"parent_number"`
			ChildCounts  *struct {
				Open  int `json:"open"`
				Total int `json:"total"`
			} `json:"child_counts"`
		} `json:"issues"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	require.Len(t, out.Issues, 2)
	byNumber := map[int64]struct {
		ParentNumber *int64
		ChildCounts  *struct {
			Open  int `json:"open"`
			Total int `json:"total"`
		}
	}{}
	for _, iss := range out.Issues {
		byNumber[iss.Number] = struct {
			ParentNumber *int64
			ChildCounts  *struct {
				Open  int `json:"open"`
				Total int `json:"total"`
			}
		}{ParentNumber: iss.ParentNumber, ChildCounts: iss.ChildCounts}
	}
	require.NotNil(t, byNumber[parent].ChildCounts)
	assert.Equal(t, 1, byNumber[parent].ChildCounts.Open)
	assert.Equal(t, 1, byNumber[parent].ChildCounts.Total)
	require.NotNil(t, byNumber[child].ParentNumber)
	assert.Equal(t, parent, *byNumber[child].ParentNumber)
}

func TestListIssues_IncludesBlockerMetadata(t *testing.T) {
	env := testenv.New(t)
	pid := initWorkspaceViaHTTP(t, env, "https://github.com/wesm/kata.git")
	blocker := createIssueViaHTTP(t, env, pid, "blocker")
	blocked := createIssueViaHTTP(t, env, pid, "blocked")
	postLink(t, env, pid, blocker, "blocks", blocked)

	resp, err := env.HTTP.Get(env.URL +
		"/api/v1/projects/" + strconv.FormatInt(pid, 10) + "/issues")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, 200, resp.StatusCode)
	var out struct {
		Issues []struct {
			Number int64   `json:"number"`
			Blocks []int64 `json:"blocks,omitempty"`
		} `json:"issues"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	byNumber := map[int64][]int64{}
	for _, iss := range out.Issues {
		byNumber[iss.Number] = iss.Blocks
	}
	assert.Equal(t, []int64{blocked}, byNumber[blocker])
	assert.Empty(t, byNumber[blocked])
}

// TestListAllIssues_AcrossProjects pins #22's wire contract: GET /api/v1/issues
// with no project_id returns issues from every project, hydrating labels
// per-issue across project boundaries.
func TestListAllIssues_AcrossProjects(t *testing.T) {
	env := testenv.New(t)
	pidA := initWorkspaceViaHTTP(t, env, "https://github.com/wesm/proj-a.git")
	pidB := initWorkspaceViaHTTP(t, env, "https://github.com/wesm/proj-b.git")
	createIssueViaHTTP(t, env, pidA, "alpha-1")
	createIssueViaHTTP(t, env, pidB, "beta-1")
	createIssueViaHTTP(t, env, pidA, "alpha-2")

	resp, err := env.HTTP.Get(env.URL + "/api/v1/issues")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, 200, resp.StatusCode)

	var out struct {
		Issues []struct {
			ProjectID int64  `json:"project_id"`
			Title     string `json:"title"`
		} `json:"issues"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	require.Len(t, out.Issues, 3)
	projectIDs := map[int64]int{}
	for _, iss := range out.Issues {
		projectIDs[iss.ProjectID]++
	}
	assert.Equal(t, 2, projectIDs[pidA], "two issues from project A")
	assert.Equal(t, 1, projectIDs[pidB], "one issue from project B")
}

// TestListAllIssues_ProjectFilter pins the optional ?project_id= query: the
// cross-project endpoint can also serve as a single-project list when needed.
func TestListAllIssues_ProjectFilter(t *testing.T) {
	env := testenv.New(t)
	pidA := initWorkspaceViaHTTP(t, env, "https://github.com/wesm/proj-a.git")
	pidB := initWorkspaceViaHTTP(t, env, "https://github.com/wesm/proj-b.git")
	createIssueViaHTTP(t, env, pidA, "alpha-1")
	createIssueViaHTTP(t, env, pidB, "beta-1")

	resp, err := env.HTTP.Get(env.URL +
		"/api/v1/issues?project_id=" + strconv.FormatInt(pidB, 10))
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, 200, resp.StatusCode)
	var out struct {
		Issues []struct {
			ProjectID int64  `json:"project_id"`
			Title     string `json:"title"`
		} `json:"issues"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	require.Len(t, out.Issues, 1)
	assert.Equal(t, pidB, out.Issues[0].ProjectID)
	assert.Equal(t, "beta-1", out.Issues[0].Title)
}

// TestListAllIssues_ProjectNotFound pins the 404 path: a project_id that
// doesn't exist surfaces as project_not_found, matching the per-project
// endpoint's contract for invalid IDs.
func TestListAllIssues_ProjectNotFound(t *testing.T) {
	env := testenv.New(t)
	resp, err := env.HTTP.Get(env.URL + "/api/v1/issues?project_id=9999")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	bs, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal(bs, &body), string(bs))
	assert.Equal(t, "project_not_found", body.Error.Code)
}

// TestListAllIssues_HydratesLabelsAcrossProjects pins that labels attach
// correctly to rows from different projects — the hydration helper groups by
// project_id internally so labels stay scoped to the right issue.
func TestListAllIssues_HydratesLabelsAcrossProjects(t *testing.T) {
	env := testenv.New(t)
	pidA := initWorkspaceViaHTTP(t, env, "https://github.com/wesm/proj-a.git")
	pidB := initWorkspaceViaHTTP(t, env, "https://github.com/wesm/proj-b.git")
	a1 := createIssueViaHTTP(t, env, pidA, "alpha-1")
	b1 := createIssueViaHTTP(t, env, pidB, "beta-1")
	postLabel(t, env, pidA, a1, "bug")
	postLabel(t, env, pidB, b1, "enhancement")

	resp, err := env.HTTP.Get(env.URL + "/api/v1/issues")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, 200, resp.StatusCode)
	var out struct {
		Issues []struct {
			ProjectID int64    `json:"project_id"`
			Number    int64    `json:"number"`
			Labels    []string `json:"labels"`
		} `json:"issues"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	labelsByKey := map[string][]string{}
	for _, iss := range out.Issues {
		key := strconv.FormatInt(iss.ProjectID, 10) + "/" + strconv.FormatInt(iss.Number, 10)
		labelsByKey[key] = iss.Labels
	}
	assert.Equal(t, []string{"bug"},
		labelsByKey[strconv.FormatInt(pidA, 10)+"/"+strconv.FormatInt(a1, 10)])
	assert.Equal(t, []string{"enhancement"},
		labelsByKey[strconv.FormatInt(pidB, 10)+"/"+strconv.FormatInt(b1, 10)])
}

func TestShowIssue_IncludesLinksAndLabels(t *testing.T) {
	env := testenv.New(t)
	pid, parent, child := setupTwoIssues(t, env)
	postLabel(t, env, pid, child, "bug")
	postLink(t, env, pid, child, "parent", parent)

	resp, err := env.HTTP.Get(env.URL +
		"/api/v1/projects/" + strconv.FormatInt(pid, 10) +
		"/issues/" + strconv.FormatInt(child, 10))
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, 200, resp.StatusCode)
	var out struct {
		Links []struct {
			Type       string `json:"type"`
			FromNumber int64  `json:"from_number"`
			ToNumber   int64  `json:"to_number"`
		} `json:"links"`
		Labels []struct {
			Label string `json:"label"`
		} `json:"labels"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	require.Len(t, out.Links, 1)
	assert.Equal(t, "parent", out.Links[0].Type)
	assert.Equal(t, child, out.Links[0].FromNumber)
	assert.Equal(t, parent, out.Links[0].ToNumber)
	require.Len(t, out.Labels, 1)
	assert.Equal(t, "bug", out.Labels[0].Label)
}

func TestShowIssue_IncludesParentAndChildren(t *testing.T) {
	env := testenv.New(t)
	pid := initWorkspaceViaHTTP(t, env, "https://github.com/wesm/kata.git")
	parent := createIssueViaHTTP(t, env, pid, "parent")
	child := createIssueViaHTTP(t, env, pid, "child")
	grandchild := createIssueViaHTTP(t, env, pid, "grandchild")
	greatGrandchild := createIssueViaHTTP(t, env, pid, "great grandchild")
	postLink(t, env, pid, child, "parent", parent)
	postLink(t, env, pid, grandchild, "parent", child)
	postLink(t, env, pid, greatGrandchild, "parent", grandchild)
	postLabel(t, env, pid, grandchild, "bug")

	resp, err := env.HTTP.Get(env.URL +
		"/api/v1/projects/" + strconv.FormatInt(pid, 10) +
		"/issues/" + strconv.FormatInt(child, 10))
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, 200, resp.StatusCode)
	var out struct {
		Parent *struct {
			Number int64  `json:"number"`
			Title  string `json:"title"`
			Status string `json:"status"`
		} `json:"parent"`
		Children []struct {
			Number      int64    `json:"number"`
			Labels      []string `json:"labels"`
			ChildCounts *struct {
				Open  int `json:"open"`
				Total int `json:"total"`
			} `json:"child_counts"`
		} `json:"children"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	require.NotNil(t, out.Parent)
	assert.Equal(t, parent, out.Parent.Number)
	assert.Equal(t, "parent", out.Parent.Title)
	assert.Equal(t, "open", out.Parent.Status)
	require.Len(t, out.Children, 1)
	assert.Equal(t, grandchild, out.Children[0].Number)
	assert.Equal(t, []string{"bug"}, out.Children[0].Labels)
	require.NotNil(t, out.Children[0].ChildCounts)
	assert.Equal(t, 1, out.Children[0].ChildCounts.Open)
	assert.Equal(t, 1, out.Children[0].ChildCounts.Total)
}
