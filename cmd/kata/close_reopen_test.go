package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCloseReopen_RoundTrip(t *testing.T) {
	env, dir, _ := setupWorkspaceWithIssue(t, "test issue")

	out := runCLI(t, env, dir, "close", "1", "--reason", "wontfix")
	assert.Contains(t, out, "closed")

	out = runCLI(t, env, dir, "reopen", "1")
	assert.Contains(t, out, "open")
}
