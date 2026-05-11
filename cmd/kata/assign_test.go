package main

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestAssign_RoundTrip(t *testing.T) {
	env, dir, _, ref := setupWorkspaceWithIssue(t, "x")

	out := runCLI(t, env, dir, "assign", ref, "alice")
	assert.True(t, strings.Contains(out, "assigned") ||
		strings.Contains(out, "alice"))

	uOut := runCLI(t, env, dir, "unassign", ref)
	assert.True(t, strings.Contains(uOut, "unassigned"))
}
