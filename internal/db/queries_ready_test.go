package db_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wesm/kata/internal/db"
)

func TestReadyIssues_FiltersOutClosed(t *testing.T) {
	d, ctx, p := setupTestProject(t)
	open := makeIssue(t, ctx, d, p.ID, "open", "tester")
	closed := makeIssue(t, ctx, d, p.ID, "closed", "tester")
	_, _, _, err := d.CloseIssue(ctx, closed.ID, "done", "tester")
	require.NoError(t, err)

	got := readyNumbers(t, ctx, d, p.ID)
	assert.Contains(t, got, open.Number)
	assert.NotContains(t, got, closed.Number)
}

func TestReadyIssues_ExcludesIssuesBlockedByOpenBlocker(t *testing.T) {
	d, ctx, p := setupTestProject(t)
	blocker := makeIssue(t, ctx, d, p.ID, "blocker", "tester")
	blocked := makeIssue(t, ctx, d, p.ID, "blocked", "tester")
	standalone := makeIssue(t, ctx, d, p.ID, "standalone", "tester")
	makeLink(ctx, t, d, p.ID, blocker.ID, blocked.ID, "blocks")

	got := readyNumbers(t, ctx, d, p.ID)
	assert.Contains(t, got, blocker.Number, "blocker is ready (not blocked itself)")
	assert.Contains(t, got, standalone.Number, "standalone is ready")
	assert.NotContains(t, got, blocked.Number, "blocked is not ready while blocker is open")
}

func TestReadyIssues_ClosedBlockerUnblocksDownstream(t *testing.T) {
	d, ctx, p := setupTestProject(t)
	blocker := makeIssue(t, ctx, d, p.ID, "blocker", "tester")
	blocked := makeIssue(t, ctx, d, p.ID, "blocked", "tester")
	makeLink(ctx, t, d, p.ID, blocker.ID, blocked.ID, "blocks")
	_, _, _, err := d.CloseIssue(ctx, blocker.ID, "done", "tester")
	require.NoError(t, err)

	got := readyNumbers(t, ctx, d, p.ID)
	assert.Contains(t, got, blocked.Number, "blocked is ready once blocker closes")
}

// readyNumbers fetches ready issues for projectID and returns their numbers.
//
//nolint:revive // test helper: t *testing.T conventionally precedes ctx.
func readyNumbers(t *testing.T, ctx context.Context, d *db.DB, projectID int64) []int64 {
	t.Helper()
	rows, err := d.ReadyIssues(ctx, projectID, 0)
	require.NoError(t, err)
	out := make([]int64, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.Number)
	}
	return out
}
