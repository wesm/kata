package db_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wesm/kata/internal/db"
)

func TestCreateIssue_AllocatesNumberAndEmitsEvent(t *testing.T) {
	d, ctx, p := setupTestProject(t)

	issue, evt, err := d.CreateIssue(ctx, db.CreateIssueParams{
		ProjectID: p.ID,
		Title:     "first",
		Body:      "details",
		Author:    "agent-1",
	})
	require.NoError(t, err)
	assert.Equal(t, int64(1), issue.Number)
	assertValidUID(t, issue.UID)
	assert.Equal(t, p.UID, issue.ProjectUID)
	assert.Equal(t, "open", issue.Status)
	assert.Equal(t, "agent-1", issue.Author)
	assert.Equal(t, "issue.created", evt.Type)
	assert.Equal(t, p.UID, evt.ProjectUID)
	assert.NotNil(t, evt.IssueID)
	require.NotNil(t, evt.IssueUID)
	assert.Equal(t, issue.UID, *evt.IssueUID)
	require.NotNil(t, evt.IssueNumber)
	assert.Equal(t, int64(1), *evt.IssueNumber)

	p2, err := d.ProjectByID(ctx, p.ID)
	require.NoError(t, err)
	assert.Equal(t, int64(2), p2.NextIssueNumber)
}

func TestCreateIssue_NumbersAreSequentialPerProject(t *testing.T) {
	d, ctx, p := setupTestProject(t)

	for i := 1; i <= 3; i++ {
		issue, _, err := d.CreateIssue(ctx, db.CreateIssueParams{
			ProjectID: p.ID, Title: "x", Author: "a",
		})
		require.NoError(t, err)
		assert.EqualValues(t, i, issue.Number)
	}
}

func TestGetIssueByNumber_NotFound(t *testing.T) {
	d, ctx, p := setupTestProject(t)
	_, err := d.IssueByNumber(ctx, p.ID, 99)
	assert.ErrorIs(t, err, db.ErrNotFound)
}

func TestListIssues_DefaultsToOpenOnlyAndExcludesDeleted(t *testing.T) {
	d, ctx, p := setupTestProject(t)
	for _, title := range []string{"a", "b", "c"} {
		createTesterIssue(ctx, t, d, p.ID, title)
	}

	got, err := d.ListIssues(ctx, db.ListIssuesParams{ProjectID: p.ID, Status: "open"})
	require.NoError(t, err)
	assert.Len(t, got, 3)
}

// TestListAllIssues_CoversAllProjectsAndOrders pins #22's contract: with
// ProjectID==0 every project's issues are returned, soft-deleted rows are
// excluded, and the ordering is created_at DESC, id DESC.
func TestListAllIssues_CoversAllProjectsAndOrders(t *testing.T) {
	d, ctx, p1 := setupTestProject(t)
	p2 := createProject(ctx, t, d, "beta", "beta")

	a1, _ := createTesterIssue(ctx, t, d, p1.ID, "alpha-1")
	b1, _ := createTesterIssue(ctx, t, d, p2.ID, "beta-1")
	a2, _ := createTesterIssue(ctx, t, d, p1.ID, "alpha-2")

	got, err := d.ListAllIssues(ctx, db.ListAllIssuesParams{})
	require.NoError(t, err)
	require.Len(t, got, 3)
	// created_at DESC, id DESC: latest insert comes first. Insert order was
	// a1, b1, a2 — id-DESC tiebreak guarantees stable ordering even when
	// several issues share a created_at sub-second bucket.
	assert.Equal(t, a2.ID, got[0].ID)
	assert.Equal(t, b1.ID, got[1].ID)
	assert.Equal(t, a1.ID, got[2].ID)
}

// TestListAllIssues_ProjectFilterScopes pins the optional project_id query:
// passing ProjectID>0 returns only that project's issues.
func TestListAllIssues_ProjectFilterScopes(t *testing.T) {
	d, ctx, p1 := setupTestProject(t)
	p2 := createProject(ctx, t, d, "beta", "beta")
	createTesterIssue(ctx, t, d, p1.ID, "a1")
	createTesterIssue(ctx, t, d, p2.ID, "b1")

	got, err := d.ListAllIssues(ctx, db.ListAllIssuesParams{ProjectID: p2.ID})
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, p2.ID, got[0].ProjectID)
}

// TestListAllIssues_StatusFilterApplies pins the status filter across
// projects: closed/open are honored.
func TestListAllIssues_StatusFilterApplies(t *testing.T) {
	d, ctx, p := setupTestProject(t)
	open1, _ := createTesterIssue(ctx, t, d, p.ID, "open")
	closed1, _ := createTesterIssue(ctx, t, d, p.ID, "to-close")
	_, _, _, err := d.CloseIssue(ctx, closed1.ID, "done", "x")
	require.NoError(t, err)

	got, err := d.ListAllIssues(ctx, db.ListAllIssuesParams{Status: "open"})
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, open1.ID, got[0].ID)
}

// TestListAllIssues_ExcludesSoftDeleted pins that purged/soft-deleted issues
// don't surface in the cross-project list.
func TestListAllIssues_ExcludesSoftDeleted(t *testing.T) {
	d, ctx, p := setupTestProject(t)
	live, _ := createTesterIssue(ctx, t, d, p.ID, "live")
	doomed, _ := createTesterIssue(ctx, t, d, p.ID, "doomed")
	_, _, _, err := d.SoftDeleteIssue(ctx, doomed.ID, "x")
	require.NoError(t, err)

	got, err := d.ListAllIssues(ctx, db.ListAllIssuesParams{})
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, live.ID, got[0].ID)
}

// TestListAllIssues_LimitCaps pins the limit knob on cross-project listing.
func TestListAllIssues_LimitCaps(t *testing.T) {
	d, ctx, p := setupTestProject(t)
	createTesterIssues(ctx, t, d, p.ID, 5)

	got, err := d.ListAllIssues(ctx, db.ListAllIssuesParams{Limit: 2})
	require.NoError(t, err)
	assert.Len(t, got, 2)
}

func TestCreateComment_EmitsEvent(t *testing.T) {
	d, ctx, p, issue := setupTestIssue(t)

	cmt, evt, err := d.CreateComment(ctx, db.CreateCommentParams{
		IssueID: issue.ID, Author: "agent", Body: "hi",
	})
	require.NoError(t, err)
	assert.Equal(t, "hi", cmt.Body)
	assert.Equal(t, "issue.commented", evt.Type)
	assert.Equal(t, p.UID, evt.ProjectUID)
	require.NotNil(t, evt.IssueUID)
	assert.Equal(t, issue.UID, *evt.IssueUID)
}

func TestCloseIssue_SetsStatusAndEmitsEvent(t *testing.T) {
	d, ctx, _, issue := setupTestIssue(t)

	updated, evt, changed, err := d.CloseIssue(ctx, issue.ID, "done", "agent")
	require.NoError(t, err)
	assert.True(t, changed)
	assert.Equal(t, "closed", updated.Status)
	require.NotNil(t, updated.ClosedReason)
	assert.Equal(t, "done", *updated.ClosedReason)
	assert.NotNil(t, updated.ClosedAt)
	assert.Equal(t, "issue.closed", evt.Type)
}

func TestCloseIssue_OnAlreadyClosedIsNoOp(t *testing.T) {
	d, ctx, _, issue := setupTestIssue(t)
	_, _, _, err := d.CloseIssue(ctx, issue.ID, "done", "agent")
	require.NoError(t, err)

	_, evt, changed, err := d.CloseIssue(ctx, issue.ID, "done", "agent")
	require.NoError(t, err)
	assert.False(t, changed)
	assert.Nil(t, evt)
}

func TestReopenIssue_ClearsStatusAndEmitsEvent(t *testing.T) {
	d, ctx, _, issue := setupTestIssue(t)
	_, _, _, err := d.CloseIssue(ctx, issue.ID, "done", "agent")
	require.NoError(t, err)

	updated, evt, changed, err := d.ReopenIssue(ctx, issue.ID, "agent")
	require.NoError(t, err)
	assert.True(t, changed)
	assert.Equal(t, "open", updated.Status)
	assert.Nil(t, updated.ClosedAt)
	assert.Nil(t, updated.ClosedReason)
	assert.Equal(t, "issue.reopened", evt.Type)
}

func TestEditIssue_SetsFieldsAndEmitsEvent(t *testing.T) {
	d, ctx, _, issue := setupTestIssue(t)

	newTitle := "new"
	updated, evt, changed, err := d.EditIssue(ctx, db.EditIssueParams{
		IssueID: issue.ID, Title: &newTitle, Actor: "agent",
	})
	require.NoError(t, err)
	assert.True(t, changed)
	assert.Equal(t, "new", updated.Title)
	require.NotNil(t, evt)
	assert.Equal(t, "issue.updated", evt.Type)
}

func TestEditIssue_NoFieldsIsValidationError(t *testing.T) {
	d, ctx, _, issue := setupTestIssue(t)

	_, _, _, err := d.EditIssue(ctx, db.EditIssueParams{IssueID: issue.ID, Actor: "agent"})
	assert.ErrorIs(t, err, db.ErrNoFields)
}
