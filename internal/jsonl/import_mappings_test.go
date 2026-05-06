package jsonl_test

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wesm/kata/internal/db"
	"github.com/wesm/kata/internal/jsonl"
)

func TestExportImport_PreservesImportMappings(t *testing.T) {
	ctx := context.Background()
	src := openExportTestDB(t)
	p, err := src.CreateProject(ctx, "github.com/wesm/kata", "kata")
	require.NoError(t, err)
	issue, _, err := src.CreateIssue(ctx, db.CreateIssueParams{ProjectID: p.ID, Title: "from beads", Author: "tester"})
	require.NoError(t, err)
	comment, _, err := src.CreateComment(ctx, db.CreateCommentParams{IssueID: issue.ID, Author: "tester", Body: "comment"})
	require.NoError(t, err)
	srcUpdated := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)

	_, err = src.UpsertImportMapping(ctx, db.ImportMappingParams{
		Source:          "beads",
		ExternalID:      "issue-1",
		ObjectType:      "issue",
		ProjectID:       p.ID,
		IssueID:         &issue.ID,
		SourceUpdatedAt: &srcUpdated,
	})
	require.NoError(t, err)
	_, err = src.UpsertImportMapping(ctx, db.ImportMappingParams{
		Source:     "beads",
		ExternalID: "comment-1",
		ObjectType: "comment",
		ProjectID:  p.ID,
		IssueID:    &issue.ID,
		CommentID:  &comment.ID,
	})
	require.NoError(t, err)

	var buf bytes.Buffer
	require.NoError(t, jsonl.Export(ctx, src, &buf, jsonl.ExportOptions{IncludeDeleted: true}))
	assert.Contains(t, buf.String(), `"kind":"import_mapping"`)

	dst := openExportTestDB(t)
	require.NoError(t, jsonl.Import(ctx, bytes.NewReader(buf.Bytes()), dst))

	got, err := dst.ImportMappingsByProjectSource(ctx, p.ID, "beads")
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, "issue-1", got[0].ExternalID)
	assert.Equal(t, "comment-1", got[1].ExternalID)
	require.NotNil(t, got[0].SourceUpdatedAt)
	assert.True(t, got[0].SourceUpdatedAt.Equal(srcUpdated), "got %s, want %s", got[0].SourceUpdatedAt, srcUpdated)
}
