package db_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wesm/kata/internal/db"
	"github.com/wesm/kata/internal/uid"
)

func TestSoftDeleteIssue_SetsDeletedAtAndEmitsEvent(t *testing.T) {
	d, ctx, _, issue := setupTestIssue(t)

	updated, evt, changed, err := d.SoftDeleteIssue(ctx, issue.ID, "agent")
	require.NoError(t, err)
	require.NotNil(t, evt)
	assert.True(t, changed)
	assert.Equal(t, "issue.soft_deleted", evt.Type)
	assert.Equal(t, "agent", evt.Actor)
	assert.JSONEq(t, "{}", string(evt.Payload), "soft_deleted event has empty payload")
	require.NotNil(t, evt.IssueID)
	assert.Equal(t, issue.ID, *evt.IssueID, "event refs the soft-deleted issue id")
	require.NotNil(t, evt.IssueNumber)
	assert.Equal(t, issue.Number, *evt.IssueNumber, "event refs the soft-deleted issue number")
	require.NotNil(t, updated.DeletedAt)
	assert.True(t, updated.UpdatedAt.After(issue.UpdatedAt) || updated.UpdatedAt.Equal(issue.UpdatedAt),
		"updated_at must not regress on soft-delete")
}

func TestSoftDeleteIssue_AlreadyDeletedIsNoOp(t *testing.T) {
	d, ctx, _, issue := setupSoftDeletedIssue(t)

	updated, evt, changed, err := d.SoftDeleteIssue(ctx, issue.ID, "agent")
	require.NoError(t, err)
	assert.Nil(t, evt, "no-op should return nil event")
	assert.False(t, changed)
	assert.NotNil(t, updated.DeletedAt, "issue stays deleted")
}

func TestSoftDeleteIssue_UnknownIssueIsErrNotFound(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	_, _, _, err := d.SoftDeleteIssue(ctx, 9999, "agent")
	assert.True(t, errors.Is(err, db.ErrNotFound))
}

func TestRestoreIssue_ClearsDeletedAtAndEmitsEvent(t *testing.T) {
	d, ctx, _, issue := setupSoftDeletedIssue(t)

	updated, evt, changed, err := d.RestoreIssue(ctx, issue.ID, "agent")
	require.NoError(t, err)
	require.NotNil(t, evt)
	assert.True(t, changed)
	assert.Equal(t, "issue.restored", evt.Type)
	assert.Nil(t, updated.DeletedAt)
}

func TestRestoreIssue_NotDeletedIsNoOp(t *testing.T) {
	d, ctx, _, issue := setupTestIssue(t)

	_, evt, changed, err := d.RestoreIssue(ctx, issue.ID, "agent")
	require.NoError(t, err)
	assert.Nil(t, evt)
	assert.False(t, changed)
}

func TestRestoreIssue_UnknownIssueIsErrNotFound(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	_, _, _, err := d.RestoreIssue(ctx, 9999, "agent")
	assert.True(t, errors.Is(err, db.ErrNotFound))
}

func TestSoftDeleteRestore_RoundTripVisibility(t *testing.T) {
	d, ctx, p := setupTestProject(t)
	issue, _ := createTesterIssue(ctx, t, d, p.ID, "round trip")

	// Initial: visible in default list.
	listed, err := d.ListIssues(ctx, db.ListIssuesParams{ProjectID: p.ID})
	require.NoError(t, err)
	require.Len(t, listed, 1)
	assert.Equal(t, issue.ID, listed[0].ID)

	// After soft-delete: hidden from default list.
	_, _, _, err = d.SoftDeleteIssue(ctx, issue.ID, "agent")
	require.NoError(t, err)
	listed, err = d.ListIssues(ctx, db.ListIssuesParams{ProjectID: p.ID})
	require.NoError(t, err)
	assert.Empty(t, listed, "soft-deleted issue must be hidden from default list")

	// After restore: visible again.
	_, _, _, err = d.RestoreIssue(ctx, issue.ID, "agent")
	require.NoError(t, err)
	listed, err = d.ListIssues(ctx, db.ListIssuesParams{ProjectID: p.ID})
	require.NoError(t, err)
	require.Len(t, listed, 1)
	assert.Equal(t, issue.ID, listed[0].ID, "restored issue is visible again")
}

func TestRestoreIssue_EmitsEventWithPayloadAndRefs(t *testing.T) {
	d, ctx, _, issue := setupSoftDeletedIssue(t)

	updated, evt, changed, err := d.RestoreIssue(ctx, issue.ID, "agent")
	require.NoError(t, err)
	require.NotNil(t, evt)
	assert.True(t, changed)
	assert.JSONEq(t, "{}", string(evt.Payload), "restored event has empty payload")
	require.NotNil(t, evt.IssueID)
	assert.Equal(t, issue.ID, *evt.IssueID)
	require.NotNil(t, evt.IssueNumber)
	assert.Equal(t, issue.Number, *evt.IssueNumber)
	assert.True(t, updated.UpdatedAt.After(issue.UpdatedAt) || updated.UpdatedAt.Equal(issue.UpdatedAt),
		"updated_at must not regress on restore")
}

func TestSoftDeleteIssue_ScopesByIssueID(t *testing.T) {
	// SoftDeleteIssue takes an issue ID, not a project ID — it must work
	// across projects without requiring a project context.
	d := openTestDB(t)
	ctx := context.Background()
	p1, err := d.CreateProject(ctx, "p1", "p1")
	require.NoError(t, err)
	p2, err := d.CreateProject(ctx, "p2", "p2")
	require.NoError(t, err)
	_, _, err = d.CreateIssue(ctx, db.CreateIssueParams{
		ProjectID: p1.ID, Title: "in p1", Author: "tester",
	})
	require.NoError(t, err)
	target, _, err := d.CreateIssue(ctx, db.CreateIssueParams{
		ProjectID: p2.ID, Title: "in p2", Author: "tester",
	})
	require.NoError(t, err)

	updated, evt, changed, err := d.SoftDeleteIssue(ctx, target.ID, "agent")
	require.NoError(t, err)
	require.NotNil(t, evt)
	assert.True(t, changed)
	require.NotNil(t, updated.DeletedAt)
	assert.Equal(t, p2.ID, updated.ProjectID, "deleted issue belongs to p2")
}

func TestPurgeIssue_RemovesAllDependentsAndAudits(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p := createKataProject(ctx, t, d)
	target, _, err := d.CreateIssue(ctx, db.CreateIssueParams{
		ProjectID: p.ID, Title: "delete me", Body: "body", Author: "tester",
	})
	require.NoError(t, err)
	keeper, _ := createTesterIssue(ctx, t, d, p.ID, "keep me")

	// Add a comment, a label (with event), and a link from keeper → target so
	// cascade removes a non-trivial set of dependents.
	_, _, err = d.CreateComment(ctx, db.CreateCommentParams{
		IssueID: target.ID, Author: "tester", Body: "comment body",
	})
	require.NoError(t, err)
	_, _, err = d.AddLabelAndEvent(ctx, target.ID, db.LabelEventParams{
		EventType: "issue.labeled", Label: "bug", Actor: "tester",
	})
	require.NoError(t, err)
	_, _, err = d.CreateLinkAndEvent(ctx, db.CreateLinkParams{
		ProjectID: p.ID, FromIssueID: keeper.ID, ToIssueID: target.ID,
		Type: "blocks", Author: "tester",
	}, db.LinkEventParams{
		EventType: "issue.linked", EventIssueID: keeper.ID, EventIssueNumber: keeper.Number,
		FromNumber: keeper.Number, ToNumber: target.Number, Actor: "tester",
	})
	require.NoError(t, err)

	pl, err := d.PurgeIssue(ctx, target.ID, "agent", nil)
	require.NoError(t, err)

	assert.Equal(t, target.ID, pl.PurgedIssueID)
	assert.Equal(t, target.UID, *pl.IssueUID)
	assert.Equal(t, p.UID, *pl.ProjectUID)
	assert.Equal(t, "github.com/wesm/kata", pl.ProjectIdentity)
	assert.Equal(t, target.Number, pl.IssueNumber)
	assert.Equal(t, "delete me", pl.IssueTitle)
	assert.Equal(t, "tester", pl.IssueAuthor)
	assert.Equal(t, int64(1), pl.CommentCount)
	assert.Equal(t, int64(1), pl.LinkCount)
	assert.Equal(t, int64(1), pl.LabelCount)
	// Events: issue.created + issue.commented + issue.labeled = 3 attached to target,
	// plus 1 issue.linked attributed to target via related_issue_id (keeper's event).
	// Total: 4.
	assert.Equal(t, int64(4), pl.EventCount)
	require.NotNil(t, pl.EventsDeletedMinID)
	require.NotNil(t, pl.EventsDeletedMaxID)
	require.NotNil(t, pl.PurgeResetAfterEventID, "events were deleted, reset cursor must be set")

	// Verify rows actually gone.
	assertRowCount(ctx, t, d, 0, "issue row removed",
		`SELECT count(*) FROM issues WHERE id = ?`, target.ID)
	assertRowCount(ctx, t, d, 0, "comments removed",
		`SELECT count(*) FROM comments WHERE issue_id = ?`, target.ID)
	assertRowCount(ctx, t, d, 0, "links removed",
		`SELECT count(*) FROM links WHERE from_issue_id = ? OR to_issue_id = ?`,
		target.ID, target.ID)
	assertRowCount(ctx, t, d, 0, "labels removed",
		`SELECT count(*) FROM issue_labels WHERE issue_id = ?`, target.ID)
	assertRowCount(ctx, t, d, 0, "events removed",
		`SELECT count(*) FROM events WHERE issue_id = ? OR related_issue_id = ?`,
		target.ID, target.ID)
	assertRowCount(ctx, t, d, 0, "FTS row removed",
		`SELECT count(*) FROM issues_fts WHERE rowid = ?`, target.ID)

	// keeper's events.created is the only event attributed to keeper that
	// survives — keeper's issue.linked was deleted because related_issue_id
	// pointed to target.
	assertRowCount(ctx, t, d, 1,
		"keeper's issue.created survives; its issue.linked was cascade-deleted via related_issue_id",
		`SELECT count(*) FROM events WHERE issue_id = ?`, keeper.ID)
}

func TestPurgeIssue_NoEventsLeavesResetCursorNull(t *testing.T) {
	// Manually craft an issue row with no events: insert directly so we
	// bypass CreateIssue's automatic issue.created event. Verify that
	// PurgeIssue sees zero attached events and leaves PurgeResetAfterEventID
	// as nil (no SSE cursor reservation needed).
	d, ctx, p := setupTestProject(t)
	issueUID, err := uid.New()
	require.NoError(t, err)
	res, err := d.ExecContext(ctx,
		`INSERT INTO issues(uid, project_id, number, title, author) VALUES(?, ?, 1, 'no-events', 'tester')`,
		issueUID, p.ID)
	require.NoError(t, err)
	id, err := res.LastInsertId()
	require.NoError(t, err)

	pl, err := d.PurgeIssue(ctx, id, "agent", nil)
	require.NoError(t, err)
	assert.Equal(t, int64(0), pl.EventCount)
	assert.Nil(t, pl.EventsDeletedMinID)
	assert.Nil(t, pl.EventsDeletedMaxID)
	assert.Nil(t, pl.PurgeResetAfterEventID, "no events deleted → no reset cursor")
}

func TestPurgeIssue_ReservesSqliteSequenceAboveMaxEventID(t *testing.T) {
	d, ctx, p, target := setupTestIssue(t)
	// Capture max events.id BEFORE purge.
	var maxBefore int64
	require.NoError(t, d.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(id), 0) FROM events`).Scan(&maxBefore))

	pl, err := d.PurgeIssue(ctx, target.ID, "agent", nil)
	require.NoError(t, err)
	require.NotNil(t, pl.PurgeResetAfterEventID)
	assert.Greater(t, *pl.PurgeResetAfterEventID, maxBefore,
		"reserved cursor must exceed every events.id that existed at purge time")

	// Now create another issue and verify the next events.id is strictly
	// greater than the reserved cursor.
	_, evt, err := d.CreateIssue(ctx, db.CreateIssueParams{
		ProjectID: p.ID, Title: "next", Author: "tester",
	})
	require.NoError(t, err)
	assert.Greater(t, evt.ID, *pl.PurgeResetAfterEventID,
		"next real events.id must continue from reserved+1")
}

func TestPurgeIssue_UnknownIssueIsErrNotFound(t *testing.T) {
	d := openTestDB(t)
	_, err := d.PurgeIssue(context.Background(), 9999, "agent", nil)
	assert.True(t, errors.Is(err, db.ErrNotFound))
}

func TestPurgeIssue_PersistsReason(t *testing.T) {
	// Reason threads through to purge_log.reason and round-trips on the
	// returned PurgeLog. Catches argument-order regressions in the INSERT.
	d, ctx, _, target := setupTestIssue(t)

	reason := "ops cleanup"
	pl, err := d.PurgeIssue(ctx, target.ID, "agent", &reason)
	require.NoError(t, err)
	require.NotNil(t, pl.Reason)
	assert.Equal(t, "ops cleanup", *pl.Reason)
}

func TestPurgeIssue_OnSoftDeletedIssue(t *testing.T) {
	// PurgeIssue must work on already soft-deleted issues — the destructive
	// ladder is delete → purge, not delete-XOR-purge. lookupIssueIncludingDeleted
	// is the right primitive; this test pins the contract so a future swap
	// to a deleted-filtering lookup would fail loudly.
	d, ctx, _, target := setupSoftDeletedIssue(t)

	pl, err := d.PurgeIssue(ctx, target.ID, "agent", nil)
	require.NoError(t, err)
	assert.Equal(t, target.ID, pl.PurgedIssueID)

	// Row is gone from issues.
	assertRowCount(ctx, t, d, 0, "issue row removed even though it was soft-deleted first",
		`SELECT count(*) FROM issues WHERE id = ?`, target.ID)
}
