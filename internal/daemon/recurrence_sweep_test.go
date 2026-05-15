package daemon_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wesm/kata/internal/daemon"
	"github.com/wesm/kata/internal/db"
)

// openSweepTestDB opens a fresh DB for sweep tests; mirrors openInboxTestDB.
func openSweepTestDB(t *testing.T) *db.DB {
	t.Helper()
	d, err := db.Open(context.Background(), filepath.Join(t.TempDir(), "kata.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = d.Close() })
	return d
}

// seedSweepInstance creates an issue and links it to a recurrence via raw SQL,
// mirroring the db-package seedRecurrenceInstance pattern.
func seedSweepInstance(
	t *testing.T, d *db.DB, projectID, recurrenceID int64, occurrenceKey, title string,
) (int64, string) {
	t.Helper()
	ctx := context.Background()
	iss, _, err := d.CreateIssue(ctx, db.CreateIssueParams{
		ProjectID: projectID, Title: title, Author: "tester",
	})
	require.NoError(t, err)
	_, err = d.ExecContext(ctx,
		`UPDATE issues SET recurrence_id = ?, occurrence_key = ? WHERE id = ?`,
		recurrenceID, occurrenceKey, iss.ID)
	require.NoError(t, err)
	return iss.ID, iss.UID
}

// countRecurrenceIssues returns the number of non-deleted issues linked to a
// recurrence — both open and closed.
func countRecurrenceIssues(t *testing.T, d *db.DB, recurrenceID int64) int {
	t.Helper()
	var n int
	require.NoError(t, d.QueryRow(
		`SELECT COUNT(*) FROM issues WHERE recurrence_id = ? AND deleted_at IS NULL`,
		recurrenceID,
	).Scan(&n))
	return n
}

// closeInstanceDirectly closes the issue via raw SQL — bypassing CloseIssue's
// MaterializeNext side effect. Used to simulate the post-crash state where a
// recurring issue was closed-done but the next-instance materialization never
// landed.
func closeInstanceDirectly(t *testing.T, d *db.DB, issueID int64) {
	t.Helper()
	_, err := d.ExecContext(context.Background(),
		`UPDATE issues
		    SET status        = 'closed',
		        closed_reason = 'done',
		        closed_at     = strftime('%Y-%m-%dT%H:%M:%fZ','now'),
		        updated_at    = strftime('%Y-%m-%dT%H:%M:%fZ','now')
		  WHERE id = ?`, issueID)
	require.NoError(t, err)
}

func TestSweep_MaterializesMissingNext(t *testing.T) {
	d := openSweepTestDB(t)
	ctx := context.Background()
	p, err := d.CreateProject(ctx, "p")
	require.NoError(t, err)
	rec, err := d.CreateRecurrence(ctx, db.CreateRecurrenceIn{
		ProjectID: p.ID, Actor: "tester",
		Rule: "FREQ=WEEKLY", DTStart: "2026-05-11", Timezone: "UTC",
		Template: db.RecurrenceTemplate{Title: "Pay rent"},
	})
	require.NoError(t, err)

	// Simulate post-crash state directly: N=1 (2026-05-11) is closed-done,
	// but no N=2 exists and last_materialized_uid is stale (still pointing
	// at whatever cursor was set on creation — pre-close, doesn't match N=1).
	firstID, _ := seedSweepInstance(t, d, p.ID, rec.ID, "2026-05-11", "Pay rent")
	closeInstanceDirectly(t, d, firstID)

	require.NoError(t, daemon.RunRecurrenceSweep(ctx, d))

	// A fresh N=2 issue should exist for 2026-05-18.
	var n int
	require.NoError(t, d.QueryRow(`
		SELECT COUNT(*) FROM issues
		 WHERE recurrence_id = ? AND occurrence_key = ? AND deleted_at IS NULL`,
		rec.ID, "2026-05-18",
	).Scan(&n))
	assert.Equal(t, 1, n, "sweep should materialize the missing next instance")
}

func TestSweep_Idempotent(t *testing.T) {
	d := openSweepTestDB(t)
	ctx := context.Background()
	p, err := d.CreateProject(ctx, "p")
	require.NoError(t, err)
	rec, err := d.CreateRecurrence(ctx, db.CreateRecurrenceIn{
		ProjectID: p.ID, Actor: "tester",
		Rule: "FREQ=WEEKLY", DTStart: "2026-05-11", Timezone: "UTC",
		Template: db.RecurrenceTemplate{Title: "t"},
	})
	require.NoError(t, err)
	firstID, _ := seedSweepInstance(t, d, p.ID, rec.ID, "2026-05-11", "t")
	closeInstanceDirectly(t, d, firstID)

	require.NoError(t, daemon.RunRecurrenceSweep(ctx, d))
	afterFirst := countRecurrenceIssues(t, d, rec.ID)

	// Run a second time — must be a no-op because the latest instance is now
	// the freshly-materialized N=2 (open), which the sweep skips.
	require.NoError(t, daemon.RunRecurrenceSweep(ctx, d))
	afterSecond := countRecurrenceIssues(t, d, rec.ID)
	assert.Equal(t, afterFirst, afterSecond,
		"second sweep run must not materialize a duplicate instance")
}

func TestSweep_SkipsSoftDeletedRecurrence(t *testing.T) {
	d := openSweepTestDB(t)
	ctx := context.Background()
	p, err := d.CreateProject(ctx, "p")
	require.NoError(t, err)
	rec, err := d.CreateRecurrence(ctx, db.CreateRecurrenceIn{
		ProjectID: p.ID, Actor: "tester",
		Rule: "FREQ=WEEKLY", DTStart: "2026-05-11", Timezone: "UTC",
		Template: db.RecurrenceTemplate{Title: "t"},
	})
	require.NoError(t, err)
	// Set up the "needs recovery" state, then soft-delete the recurrence.
	firstID, _ := seedSweepInstance(t, d, p.ID, rec.ID, "2026-05-11", "t")
	closeInstanceDirectly(t, d, firstID)
	require.NoError(t, d.SoftDeleteRecurrence(ctx, rec.ID, "tester"))
	before := countRecurrenceIssues(t, d, rec.ID)

	require.NoError(t, daemon.RunRecurrenceSweep(ctx, d))

	after := countRecurrenceIssues(t, d, rec.ID)
	assert.Equal(t, before, after,
		"sweep must skip soft-deleted recurrences")
}

func TestSweep_SkipsOpenLatestInstance(t *testing.T) {
	d := openSweepTestDB(t)
	ctx := context.Background()
	p, err := d.CreateProject(ctx, "p")
	require.NoError(t, err)
	rec, err := d.CreateRecurrence(ctx, db.CreateRecurrenceIn{
		ProjectID: p.ID, Actor: "tester",
		Rule: "FREQ=WEEKLY", DTStart: "2026-05-11", Timezone: "UTC",
		Template: db.RecurrenceTemplate{Title: "t"},
	})
	require.NoError(t, err)
	// Latest instance is OPEN — sweep must not materialize next.
	_, _ = seedSweepInstance(t, d, p.ID, rec.ID, "2026-05-11", "t")
	before := countRecurrenceIssues(t, d, rec.ID)

	require.NoError(t, daemon.RunRecurrenceSweep(ctx, d))

	after := countRecurrenceIssues(t, d, rec.ID)
	assert.Equal(t, before, after,
		"sweep must not materialize next when latest instance is open")
}

func TestSweep_SkipsAlreadyMaterialized(t *testing.T) {
	d := openSweepTestDB(t)
	ctx := context.Background()
	p, err := d.CreateProject(ctx, "p")
	require.NoError(t, err)
	rec, err := d.CreateRecurrence(ctx, db.CreateRecurrenceIn{
		ProjectID: p.ID, Actor: "tester",
		Rule: "FREQ=WEEKLY", DTStart: "2026-05-11", Timezone: "UTC",
		Template: db.RecurrenceTemplate{Title: "t"},
	})
	require.NoError(t, err)
	firstID, firstUID := seedSweepInstance(t, d, p.ID, rec.ID, "2026-05-11", "t")
	closeInstanceDirectly(t, d, firstID)
	// Pin last_materialized_uid to the latest closed-done instance's UID —
	// sweep should treat the recurrence as already materialized past N=1.
	_, err = d.ExecContext(ctx,
		`UPDATE recurrences SET last_materialized_uid = ? WHERE id = ?`,
		firstUID, rec.ID)
	require.NoError(t, err)
	before := countRecurrenceIssues(t, d, rec.ID)

	require.NoError(t, daemon.RunRecurrenceSweep(ctx, d))

	after := countRecurrenceIssues(t, d, rec.ID)
	assert.Equal(t, before, after,
		"sweep must not re-materialize when last_materialized_uid matches the latest")
}

func TestSweep_SkipsLegacyNullOccurrenceKey(t *testing.T) {
	d := openSweepTestDB(t)
	ctx := context.Background()
	p, err := d.CreateProject(ctx, "p")
	require.NoError(t, err)
	rec, err := d.CreateRecurrence(ctx, db.CreateRecurrenceIn{
		ProjectID: p.ID, Actor: "tester",
		Rule: "FREQ=WEEKLY", DTStart: "2026-05-11", Timezone: "UTC",
		Template: db.RecurrenceTemplate{Title: "t"},
	})
	require.NoError(t, err)
	// Seed a closed-done instance with recurrence_id set but occurrence_key NULL
	// (legacy / ill-formed row that predates the partial unique index).
	iss, _, err := d.CreateIssue(ctx, db.CreateIssueParams{
		ProjectID: p.ID, Title: "legacy", Author: "tester",
	})
	require.NoError(t, err)
	_, err = d.ExecContext(ctx,
		`UPDATE issues SET recurrence_id = ?, occurrence_key = NULL WHERE id = ?`,
		rec.ID, iss.ID)
	require.NoError(t, err)
	closeInstanceDirectly(t, d, iss.ID)
	before := countRecurrenceIssues(t, d, rec.ID)

	require.NoError(t, daemon.RunRecurrenceSweep(ctx, d))

	after := countRecurrenceIssues(t, d, rec.ID)
	assert.Equal(t, before, after,
		"sweep must skip rows with NULL occurrence_key instead of panicking")
}
