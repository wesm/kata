package jsonl_test

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wesm/kata/internal/db"
	"github.com/wesm/kata/internal/jsonl"
	"github.com/wesm/kata/internal/uid"
)

func TestImportRoundTripsExportedRows(t *testing.T) {
	ctx := context.Background()
	src := openExportTestDB(t)
	p, err := src.CreateProject(ctx, "github.com/wesm/kata", "kata")
	require.NoError(t, err)
	issue, _, err := src.CreateIssue(ctx, db.CreateIssueParams{
		ProjectID: p.ID,
		Title:     "round trip",
		Author:    "tester",
		Labels:    []string{"bug"},
	})
	require.NoError(t, err)
	_, _, err = src.CreateComment(ctx, db.CreateCommentParams{IssueID: issue.ID, Author: "tester", Body: "comment"})
	require.NoError(t, err)

	var exported bytes.Buffer
	require.NoError(t, jsonl.Export(ctx, src, &exported, jsonl.ExportOptions{IncludeDeleted: true}))

	dst := openImportTargetDB(t)
	require.NoError(t, jsonl.Import(ctx, bytes.NewReader(exported.Bytes()), dst))

	assertTableCount(t, src, dst, "projects")
	assertTableCount(t, src, dst, "issues")
	assertTableCount(t, src, dst, "comments")
	assertTableCount(t, src, dst, "issue_labels")
	assertTableCount(t, src, dst, "events")

	got, err := dst.IssueByID(ctx, issue.ID)
	require.NoError(t, err)
	assert.Equal(t, issue.Number, got.Number)
	assert.Equal(t, issue.Title, got.Title)
}

func TestImportSQLiteSequenceUsesUpdateOrInsert(t *testing.T) {
	ctx := context.Background()
	target := openImportTargetDB(t)
	input := strings.Join([]string{
		`{"kind":"meta","data":{"key":"export_version","value":"1"}}`,
		`{"kind":"sqlite_sequence","data":{"name":"issues","seq":150}}`,
		`{"kind":"sqlite_sequence","data":{"name":"issues","seq":150}}`,
	}, "\n") + "\n"

	require.NoError(t, jsonl.Import(ctx, strings.NewReader(input), target))

	var rows int
	require.NoError(t, target.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_sequence WHERE name='issues'`).Scan(&rows))
	assert.Equal(t, 1, rows)
	var seq int64
	require.NoError(t, target.QueryRowContext(ctx,
		`SELECT seq FROM sqlite_sequence WHERE name='issues'`).Scan(&seq))
	assert.Equal(t, int64(150), seq)
}

func TestImportV1FillsUIDsDeterministically(t *testing.T) {
	ctx := context.Background()
	input := strings.Join([]string{
		`{"kind":"meta","data":{"key":"export_version","value":"1"}}`,
		`{"kind":"project","data":{"id":1,"identity":"github.com/wesm/kata","name":"kata","created_at":"2026-05-03T00:00:00.000Z","next_issue_number":2}}`,
		`{"kind":"issue","data":{"id":1,"project_id":1,"number":1,"title":"v1 issue","body":"","status":"open","closed_reason":null,"owner":null,"author":"tester","created_at":"2026-05-03T00:00:01.000Z","updated_at":"2026-05-03T00:00:01.000Z","closed_at":null,"deleted_at":null}}`,
		`{"kind":"event","data":{"id":1,"project_id":1,"project_identity":"github.com/wesm/kata","issue_id":1,"issue_number":1,"related_issue_id":null,"type":"issue.created","actor":"tester","payload":{},"created_at":"2026-05-03T00:00:01.000Z"}}`,
	}, "\n") + "\n"

	first := openImportTargetDB(t)
	require.NoError(t, jsonl.Import(ctx, strings.NewReader(input), first))
	second := openImportTargetDB(t)
	require.NoError(t, jsonl.Import(ctx, strings.NewReader(input), second))

	firstUIDs := readFilledUIDs(t, first)
	secondUIDs := readFilledUIDs(t, second)
	assert.Equal(t, firstUIDs, secondUIDs)
	for _, got := range firstUIDs {
		assert.True(t, uid.Valid(got), "invalid uid %q", got)
	}
	var schemaVersion string
	require.NoError(t, first.QueryRow(`SELECT value FROM meta WHERE key='schema_version'`).Scan(&schemaVersion))
	assert.Equal(t, "4", schemaVersion)
}

func TestImportV1RejectsCorruptEventFK(t *testing.T) {
	ctx := context.Background()
	target := openImportTargetDB(t)
	input := strings.Join([]string{
		`{"kind":"meta","data":{"key":"export_version","value":"1"}}`,
		`{"kind":"project","data":{"id":1,"identity":"github.com/wesm/kata","name":"kata","created_at":"2026-05-03T00:00:00.000Z","next_issue_number":1}}`,
		`{"kind":"event","data":{"id":7,"project_id":1,"project_identity":"github.com/wesm/kata","issue_id":999,"issue_number":1,"related_issue_id":null,"type":"issue.created","actor":"tester","payload":{},"created_at":"2026-05-03T00:00:01.000Z"}}`,
	}, "\n") + "\n"

	err := jsonl.Import(ctx, strings.NewReader(input), target)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "corrupt_event_fk")
	assert.Contains(t, err.Error(), "event 7 issue_id 999")
}

func TestImportRejectsInvalidExportVersion(t *testing.T) {
	ctx := context.Background()
	target := openImportTargetDB(t)
	input := strings.Join([]string{
		`{"kind":"meta","data":{"key":"export_version","value":"not-a-version"}}`,
		`{"kind":"project","data":{"id":1,"identity":"github.com/wesm/kata","name":"kata","created_at":"2026-05-03T00:00:00.000Z","next_issue_number":1}}`,
	}, "\n") + "\n"

	err := jsonl.Import(ctx, strings.NewReader(input), target)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "export_version")
	assertTableEmpty(t, target, "projects")
}

func TestImportRejectsTooNewExportVersion(t *testing.T) {
	ctx := context.Background()
	target := openImportTargetDB(t)
	input := strings.Join([]string{
		`{"kind":"meta","data":{"key":"export_version","value":"999"}}`,
		`{"kind":"project","data":{"id":1,"identity":"github.com/wesm/kata","name":"kata","created_at":"2026-05-03T00:00:00.000Z","next_issue_number":1}}`,
	}, "\n") + "\n"

	err := jsonl.Import(ctx, strings.NewReader(input), target)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported export_version")
	assertTableEmpty(t, target, "projects")
}

func TestImportRejectsForeignKeyViolationBeforeCommit(t *testing.T) {
	ctx := context.Background()
	target := openImportTargetDB(t)
	input := strings.Join([]string{
		`{"kind":"meta","data":{"key":"export_version","value":"1"}}`,
		`{"kind":"project_alias","data":{"id":1,"project_id":999,"alias_identity":"missing","alias_kind":"git","root_path":"/tmp/missing","created_at":"2026-05-03T00:00:00.000Z","last_seen_at":"2026-05-03T00:00:00.000Z"}}`,
	}, "\n") + "\n"

	err := jsonl.Import(ctx, strings.NewReader(input), target)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "foreign_key_check")
	var count int
	require.NoError(t, target.QueryRowContext(ctx, `SELECT COUNT(*) FROM project_aliases`).Scan(&count))
	assert.Equal(t, 0, count)
}

func openImportTargetDB(t *testing.T) *db.DB {
	t.Helper()
	d, err := db.Open(context.Background(), filepath.Join(t.TempDir(), "target.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = d.Close() })
	return d
}

func assertTableCount(t *testing.T, src, dst *db.DB, table string) {
	t.Helper()
	var want, got int
	require.NoError(t, src.QueryRow(`SELECT COUNT(*) FROM `+table).Scan(&want))
	require.NoError(t, dst.QueryRow(`SELECT COUNT(*) FROM `+table).Scan(&got))
	assert.Equal(t, want, got, table)
}

func assertTableEmpty(t *testing.T, d *db.DB, table string) {
	t.Helper()
	var got int
	require.NoError(t, d.QueryRow(`SELECT COUNT(*) FROM `+table).Scan(&got))
	assert.Equal(t, 0, got, table)
}

func readFilledUIDs(t *testing.T, d *db.DB) []string {
	t.Helper()
	var projectUID, issueUID, eventIssueUID string
	require.NoError(t, d.QueryRow(`SELECT uid FROM projects WHERE id = 1`).Scan(&projectUID))
	require.NoError(t, d.QueryRow(`SELECT uid FROM issues WHERE id = 1`).Scan(&issueUID))
	require.NoError(t, d.QueryRow(`SELECT issue_uid FROM events WHERE id = 1`).Scan(&eventIssueUID))
	return []string{projectUID, issueUID, eventIssueUID}
}
