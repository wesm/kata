package db_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wesm/kata/internal/db"
)

func TestCreateIssue_WithInitialPriority(t *testing.T) {
	d, ctx, p := setupTestProject(t)
	for _, prio := range []int64{0, 1, 2, 3, 4} {
		prio := prio
		t.Run(t.Name()+"_p"+string(rune('0'+prio)), func(t *testing.T) {
			issue, evt, err := d.CreateIssue(ctx, db.CreateIssueParams{
				ProjectID: p.ID, Title: "x", Author: "tester",
				Priority: &prio,
			})
			require.NoError(t, err)
			require.NotNil(t, issue.Priority)
			assert.Equal(t, prio, *issue.Priority)

			payload := unmarshalPayload[struct {
				Priority *int64 `json:"priority"`
			}](t, evt.Payload)
			require.NotNil(t, payload.Priority)
			assert.Equal(t, prio, *payload.Priority)
		})
	}
}

func TestCreateIssue_NoPriorityIsNullAndOmittedFromPayload(t *testing.T) {
	d, ctx, p := setupTestProject(t)
	issue, evt, err := d.CreateIssue(ctx, db.CreateIssueParams{
		ProjectID: p.ID, Title: "x", Author: "tester",
	})
	require.NoError(t, err)
	assert.Nil(t, issue.Priority)
	// No priority key means buildCreatedPayload still emits the empty payload.
	assert.Equal(t, "{}", evt.Payload)
}

func TestCreateIssue_RejectsPriorityOutOfRange(t *testing.T) {
	d, ctx, p := setupTestProject(t)
	for _, bad := range []int64{-1, 5, 99} {
		bad := bad
		_, _, err := d.CreateIssue(ctx, db.CreateIssueParams{
			ProjectID: p.ID, Title: "x", Author: "tester",
			Priority: &bad,
		})
		require.Error(t, err, "priority %d should violate CHECK", bad)
		// The schema CHECK fires at INSERT; surface includes "CHECK" in the
		// driver-level message regardless of the specific wrapper kata adds.
		assert.True(t, strings.Contains(strings.ToLower(err.Error()), "check"),
			"error %q should mention CHECK constraint", err.Error())
	}
}

func TestIssueByNumber_SurfacesPriority(t *testing.T) {
	d, ctx, p := setupTestProject(t)
	prio := int64(2)
	created, _, err := d.CreateIssue(ctx, db.CreateIssueParams{
		ProjectID: p.ID, Title: "x", Author: "tester",
		Priority: &prio,
	})
	require.NoError(t, err)

	got, err := d.IssueByNumber(ctx, p.ID, created.Number)
	require.NoError(t, err)
	require.NotNil(t, got.Priority)
	assert.Equal(t, prio, *got.Priority)
}
