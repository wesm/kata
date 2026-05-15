package db_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wesm/kata/internal/db"
)

func TestPatchIssueMetadata_HappyPath(t *testing.T) {
	d, ctx, _, iss := setupTestIssue(t)

	res, err := d.PatchIssueMetadata(ctx, db.PatchIssueMetadataIn{
		IssueID: iss.ID, IfMatchRev: iss.Revision, Actor: "tester",
		Patch: map[string]json.RawMessage{
			"scheduled_on": json.RawMessage(`"2026-05-20"`),
		},
	})
	require.NoError(t, err)
	assert.True(t, res.Changed)
	assert.Equal(t, iss.Revision+1, res.NewRevision)
	assert.Contains(t, string(res.Issue.Metadata), `"scheduled_on":"2026-05-20"`)
	assert.NotZero(t, res.Event.ID)
	assert.Equal(t, "issue.metadata_updated", res.Event.Type)
}

func TestPatchIssueMetadata_StaleRevisionReturns409(t *testing.T) {
	d, ctx, _, iss := setupTestIssue(t)

	_, err := d.PatchIssueMetadata(ctx, db.PatchIssueMetadataIn{
		IssueID: iss.ID, IfMatchRev: 99, Actor: "tester",
		Patch: map[string]json.RawMessage{
			"scheduled_on": json.RawMessage(`"2026-05-20"`),
		},
	})
	require.Error(t, err)
	var conflict *db.RevisionConflictError
	require.ErrorAs(t, err, &conflict)
	assert.Equal(t, iss.Revision, conflict.CurrentRevision)
}

func TestPatchIssueMetadata_EmptyDiffNoEvent(t *testing.T) {
	d, ctx, _, iss := setupTestIssue(t)

	// First patch sets the key (revision bumps).
	res1, err := d.PatchIssueMetadata(ctx, db.PatchIssueMetadataIn{
		IssueID: iss.ID, IfMatchRev: iss.Revision, Actor: "tester",
		Patch: map[string]json.RawMessage{
			"scheduled_on": json.RawMessage(`"2026-05-20"`),
		},
	})
	require.NoError(t, err)
	require.True(t, res1.Changed)

	// Re-applying the same value is a no-op.
	res2, err := d.PatchIssueMetadata(ctx, db.PatchIssueMetadataIn{
		IssueID: iss.ID, IfMatchRev: res1.NewRevision, Actor: "tester",
		Patch: map[string]json.RawMessage{
			"scheduled_on": json.RawMessage(`"2026-05-20"`),
		},
	})
	require.NoError(t, err)
	assert.False(t, res2.Changed)
	assert.Zero(t, res2.Event.ID, "no-op patch must not emit an event")
	assert.Equal(t, res1.NewRevision, res2.NewRevision, "revision unchanged on no-op")

	// Verify no second issue.metadata_updated event in the events table.
	var n int
	require.NoError(t, d.QueryRow(`
		SELECT COUNT(*) FROM events WHERE type='issue.metadata_updated' AND issue_id = ?
	`, iss.ID).Scan(&n))
	assert.Equal(t, 1, n, "no-op patch must not append another event row")
}

func TestPatchIssueMetadata_InvalidKeyValueRejected(t *testing.T) {
	d, ctx, _, iss := setupTestIssue(t)

	_, err := d.PatchIssueMetadata(ctx, db.PatchIssueMetadataIn{
		IssueID: iss.ID, IfMatchRev: iss.Revision, Actor: "tester",
		Patch: map[string]json.RawMessage{
			"today_bucket": json.RawMessage(`"midnight"`), // not in enum
		},
	})
	require.Error(t, err)
}

func TestPatchIssueMetadata_ClearKeyWithNull(t *testing.T) {
	d, ctx, _, iss := setupTestIssue(t)

	// Set a key first.
	res1, err := d.PatchIssueMetadata(ctx, db.PatchIssueMetadataIn{
		IssueID: iss.ID, IfMatchRev: iss.Revision, Actor: "tester",
		Patch: map[string]json.RawMessage{
			"scheduled_on": json.RawMessage(`"2026-05-20"`),
		},
	})
	require.NoError(t, err)

	// Clear it with null.
	res2, err := d.PatchIssueMetadata(ctx, db.PatchIssueMetadataIn{
		IssueID: iss.ID, IfMatchRev: res1.NewRevision, Actor: "tester",
		Patch: map[string]json.RawMessage{
			"scheduled_on": json.RawMessage(`null`),
		},
	})
	require.NoError(t, err)
	assert.True(t, res2.Changed)
	assert.NotContains(t, string(res2.Issue.Metadata), "scheduled_on")
}
