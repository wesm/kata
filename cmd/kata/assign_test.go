package main

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestAssign_RoundTrip(t *testing.T) {
	env, dir, _ := setupWorkspaceWithIssue(t, "x")

	out := runCLI(t, env, dir, "assign", "1", "alice")
	assert.True(t, strings.Contains(out, "assigned") ||
		strings.Contains(out, "alice"))

	uOut := runCLI(t, env, dir, "unassign", "1")
	assert.True(t, strings.Contains(uOut, "unassigned"))
}
