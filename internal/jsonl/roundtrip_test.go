package jsonl_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
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
