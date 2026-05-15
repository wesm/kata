package db_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wesm/kata/internal/db"
)

func TestMoveIssueProject_HappyPath(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	srcP, err := d.CreateProject(ctx, "src")
	require.NoError(t, err)
	tgtP, err := d.CreateProject(ctx, "target")
	require.NoError(t, err)
	srcI := seedIssueInProject(t, d, srcP.ID, "Move me", "tester")

	res, err := d.MoveIssueProject(ctx, db.MoveIssueProjectIn{
		IssueID:       srcI.ID,
		FromProjectID: srcP.ID,
		ToProjectID:   tgtP.ID,
		IfMatchRev:    1,
		Actor:         "tester",
	})
	require.NoError(t, err)
	assert.Equal(t, tgtP.ID, res.Issue.ProjectID)
	assert.NotEmpty(t, res.NewShortID)
	assert.Equal(t, int64(2), res.NewRevision)

	var projectUID string
	require.NoError(t, d.QueryRowContext(ctx,
		`SELECT p.uid FROM issues i JOIN projects p ON p.id = i.project_id
           WHERE i.id = ?`, srcI.ID,
	).Scan(&projectUID))
	assert.Equal(t, tgtP.UID, projectUID)

	var payload string
	require.NoError(t, d.QueryRowContext(ctx,
		`SELECT payload FROM events WHERE type='issue.moved'
           ORDER BY id DESC LIMIT 1`).Scan(&payload))
	assert.Contains(t, payload, `"from_project_uid":"`+srcP.UID+`"`)
	assert.Contains(t, payload, `"to_project_uid":"`+tgtP.UID+`"`)
	assert.Contains(t, payload, `"from_short_id":"`+srcI.ShortID+`"`)
	assert.Contains(t, payload, `"to_short_id":"`+res.NewShortID+`"`)
}

func TestMoveIssueProject_RefusesCrossProjectLinks(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	srcP, _ := d.CreateProject(ctx, "src")
	tgtP, _ := d.CreateProject(ctx, "target")
	a := seedIssueInProject(t, d, srcP.ID, "A", "tester")
	b := seedIssueInProject(t, d, srcP.ID, "B", "tester")
	seedLink(t, d, srcP.ID, a.ID, b.ID, "blocks", "tester")

	_, err := d.MoveIssueProject(ctx, db.MoveIssueProjectIn{
		IssueID: a.ID, FromProjectID: srcP.ID, ToProjectID: tgtP.ID,
		IfMatchRev: 1, Actor: "tester",
	})
	var cpe *db.CrossProjectLinksError
	require.ErrorAs(t, err, &cpe)
	assert.GreaterOrEqual(t, len(cpe.Blockers), 1)
}

func TestMoveIssueProject_RefusesRecurrencePinned(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	src, _ := d.CreateProject(ctx, "src")
	tgt, _ := d.CreateProject(ctx, "target")
	iss := seedIssueInProject(t, d, src.ID, "weekly", "tester")
	rid := seedRecurrence(t, d, src.ID, "FREQ=WEEKLY", "2026-05-11", "UTC", "tpl")
	_, err := d.ExecContext(ctx,
		`UPDATE issues SET recurrence_id = ?, occurrence_key = '2026-05-11' WHERE id = ?`,
		rid, iss.ID)
	require.NoError(t, err)

	_, err = d.MoveIssueProject(ctx, db.MoveIssueProjectIn{
		IssueID: iss.ID, FromProjectID: src.ID, ToProjectID: tgt.ID,
		IfMatchRev: 1, Actor: "tester",
	})
	var rpe *db.RecurrencePinnedError
	require.ErrorAs(t, err, &rpe)
}

func TestMoveIssueProject_RefusesRevisionConflict(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	src, _ := d.CreateProject(ctx, "src")
	tgt, _ := d.CreateProject(ctx, "target")
	iss := seedIssueInProject(t, d, src.ID, "stale", "tester")

	_, err := d.MoveIssueProject(ctx, db.MoveIssueProjectIn{
		IssueID: iss.ID, FromProjectID: src.ID, ToProjectID: tgt.ID,
		IfMatchRev: 99, Actor: "tester",
	})
	var rce *db.RevisionConflictError
	require.ErrorAs(t, err, &rce)
	assert.Equal(t, int64(1), rce.CurrentRevision)
}

func TestMoveIssueProject_RefusesSameProject(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p, _ := d.CreateProject(ctx, "p")
	iss := seedIssueInProject(t, d, p.ID, "same", "tester")

	_, err := d.MoveIssueProject(ctx, db.MoveIssueProjectIn{
		IssueID: iss.ID, FromProjectID: p.ID, ToProjectID: p.ID,
		IfMatchRev: 1, Actor: "tester",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "same")
}
