package db_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wesm/kata/internal/db"
)

func TestCreateIssue_AllocatesNumberAndEmitsEvent(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p, _ := d.CreateProject(ctx, "p", "p")

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
	d := openTestDB(t)
	ctx := context.Background()
	p, _ := d.CreateProject(ctx, "p", "p")

	for i := 1; i <= 3; i++ {
		issue, _, err := d.CreateIssue(ctx, db.CreateIssueParams{
			ProjectID: p.ID, Title: "x", Author: "a",
		})
		require.NoError(t, err)
		assert.EqualValues(t, i, issue.Number)
	}
}

func TestGetIssueByNumber_NotFound(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p, _ := d.CreateProject(ctx, "p", "p")
	_, err := d.IssueByNumber(ctx, p.ID, 99)
	assert.ErrorIs(t, err, db.ErrNotFound)
}

func TestListIssues_DefaultsToOpenOnlyAndExcludesDeleted(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p, _ := d.CreateProject(ctx, "p", "p")
	for _, title := range []string{"a", "b", "c"} {
		_, _, err := d.CreateIssue(ctx, db.CreateIssueParams{
			ProjectID: p.ID, Title: title, Author: "x",
		})
		require.NoError(t, err)
	}

	got, err := d.ListIssues(ctx, db.ListIssuesParams{ProjectID: p.ID, Status: "open"})
	require.NoError(t, err)
	assert.Len(t, got, 3)
}

// TestListAllIssues_CoversAllProjectsAndOrders pins #22's contract: with
// ProjectID==0 every project's issues are returned, soft-deleted rows are
// excluded, and the ordering is created_at DESC, id DESC.
func TestListAllIssues_CoversAllProjectsAndOrders(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p1, err := d.CreateProject(ctx, "alpha", "alpha")
	require.NoError(t, err)
	p2, err := d.CreateProject(ctx, "beta", "beta")
	require.NoError(t, err)

	a1, _, err := d.CreateIssue(ctx, db.CreateIssueParams{ProjectID: p1.ID, Title: "alpha-1", Author: "x"})
	require.NoError(t, err)
	b1, _, err := d.CreateIssue(ctx, db.CreateIssueParams{ProjectID: p2.ID, Title: "beta-1", Author: "x"})
	require.NoError(t, err)
	a2, _, err := d.CreateIssue(ctx, db.CreateIssueParams{ProjectID: p1.ID, Title: "alpha-2", Author: "x"})
	require.NoError(t, err)

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
	d := openTestDB(t)
	ctx := context.Background()
	p1, err := d.CreateProject(ctx, "alpha", "alpha")
	require.NoError(t, err)
	p2, err := d.CreateProject(ctx, "beta", "beta")
	require.NoError(t, err)
	_, _, err = d.CreateIssue(ctx, db.CreateIssueParams{ProjectID: p1.ID, Title: "a1", Author: "x"})
	require.NoError(t, err)
	_, _, err = d.CreateIssue(ctx, db.CreateIssueParams{ProjectID: p2.ID, Title: "b1", Author: "x"})
	require.NoError(t, err)

	got, err := d.ListAllIssues(ctx, db.ListAllIssuesParams{ProjectID: p2.ID})
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, p2.ID, got[0].ProjectID)
}

// TestListAllIssues_StatusFilterApplies pins the status filter across
// projects: closed/open are honored.
func TestListAllIssues_StatusFilterApplies(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p, err := d.CreateProject(ctx, "p", "p")
	require.NoError(t, err)
	open1, _, err := d.CreateIssue(ctx, db.CreateIssueParams{ProjectID: p.ID, Title: "open", Author: "x"})
	require.NoError(t, err)
	closed1, _, err := d.CreateIssue(ctx, db.CreateIssueParams{ProjectID: p.ID, Title: "to-close", Author: "x"})
	require.NoError(t, err)
	_, _, _, err = d.CloseIssue(ctx, closed1.ID, "done", "x")
	require.NoError(t, err)

	got, err := d.ListAllIssues(ctx, db.ListAllIssuesParams{Status: "open"})
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, open1.ID, got[0].ID)
}

// TestListAllIssues_ExcludesSoftDeleted pins that purged/soft-deleted issues
// don't surface in the cross-project list.
func TestListAllIssues_ExcludesSoftDeleted(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p, err := d.CreateProject(ctx, "p", "p")
	require.NoError(t, err)
	live, _, err := d.CreateIssue(ctx, db.CreateIssueParams{ProjectID: p.ID, Title: "live", Author: "x"})
	require.NoError(t, err)
	doomed, _, err := d.CreateIssue(ctx, db.CreateIssueParams{ProjectID: p.ID, Title: "doomed", Author: "x"})
	require.NoError(t, err)
	_, _, _, err = d.SoftDeleteIssue(ctx, doomed.ID, "x")
	require.NoError(t, err)

	got, err := d.ListAllIssues(ctx, db.ListAllIssuesParams{})
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, live.ID, got[0].ID)
}

// TestListAllIssues_LimitCaps pins the limit knob on cross-project listing.
func TestListAllIssues_LimitCaps(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p, err := d.CreateProject(ctx, "p", "p")
	require.NoError(t, err)
	for i := 0; i < 5; i++ {
		_, _, err = d.CreateIssue(ctx, db.CreateIssueParams{ProjectID: p.ID, Title: "i", Author: "x"})
		require.NoError(t, err)
	}

	got, err := d.ListAllIssues(ctx, db.ListAllIssuesParams{Limit: 2})
	require.NoError(t, err)
	assert.Len(t, got, 2)
}

func TestCreateComment_EmitsEvent(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p, _ := d.CreateProject(ctx, "p", "p")
	issue, _, _ := d.CreateIssue(ctx, db.CreateIssueParams{ProjectID: p.ID, Title: "x", Author: "x"})

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
	d := openTestDB(t)
	ctx := context.Background()
	p, _ := d.CreateProject(ctx, "p", "p")
	issue, _, _ := d.CreateIssue(ctx, db.CreateIssueParams{ProjectID: p.ID, Title: "x", Author: "x"})

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
	d := openTestDB(t)
	ctx := context.Background()
	p, _ := d.CreateProject(ctx, "p", "p")
	issue, _, _ := d.CreateIssue(ctx, db.CreateIssueParams{ProjectID: p.ID, Title: "x", Author: "x"})
	_, _, _, err := d.CloseIssue(ctx, issue.ID, "done", "agent")
	require.NoError(t, err)

	_, evt, changed, err := d.CloseIssue(ctx, issue.ID, "done", "agent")
	require.NoError(t, err)
	assert.False(t, changed)
	assert.Nil(t, evt)
}

func TestReopenIssue_ClearsStatusAndEmitsEvent(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p, _ := d.CreateProject(ctx, "p", "p")
	issue, _, _ := d.CreateIssue(ctx, db.CreateIssueParams{ProjectID: p.ID, Title: "x", Author: "x"})
	_, _, _, _ = d.CloseIssue(ctx, issue.ID, "done", "agent")

	updated, evt, changed, err := d.ReopenIssue(ctx, issue.ID, "agent")
	require.NoError(t, err)
	assert.True(t, changed)
	assert.Equal(t, "open", updated.Status)
	assert.Nil(t, updated.ClosedAt)
	assert.Nil(t, updated.ClosedReason)
	assert.Equal(t, "issue.reopened", evt.Type)
}

func TestEditIssue_SetsFieldsAndEmitsEvent(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p, _ := d.CreateProject(ctx, "p", "p")
	issue, _, _ := d.CreateIssue(ctx, db.CreateIssueParams{ProjectID: p.ID, Title: "old", Body: "ob", Author: "x"})

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
	d := openTestDB(t)
	ctx := context.Background()
	p, _ := d.CreateProject(ctx, "p", "p")
	issue, _, _ := d.CreateIssue(ctx, db.CreateIssueParams{ProjectID: p.ID, Title: "x", Author: "x"})

	_, _, _, err := d.EditIssue(ctx, db.EditIssueParams{IssueID: issue.ID, Actor: "agent"})
	assert.ErrorIs(t, err, db.ErrNoFields)
}
