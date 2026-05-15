package db_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wesm/kata/internal/db"
)

func TestCloseDone_MaterializesNextRecurrence(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p, err := d.CreateProject(ctx, "p")
	require.NoError(t, err)
	rec, err := d.CreateRecurrence(ctx, db.CreateRecurrenceIn{
		ProjectID: p.ID, Actor: "tester",
		Rule: "FREQ=WEEKLY", DTStart: "2026-05-11", Timezone: "UTC",
		Template: db.RecurrenceTemplate{Title: "Pay rent"},
	})
	require.NoError(t, err)
	firstID, _ := seedRecurrenceInstance(t, d, p.ID, rec.ID, "2026-05-11", "Pay rent")

	_, _, _, err = d.CloseIssue(ctx, firstID, "done", "tester", "paid", nil)
	require.NoError(t, err)

	// Next-week instance must exist.
	var n int
	require.NoError(t, d.QueryRow(`
		SELECT COUNT(*) FROM issues
		 WHERE recurrence_id = ? AND occurrence_key = ?`,
		rec.ID, "2026-05-18",
	).Scan(&n))
	assert.Equal(t, 1, n, "next-week instance should be auto-created")

	// recurrence.materialized event was emitted.
	require.NoError(t, d.QueryRow(`
		SELECT COUNT(*) FROM events WHERE type='recurrence.materialized'
	`).Scan(&n))
	assert.Equal(t, 1, n, "recurrence.materialized event should be emitted")

	// issue.created event for the new instance should also exist.
	require.NoError(t, d.QueryRow(`
		SELECT COUNT(*) FROM events WHERE type='issue.created'
		  AND issue_id = (SELECT id FROM issues WHERE recurrence_id = ? AND occurrence_key = ?)`,
		rec.ID, "2026-05-18",
	).Scan(&n))
	assert.Equal(t, 1, n, "issue.created event should be emitted for new instance")

	// recurrence next_occurrence_key should advance to the week after 2026-05-18.
	var nextKey *string
	require.NoError(t, d.QueryRow(`
		SELECT next_occurrence_key FROM recurrences WHERE id = ?`, rec.ID,
	).Scan(&nextKey))
	require.NotNil(t, nextKey)
	assert.Equal(t, "2026-05-25", *nextKey,
		"next_occurrence_key should advance two steps past the closed instance")
}

func TestCloseWontfix_DoesNotMaterialize(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p, err := d.CreateProject(ctx, "p")
	require.NoError(t, err)
	rec, err := d.CreateRecurrence(ctx, db.CreateRecurrenceIn{
		ProjectID: p.ID, Actor: "tester",
		Rule: "FREQ=WEEKLY", DTStart: "2026-05-11", Timezone: "UTC",
		Template: db.RecurrenceTemplate{Title: "x"},
	})
	require.NoError(t, err)
	firstID, _ := seedRecurrenceInstance(t, d, p.ID, rec.ID, "2026-05-11", "x")

	_, _, _, err = d.CloseIssue(ctx, firstID, "wontfix", "tester", "skip", nil)
	require.NoError(t, err)

	var n int
	require.NoError(t, d.QueryRow(`
		SELECT COUNT(*) FROM events WHERE type='recurrence.materialized'
	`).Scan(&n))
	assert.Equal(t, 0, n, "wontfix close must not trigger materialization")

	// Confirm no new recurrence instance was created.
	require.NoError(t, d.QueryRow(`
		SELECT COUNT(*) FROM issues WHERE recurrence_id = ? AND status='open'`, rec.ID,
	).Scan(&n))
	assert.Equal(t, 0, n, "no open instances should exist after wontfix close")
}

func TestCloseDone_NonRecurrenceIssueIsNoOp(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p, err := d.CreateProject(ctx, "p")
	require.NoError(t, err)
	iss := seedIssueInProject(t, d, p.ID, "regular", "tester")

	_, _, _, err = d.CloseIssue(ctx, iss.ID, "done", "tester", "msg", nil)
	require.NoError(t, err)

	// No recurrence-related events.
	var n int
	require.NoError(t, d.QueryRow(`
		SELECT COUNT(*) FROM events
		 WHERE type IN (
		   'recurrence.materialized',
		   'recurrence.materialization_skipped',
		   'recurrence.exhausted'
		 )
	`).Scan(&n))
	assert.Equal(t, 0, n, "non-recurrence issue close must not emit recurrence events")
}

func TestCloseDone_LabelsSeededFromTemplate(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p, err := d.CreateProject(ctx, "p")
	require.NoError(t, err)
	rec, err := d.CreateRecurrence(ctx, db.CreateRecurrenceIn{
		ProjectID: p.ID, Actor: "tester",
		Rule: "FREQ=WEEKLY", DTStart: "2026-05-11", Timezone: "UTC",
		Template: db.RecurrenceTemplate{
			Title:  "Weekly sync",
			Labels: []string{"recurring", "auto"},
		},
	})
	require.NoError(t, err)
	firstID, _ := seedRecurrenceInstance(t, d, p.ID, rec.ID, "2026-05-11", "Weekly sync")

	_, _, _, err = d.CloseIssue(ctx, firstID, "done", "tester", "", nil)
	require.NoError(t, err)

	// Retrieve the new instance's ID.
	var newID int64
	require.NoError(t, d.QueryRow(`
		SELECT id FROM issues WHERE recurrence_id = ? AND occurrence_key = ?`,
		rec.ID, "2026-05-18",
	).Scan(&newID))

	// Both template labels must be present.
	var labelCount int
	require.NoError(t, d.QueryRow(`
		SELECT COUNT(*) FROM issue_labels WHERE issue_id = ?`, newID,
	).Scan(&labelCount))
	assert.Equal(t, 2, labelCount, "new instance should inherit both template labels")
}

func TestCloseDone_MetadataContainsScheduledOn(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p, err := d.CreateProject(ctx, "p")
	require.NoError(t, err)
	rec, err := d.CreateRecurrence(ctx, db.CreateRecurrenceIn{
		ProjectID: p.ID, Actor: "tester",
		Rule: "FREQ=WEEKLY", DTStart: "2026-05-11", Timezone: "UTC",
		Template: db.RecurrenceTemplate{Title: "t"},
	})
	require.NoError(t, err)
	firstID, _ := seedRecurrenceInstance(t, d, p.ID, rec.ID, "2026-05-11", "t")

	_, _, _, err = d.CloseIssue(ctx, firstID, "done", "tester", "", nil)
	require.NoError(t, err)

	var metadata string
	require.NoError(t, d.QueryRow(`
		SELECT metadata FROM issues WHERE recurrence_id = ? AND occurrence_key = ?`,
		rec.ID, "2026-05-18",
	).Scan(&metadata))
	assert.Contains(t, metadata, `"scheduled_on"`)
	assert.Contains(t, metadata, "2026-05-18")
}
