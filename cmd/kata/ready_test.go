package main

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestReady_FiltersBlocked(t *testing.T) {
	env, dir, pid := setupCLIWorkspace(t)
	blocker := createIssue(t, env, pid, "blocker")
	blocked := createIssue(t, env, pid, "blocked")
	createIssue(t, env, pid, "standalone")
	createLinkViaHTTP(t, env, pid, blocker, "blocks", blocked)

	out := runCLI(t, env, dir, "ready")
	assert.Contains(t, out, "blocker")
	assert.Contains(t, out, "standalone")
	assert.False(t, strings.Contains(out, "blocked"),
		"blocked is hidden while blocker is open")
}
