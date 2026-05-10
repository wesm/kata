package jsonl_test

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wesm/kata/internal/db"
	"github.com/wesm/kata/internal/jsonl"
)

// TestCutoverV7_AssignsShortIDsInULIDOrder pins spec §4.2 and §8 for the
// v7→v8 step: two issues whose ULIDs share their last 4 chars are replayed in
// ULID-ascending order; the lexically-earlier ULID wins length-4 and the
// later one extends to length 5.
func TestCutoverV7_AssignsShortIDsInULIDOrder(t *testing.T) {
	ctx := context.Background()
	target := openImportTargetDB(t)

	// UIDs share the same last 4 chars ("d4ex"). The lexically-earlier UID
	// (no extra 'x' before the suffix) should win the length-4 short_id.
	jsonlInput := buildV7Fixture(t, []v7Issue{
		{ProjectID: 1, ProjectName: "demo", UID: "01HZNQ7VFPK1XGD8R5MABCD4EX", Number: 1, Title: "first"},
		{ProjectID: 1, ProjectName: "demo", UID: "01HZNQ7VFPK1XGD8R5MABXD4EX", Number: 2, Title: "second"},
	})
	require.NoError(t, jsonl.Import(ctx, strings.NewReader(jsonlInput), target))

	proj, err := target.ProjectByName(ctx, "demo")
	require.NoError(t, err)
	rows, err := target.ListIssues(ctx, db.ListIssuesParams{ProjectID: proj.ID})
	require.NoError(t, err)
	require.Len(t, rows, 2)

	byUID := map[string]db.Issue{}
	for _, r := range rows {
		byUID[r.UID] = r
	}
	first := byUID["01HZNQ7VFPK1XGD8R5MABCD4EX"]
	second := byUID["01HZNQ7VFPK1XGD8R5MABXD4EX"]
	assert.Equal(t, "d4ex", first.ShortID, "lexically-earlier UID wins length-4")
	assert.Equal(t, "xd4ex", second.ShortID, "second issue extends to length 5")
}

// TestCutoverV7_RejectsProjectNameWithHash pins spec §3: project names cannot
// contain '#' after cutover. The cutover must fail before any inserts with an
// error that names the offending project.
func TestCutoverV7_RejectsProjectNameWithHash(t *testing.T) {
	ctx := context.Background()
	target := openImportTargetDB(t)

	jsonlInput := buildV7Fixture(t, []v7Issue{
		{ProjectID: 1, ProjectName: "has#hash", UID: "01HZNQ7VFPK1XGD8R5MABCD4EX", Number: 1, Title: "ok"},
	})
	err := jsonl.Import(ctx, strings.NewReader(jsonlInput), target)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "has#hash")
	assert.Contains(t, err.Error(), "must not contain '#'")

	// Cutover must fail BEFORE any inserts.
	var n int
	require.NoError(t, target.QueryRowContext(ctx, `SELECT COUNT(*) FROM projects`).Scan(&n))
	assert.Equal(t, 0, n, "cutover must fail before mutating target")
}

// TestCutover_PreservesStoredShortIDs pins spec §8.1: a round-trip on a v8
// database preserves stored short_ids byte-for-byte. The current-version
// import path must honor the source's short_id rather than re-deriving it.
func TestCutover_PreservesStoredShortIDs(t *testing.T) {
	ctx := context.Background()

	src := openExportTestDB(t)
	p, err := src.CreateProject(ctx, "demo")
	require.NoError(t, err)
	a, _, err := src.CreateIssue(ctx, db.CreateIssueParams{
		ProjectID: p.ID,
		UID:       "01HZNQ7VFPK1XGD8R5MABCD4EX",
		Title:     "a",
		Author:    "tester",
	})
	require.NoError(t, err)
	b, _, err := src.CreateIssue(ctx, db.CreateIssueParams{
		ProjectID: p.ID,
		UID:       "01HZNQ7VFPK1XGD8R5MABXD4EX",
		Title:     "b",
		Author:    "tester",
	})
	require.NoError(t, err)

	var buf bytes.Buffer
	require.NoError(t, jsonl.Export(ctx, src, &buf, jsonl.ExportOptions{}))

	dst := openImportTargetDB(t)
	require.NoError(t, jsonl.Import(ctx, bytes.NewReader(buf.Bytes()), dst))

	got, err := dst.IssueByUID(ctx, a.UID)
	require.NoError(t, err)
	assert.Equal(t, a.ShortID, got.ShortID)
	got, err = dst.IssueByUID(ctx, b.UID)
	require.NoError(t, err)
	assert.Equal(t, b.ShortID, got.ShortID)
}
