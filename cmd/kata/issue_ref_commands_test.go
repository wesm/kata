package main

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wesm/kata/internal/db"
)

func TestIssueRefCommandsAcceptUIDs(t *testing.T) {
	env, dir := setupCLIEnv(t)
	pid := resolvePIDViaHTTP(t, env.URL, dir)

	issue := func(title string) db.Issue {
		t.Helper()
		num := createIssueViaHTTP(t, env, dir, title)
		iss, err := env.DB.IssueByNumber(context.Background(), pid, num)
		require.NoError(t, err)
		return iss
	}
	run := func(args ...string) string {
		t.Helper()
		resetFlags(t)
		return runCLI(t, env, dir, args...)
	}

	assign := issue("assign by uid")
	out := run("assign", assign.UID, "alice")
	require.Contains(t, out, "alice")

	out = run("unassign", assign.UID[:12])
	require.Contains(t, out, "unassigned")

	comment := issue("comment by uid")
	out = run("comment", comment.UID, "--body", "uid comment")
	require.True(t, strings.Contains(out, "uid comment") || strings.Contains(out, "comment"))

	label := issue("label by uid")
	out = run("label", "add", label.UID, "uid-label")
	require.Contains(t, out, "uid-label")
	out = run("label", "rm", label.UID[:12], "uid-label")
	require.True(t, strings.Contains(out, "removed") || strings.Contains(out, "unlabeled"))

	edit := issue("edit by uid")
	out = run("edit", edit.UID, "--title", "edited through uid")
	require.Contains(t, out, "edited through uid")

	closeReopen := issue("close by uid")
	out = run("close", closeReopen.UID, "--reason", "wontfix")
	require.Contains(t, out, "closed")
	out = run("reopen", closeReopen.UID[:12])
	require.Contains(t, out, "open")

	deleteRestore := issue("delete by uid")
	out = run("delete", deleteRestore.UID, "--force", "--confirm", fmt.Sprintf("DELETE #%d", deleteRestore.Number))
	require.Contains(t, out, "deleted")
	out = run("restore", deleteRestore.UID[:12])
	require.Contains(t, out, "restored")

	purge := issue("purge by uid")
	out = run("purge", purge.UID, "--force", "--confirm", fmt.Sprintf("PURGE #%d", purge.Number))
	require.Contains(t, out, "purged")
}
