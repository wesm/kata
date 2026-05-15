package jsonl_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wesm/kata/internal/db"
	"github.com/wesm/kata/internal/jsonl"
)

// seedV9SchemaDB builds a SQLite DB whose actual on-disk schema matches v9:
// no events.origin_seq column, no recurrences table, and no recurrence_id /
// occurrence_key on issues. The meta.schema_version is rewritten to '9' so
// jsonl.Export's version-dispatch picks the pre-v10 branches. This is the
// only fixture that exercises the v8/v9 export path against a schema that
// actually lacks the v10 columns — seedV8DBWithOrphans keeps the on-disk
// v10 schema and only relabels the meta value.
func seedV9SchemaDB(t *testing.T, path string) {
	t.Helper()
	ctx := context.Background()

	d, err := db.Open(ctx, path)
	require.NoError(t, err)
	require.NoError(t, d.Close())

	raw, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	defer func() { _ = raw.Close() }()

	// Drop v10 additions so the table shapes match what a v9 binary
	// would have written. SQLite supports DROP COLUMN since 3.35 (the
	// modernc.org/sqlite driver bundles a newer engine), but indexes
	// referencing the dropped columns must be removed first.
	_, err = raw.Exec(`DROP INDEX IF EXISTS issues_recurrence_occurrence_uniq`)
	require.NoError(t, err)
	_, err = raw.Exec(`DROP INDEX IF EXISTS events_origin_seq_uniq`)
	require.NoError(t, err)
	_, err = raw.Exec(`ALTER TABLE issues DROP COLUMN recurrence_id`)
	require.NoError(t, err)
	_, err = raw.Exec(`ALTER TABLE issues DROP COLUMN occurrence_key`)
	require.NoError(t, err)
	_, err = raw.Exec(`ALTER TABLE events DROP COLUMN origin_seq`)
	require.NoError(t, err)
	_, err = raw.Exec(`DROP TABLE recurrences`)
	require.NoError(t, err)

	const projectUID = "01HZZZZZZZZZZZZZZZZZZZZZZZ"
	const issueUID = "01HZZZZZZZZZZZZZZZZZZZZA01"
	_, err = raw.Exec(`INSERT INTO projects(id, uid, name) VALUES (1, ?, 'kata')`, projectUID)
	require.NoError(t, err)
	_, err = raw.Exec(
		`INSERT INTO issues(id, uid, project_id, short_id, title, author)
		 VALUES (1, ?, 1, 'za01', 'v9 issue', 'tester')`, issueUID)
	require.NoError(t, err)
	_, err = raw.Exec(
		`INSERT INTO events(uid, origin_instance_uid, project_id, project_name, issue_id, type, actor, payload)
		 VALUES ('01HZZZZZZZZZZZZZZZZZEVAL01', '01HZZZZZZZZZZZZZZZZZZZZZ00', 1, 'kata', 1, 'issue.created', 'tester', '{}')`)
	require.NoError(t, err)

	_, err = raw.Exec(`UPDATE meta SET value='9' WHERE key='schema_version'`)
	require.NoError(t, err)
}

// TestExport_PreV10_NoMissingColumnError pins the fix for the v8/v9 export
// path. Before this fix, jsonl.Export against a v9 schema DB selected
// events.origin_seq (added in v10) and joined issues against the
// recurrences table (added in v10), producing "no such column" / "no
// such table" failures during JSONL cutover. The fix gates both
// projections on sourceSchemaVersion >= 10.
func TestExport_PreV10_NoMissingColumnError(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "kata.db")
	seedV9SchemaDB(t, path)

	d, err := db.OpenReadOnly(ctx, path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = d.Close() })

	var buf bytes.Buffer
	err = jsonl.Export(ctx, d, &buf, jsonl.ExportOptions{IncludeDeleted: true})
	require.NoError(t, err, "export must not reference v10-only columns when source is v9")

	records := decodeJSONLLines(t, buf.Bytes())

	// The pre-v10 issue projection must omit recurrence linkage fields.
	// The pre-v10 event projection must omit origin_seq. Confirm the
	// absence so a future schema bump that leaks v10 columns back into
	// the v8/v9 path fails this test.
	var sawIssue, sawEvent, sawRecurrence bool
	for _, rec := range records {
		data, _ := rec["data"].(map[string]any)
		switch rec["kind"] {
		case "issue":
			sawIssue = true
			assert.NotContains(t, data, "recurrence_id", "v9 issue export must omit recurrence_id")
			assert.NotContains(t, data, "recurrence_uid", "v9 issue export must omit recurrence_uid")
			assert.NotContains(t, data, "occurrence_key", "v9 issue export must omit occurrence_key")
			assert.Contains(t, data, "metadata", "v9 issue export must keep metadata (G1)")
			assert.Contains(t, data, "revision", "v9 issue export must keep revision (G1)")
		case "event":
			sawEvent = true
			assert.NotContains(t, data, "origin_seq", "v9 event export must omit origin_seq")
			assert.Contains(t, data, "uid", "v9 event export must keep uid")
			assert.Contains(t, data, "origin_instance_uid",
				"v9 event export must keep origin_instance_uid")
		case "recurrence":
			sawRecurrence = true
		}
	}
	assert.True(t, sawIssue, "expected at least one issue record")
	assert.True(t, sawEvent, "expected at least one event record")
	assert.False(t, sawRecurrence, "v9 export must not emit recurrence records")
}

// TestExport_PreV10_RoundtripsThroughImport pins the end-to-end cutover
// path: a v9-shape DB exports without error, and the JSONL imports
// cleanly into a fresh v10 target. This is the user-facing regression
// signal for the v9->v10 cutover.
func TestExport_PreV10_RoundtripsThroughImport(t *testing.T) {
	ctx := context.Background()
	srcPath := filepath.Join(t.TempDir(), "src.db")
	seedV9SchemaDB(t, srcPath)

	src, err := db.OpenReadOnly(ctx, srcPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = src.Close() })

	var buf bytes.Buffer
	require.NoError(t, jsonl.Export(ctx, src, &buf, jsonl.ExportOptions{IncludeDeleted: true}))

	// Sanity: the export must start with export_version=9 so Import
	// routes through the pre-v10 envelope shape.
	first := strings.SplitN(buf.String(), "\n", 2)[0]
	var meta map[string]any
	require.NoError(t, json.Unmarshal([]byte(first), &meta))
	data := meta["data"].(map[string]any)
	assert.Equal(t, "9", data["value"], "export_version should reflect source schema_version")

	target := openImportTargetDB(t)
	require.NoError(t, jsonl.Import(ctx, &buf, target))

	var issueCount, eventCount int
	require.NoError(t, target.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM issues`).Scan(&issueCount))
	require.NoError(t, target.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM events`).Scan(&eventCount))
	assert.Equal(t, 1, issueCount, "v9 issue must survive cutover")
	assert.Equal(t, 1, eventCount, "v9 event must survive cutover")
}
