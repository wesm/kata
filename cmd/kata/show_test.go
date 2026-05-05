package main

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestShow_RendersLabelsAndLinksSections(t *testing.T) {
	env, dir, pid := setupCLIWorkspace(t)
	createIssue(t, env, pid, "parent") // #1
	createIssue(t, env, pid, "child")  // #2
	// Two labels so we exercise the comma-join.
	for _, label := range []string{"bug", "priority:high"} {
		runCLI(t, env, dir, "label", "add", "2", label)
	}
	createLinkViaHTTP(t, env, pid, 2, "parent", 1)

	out := runCLI(t, env, dir, "show", "2")
	// Exact section headers and comma-joined label rendering.
	assert.Contains(t, out, "--- labels ---")
	assert.Contains(t, out, "bug, priority:high")
	// Links section: child is the link's "from" side, so the arrow points
	// outward (→) toward parent #1.
	assert.Contains(t, out, "--- links ---")
	assert.Contains(t, out, "parent → #1")
}

// TestShow_LinkArrowReversesOnToSide verifies that when show runs against
// the link's "to" side, the rendered arrow flips (←) so the line still reads
// from the perspective of the issue being shown.
func TestShow_LinkArrowReversesOnToSide(t *testing.T) {
	env, dir, pid := setupCLIWorkspace(t)
	createIssue(t, env, pid, "parent") // #1
	createIssue(t, env, pid, "child")  // #2
	// child → parent stores (from=2, to=1). Showing #1 puts us on the to side.
	createLinkViaHTTP(t, env, pid, 2, "parent", 1)

	out := runCLI(t, env, dir, "show", "1")
	assert.Contains(t, out, "parent ← #2", "to-side show must reverse the arrow")
}

func TestShow_AcceptsHashFullUIDAndUniquePrefix(t *testing.T) {
	env, dir, pid := setupCLIWorkspace(t)
	createIssue(t, env, pid, "uid target")
	issue, err := env.DB.IssueByNumber(context.Background(), pid, 1)
	require.NoError(t, err)

	for _, ref := range []string{"#1", issue.UID, issue.UID[:12]} {
		out := runCLI(t, env, dir, "show", ref)
		assert.Contains(t, out, "uid target", "ref %s", ref)
	}
}

func TestShow_UIDPrefixErrors(t *testing.T) {
	env, dir, pid := setupCLIWorkspace(t)
	createIssue(t, env, pid, "a")
	createIssue(t, env, pid, "b")
	uidA := "01JZ0000000000000000000001"
	uidB := "01JZ0000000000000000000002"
	_, err := env.DB.ExecContext(context.Background(), `DROP TRIGGER trg_issues_uid_immutable`)
	require.NoError(t, err)
	_, err = env.DB.ExecContext(context.Background(), `UPDATE issues SET uid = ? WHERE project_id = ? AND number = 1`, uidA, pid)
	require.NoError(t, err)
	_, err = env.DB.ExecContext(context.Background(), `UPDATE issues SET uid = ? WHERE project_id = ? AND number = 2`, uidB, pid)
	require.NoError(t, err)
	first, err := env.DB.IssueByNumber(context.Background(), pid, 1)
	require.NoError(t, err)

	_, err = runCLICapture(t, env, dir, "show", first.UID[:4])
	require.Error(t, err)
	var ce *cliError
	require.True(t, errors.As(err, &ce))
	assert.Equal(t, "prefix_too_short", ce.Code)

	_, err = runCLICapture(t, env, dir, "show", first.UID[:8])
	require.Error(t, err)
	require.True(t, errors.As(err, &ce))
	assert.Equal(t, "prefix_ambiguous", ce.Code)
	assert.Contains(t, ce.Message, first.UID)
}
