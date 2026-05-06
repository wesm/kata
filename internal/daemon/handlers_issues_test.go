package daemon_test

import (
	"encoding/json"
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

func TestIssues_CreateRoundtrip(t *testing.T) {
	h, projectID := bootstrapProject(t)
	resp, bs := postJSON(t, h.ts.(*httptest.Server), issuesURL(projectID),
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
	resp, bs := postJSON(t, ts, issuesURL(projectID),
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

	listBS := getBody(t, ts, issuesURL(projectID))
	assert.Contains(t, listBS, `"uid":"`+created.Issue.UID+`"`)
	assert.Contains(t, listBS, `"project_uid":"`+created.Issue.ProjectUID+`"`)

	byUIDResp, byUIDBS := getStatusBody(t, ts, "/api/v1/issues/"+created.Issue.UID)
	require.Equal(t, 200, byUIDResp.StatusCode, string(byUIDBS))
	assert.Contains(t, string(byUIDBS), `"number":`+strconv.FormatInt(created.Issue.Number, 10))
	assert.Contains(t, string(byUIDBS), `"uid":"`+created.Issue.UID+`"`)

	badResp, badBS := getStatusBody(t, ts, "/api/v1/issues/not-a-ulid")
	assert.Equal(t, 400, badResp.StatusCode, string(badBS))
	assert.Contains(t, string(badBS), `"code":"validation"`)
}

func TestIssues_ListAndShow(t *testing.T) {
	h, pid := bootstrapProject(t)
	ts := h.ts.(*httptest.Server)
	for _, title := range []string{"a", "b"} {
		_, _ = postJSON(t, ts, issuesURL(pid),
			map[string]any{"actor": "x", "title": title})
	}

	listBS := getBody(t, ts, issuesURL(pid)+"?status=open")
	assert.Contains(t, listBS, `"title":"a"`)
	assert.Contains(t, listBS, `"title":"b"`)

	showBS := getBody(t, ts, issueURL(pid, 1, ""))
	assert.Contains(t, showBS, `"comments":`)
}

func TestIssues_ListMissingProjectIs404(t *testing.T) {
	h, _ := bootstrapProject(t)
	resp, bs := getStatusBody(t, h.ts.(*httptest.Server), "/api/v1/projects/9999/issues")
	assertAPIError(t, resp.StatusCode, bs, 404, "project_not_found")
}

func TestIssues_PatchEditTitleAndBody(t *testing.T) {
	h, pid := bootstrapProject(t)
	ts := h.ts.(*httptest.Server)
	_, _ = postJSON(t, ts, issuesURL(pid),
		map[string]any{"actor": "x", "title": "old"})

	resp, bs := patchJSON(t, ts, issueURL(pid, 1, ""),
		map[string]any{"actor": "x", "title": "new"})
	require.Equal(t, 200, resp.StatusCode, string(bs))
	assert.Contains(t, string(bs), `"title":"new"`)
}

func TestCreateIssue_BlankActorIs400(t *testing.T) {
	h, pid := bootstrapProject(t)
	resp, bs := postJSON(t, h.ts.(*httptest.Server), issuesURL(pid),
		map[string]any{"actor": "   ", "title": "x"})
	assertAPIError(t, resp.StatusCode, bs, 400, "validation")
}

func TestEditIssue_BlankActorIs400(t *testing.T) {
	h, pid := bootstrapProject(t)
	ts := h.ts.(*httptest.Server)
	_, _ = postJSON(t, ts, issuesURL(pid),
		map[string]any{"actor": "x", "title": "old"})

	resp, bs := patchJSON(t, ts, issueURL(pid, 1, ""),
		map[string]any{"actor": "   ", "title": "new"})
	assertAPIError(t, resp.StatusCode, bs, 400, "validation")
}

func TestCreateIssue_WithInitialState(t *testing.T) {
	env := testenv.New(t)
	pid, parent, _ := setupTwoIssues(t, env)

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
	envPostJSON(t, env, projectPath(pid)+"/issues", map[string]any{
		"actor":  "tester",
		"title":  "child",
		"owner":  "alice",
		"labels": []string{"bug", "needs-review"},
		"links":  []map[string]any{{"type": "parent", "to_number": parent}},
	}, &out)
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
	resp, _ := envDoRaw(t, env, http.MethodPost, projectPath(pid)+"/issues", map[string]any{
		"actor": "tester", "title": "child",
		"links": []map[string]any{{"type": "parent", "to_number": 99}},
	}, nil)
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

	resp, bs := postJSON(t, h.ts.(*httptest.Server), issuesURL(projectID),
		map[string]any{"actor": "agent-1", "title": "should fail", "body": "details"})
	assertAPIError(t, resp.StatusCode, bs, http.StatusNotFound, "project_not_found")
}

func TestCreateIssue_InvalidLabelIs400(t *testing.T) {
	env := testenv.New(t)
	pid := initWorkspaceViaHTTP(t, env, "https://github.com/wesm/kata.git")
	resp, _ := envDoRaw(t, env, http.MethodPost, projectPath(pid)+"/issues", map[string]any{
		"actor": "tester", "title": "x",
		"labels": []string{"BadCase"},
	}, nil)
	assert.Equal(t, 400, resp.StatusCode)
}

// TestCreateIssue_InitialSelfLinkIs400 verifies that an initial link whose
// to_number equals the issue being created (which lands as #1 in a fresh
// project) surfaces as a 400 validation error, not a 500. The DB layer
// catches this via ErrSelfLink and the handler must map it.
func TestCreateIssue_InitialSelfLinkIs400(t *testing.T) {
	env := testenv.New(t)
	pid := initWorkspaceViaHTTP(t, env, "https://github.com/wesm/kata.git")
	resp, _ := envDoRaw(t, env, http.MethodPost, projectPath(pid)+"/issues", map[string]any{
		"actor": "tester", "title": "self",
		"links": []map[string]any{{"type": "parent", "to_number": 1}},
	}, nil)
	assert.Equal(t, 400, resp.StatusCode)
}

// TestCreate_IdempotencyReuse_SameFingerprint verifies that a second create
// with the same Idempotency-Key + same body returns the reuse envelope: no
// fresh event, the original_event populated, changed=false, reused=true.
func TestCreate_IdempotencyReuse_SameFingerprint(t *testing.T) {
	h, pid := bootstrapProject(t)
	ts := h.ts.(*httptest.Server)
	path := issuesURL(pid)
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
	path := issuesURL(pid)

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

// TestCreate_IdempotencyMismatchOnPriorityChange verifies that reusing the
// same Idempotency-Key with a different priority is a 409 mismatch — priority
// is part of the request identity, so a creator who keys on a body+priority
// pair gets the right surface when a later request shifts priority.
func TestCreate_IdempotencyMismatchOnPriorityChange(t *testing.T) {
	h, pid := bootstrapProject(t)
	ts := h.ts.(*httptest.Server)
	path := issuesURL(pid)

	first := postWithHeader(t, ts, path, map[string]string{"Idempotency-Key": "Kprio"},
		map[string]any{"actor": "agent-1", "title": "issue", "body": "body", "priority": 1})
	requireOK(t, first)

	second := postWithHeader(t, ts, path, map[string]string{"Idempotency-Key": "Kprio"},
		map[string]any{"actor": "agent-1", "title": "issue", "body": "body", "priority": 2})
	require.Equal(t, 409, second.status, string(second.body))
	assert.Contains(t, string(second.body), `"code":"idempotency_mismatch"`)

	// Re-sending with the same priority reuses the original issue.
	third := postWithHeader(t, ts, path, map[string]string{"Idempotency-Key": "Kprio"},
		map[string]any{"actor": "agent-1", "title": "issue", "body": "body", "priority": 1})
	requireOK(t, third)
	var thirdOut struct {
		Reused bool `json:"reused"`
	}
	require.NoError(t, json.Unmarshal(third.body, &thirdOut))
	assert.True(t, thirdOut.Reused, "same key + same priority should reuse")
}

// TestCreate_PriorityValidatedBeforeIdempotency verifies that an out-of-range
// priority surfaces as a 400 even when an Idempotency-Key matches a prior
// issue. The validate-before-lookup ordering keeps the API contract honest:
// invalid input is never silently absorbed by a reuse path.
func TestCreate_PriorityValidatedBeforeIdempotency(t *testing.T) {
	h, pid := bootstrapProject(t)
	ts := h.ts.(*httptest.Server)
	path := issuesURL(pid)

	first := postWithHeader(t, ts, path, map[string]string{"Idempotency-Key": "Kbad"},
		map[string]any{"actor": "agent-1", "title": "issue", "body": "body"})
	requireOK(t, first)

	bad := postWithHeader(t, ts, path, map[string]string{"Idempotency-Key": "Kbad"},
		map[string]any{"actor": "agent-1", "title": "issue", "body": "body", "priority": 9})
	require.Equal(t, 400, bad.status, string(bad.body))
	assert.Contains(t, string(bad.body), "priority must be between 0 and 4")
}

// TestCreate_IdempotencyDeletedIs409 verifies the §3.6 deleted-issue branch:
// when the idempotent-matched issue has been soft-deleted, retrying with the
// same key yields 409 idempotency_deleted with a restore hint.
func TestCreate_IdempotencyDeletedIs409(t *testing.T) {
	h, pid := bootstrapProject(t)
	ts := h.ts.(*httptest.Server)
	path := issuesURL(pid)

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
	path := issuesURL(pid)
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
	path := issuesURL(pid)

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
	path := issuesURL(pid)
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

	var out struct {
		Issues []struct {
			Number int64    `json:"number"`
			Labels []string `json:"labels"`
		} `json:"issues"`
	}
	envGetJSON(t, env, projectPath(pid)+"/issues", &out)
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
	envGetJSON(t, env, projectPath(pid)+"/issues", &out)
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

	var out struct {
		Issues []struct {
			Number int64   `json:"number"`
			Blocks []int64 `json:"blocks,omitempty"`
		} `json:"issues"`
	}
	envGetJSON(t, env, projectPath(pid)+"/issues", &out)
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

	var out struct {
		Issues []struct {
			ProjectID int64  `json:"project_id"`
			Title     string `json:"title"`
		} `json:"issues"`
	}
	envGetJSON(t, env, "/api/v1/issues", &out)
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

	var out struct {
		Issues []struct {
			ProjectID int64  `json:"project_id"`
			Title     string `json:"title"`
		} `json:"issues"`
	}
	envGetJSON(t, env, "/api/v1/issues?project_id="+strconv.FormatInt(pidB, 10), &out)
	require.Len(t, out.Issues, 1)
	assert.Equal(t, pidB, out.Issues[0].ProjectID)
	assert.Equal(t, "beta-1", out.Issues[0].Title)
}

// TestListAllIssues_ProjectNotFound pins the 404 path: a project_id that
// doesn't exist surfaces as project_not_found, matching the per-project
// endpoint's contract for invalid IDs.
func TestListAllIssues_ProjectNotFound(t *testing.T) {
	env := testenv.New(t)
	resp, bs := envGetRaw(t, env, "/api/v1/issues?project_id=9999")
	assertAPIError(t, resp.StatusCode, bs, http.StatusNotFound, "project_not_found")
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

	var out struct {
		Issues []struct {
			ProjectID int64    `json:"project_id"`
			Number    int64    `json:"number"`
			Labels    []string `json:"labels"`
		} `json:"issues"`
	}
	envGetJSON(t, env, "/api/v1/issues", &out)
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
	envGetJSON(t, env, issuePath(pid, child, ""), &out)
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
	envGetJSON(t, env, issuePath(pid, child, ""), &out)
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
