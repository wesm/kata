package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRestore_ClearsDeletedAt(t *testing.T) {
	env, dir, pid := setupCLIWorkspace(t)
	short := createIssue(t, env, pid, "delete me")

	runCLI(t, env, dir, "delete", short, "--force", "--confirm", "DELETE kata#"+short)
	output := runCLI(t, env, dir, "restore", short)
	assert.Contains(t, output, "restored")
}
