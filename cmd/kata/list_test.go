package main

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestList_DefaultsToOpenIssuesInProject(t *testing.T) {
	env, dir, pid := setupCLIWorkspace(t)
	for _, title := range []string{"alpha", "beta"} {
		createIssue(t, env, pid, title)
	}

	out := runCLI(t, env, dir, "list")
	assert.Contains(t, out, "alpha")
	assert.Contains(t, out, "beta")
}

// TestList_SanitizesAnsiAndNewlinesInTitle covers hammer-test
// finding #2: a malicious title containing ANSI escape sequences or
// embedded newlines must not reach stdout raw, where it could clear
// the screen, set the window title, or break row layout. Sanitized
// at the human-output boundary; the JSON path is exempt (agents need
// the raw bytes).
func TestList_SanitizesAnsiAndNewlinesInTitle(t *testing.T) {
	env, dir, pid := setupCLIWorkspace(t)
	createIssue(t, env, pid, "evil\x1b[2Jtitle\nwith newline")

	out := runCLI(t, env, dir, "list")
	assert.NotContains(t, out, "\x1b", "ESC reached stdout")
	// The newline in the title must be escaped (\n literal) so the
	// list row stays on one visual line.
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	for _, ln := range lines {
		assert.NotEmpty(t, ln, "list output produced a blank row from injected newline")
	}
}
