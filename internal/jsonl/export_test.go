package jsonl_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wesm/kata/internal/db"
	"github.com/wesm/kata/internal/jsonl"
)

func TestExportWritesOrderedRecordsWithSequenceLast(t *testing.T) {
	ctx, d, p := newExportEnv(t)
	attachAlias(ctx, t, d, p.ID, "github.com/wesm/kata", "git", "/tmp/kata")
	issue := createTesterIssue(ctx, t, d, p.ID, "export me", "", "bug")
	addTesterComment(ctx, t, d, issue.ID, "jsonl comment")

	records := exportAndDecode(ctx, t, d, jsonl.ExportOptions{IncludeDeleted: true})

	require.NotEmpty(t, records)
	assert.Equal(t, "meta", records[0]["kind"])
	assert.Equal(t, map[string]any{"key": "export_version", "value": "4"}, records[0]["data"])
	assert.Equal(t, "sqlite_sequence", records[len(records)-1]["kind"])

	assertKindOrder(t, records)
}

func TestExportEmitsEventPayloadAsJSONObject(t *testing.T) {
	ctx, d, p := newExportEnv(t)
	_, _, err := d.CreateIssue(ctx, db.CreateIssueParams{
		ProjectID:      p.ID,
		Title:          "payload",
		Author:         "tester",
		IdempotencyKey: "abc",
	})
	require.NoError(t, err)

	records := exportAndDecode(ctx, t, d, jsonl.ExportOptions{IncludeDeleted: true})

	var found bool
	for _, rec := range records {
		if rec["kind"] != "event" {
			continue
		}
		data := rec["data"].(map[string]any)
		payload, ok := data["payload"].(map[string]any)
		require.True(t, ok, "payload should be a JSON object, got %T", data["payload"])
		assert.Equal(t, "abc", payload["idempotency_key"])
		found = true
	}
	assert.True(t, found, "expected at least one event record")
}

func TestExportLegacyV1OmitsUIDFields(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "legacy.db")
	writeLegacyV1DB(t, path)
	d, err := db.OpenReadOnly(ctx, path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = d.Close() })

	records := exportAndDecode(ctx, t, d, jsonl.ExportOptions{IncludeDeleted: true})

	assert.Equal(t, map[string]any{"key": "export_version", "value": "1"}, records[0]["data"])
	for _, rec := range records {
		data, _ := rec["data"].(map[string]any)
		switch rec["kind"] {
		case "project", "issue":
			assert.NotContains(t, data, "uid")
		case "link":
			assert.NotContains(t, data, "from_issue_uid")
			assert.NotContains(t, data, "to_issue_uid")
		case "event":
			assert.NotContains(t, data, "issue_uid")
			assert.NotContains(t, data, "related_issue_uid")
		case "purge_log":
			assert.NotContains(t, data, "issue_uid")
			assert.NotContains(t, data, "project_uid")
		}
	}
}

func TestExportProjectIDFiltersProjectScopedRows(t *testing.T) {
	ctx := context.Background()
	d := openExportTestDB(t)
	p1, err := d.CreateProject(ctx, "github.com/wesm/kata", "kata")
	require.NoError(t, err)
	p2, err := d.CreateProject(ctx, "github.com/wesm/other", "other")
	require.NoError(t, err)
	attachAlias(ctx, t, d, p1.ID, "github.com/wesm/kata", "git", "/tmp/kata")
	attachAlias(ctx, t, d, p2.ID, "github.com/wesm/other", "git", "/tmp/other")
	createTesterIssue(ctx, t, d, p1.ID, "keep me", "")
	createTesterIssue(ctx, t, d, p2.ID, "drop me", "")

	records := exportAndDecode(ctx, t, d, jsonl.ExportOptions{
		ProjectID:      p1.ID,
		IncludeDeleted: true,
	})

	assertRecordsDoNotContain(t, records, "drop me")
	assertProjectIDs(t, records, map[int64]bool{p1.ID: true})
}

func TestExportNoIncludeDeletedOmitsSoftDeletedIssueDependents(t *testing.T) {
	ctx, d, p := newExportEnv(t)
	kept := createTesterIssue(ctx, t, d, p.ID, "kept issue", "")
	deleted := createTesterIssue(ctx, t, d, p.ID, "deleted issue", "", "gone")
	addTesterComment(ctx, t, d, deleted.ID, "deleted comment")
	_, _, err := d.CreateLinkAndEvent(ctx, db.CreateLinkParams{
		ProjectID:   p.ID,
		FromIssueID: deleted.ID,
		ToIssueID:   kept.ID,
		Type:        "blocks",
		Author:      "tester",
	}, db.LinkEventParams{
		EventType: "issue.linked", EventIssueID: deleted.ID, EventIssueNumber: deleted.Number,
		FromNumber: deleted.Number, ToNumber: kept.Number, Actor: "tester",
	})
	require.NoError(t, err)
	_, err = d.ExecContext(ctx, `UPDATE issues SET deleted_at = CURRENT_TIMESTAMP WHERE id = ?`, deleted.ID)
	require.NoError(t, err)

	records := exportAndDecode(ctx, t, d, jsonl.ExportOptions{IncludeDeleted: false})

	assertRecordsDoNotContain(t, records, "deleted issue")
	assertRecordsDoNotContain(t, records, "deleted comment")
	assertRecordsDoNotContain(t, records, "gone")
	for _, rec := range records {
		data, _ := rec["data"].(map[string]any)
		if rec["kind"] == "link" {
			assert.NotEqual(t, float64(deleted.ID), data["from_issue_id"])
			assert.NotEqual(t, float64(deleted.ID), data["to_issue_id"])
		}
		if rec["kind"] == "event" {
			assert.NotEqual(t, float64(deleted.ID), data["issue_id"])
			assert.NotEqual(t, float64(deleted.ID), data["related_issue_id"])
		}
	}
}

func openExportTestDB(t *testing.T) *db.DB {
	t.Helper()
	d, err := db.Open(context.Background(), filepath.Join(t.TempDir(), "kata.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = d.Close() })
	return d
}

// newExportEnv opens a fresh test DB and seeds the canonical "kata" project
// used by most export tests.
func newExportEnv(t *testing.T) (context.Context, *db.DB, db.Project) {
	t.Helper()
	ctx := context.Background()
	d := openExportTestDB(t)
	p, err := d.CreateProject(ctx, "github.com/wesm/kata", "kata")
	require.NoError(t, err)
	return ctx, d, p
}

// exportAndDecode runs jsonl.Export into a buffer and decodes the resulting
// JSONL stream into records.
func exportAndDecode(ctx context.Context, t *testing.T, d *db.DB, opts jsonl.ExportOptions) []map[string]any {
	t.Helper()
	var out bytes.Buffer
	require.NoError(t, jsonl.Export(ctx, d, &out, opts))
	return decodeJSONLLines(t, out.Bytes())
}

func assertRecordsDoNotContain(t *testing.T, records []map[string]any, needle string) {
	t.Helper()
	for _, rec := range records {
		bs, err := json.Marshal(rec)
		require.NoError(t, err)
		assert.NotContains(t, string(bs), needle)
	}
}

func assertProjectIDs(t *testing.T, records []map[string]any, allowed map[int64]bool) {
	t.Helper()
	for _, rec := range records {
		data, _ := rec["data"].(map[string]any)
		v, ok := data["project_id"]
		if !ok {
			if rec["kind"] == "project" {
				v = data["id"]
			} else {
				continue
			}
		}
		id := int64(v.(float64))
		assert.True(t, allowed[id], "record kind=%s has project id %d outside filter", rec["kind"], id)
	}
}

func decodeJSONLLines(t *testing.T, bs []byte) []map[string]any {
	t.Helper()
	scanner := bufio.NewScanner(bytes.NewReader(bs))
	var out []map[string]any
	for scanner.Scan() {
		var rec map[string]any
		require.NoError(t, json.Unmarshal(scanner.Bytes(), &rec))
		out = append(out, rec)
	}
	require.NoError(t, scanner.Err())
	return out
}

func assertKindOrder(t *testing.T, records []map[string]any) {
	t.Helper()
	order := map[string]int{
		"meta": 0, "project": 1, "project_alias": 2, "issue": 3,
		"comment": 4, "issue_label": 5, "link": 6, "event": 7,
		"purge_log": 8, "sqlite_sequence": 9,
	}
	last := -1
	for _, rec := range records {
		kind := rec["kind"].(string)
		rank, ok := order[kind]
		require.True(t, ok, "unknown kind %q", kind)
		require.GreaterOrEqual(t, rank, last, "kind %q out of order", kind)
		last = rank
	}
}
