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
	d, _, _, rec := setupRecurrence(t, db.CreateRecurrenceIn{
		Rule:     "FREQ=WEEKLY;BYDAY=MO",
		DTStart:  "2026-05-11",
		Timezone: "America/New_York",
		Template: db.RecurrenceTemplate{
			Title: "Weekly review",
			Body:  "What got done?",
		},
	})
	assert.Len(t, rec.UID, 26)
	assert.Equal(t, "FREQ=WEEKLY;BYDAY=MO", rec.RRule)
	assert.Equal(t, "Weekly review", rec.TemplateTitle)
	assert.Equal(t, int64(1), rec.Revision)

	assertEventCount(t, d, "recurrence.created", 1)
}

func TestPatchRecurrence_BumpsRevisionAndEmitsDiff(t *testing.T) {
	d, ctx, _, rec := setupRecurrence(t, db.CreateRecurrenceIn{
		Rule: "FREQ=WEEKLY", DTStart: "2026-05-15", Timezone: "UTC",
		Template: db.RecurrenceTemplate{Title: "Old"},
	})

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
	d, ctx, _, rec := setupRecurrence(t, db.CreateRecurrenceIn{
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

	assertEventCount(t, d, "recurrence.updated", 0)
}

func TestPatchRecurrence_RevisionConflict(t *testing.T) {
	d, ctx, _, rec := setupRecurrence(t, db.CreateRecurrenceIn{
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
	d, ctx, p, rec := setupRecurrence(t, db.CreateRecurrenceIn{
		Rule: "FREQ=WEEKLY", DTStart: "2026-05-15", Timezone: "UTC",
		Template: db.RecurrenceTemplate{Title: "X"},
	})
	require.NoError(t, d.SoftDeleteRecurrence(ctx, rec.ID, "tester"))

	list, err := d.ListRecurrencesByProject(ctx, p.ID)
	require.NoError(t, err)
	assert.Empty(t, list, "soft-deleted recurrence must not appear in listing")

	assertEventCount(t, d, "recurrence.deleted", 1)

	// Fetching by ID still works (soft-delete is preserved).
	got, err := d.GetRecurrenceByID(ctx, rec.ID)
	require.NoError(t, err)
	require.NotNil(t, got.DeletedAt, "DeletedAt must be set after soft-delete")
}

func TestGetRecurrenceByUID(t *testing.T) {
	d, ctx, _, rec := setupRecurrence(t, db.CreateRecurrenceIn{
		Rule: "FREQ=WEEKLY", DTStart: "2026-05-15", Timezone: "UTC",
		Template: db.RecurrenceTemplate{Title: "X"},
	})
	got, err := d.GetRecurrenceByUID(ctx, rec.UID)
	require.NoError(t, err)
	assert.Equal(t, rec.ID, got.ID)
}

func TestGetRecurrenceByUID_MissingReturnsErrNotFound(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	_, err := d.GetRecurrenceByUID(ctx, "nosuchuid")
	assert.ErrorIs(t, err, db.ErrNotFound)
}

func TestListRecurrencesByProject_ExcludesArchivedProject(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	p, err := d.CreateProject(ctx, "archived-p")
	require.NoError(t, err)

	_, err = d.CreateRecurrence(ctx, db.CreateRecurrenceIn{
		ProjectID: p.ID, Actor: "tester",
		Rule: "FREQ=WEEKLY", DTStart: "2026-05-15", Timezone: "UTC",
		Template: db.RecurrenceTemplate{Title: "should disappear"},
	})
	require.NoError(t, err)

	// Archive the project.
	_, err = d.ExecContext(ctx,
		`UPDATE projects SET deleted_at = strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id = ?`, p.ID)
	require.NoError(t, err)

	// Listing must return empty — the project is archived.
	list, err := d.ListRecurrencesByProject(ctx, p.ID)
	require.NoError(t, err)
	assert.Empty(t, list, "archived project's recurrences must not appear in listing")
}

func TestCreateRecurrence_DedupesTemplateLabels(t *testing.T) {
	// Labels with duplicates, varied case, and extra whitespace.
	_, _, _, rec := setupRecurrence(t, db.CreateRecurrenceIn{
		Rule: "FREQ=WEEKLY", DTStart: "2026-05-15", Timezone: "UTC",
		Template: db.RecurrenceTemplate{
			Title:  "dedup",
			Labels: []string{"foo", "Foo", "  foo  ", "bar"},
		},
	})

	// Stored labels must be normalized and sorted.
	assert.JSONEq(t, `["bar","foo"]`, string(rec.TemplateLabels))
}

func TestCreateRecurrence_RejectsInvalidLabel(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p, _ := d.CreateProject(ctx, "p")

	// "hello world" contains a space (after trimming the full label, spaces
	// inside the label are disallowed by the schema CHECK pattern).
	_, err := d.CreateRecurrence(ctx, db.CreateRecurrenceIn{
		ProjectID: p.ID, Actor: "tester",
		Rule: "FREQ=WEEKLY", DTStart: "2026-05-15", Timezone: "UTC",
		Template: db.RecurrenceTemplate{
			Title:  "bad label",
			Labels: []string{"valid", "hello world"},
		},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid character")
}

// TestCreateRecurrence_SeedsNextOccurrenceCursor pins the invariant that a
// freshly-created recurrence has a populated next_occurrence_key so callers
// reading the cursor don't see NULL (which MaterializeNext docs as the
// exhausted-state signal).
func TestCreateRecurrence_SeedsNextOccurrenceCursor(t *testing.T) {
	_, _, _, rec := setupRecurrence(t, db.CreateRecurrenceIn{
		Rule: "FREQ=WEEKLY", DTStart: "2026-05-15", Timezone: "UTC",
		Template: db.RecurrenceTemplate{Title: "weekly review"},
	})
	require.NotNil(t, rec.NextOccurrenceKey, "next_occurrence_key must be set at create time")
	assert.Equal(t, "2026-05-15", *rec.NextOccurrenceKey)
}

// TestCreateRecurrence_RejectsBadRecurrenceInputs covers create-time validation
// of rrule / dtstart / timezone. The validation is a side effect of computing
// the initial cursor — if recurrence.Next can't evaluate the inputs, the row
// must not be persisted.
func TestCreateRecurrence_RejectsBadRecurrenceInputs(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p, err := d.CreateProject(ctx, "p")
	require.NoError(t, err)

	cases := []struct {
		name string
		in   db.CreateRecurrenceIn
	}{
		{
			"bad_timezone",
			db.CreateRecurrenceIn{
				ProjectID: p.ID, Actor: "tester",
				Rule: "FREQ=WEEKLY", DTStart: "2026-05-15", Timezone: "Mars/Phobos",
				Template: db.RecurrenceTemplate{Title: "x"},
			},
		},
		{
			"malformed_dtstart",
			db.CreateRecurrenceIn{
				ProjectID: p.ID, Actor: "tester",
				Rule: "FREQ=WEEKLY", DTStart: "not-a-date", Timezone: "UTC",
				Template: db.RecurrenceTemplate{Title: "x"},
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := d.CreateRecurrence(ctx, c.in)
			require.Error(t, err)
		})
	}
}

func TestMaterializeNext_NormalizesLegacyDuplicateLabels(t *testing.T) {
	// Create a recurrence the normal way, then bypass dedupe normalization by
	// overwriting template_labels directly with a duplicate-containing array.
	d, ctx, p, rec := setupRecurrence(t, db.CreateRecurrenceIn{
		Rule: "FREQ=WEEKLY", DTStart: "2026-05-15", Timezone: "UTC",
		Template: db.RecurrenceTemplate{Title: "t"},
	})
	_, err := d.ExecContext(ctx,
		`UPDATE recurrences SET template_labels = ? WHERE id = ?`,
		`["foo","foo","bar"]`, rec.ID)
	require.NoError(t, err)

	// Seed the first instance and close it to trigger MaterializeNext.
	firstID, _ := seedRecurrenceInstance(t, d, p.ID, rec.ID, "2026-05-15", "t")
	_, _, _, err = d.CloseIssue(ctx, firstID, "done", "tester", "", nil)
	require.NoError(t, err)

	// The new instance (2026-05-22) must exist and carry deduplicated labels.
	var newIssueID int64
	require.NoError(t, d.QueryRowContext(ctx,
		`SELECT id FROM issues WHERE recurrence_id = ? AND occurrence_key = ?`,
		rec.ID, "2026-05-22",
	).Scan(&newIssueID))

	rows, err := d.QueryContext(ctx,
		`SELECT label FROM issue_labels WHERE issue_id = ? ORDER BY label ASC`, newIssueID)
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()
	var got []string
	for rows.Next() {
		var lbl string
		require.NoError(t, rows.Scan(&lbl))
		got = append(got, lbl)
	}
	require.NoError(t, rows.Err())
	assert.Equal(t, []string{"bar", "foo"}, got, "labels must be sorted and deduplicated")
}
