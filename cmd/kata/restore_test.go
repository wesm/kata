package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRestore_ClearsDeletedAt(t *testing.T) {
	env, dir, pid := setupCLIWorkspace(t)
	createIssue(t, env, pid, "delete me")

	runCLI(t, env, dir, "delete", "1", "--force", "--confirm", "DELETE #1")
	output := runCLI(t, env, dir, "restore", "1")
	assert.Contains(t, output, "restored")
}
