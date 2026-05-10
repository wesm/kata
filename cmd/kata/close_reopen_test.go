package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCloseReopen_RoundTrip(t *testing.T) {
	env, dir, _, ref := setupWorkspaceWithIssue(t, "test issue")

	out := runCLI(t, env, dir, "close", ref, "--reason", "wontfix")
	assert.Contains(t, out, "closed")

	out = runCLI(t, env, dir, "reopen", ref)
	assert.Contains(t, out, "open")
}
