package main

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLink_GenericRoundTrip(t *testing.T) {
	env, dir, pid := setupCLIWorkspace(t)
	createIssue(t, env, pid, "a")
	createIssue(t, env, pid, "b")

	out := runCLI(t, env, dir, "link", "1", "blocks", "2")
	assert.True(t, strings.Contains(out, "linked") || strings.Contains(out, "blocks"))
}

func TestLink_AcceptsUIDRefs(t *testing.T) {
	env, dir, pid := setupCLIWorkspace(t)
	createIssue(t, env, pid, "a")
	createIssue(t, env, pid, "b")
	a, err := env.DB.IssueByNumber(context.Background(), pid, 1)
	require.NoError(t, err)
	b, err := env.DB.IssueByNumber(context.Background(), pid, 2)
	require.NoError(t, err)

	out := runCLI(t, env, dir, "link", a.UID, "blocks", b.UID)
	assert.True(t, strings.Contains(out, "linked") || strings.Contains(out, "blocks"))

	link, err := env.DB.LinkByEndpoints(context.Background(), a.ID, b.ID, "blocks")
	require.NoError(t, err)
	assert.Equal(t, a.UID, link.FromIssueUID)
	assert.Equal(t, b.UID, link.ToIssueUID)
}

func TestUnlink_AcceptsUIDRefs(t *testing.T) {
	env, dir, pid := setupCLIWorkspace(t)
	createIssue(t, env, pid, "a")
	createIssue(t, env, pid, "b")
	a, err := env.DB.IssueByNumber(context.Background(), pid, 1)
	require.NoError(t, err)
	b, err := env.DB.IssueByNumber(context.Background(), pid, 2)
	require.NoError(t, err)
	createLinkViaHTTP(t, env, pid, 1, "blocks", 2)

	out := runCLI(t, env, dir, "unlink", a.UID, "blocks", b.UID)
	assert.Contains(t, out, "unlinked")
}

func TestParent_WithReplace(t *testing.T) {
	env, dir, pid := setupCLIWorkspace(t)
	createIssue(t, env, pid, "child")
	createIssue(t, env, pid, "p1")
	createIssue(t, env, pid, "p2")

	runCLI(t, env, dir, "parent", "1", "2")
	out := runCLI(t, env, dir, "parent", "1", "3", "--replace")
	assert.True(t, strings.Contains(out, "linked") || strings.Contains(out, "parent"))
}

func TestUnlink_RemovesLink(t *testing.T) {
	env, dir, pid := setupCLIWorkspace(t)
	createIssue(t, env, pid, "a")
	createIssue(t, env, pid, "b")
	createLinkViaHTTP(t, env, pid, 1, "blocks", 2)

	out := runCLI(t, env, dir, "unlink", "1", "blocks", "2")
	assert.True(t, strings.Contains(out, "unlinked") || strings.Contains(out, "removed"))
}

func TestUnparent_RemovesParentLink(t *testing.T) {
	env, dir, pid := setupCLIWorkspace(t)
	createIssue(t, env, pid, "child")
	createIssue(t, env, pid, "p")
	createLinkViaHTTP(t, env, pid, 1, "parent", 2)

	out := runCLI(t, env, dir, "unparent", "1")
	assert.True(t, strings.Contains(out, "unlinked") || strings.Contains(out, "removed"))
}

func TestRelate_CanonicalOrderingHidesArgOrder(t *testing.T) {
	env, dir, pid := setupCLIWorkspace(t)
	createIssue(t, env, pid, "a")
	createIssue(t, env, pid, "b")

	runCLI(t, env, dir, "relate", "2", "1")
	out := runCLI(t, env, dir, "relate", "1", "2")
	assert.Contains(t, out, "no-op")
}

func TestBlock_RoundTrip(t *testing.T) {
	env, dir, pid := setupCLIWorkspace(t)
	createIssue(t, env, pid, "blocker")
	createIssue(t, env, pid, "blocked")

	runCLI(t, env, dir, "block", "1", "2")
	out := runCLI(t, env, dir, "unblock", "1", "2")
	assert.True(t, strings.Contains(out, "unlinked") || strings.Contains(out, "removed"))
}

// TestUnlink_RelatedReverseOrderStillFinds verifies that `kata unlink 5
// related 3` matches a link stored canonically as (3,5). The daemon
// canonicalizes related storage to (min,max), so order at lookup time must
// not matter.
func TestUnlink_RelatedReverseOrderStillFinds(t *testing.T) {
	env, dir, pid := setupCLIWorkspace(t)
	createIssue(t, env, pid, "a")
	createIssue(t, env, pid, "b")

	// Daemon stores this canonically as (1,2).
	createLinkViaHTTP(t, env, pid, 1, "related", 2)

	// User passes (2,1) — reverse of the canonical storage order.
	out := runCLI(t, env, dir, "unlink", "2", "related", "1")
	assert.Contains(t, out, "unlinked")
}
