package db_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wesm/kata/internal/db"
)

func TestCreateRecurrence_HappyPath(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p, err := d.CreateProject(ctx, "p")
	require.NoError(t, err)

	rec, err := d.CreateRecurrence(ctx, db.CreateRecurrenceIn{
		ProjectID: p.ID,
		Actor:     "tester",
		Rule:      "FREQ=WEEKLY;BYDAY=MO",
		DTStart:   "2026-05-11",
		Timezone:  "America/New_York",
		Template: db.RecurrenceTemplate{
			Title: "Weekly review",
			Body:  "What got done?",
		},
	})
	require.NoError(t, err)
	assert.Len(t, rec.UID, 26)
	assert.Equal(t, "FREQ=WEEKLY;BYDAY=MO", rec.RRule)
	assert.Equal(t, "Weekly review", rec.TemplateTitle)
	assert.Equal(t, int64(1), rec.Revision)

	var n int
	require.NoError(t, d.QueryRow(`SELECT COUNT(*) FROM events
        WHERE type='recurrence.created'`).Scan(&n))
	assert.Equal(t, 1, n)
}

func TestPatchRecurrence_BumpsRevisionAndEmitsDiff(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p, err := d.CreateProject(ctx, "p")
	require.NoError(t, err)

	rec, err := d.CreateRecurrence(ctx, db.CreateRecurrenceIn{
		ProjectID: p.ID, Actor: "tester",
		Rule: "FREQ=WEEKLY", DTStart: "2026-05-15", Timezone: "UTC",
		Template: db.RecurrenceTemplate{Title: "Old"},
	})
	require.NoError(t, err)

	res, err := d.PatchRecurrence(ctx, db.PatchRecurrenceIn{
		RecurrenceID: rec.ID, IfMatchRev: 1, Actor: "tester",
		Update: db.RecurrenceUpdate{
			Rule:          strPtr("FREQ=DAILY"),
			TemplateTitle: strPtr("New"),
		},
	})
	require.NoError(t, err)
	assert.True(t, res.Changed)
	assert.Equal(t, int64(2), res.NewRevision)

	var payload string
	require.NoError(t, d.QueryRow(`SELECT payload FROM events
        WHERE type='recurrence.updated' ORDER BY id DESC LIMIT 1`).Scan(&payload))
	var p2 struct {
		Diff map[string]struct {
			From, To json.RawMessage
		} `json:"diff"`
	}
	require.NoError(t, json.Unmarshal([]byte(payload), &p2))
	assert.Contains(t, p2.Diff, "rrule")
	assert.Contains(t, p2.Diff, "template_title")
}

func TestPatchRecurrence_NoChangeIsNoOp(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p, _ := d.CreateProject(ctx, "p")
	rec, _ := d.CreateRecurrence(ctx, db.CreateRecurrenceIn{
		ProjectID: p.ID, Actor: "tester",
		Rule: "FREQ=WEEKLY", DTStart: "2026-05-15", Timezone: "UTC",
		Template: db.RecurrenceTemplate{Title: "Same"},
	})

	// Patch with the same values → no change.
	res, err := d.PatchRecurrence(ctx, db.PatchRecurrenceIn{
		RecurrenceID: rec.ID, IfMatchRev: 1, Actor: "tester",
		Update: db.RecurrenceUpdate{TemplateTitle: strPtr("Same")},
	})
	require.NoError(t, err)
	assert.False(t, res.Changed)
	assert.Equal(t, int64(1), res.NewRevision)

	var n int
	require.NoError(t, d.QueryRow(`SELECT COUNT(*) FROM events
        WHERE type='recurrence.updated'`).Scan(&n))
	assert.Equal(t, 0, n, "no-op patch must not emit recurrence.updated event")
}

func TestPatchRecurrence_RevisionConflict(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p, _ := d.CreateProject(ctx, "p")
	rec, _ := d.CreateRecurrence(ctx, db.CreateRecurrenceIn{
		ProjectID: p.ID, Actor: "tester",
		Rule: "FREQ=WEEKLY", DTStart: "2026-05-15", Timezone: "UTC",
		Template: db.RecurrenceTemplate{Title: "X"},
	})
	_, err := d.PatchRecurrence(ctx, db.PatchRecurrenceIn{
		RecurrenceID: rec.ID, IfMatchRev: 99, Actor: "tester",
		Update: db.RecurrenceUpdate{TemplateTitle: strPtr("Y")},
	})
	var rce *db.RevisionConflictError
	require.ErrorAs(t, err, &rce)
	assert.Equal(t, int64(1), rce.CurrentRevision)
}

func TestSoftDeleteRecurrence(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p, _ := d.CreateProject(ctx, "p")
	rec, _ := d.CreateRecurrence(ctx, db.CreateRecurrenceIn{
		ProjectID: p.ID, Actor: "tester",
		Rule: "FREQ=WEEKLY", DTStart: "2026-05-15", Timezone: "UTC",
		Template: db.RecurrenceTemplate{Title: "X"},
	})
	require.NoError(t, d.SoftDeleteRecurrence(ctx, rec.ID, "tester"))

	list, err := d.ListRecurrencesByProject(ctx, p.ID)
	require.NoError(t, err)
	assert.Empty(t, list, "soft-deleted recurrence must not appear in listing")

	var n int
	require.NoError(t, d.QueryRow(`SELECT COUNT(*) FROM events
        WHERE type='recurrence.deleted'`).Scan(&n))
	assert.Equal(t, 1, n)

	// Fetching by ID still works (soft-delete is preserved).
	got, err := d.GetRecurrenceByID(ctx, rec.ID)
	require.NoError(t, err)
	require.NotNil(t, got.DeletedAt, "DeletedAt must be set after soft-delete")
}

func TestGetRecurrenceByUID(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p, _ := d.CreateProject(ctx, "p")
	rec, _ := d.CreateRecurrence(ctx, db.CreateRecurrenceIn{
		ProjectID: p.ID, Actor: "tester",
		Rule: "FREQ=WEEKLY", DTStart: "2026-05-15", Timezone: "UTC",
		Template: db.RecurrenceTemplate{Title: "X"},
	})
	got, err := d.GetRecurrenceByUID(ctx, rec.UID)
	require.NoError(t, err)
	assert.Equal(t, rec.ID, got.ID)
}
