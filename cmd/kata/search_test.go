package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSearch_ReturnsMatchedIssues(t *testing.T) {
	env, dir, pid := setupCLIWorkspace(t)
	createIssue(t, env, pid, "fix login crash on Safari")
	createIssue(t, env, pid, "unrelated issue")

	out := runCLI(t, env, dir, "search", "login Safari")
	assert.Contains(t, out, "fix login crash on Safari")
	assert.NotContains(t, out, "unrelated issue")
}

func TestSearch_EmptyQueryIsValidationError(t *testing.T) {
	f := newCLIFixture(t)
	_ = requireCLIError(t, f.execute("search", "  "), ExitValidation)
}

// TestSearch_UnquotedMultiTerm verifies that `kata search login Safari`
// (no quotes) joins the args with spaces and matches the same way as the
// quoted form. Required by the BM25 implicit-AND contract.
func TestSearch_UnquotedMultiTerm(t *testing.T) {
	env, dir, pid := setupCLIWorkspace(t)
	createIssue(t, env, pid, "fix login crash on Safari")
	createIssue(t, env, pid, "unrelated issue")

	out := runCLI(t, env, dir, "search", "login", "Safari")
	assert.Contains(t, out, "fix login crash on Safari")
	assert.NotContains(t, out, "unrelated issue")
}

// TestSearch_RejectsNonPositiveLimit covers hammer-test #5: --limit
// 0/-1 used to be silently treated as "no limit" because
// buildSearchURL only set the param when limit > 0. Now mirrors
// list/ready/events/daemon-logs validation.
func TestSearch_RejectsNonPositiveLimit(t *testing.T) {
	for _, lim := range []string{"0", "-1"} {
		_, err := runCmdOutput(t, nil, "search", "x", "--limit", lim)
		_ = requireCLIError(t, err, ExitValidation)
	}
}
