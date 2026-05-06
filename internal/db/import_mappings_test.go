package db_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wesm/kata/internal/db"
)

func TestImportMapping_UpsertAndLookupIssue(t *testing.T) {
	d, ctx, p := setupTestProject(t)
	issue := makeIssue(t, ctx, d, p.ID, "imported", "tester")
	srcUpdated := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)

	m, err := d.UpsertImportMapping(ctx, db.ImportMappingParams{
		Source:          "beads",
		ExternalID:      "beads-123",
		ObjectType:      "issue",
		ProjectID:       p.ID,
		IssueID:         &issue.ID,
		SourceUpdatedAt: &srcUpdated,
	})
	require.NoError(t, err)
	assert.Equal(t, "beads", m.Source)
	assert.Equal(t, "beads-123", m.ExternalID)
	require.NotNil(t, m.IssueID)
	assert.Equal(t, issue.ID, *m.IssueID)

	got, err := d.ImportMappingBySource(ctx, p.ID, "beads", "issue", "beads-123")
	require.NoError(t, err)
	assert.Equal(t, m.ID, got.ID)
	require.NotNil(t, got.SourceUpdatedAt)
	assert.True(t, got.SourceUpdatedAt.Equal(srcUpdated))
}

func TestImportMapping_ListByProjectSource(t *testing.T) {
	d, ctx, p := setupTestProject(t)
	issue := makeIssue(t, ctx, d, p.ID, "imported", "tester")
	comment, _, err := d.CreateComment(ctx, db.CreateCommentParams{IssueID: issue.ID, Author: "tester", Body: "hi"})
	require.NoError(t, err)

	_, err = d.UpsertImportMapping(ctx, db.ImportMappingParams{
		Source: "beads", ExternalID: "beads-123", ObjectType: "issue", ProjectID: p.ID, IssueID: &issue.ID,
	})
	require.NoError(t, err)
	_, err = d.UpsertImportMapping(ctx, db.ImportMappingParams{
		Source: "beads", ExternalID: "comment-1", ObjectType: "comment", ProjectID: p.ID, IssueID: &issue.ID, CommentID: &comment.ID,
	})
	require.NoError(t, err)

	got, err := d.ImportMappingsByProjectSource(ctx, p.ID, "beads")
	require.NoError(t, err)
	assert.Len(t, got, 2)
}
