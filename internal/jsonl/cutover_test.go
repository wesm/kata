package jsonl_test

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wesm/kata/internal/db"
	"github.com/wesm/kata/internal/jsonl"
	"github.com/wesm/kata/internal/uid"
	_ "modernc.org/sqlite"
)

func TestAutoCutoverNoopsAtCurrentSchema(t *testing.T) {
	ctx, path := setupClosedTestDB(t)

	require.NoError(t, jsonl.AutoCutover(ctx, path))

	assertCurrentSchemaVersion(t, path)
	assertNoCutoverTemps(t, path)
}

func TestAutoCutoverRefusesExistingTempFiles(t *testing.T) {
	ctx, path := setupClosedTestDB(t)
	require.NoError(t, os.WriteFile(path+".import.tmp.jsonl", []byte("partial"), 0o600))

	err := jsonl.AutoCutover(ctx, path)

	require.Error(t, err)
	assert.True(t, errors.Is(err, jsonl.ErrCutoverInProgress))
}

func TestAutoCutoverFailureLeavesSourceAndRemovesTemps(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "kata.db")
	writeVersionZeroDB(t, path)
	before, err := os.ReadFile(path) //nolint:gosec // test fixture under TempDir
	require.NoError(t, err)

	err = jsonl.AutoCutover(ctx, path)

	require.Error(t, err)
	after, readErr := os.ReadFile(path) //nolint:gosec // test fixture under TempDir
	require.NoError(t, readErr)
	assert.Equal(t, before, after)
	assertNoCutoverTemps(t, path)
}

func TestAutoCutoverUpgradesLegacyV1DB(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "kata.db")
	writeLegacyV1DB(t, path)

	require.NoError(t, jsonl.AutoCutover(ctx, path))

	d, err := db.Open(ctx, path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = d.Close() })
	assertCurrentSchemaVersion(t, path)
	var projectUID, issueUID, eventIssueUID string
	require.NoError(t, d.QueryRow(`SELECT uid FROM projects WHERE id = 1`).Scan(&projectUID))
	require.NoError(t, d.QueryRow(`SELECT uid FROM issues WHERE id = 1`).Scan(&issueUID))
	require.NoError(t, d.QueryRow(`SELECT issue_uid FROM events WHERE id = 1`).Scan(&eventIssueUID))
	assert.True(t, uid.Valid(projectUID))
	assert.True(t, uid.Valid(issueUID))
	assert.Equal(t, issueUID, eventIssueUID)
	assertNoCutoverTemps(t, path)
}

func TestPeekSchemaVersion(t *testing.T) {
	ctx, path := setupClosedTestDB(t)
	assertCurrentSchemaVersion(t, path)

	noMeta := filepath.Join(t.TempDir(), "empty.db")
	raw, err := sql.Open("sqlite", noMeta)
	require.NoError(t, err)
	require.NoError(t, raw.PingContext(ctx))
	require.NoError(t, raw.Close())
	ver, err := db.PeekSchemaVersion(ctx, noMeta)
	require.NoError(t, err)
	assert.Equal(t, 0, ver)
}

// TestRoundtripV4PreservesDeletedAt covers #24's projects.deleted_at column:
// an archived project in the source DB round-trips through JSONL with the
// timestamp intact, so kata-instance backups don't silently un-archive
// projects on restore.
func TestRoundtripV4PreservesDeletedAt(t *testing.T) {
	ctx := context.Background()
	src := openExportTestDB(t)
	p, err := src.CreateProject(ctx, "archived")
	require.NoError(t, err)
	_, err = src.ExecContext(ctx,
		`UPDATE projects SET deleted_at = '2026-05-04T12:00:00.000Z' WHERE id = ?`, p.ID)
	require.NoError(t, err)

	var buf bytes.Buffer
	require.NoError(t, jsonl.Export(ctx, src, &buf, jsonl.ExportOptions{IncludeDeleted: true}))

	dstPath := filepath.Join(t.TempDir(), "dst.db")
	dst, err := db.Open(ctx, dstPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = dst.Close() })
	require.NoError(t, jsonl.Import(ctx, bytes.NewReader(buf.Bytes()), dst))

	var got sql.NullTime
	require.NoError(t, dst.QueryRowContext(ctx,
		`SELECT deleted_at FROM projects WHERE id = ?`, p.ID).Scan(&got))
	require.True(t, got.Valid, "deleted_at must round-trip as a non-null timestamp")
	assert.Equal(t, "2026-05-04T12:00:00Z", got.Time.UTC().Format("2006-01-02T15:04:05Z"))
}

func writeVersionZeroDB(t *testing.T, path string) {
	t.Helper()
	ctx := context.Background()
	d, err := db.Open(ctx, path)
	require.NoError(t, err)
	_, err = d.ExecContext(ctx, `UPDATE meta SET value='0' WHERE key='schema_version'`)
	require.NoError(t, err)
	require.NoError(t, d.Close())
}

func assertNoCutoverTemps(t *testing.T, path string) {
	t.Helper()
	for _, suffix := range []string{".import.tmp.jsonl", ".import.tmp.db"} {
		_, err := os.Stat(path + suffix)
		assert.True(t, os.IsNotExist(err), path+suffix)
	}
}

// TestAutoCutover_HaltsOnUnknownFKClass: a source DB containing
// an FK violation outside the known orphan classes (a
// project_aliases row pointing at a missing project) refuses to
// cutover and reports actionable detail. The source DB is left
// untouched and no temp files remain.
func TestAutoCutover_HaltsOnUnknownFKClass(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "kata.db")
	seedV3DBWithOrphans(t, path, orphanSpec{OrphanProjectAlias: true})

	before, err := os.ReadFile(path) //nolint:gosec // test fixture under TempDir
	require.NoError(t, err)

	err = jsonl.AutoCutover(ctx, path)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "project_aliases")
	assert.Contains(t, err.Error(), "parent=projects")
	assert.Contains(t, err.Error(), "column=project_id")

	after, err := os.ReadFile(path) //nolint:gosec // test fixture under TempDir
	require.NoError(t, err)
	assert.Equal(t, before, after, "source DB must not be mutated on preflight halt")
	assertNoCutoverTemps(t, path)
}

// TestAutoCutover_PrintsOrphanSummary: when cutover discards
// orphan rows, exactly one stderr line summarizes them, listing
// only nonzero classes in the fixed order events / comments /
// links / issue_labels. NULL-scrubbed events are NOT counted.
func TestAutoCutover_PrintsOrphanSummary(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "kata.db")
	seedV3DBWithOrphans(t, path, orphanSpec{
		OrphanComments:     2,
		OrphanLinks:        2,
		OrphanIssueLabels:  1,
		OrphanEventIssueID: 1,
		OrphanEventRelated: 1, // scrub, must not appear in summary
	})

	stderr, restore := captureStderr(t)
	err := jsonl.AutoCutover(ctx, path)
	_ = restore()
	require.NoError(t, err)

	// 1 dropped event + 2 dropped comments + 2 dropped links +
	// 1 dropped issue_label = 6. The related-only event is
	// scrubbed, not dropped, and is excluded from the summary.
	expected := "kata cutover: discarded 6 orphan rows from old DB (events: 1, comments: 2, links: 2, issue_labels: 1)\n"
	assert.Equal(t, expected, stderr.String())
}

// TestAutoCutover_NoSummaryWhenClean: no stderr output when the
// source DB has zero orphans, so existing operators upgrading a
// clean DB see no behavior change.
func TestAutoCutover_NoSummaryWhenClean(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "kata.db")
	seedV3DBWithOrphans(t, path, orphanSpec{}) // baseline only

	stderr, restore := captureStderr(t)
	err := jsonl.AutoCutover(ctx, path)
	_ = restore()
	require.NoError(t, err)
	assert.Empty(t, stderr.Bytes())
}

// TestAutoCutover_V8HaltsOnOrphanImportMapping covers the v5+
// import_mappings code path that the v3 fixture cannot reach: an
// import_mappings row whose link_id points at a missing link gets
// classified as Unknown (preflight only knows the four issue-child
// classes) and halts cutover with actionable detail. Source DB and
// temp files are unchanged. This is the test that gives confidence
// the fix in this branch handles a v6/v7/v8 → v9 cutover where the
// orphan lives in import_mappings, not the four child tables the v3
// test exercises.
func TestAutoCutover_V8HaltsOnOrphanImportMapping(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "kata.db")
	seedV8DBWithOrphans(t, path, orphanSpec{OrphanImportMappingLink: 1})

	before, err := os.ReadFile(path) //nolint:gosec // test fixture under TempDir
	require.NoError(t, err)

	err = jsonl.AutoCutover(ctx, path)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "import_mappings")
	assert.Contains(t, err.Error(), "parent=links")
	assert.Contains(t, err.Error(), "column=link_id")

	after, err := os.ReadFile(path) //nolint:gosec // test fixture under TempDir
	require.NoError(t, err)
	assert.Equal(t, before, after, "source DB must not be mutated on preflight halt")
	assertNoCutoverTemps(t, path)
}

// TestAutoCutover_V8DropsAllKnownOrphanClasses mirrors the v3 drop
// test on a v8 source. It exercises the V8+ events scrub branch
// (events with NULL issue_number column) and the v5+ exportImportMappings
// round-trip path, neither of which the v3 fixture reaches. This is
// the test that gives confidence the fix handles a v6/v7/v8 → v9
// cutover with orphans across the known classes.
func TestAutoCutover_V8DropsAllKnownOrphanClasses(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "kata.db")
	seedV8DBWithOrphans(t, path, orphanSpec{
		OrphanComments:     2,
		OrphanLinks:        2,
		OrphanIssueLabels:  1,
		OrphanEventIssueID: 1,
		OrphanEventRelated: 1,
	})

	// Suppress the cutover summary stderr line; this test asserts data-
	// shape invariants. The summary is asserted by TestAutoCutover_PrintsOrphanSummary.
	_, restore := captureStderr(t)
	defer restore()
	require.NoError(t, jsonl.AutoCutover(ctx, path))

	d, err := db.Open(ctx, path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = d.Close() })

	assertCurrentSchemaVersion(t, path)

	var commentCount, linkCount, labelCount, importMappingCount int
	require.NoError(t, d.QueryRowContext(ctx, `SELECT COUNT(*) FROM comments`).Scan(&commentCount))
	require.NoError(t, d.QueryRowContext(ctx, `SELECT COUNT(*) FROM links`).Scan(&linkCount))
	require.NoError(t, d.QueryRowContext(ctx, `SELECT COUNT(*) FROM issue_labels`).Scan(&labelCount))
	require.NoError(t, d.QueryRowContext(ctx, `SELECT COUNT(*) FROM import_mappings`).Scan(&importMappingCount))

	// Baseline: 1 valid comment, 1 valid link, 1 valid label, 4 valid
	// import_mappings. Orphans dropped entirely.
	assert.Equal(t, 1, commentCount, "valid comment survives; orphan comments dropped")
	assert.Equal(t, 1, linkCount, "valid link survives; orphan links dropped")
	assert.Equal(t, 1, labelCount, "valid label survives; orphan label dropped")
	assert.Equal(t, 4, importMappingCount, "all 4 valid import_mappings round-trip")

	// The related-only-orphan event is preserved with NULL related fields;
	// the issue_id-orphan event was dropped.
	var eventScrubbedCount int
	require.NoError(t, d.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM events WHERE type = 'issue.linked' AND related_issue_id IS NULL`).Scan(&eventScrubbedCount))
	assert.Equal(t, 1, eventScrubbedCount, "orphan related event survives with NULL related")

	assertNoCutoverTemps(t, path)
}

// TestAutoCutover_V8Clean: a v8 source with no orphans cuts over
// silently (no stderr summary line) and lands at the current schema.
// Confirms the fix is transparent to operators upgrading a clean v8
// DB to v9.
func TestAutoCutover_V8Clean(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "kata.db")
	seedV8DBWithOrphans(t, path, orphanSpec{})

	stderr, restore := captureStderr(t)
	err := jsonl.AutoCutover(ctx, path)
	_ = restore()
	require.NoError(t, err)
	assert.Empty(t, stderr.Bytes())
	assertCurrentSchemaVersion(t, path)
	assertNoCutoverTemps(t, path)
}

// TestAutoCutover_DropsAllKnownOrphanClasses: a source DB with
// orphans across all four known classes (events, comments,
// links, issue_labels) cuts over successfully. Orphan rows are
// dropped; events with valid issue_id but orphan related_issue_id
// are preserved with NULL related fields.
func TestAutoCutover_DropsAllKnownOrphanClasses(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "kata.db")
	seedV3DBWithOrphans(t, path, orphanSpec{
		OrphanComments:     2,
		OrphanLinks:        2,
		OrphanIssueLabels:  1,
		OrphanEventIssueID: 1,
		OrphanEventRelated: 1,
	})

	// Suppress the cutover summary stderr line; this test asserts
	// data-shape invariants. The summary itself is asserted by
	// TestAutoCutover_PrintsOrphanSummary against the same seed.
	_, restore := captureStderr(t)
	defer restore()
	require.NoError(t, jsonl.AutoCutover(ctx, path))

	d, err := db.Open(ctx, path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = d.Close() })

	assertCurrentSchemaVersion(t, path)

	var commentCount, linkCount, labelCount, eventCount int
	require.NoError(t, d.QueryRowContext(ctx, `SELECT COUNT(*) FROM comments`).Scan(&commentCount))
	require.NoError(t, d.QueryRowContext(ctx, `SELECT COUNT(*) FROM links`).Scan(&linkCount))
	require.NoError(t, d.QueryRowContext(ctx, `SELECT COUNT(*) FROM issue_labels`).Scan(&labelCount))
	require.NoError(t, d.QueryRowContext(ctx, `SELECT COUNT(*) FROM events`).Scan(&eventCount))
	assert.Equal(t, 0, commentCount, "orphan comments should be dropped")
	assert.Equal(t, 0, linkCount, "orphan links should be dropped")
	assert.Equal(t, 0, labelCount, "orphan issue_labels should be dropped")

	var issueCount int
	require.NoError(t, d.QueryRowContext(ctx, `SELECT COUNT(*) FROM issues`).Scan(&issueCount))
	assert.Equal(t, 3, issueCount, "the 3 seeded valid issues must survive cutover")

	// One event survives: the related-only orphan, with NULL
	// related fields. The issue_id-orphan event was dropped.
	assert.Equal(t, 1, eventCount, "only the related-only-orphan event should survive")
	var relatedID, relatedUID sql.NullString
	require.NoError(t, d.QueryRowContext(ctx,
		`SELECT related_issue_id, related_issue_uid FROM events`).Scan(&relatedID, &relatedUID))
	assert.False(t, relatedID.Valid, "related_issue_id must be NULL after scrub")
	assert.False(t, relatedUID.Valid, "related_issue_uid must be NULL after scrub")

	assertNoCutoverTemps(t, path)
}
