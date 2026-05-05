package main

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLabelAdd_HappyPath(t *testing.T) {
	env, dir, _ := setupWorkspaceWithIssue(t, "a")

	out := runCLI(t, env, dir, "label", "add", "1", "needs-review")
	assert.Contains(t, out, "needs-review")
}

func TestLabelRm_HappyPath(t *testing.T) {
	env, dir, _ := setupWorkspaceWithIssue(t, "a")

	runCLI(t, env, dir, "label", "add", "1", "bug")
	out := runCLI(t, env, dir, "label", "rm", "1", "bug")
	assert.True(t, strings.Contains(out, "removed") || strings.Contains(out, "unlabeled"))
}

func TestLabelsList_PrintsCounts(t *testing.T) {
	env, dir, _ := setupWorkspaceWithIssue(t, "a")

	runCLI(t, env, dir, "label", "add", "1", "bug")
	out := runCLI(t, env, dir, "labels")
	assert.Contains(t, out, "bug")
	assert.Contains(t, out, "1")
}

// TestLabel_RejectsEmptyLabel covers hammer-test finding #8: label
// rm 1 "" used to URL-encode to "" and hit /labels/?actor=... which
// the daemon answered with a raw 404 page. label add 1 "" was already
// validated in some daemon path but the messaging was inconsistent.
// Now both reject client-side with a uniform validation message.
func TestLabel_RejectsEmptyLabel(t *testing.T) {
	for _, args := range [][]string{
		{"label", "add", "1", ""},
		{"label", "rm", "1", "  "},
	} {
		resetFlags(t)
		_, _, err := executeRootCapture(t, context.Background(), args...)
		require.Errorf(t, err, "args %v should reject", args)
		ce := requireCLIError(t, err, ExitValidation)
		assert.Contains(t, ce.Message, "label must not be empty")
	}
}

// TestCreate_RejectsWhitespaceLabel covers the create --label case
// from hammer #8. Pflag's StringSliceVar drops a literal empty
// argument (""), but a whitespace-only label like "   " makes it
// through and used to be silently dropped by the daemon. Reject
// client-side instead.
func TestCreate_RejectsWhitespaceLabel(t *testing.T) {
	resetFlags(t)
	_, _, err := executeRootCapture(t, context.Background(), "create", "title", "--label", "   ")
	ce := requireCLIError(t, err, ExitValidation)
	assert.Contains(t, ce.Message, "label must not be empty")
}
