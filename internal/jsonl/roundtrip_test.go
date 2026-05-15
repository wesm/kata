package jsonl_test

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wesm/kata/internal/db"
	"github.com/wesm/kata/internal/jsonl"
)

func TestRoundtripRichDatabaseIsByteEquivalent(t *testing.T) {
	ctx := context.Background()
	fixture := buildRichJSONLFixture(t)
	src := fixture.DB

	first := exportToBuffer(ctx, t, src)

	dst := openExportTestDB(t)
	require.NoError(t, jsonl.Import(ctx, bytes.NewReader(first.Bytes()), dst))

	second := exportToBuffer(ctx, t, dst)

	assert.Equal(t, first.String(), second.String())
	assertRoundtripTableCounts(t, src, dst)
	assertSQLiteSequenceRows(t, src, dst)
	assertSearchResultsMatch(t, src, dst, fixture.Project.ID, "orchid")
	assertSearchResultsMatch(t, src, dst, fixture.Project.ID, "watermelon")
	assertSearchResultsMatch(t, src, dst, fixture.Project.ID, "soft")
}

func assertRoundtripTableCounts(t *testing.T, src, dst *db.DB) {
	t.Helper()
	for _, table := range []string{
		"projects", "project_aliases", "issues", "comments", "issue_labels",
		"links", "events", "purge_log",
	} {
		assertTableCount(t, src, dst, table)
	}
}

func assertSQLiteSequenceRows(t *testing.T, src, dst *db.DB) {
	t.Helper()
	assert.Equal(t, sqliteSequenceSnapshot(t, src), sqliteSequenceSnapshot(t, dst))
}

func sqliteSequenceSnapshot(t *testing.T, d *db.DB) []map[string]any {
	t.Helper()
	rows, err := d.Query(`SELECT name, seq FROM sqlite_sequence ORDER BY name ASC`)
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()
	var out []map[string]any
	for rows.Next() {
		var name string
		var seq int64
		require.NoError(t, rows.Scan(&name, &seq))
		out = append(out, map[string]any{"name": name, "seq": seq})
	}
	require.NoError(t, rows.Err())
	return out
}

func assertSearchResultsMatch(t *testing.T, src, dst *db.DB, projectID int64, query string) {
	t.Helper()
	srcHits, err := src.SearchFTS(context.Background(), projectID, query, 20, true)
	require.NoError(t, err)
	dstHits, err := dst.SearchFTS(context.Background(), projectID, query, 20, true)
	require.NoError(t, err)
	assert.Equal(t, normalizeSearchHits(srcHits), normalizeSearchHits(dstHits), query)
}

func normalizeSearchHits(hits []db.SearchCandidate) []map[string]any {
	out := make([]map[string]any, 0, len(hits))
	for _, hit := range hits {
		out = append(out, map[string]any{
			"project_id": hit.Issue.ProjectID,
			"short_id":   hit.Issue.ShortID,
			"title":      hit.Issue.Title,
			"matched_in": hit.MatchedIn,
			"deleted":    hit.Issue.DeletedAt != nil,
		})
	}
	return out
}

// TestRoundtrip_PurgeLogEnvelopeCarriesShortID pins the purge-tombstone
// extension: a purge_log row's short_id snapshot must survive export and
// land on the destination so the tombstone keeps gating reuse after a
// cutover or backup restore.
func TestRoundtrip_PurgeLogEnvelopeCarriesShortID(t *testing.T) {
	ctx := context.Background()
	d := openExportTestDB(t)
	p, err := d.CreateProject(ctx, "demo")
	require.NoError(t, err)
	issue, _, err := d.CreateIssue(ctx, db.CreateIssueParams{
		ProjectID: p.ID,
		UID:       "01HZNQ7VFPK1XGD8R5MABCD4EX",
		Title:     "to be purged",
		Author:    "tester",
	})
	require.NoError(t, err)
	require.NotEmpty(t, issue.ShortID)
	_, err = d.PurgeIssue(ctx, issue.ID, "tester", nil)
	require.NoError(t, err)

	var buf bytes.Buffer
	require.NoError(t, jsonl.Export(ctx, d, &buf, jsonl.ExportOptions{}))
	exported := buf.Bytes()

	scanner := bufio.NewScanner(bytes.NewReader(exported))
	var purgePayload map[string]any
	for scanner.Scan() {
		var env jsonl.Envelope
		require.NoError(t, json.Unmarshal(scanner.Bytes(), &env))
		if env.Kind == jsonl.KindPurgeLog {
			require.NoError(t, json.Unmarshal(env.Data, &purgePayload))
			break
		}
	}
	require.NotNil(t, purgePayload, "exported stream must include a purge_log envelope")
	assert.Equal(t, issue.ShortID, purgePayload["short_id"])

	dst := openImportTargetDB(t)
	require.NoError(t, jsonl.Import(ctx, bytes.NewReader(exported), dst))

	var got *string
	require.NoError(t, dst.QueryRowContext(ctx,
		`SELECT short_id FROM purge_log WHERE project_id = ?`, p.ID).Scan(&got))
	require.NotNil(t, got)
	assert.Equal(t, issue.ShortID, *got)
}

// TestRoundtrip_IssueEnvelopeCarriesShortID pins spec §8.1: the JSONL issue
// envelope carries a short_id field at the current schema version and drops
// the legacy number field. The cutover (Task 9) handles older inputs.
func TestRoundtrip_IssueEnvelopeCarriesShortID(t *testing.T) {
	ctx := context.Background()
	d := openExportTestDB(t)
	p, err := d.CreateProject(ctx, "demo")
	require.NoError(t, err)
	created, _, err := d.CreateIssue(ctx, db.CreateIssueParams{
		ProjectID: p.ID,
		UID:       "01HZNQ7VFPK1XGD8R5MABCD4EX",
		Title:     "rt",
		Author:    "tester",
	})
	require.NoError(t, err)

	var buf bytes.Buffer
	require.NoError(t, jsonl.Export(ctx, d, &buf, jsonl.ExportOptions{}))

	scanner := bufio.NewScanner(&buf)
	var issuePayload map[string]any
	for scanner.Scan() {
		var env jsonl.Envelope
		require.NoError(t, json.Unmarshal(scanner.Bytes(), &env))
		if env.Kind == jsonl.KindIssue {
			require.NoError(t, json.Unmarshal(env.Data, &issuePayload))
			break
		}
	}
	require.NotNil(t, issuePayload)
	assert.Equal(t, created.ShortID, issuePayload["short_id"])
	_, hasNumber := issuePayload["number"]
	assert.False(t, hasNumber, "issue envelope should not carry 'number'")
}

func TestRoundtrip_IssuePreservesMetadataAndRevision(t *testing.T) {
	srcDB := openExportTestDB(t)
	ctx := context.Background()
	p, err := srcDB.CreateProject(ctx, "p")
	require.NoError(t, err)
	iss, _, err := srcDB.CreateIssue(ctx, db.CreateIssueParams{
		ProjectID: p.ID, Title: "x", Author: "tester",
	})
	require.NoError(t, err)
	_, err = srcDB.ExecContext(ctx,
		`UPDATE issues SET metadata = ?, revision = ? WHERE id = ?`,
		`{"scheduled_on":"2026-05-20","someday":false}`, int64(7), iss.ID)
	require.NoError(t, err)

	var buf bytes.Buffer
	require.NoError(t, jsonl.Export(ctx, srcDB, &buf, jsonl.ExportOptions{}))

	dstDB := openImportTargetDB(t)
	require.NoError(t, jsonl.Import(ctx, &buf, dstDB))

	var meta string
	var rev int64
	require.NoError(t, dstDB.QueryRowContext(ctx,
		`SELECT metadata, revision FROM issues LIMIT 1`).Scan(&meta, &rev))
	assert.JSONEq(t, `{"scheduled_on":"2026-05-20","someday":false}`, meta)
	assert.Equal(t, int64(7), rev)
}

func TestRoundtrip_ProjectPreservesMetadataAndRevision(t *testing.T) {
	srcDB := openExportTestDB(t)
	ctx := context.Background()
	p, err := srcDB.CreateProject(ctx, "Personal")
	require.NoError(t, err)
	_, err = srcDB.ExecContext(ctx,
		`UPDATE projects SET metadata = ?, revision = ? WHERE id = ?`,
		`{"area":"Personal","sidebar_order":2}`, int64(4), p.ID)
	require.NoError(t, err)

	var buf bytes.Buffer
	require.NoError(t, jsonl.Export(ctx, srcDB, &buf, jsonl.ExportOptions{}))
	dstDB := openImportTargetDB(t)
	require.NoError(t, jsonl.Import(ctx, &buf, dstDB))

	var meta string
	var rev int64
	require.NoError(t, dstDB.QueryRowContext(ctx,
		`SELECT metadata, revision FROM projects WHERE name = 'Personal'`,
	).Scan(&meta, &rev))
	assert.JSONEq(t, `{"area":"Personal","sidebar_order":2}`, meta)
	assert.Equal(t, int64(4), rev)
}

func TestRoundtrip_EventsPreserveOriginSeq(t *testing.T) {
	srcDB := openExportTestDB(t)
	ctx := context.Background()
	p, err := srcDB.CreateProject(ctx, "p")
	require.NoError(t, err)
	_, _, err = srcDB.CreateIssue(ctx, db.CreateIssueParams{
		ProjectID: p.ID, Title: "x", Author: "tester",
	})
	require.NoError(t, err) // produces an issue.created event

	var buf bytes.Buffer
	require.NoError(t, jsonl.Export(ctx, srcDB, &buf, jsonl.ExportOptions{}))
	dstDB := openImportTargetDB(t)
	require.NoError(t, jsonl.Import(ctx, &buf, dstDB))

	var minSeq sql.NullInt64
	require.NoError(t, dstDB.QueryRowContext(ctx,
		`SELECT MIN(origin_seq) FROM events`).Scan(&minSeq))
	assert.True(t, minSeq.Valid && minSeq.Int64 > 0)
}

func TestImport_OldExportWithoutOriginSeq_LeavesNullOrDefaults(t *testing.T) {
	dstDB := openImportTargetDB(t)
	body := bytes.NewBufferString("")
	body.WriteString(`{"kind":"meta","data":{"key":"export_version","value":"10"}}` + "\n")
	body.WriteString(`{"kind":"meta","data":{"key":"instance_uid","value":"FEDORIGIN0000000000000000A"}}` + "\n")
	body.WriteString(`{"kind":"project","data":{"id":1,"uid":"PROJ000000000000000000000A","name":"p","metadata":"{}","revision":1,"created_at":"2026-05-15T00:00:00.000Z"}}` + "\n")
	body.WriteString(`{"kind":"issue","data":{"id":1,"uid":"ISS0000000000000000000000A","project_id":1,"short_id":"000a","title":"t","body":"","status":"open","author":"t","metadata":"{}","revision":1,"created_at":"2026-05-15T00:00:00.000Z","updated_at":"2026-05-15T00:00:00.000Z"}}` + "\n")
	body.WriteString(`{"kind":"event","data":{"id":1,"uid":"EVT0000000000000000000000A","origin_instance_uid":"FEDORIGIN0000000000000000A","project_id":1,"project_name":"p","issue_id":1,"issue_uid":"ISS0000000000000000000000A","type":"issue.created","actor":"t","payload":"{}","created_at":"2026-05-15T00:00:00.000Z"}}` + "\n")

	require.NoError(t, jsonl.Import(context.Background(), body, dstDB))

	var origSeq sql.NullInt64
	require.NoError(t, dstDB.QueryRow(
		`SELECT origin_seq FROM events LIMIT 1`).Scan(&origSeq))
	// Older exports omit the field; import should leave it NULL (partial
	// unique index allows that), OR default to event_id. Either is acceptable
	// per spec — verify whichever the implementation chooses.
	if origSeq.Valid {
		var id int64
		require.NoError(t, dstDB.QueryRow(`SELECT id FROM events LIMIT 1`).Scan(&id))
		assert.Equal(t, id, origSeq.Int64,
			"if origin_seq is stamped on import, it must equal event id")
	}
}

func TestRoundtrip_RecurrenceFullFields(t *testing.T) {
	srcDB := openExportTestDB(t)
	ctx := context.Background()
	p, err := srcDB.CreateProject(ctx, "p")
	require.NoError(t, err)
	_, err = srcDB.ExecContext(ctx, `
		INSERT INTO recurrences
		  (uid, project_id, rrule, dtstart, timezone, template_title, template_body,
		   template_labels, template_metadata, author)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"REC00000000000000000000001", p.ID, "FREQ=WEEKLY;BYDAY=MO", "2026-05-11",
		"America/New_York", "Weekly review", "What got done?",
		`["chore","weekly"]`, `{}`, "tester")
	require.NoError(t, err)

	var buf bytes.Buffer
	require.NoError(t, jsonl.Export(ctx, srcDB, &buf, jsonl.ExportOptions{}))
	dstDB := openImportTargetDB(t)
	require.NoError(t, jsonl.Import(ctx, &buf, dstDB))

	var rule, dtstart, tz, title, labels string
	require.NoError(t, dstDB.QueryRowContext(ctx,
		`SELECT rrule, dtstart, timezone, template_title, template_labels
		   FROM recurrences LIMIT 1`,
	).Scan(&rule, &dtstart, &tz, &title, &labels))
	assert.Equal(t, "FREQ=WEEKLY;BYDAY=MO", rule)
	assert.Equal(t, "2026-05-11", dtstart)
	assert.Equal(t, "America/New_York", tz)
	assert.Equal(t, "Weekly review", title)
	assert.JSONEq(t, `["chore","weekly"]`, labels)
}

func TestExport_RecurrencesAppearBeforeIssuesInStream(t *testing.T) {
	srcDB := openExportTestDB(t)
	ctx := context.Background()
	p, err := srcDB.CreateProject(ctx, "p")
	require.NoError(t, err)
	_, _, err = srcDB.CreateIssue(ctx, db.CreateIssueParams{
		ProjectID: p.ID, Title: "x", Author: "tester",
	})
	require.NoError(t, err)
	_, err = srcDB.ExecContext(ctx, `
		INSERT INTO recurrences
		  (uid, project_id, rrule, dtstart, timezone, template_title, author)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		"REC00000000000000000000002", p.ID, "FREQ=WEEKLY", "2026-05-11",
		"UTC", "x", "tester")
	require.NoError(t, err)

	var buf bytes.Buffer
	require.NoError(t, jsonl.Export(ctx, srcDB, &buf, jsonl.ExportOptions{}))

	lines := strings.Split(buf.String(), "\n")
	firstRecurrence, firstIssue := -1, -1
	for i, l := range lines {
		if strings.Contains(l, `"kind":"recurrence"`) && firstRecurrence == -1 {
			firstRecurrence = i
		}
		if strings.Contains(l, `"kind":"issue"`) && firstIssue == -1 {
			firstIssue = i
		}
	}
	require.NotEqual(t, -1, firstRecurrence)
	require.NotEqual(t, -1, firstIssue)
	assert.Less(t, firstRecurrence, firstIssue,
		"recurrence records must appear before issue records — issues reference recurrences via FK")
}
