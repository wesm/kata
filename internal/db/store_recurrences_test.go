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
	d := openTestDB(t)
	ctx := context.Background()
	p, _ := d.CreateProject(ctx, "p")

	// Labels with duplicates, varied case, and extra whitespace.
	rec, err := d.CreateRecurrence(ctx, db.CreateRecurrenceIn{
		ProjectID: p.ID, Actor: "tester",
		Rule: "FREQ=WEEKLY", DTStart: "2026-05-15", Timezone: "UTC",
		Template: db.RecurrenceTemplate{
			Title:  "dedup",
			Labels: []string{"foo", "Foo", "  foo  ", "bar"},
		},
	})
	require.NoError(t, err)

	// Stored labels must be normalized and sorted.
	assert.Equal(t, `["bar","foo"]`, rec.TemplateLabels)
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

func TestMaterializeNext_NormalizesLegacyDuplicateLabels(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p, err := d.CreateProject(ctx, "p")
	require.NoError(t, err)

	// Create a recurrence the normal way, then bypass dedupe normalization by
	// overwriting template_labels directly with a duplicate-containing array.
	rec, err := d.CreateRecurrence(ctx, db.CreateRecurrenceIn{
		ProjectID: p.ID, Actor: "tester",
		Rule: "FREQ=WEEKLY", DTStart: "2026-05-15", Timezone: "UTC",
		Template: db.RecurrenceTemplate{Title: "t"},
	})
	require.NoError(t, err)
	_, err = d.ExecContext(ctx,
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

func TestListAllRecurrences_OnlyNonDeletedActiveProjects(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	// Active project + live recurrence (should appear).
	pActive, err := d.CreateProject(ctx, "active")
	require.NoError(t, err)
	recLive, err := d.CreateRecurrence(ctx, db.CreateRecurrenceIn{
		ProjectID: pActive.ID, Actor: "tester",
		Rule: "FREQ=WEEKLY", DTStart: "2026-05-15", Timezone: "UTC",
		Template: db.RecurrenceTemplate{Title: "live"},
	})
	require.NoError(t, err)

	// Soft-deleted project + live recurrence (project archived → exclude).
	pArchived, err := d.CreateProject(ctx, "archived")
	require.NoError(t, err)
	_, err = d.CreateRecurrence(ctx, db.CreateRecurrenceIn{
		ProjectID: pArchived.ID, Actor: "tester",
		Rule: "FREQ=WEEKLY", DTStart: "2026-05-15", Timezone: "UTC",
		Template: db.RecurrenceTemplate{Title: "hidden-by-archive"},
	})
	require.NoError(t, err)
	_, err = d.ExecContext(ctx,
		`UPDATE projects SET deleted_at = strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id = ?`,
		pArchived.ID)
	require.NoError(t, err)

	// Active project + soft-deleted recurrence (recurrence soft-deleted → exclude).
	pActive2, err := d.CreateProject(ctx, "active2")
	require.NoError(t, err)
	recDeleted, err := d.CreateRecurrence(ctx, db.CreateRecurrenceIn{
		ProjectID: pActive2.ID, Actor: "tester",
		Rule: "FREQ=WEEKLY", DTStart: "2026-05-15", Timezone: "UTC",
		Template: db.RecurrenceTemplate{Title: "hidden-by-soft-delete"},
	})
	require.NoError(t, err)
	require.NoError(t, d.SoftDeleteRecurrence(ctx, recDeleted.ID, "tester"))

	got, err := d.ListAllRecurrences(ctx)
	require.NoError(t, err)
	require.Len(t, got, 1, "only the live recurrence in an active project should appear")
	assert.Equal(t, recLive.ID, got[0].ID)
}

func TestLatestInstanceForRecurrence_PicksHighestOccurrenceKey(t *testing.T) {
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

	// Seed two instances with different occurrence keys.
	_, _ = seedRecurrenceInstance(t, d, p.ID, rec.ID, "2026-05-11", "first")
	_, laterUID := seedRecurrenceInstance(t, d, p.ID, rec.ID, "2026-05-18", "second")

	got, err := d.LatestInstanceForRecurrence(ctx, rec.ID)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, laterUID, got.UID)
	require.NotNil(t, got.OccurrenceKey)
	assert.Equal(t, "2026-05-18", *got.OccurrenceKey)
}

func TestLatestInstanceForRecurrence_NoInstances_NilNil(t *testing.T) {
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

	got, err := d.LatestInstanceForRecurrence(ctx, rec.ID)
	require.NoError(t, err)
	assert.Nil(t, got, "recurrence with no instances must return (nil, nil)")
}

func TestLatestInstanceForRecurrence_IgnoresSoftDeletedInstances(t *testing.T) {
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

	_, earlierUID := seedRecurrenceInstance(t, d, p.ID, rec.ID, "2026-05-11", "keep")
	laterID, _ := seedRecurrenceInstance(t, d, p.ID, rec.ID, "2026-05-18", "delete")
	// Soft-delete the later instance — the earlier one should win.
	_, err = d.ExecContext(ctx,
		`UPDATE issues SET deleted_at = strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id = ?`,
		laterID)
	require.NoError(t, err)

	got, err := d.LatestInstanceForRecurrence(ctx, rec.ID)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, earlierUID, got.UID, "soft-deleted instances must be excluded")
}
