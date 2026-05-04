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
	ctx := context.Background()
	d := openExportTestDB(t)
	p, err := d.CreateProject(ctx, "github.com/wesm/kata", "kata")
	require.NoError(t, err)
	_, err = d.AttachAlias(ctx, p.ID, "github.com/wesm/kata", "git", "/tmp/kata")
	require.NoError(t, err)
	issue, _, err := d.CreateIssue(ctx, db.CreateIssueParams{
		ProjectID: p.ID,
		Title:     "export me",
		Author:    "tester",
		Labels:    []string{"bug"},
	})
	require.NoError(t, err)
	_, _, err = d.CreateComment(ctx, db.CreateCommentParams{
		IssueID: issue.ID,
		Author:  "tester",
		Body:    "jsonl comment",
	})
	require.NoError(t, err)

	var out bytes.Buffer
	require.NoError(t, jsonl.Export(ctx, d, &out, jsonl.ExportOptions{IncludeDeleted: true}))
	records := decodeJSONLLines(t, out.Bytes())

	require.NotEmpty(t, records)
	assert.Equal(t, "meta", records[0]["kind"])
	assert.Equal(t, map[string]any{"key": "export_version", "value": "4"}, records[0]["data"])
	assert.Equal(t, "sqlite_sequence", records[len(records)-1]["kind"])

	assertKindOrder(t, records)
}

func TestExportEmitsEventPayloadAsJSONObject(t *testing.T) {
	ctx := context.Background()
	d := openExportTestDB(t)
	p, err := d.CreateProject(ctx, "github.com/wesm/kata", "kata")
	require.NoError(t, err)
	_, _, err = d.CreateIssue(ctx, db.CreateIssueParams{
		ProjectID:      p.ID,
		Title:          "payload",
		Author:         "tester",
		IdempotencyKey: "abc",
	})
	require.NoError(t, err)

	var out bytes.Buffer
	require.NoError(t, jsonl.Export(ctx, d, &out, jsonl.ExportOptions{IncludeDeleted: true}))
	records := decodeJSONLLines(t, out.Bytes())

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

	var out bytes.Buffer
	require.NoError(t, jsonl.Export(ctx, d, &out, jsonl.ExportOptions{IncludeDeleted: true}))
	records := decodeJSONLLines(t, out.Bytes())

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
	_, err = d.AttachAlias(ctx, p1.ID, "github.com/wesm/kata", "git", "/tmp/kata")
	require.NoError(t, err)
	_, err = d.AttachAlias(ctx, p2.ID, "github.com/wesm/other", "git", "/tmp/other")
	require.NoError(t, err)
	_, _, err = d.CreateIssue(ctx, db.CreateIssueParams{
		ProjectID: p1.ID,
		Title:     "keep me",
		Author:    "tester",
	})
	require.NoError(t, err)
	_, _, err = d.CreateIssue(ctx, db.CreateIssueParams{
		ProjectID: p2.ID,
		Title:     "drop me",
		Author:    "tester",
	})
	require.NoError(t, err)

	var out bytes.Buffer
	require.NoError(t, jsonl.Export(ctx, d, &out, jsonl.ExportOptions{
		ProjectID:      p1.ID,
		IncludeDeleted: true,
	}))
	records := decodeJSONLLines(t, out.Bytes())

	assertRecordsDoNotContain(t, records, "drop me")
	assertProjectIDs(t, records, map[int64]bool{p1.ID: true})
}

func TestExportNoIncludeDeletedOmitsSoftDeletedIssueDependents(t *testing.T) {
	ctx := context.Background()
	d := openExportTestDB(t)
	p, err := d.CreateProject(ctx, "github.com/wesm/kata", "kata")
	require.NoError(t, err)
	kept, _, err := d.CreateIssue(ctx, db.CreateIssueParams{
		ProjectID: p.ID,
		Title:     "kept issue",
		Author:    "tester",
	})
	require.NoError(t, err)
	deleted, _, err := d.CreateIssue(ctx, db.CreateIssueParams{
		ProjectID: p.ID,
		Title:     "deleted issue",
		Author:    "tester",
		Labels:    []string{"gone"},
	})
	require.NoError(t, err)
	_, _, err = d.CreateComment(ctx, db.CreateCommentParams{
		IssueID: deleted.ID,
		Author:  "tester",
		Body:    "deleted comment",
	})
	require.NoError(t, err)
	_, _, err = d.CreateLinkAndEvent(ctx, db.CreateLinkParams{
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

	var out bytes.Buffer
	require.NoError(t, jsonl.Export(ctx, d, &out, jsonl.ExportOptions{IncludeDeleted: false}))
	records := decodeJSONLLines(t, out.Bytes())

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
