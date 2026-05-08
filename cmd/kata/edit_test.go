package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEdit_AddsAllFourLinkDirections covers the new add flags on `kata edit`.
// One edit call attaches a parent, a blocks-out, a blocked-by, and a related
// link in a single PATCH; all four must be persisted.
func TestEdit_AddsAllFourLinkDirections(t *testing.T) {
	env, dir := setupCLIEnv(t)
	pid := resolvePIDViaHTTP(t, env.URL, dir)
	createIssue(t, env, pid, "subject")     // #1
	createIssue(t, env, pid, "the-parent")  // #2
	createIssue(t, env, pid, "the-blocked") // #3
	createIssue(t, env, pid, "the-blocker") // #4
	createIssue(t, env, pid, "the-peer")    // #5

	runCLI(t, env, dir, "edit", "1",
		"--parent", "2",
		"--blocks", "3",
		"--blocked-by", "4",
		"--related", "5",
	)

	b := fetchIssueViaHTTP(t, env, pid, 1)
	var sawParent, sawBlocks, sawBlockedBy, sawRelated bool
	for _, l := range b.Links {
		switch l.Type {
		case "parent":
			if l.FromNumber == 1 && l.ToNumber == 2 {
				sawParent = true
			}
		case "blocks":
			switch {
			case l.FromNumber == 1 && l.ToNumber == 3:
				sawBlocks = true
			case l.FromNumber == 4 && l.ToNumber == 1:
				sawBlockedBy = true
			}
		case "related":
			if (l.FromNumber == 1 && l.ToNumber == 5) || (l.FromNumber == 5 && l.ToNumber == 1) {
				sawRelated = true
			}
		}
	}
	assert.True(t, sawParent, "parent link missing")
	assert.True(t, sawBlocks, "blocks link missing")
	assert.True(t, sawBlockedBy, "blocked-by link missing")
	assert.True(t, sawRelated, "related link missing")
}

// TestEdit_RemoveParent_StrictMatch removes the parent link when the asserted
// parent number matches the current one.
func TestEdit_RemoveParent_StrictMatch(t *testing.T) {
	env, dir := setupCLIEnv(t)
	pid := resolvePIDViaHTTP(t, env.URL, dir)
	createIssue(t, env, pid, "child")
	createIssue(t, env, pid, "parent")
	runCLI(t, env, dir, "edit", "1", "--parent", "2")

	runCLI(t, env, dir, "edit", "1", "--remove-parent", "2")

	b := fetchIssueViaHTTP(t, env, pid, 1)
	for _, l := range b.Links {
		assert.NotEqual(t, "parent", l.Type, "parent link should be gone")
	}
}

// TestEdit_RemoveParent_MismatchFails surfaces a 409-flavored error when the
// asserted parent number does not match the current parent. Protects agents
// from acting on stale state.
func TestEdit_RemoveParent_MismatchFails(t *testing.T) {
	env, dir := setupCLIEnv(t)
	pid := resolvePIDViaHTTP(t, env.URL, dir)
	createIssue(t, env, pid, "child")
	createIssue(t, env, pid, "parent")
	createIssue(t, env, pid, "wrong-parent")
	runCLI(t, env, dir, "edit", "1", "--parent", "2")

	_, err := runCLICapture(t, env, dir, "edit", "1", "--remove-parent", "3")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parent")
}

// TestEdit_RemoveLinksAreIdempotent succeeds with no error and no panic when
// the requested link is already gone.
func TestEdit_RemoveLinksAreIdempotent(t *testing.T) {
	env, dir := setupCLIEnv(t)
	pid := resolvePIDViaHTTP(t, env.URL, dir)
	createIssue(t, env, pid, "subject")
	createIssue(t, env, pid, "other")

	runCLI(t, env, dir, "edit", "1", "--remove-blocks", "2")

	b := fetchIssueViaHTTP(t, env, pid, 1)
	assert.Empty(t, b.Links, "no links should exist after no-op remove")
}

// TestEdit_LinkFlagsRejectEmptyOrCommaOnly pins that an empty or
// comma-only flag value (e.g. `--blocks ""`, `--blocks ","`) fails
// validation before any field landed. Silently dropping the malformed
// flag would otherwise let mixed PATCHes apply the non-link mutations
// while quietly discarding the bad relationship operand.
func TestEdit_LinkFlagsRejectEmptyOrCommaOnly(t *testing.T) {
	env, dir := setupCLIEnv(t)
	pid := resolvePIDViaHTTP(t, env.URL, dir)
	createIssue(t, env, pid, "subject")

	for _, val := range []string{"", ","} {
		t.Run("blocks="+val, func(t *testing.T) {
			_, err := runCLICapture(t, env, dir, "edit", "1", "--blocks", val)
			require.Error(t, err, "empty/comma-only --blocks must fail validation")
			assert.Contains(t, err.Error(), "must not be empty")
		})
	}

	// Mixed PATCH: --title T --blocks "" must fail entirely; the title
	// update must NOT land. The validation runs CLI-side before the
	// daemon round-trip.
	_, err := runCLICapture(t, env, dir, "edit", "1", "--title", "NEW", "--blocks", "")
	require.Error(t, err)
	got := fetchIssueViaHTTP(t, env, pid, 1)
	assert.Equal(t, "subject", got.Issue.Title,
		"title must be unchanged when a sibling link flag rejected the whole edit")
}

// TestEdit_RemoveLinksTolerateNonexistentTarget pins the contract that
// the idempotent remove flags (--remove-blocks / --remove-blocked-by /
// --remove-related) treat "target issue doesn't exist" the same as
// "edge doesn't exist": both are no-ops. The desired end state is "no
// link from this issue to N"; if there's no N, that end state already
// holds, so the request should succeed.
//
// The strict --remove-parent flag keeps its existing behavior — it
// asserts a fact about the current parent and a missing target is
// still a 4xx (covered by TestEdit_RemoveParent_MismatchFails).
func TestEdit_RemoveLinksTolerateNonexistentTarget(t *testing.T) {
	env, dir := setupCLIEnv(t)
	pid := resolvePIDViaHTTP(t, env.URL, dir)
	createIssue(t, env, pid, "subject")

	// #99 has never existed. Each idempotent remove must succeed as a no-op.
	runCLI(t, env, dir, "edit", "1", "--remove-blocks", "99")
	runCLI(t, env, dir, "edit", "1", "--remove-blocked-by", "99")
	runCLI(t, env, dir, "edit", "1", "--remove-related", "99")

	b := fetchIssueViaHTTP(t, env, pid, 1)
	assert.Empty(t, b.Links, "issue must have no links after idempotent removes against missing targets")
}

// TestEdit_RemoveLinksTolerateNonresolvableUIDPrefix covers the
// non-numeric path of the same idempotent contract. UID/prefix refs
// resolve via the daemon — when the prefix matches no issue, the
// resolver drops it CLI-side rather than letting it reach the
// daemon. The PATCH would then carry an empty links_delta, which the
// daemon would reject as "no fields to update" (400). The CLI must
// short-circuit pure-link no-ops locally and emit the standard
// "(no changes applied)" human-mode tail without a daemon round-trip.
func TestEdit_RemoveLinksTolerateNonresolvableUIDPrefix(t *testing.T) {
	env, dir := setupCLIEnv(t)
	pid := resolvePIDViaHTTP(t, env.URL, dir)
	createIssue(t, env, pid, "subject")

	// 8+ chars, valid Crockford alphabet, deliberately not matching any
	// real issue UID in this fresh project.
	bogusPrefix := "0Z9Z9Z9Z"

	out := runCLI(t, env, dir, "edit", "1", "--remove-blocks", bogusPrefix)
	assert.Contains(t, out, "(no changes applied)",
		"non-resolvable UID prefix on idempotent remove must succeed as a no-op")

	out = runCLI(t, env, dir, "edit", "1", "--remove-related", bogusPrefix)
	assert.Contains(t, out, "(no changes applied)")

	out = runCLI(t, env, dir, "edit", "1", "--remove-blocked-by", bogusPrefix)
	assert.Contains(t, out, "(no changes applied)")

	b := fetchIssueViaHTTP(t, env, pid, 1)
	assert.Empty(t, b.Links)
}

// TestEdit_LinkFlagsAcceptIssueRefs covers the kata#1 follow-up that
// keeps non-numeric refs (#N, full UID, UID prefix) usable on the new
// link flags after the dedicated link/parent/block/relate commands were
// retired. Without ref resolution, scripts that link by UID would break
// silently.
func TestEdit_LinkFlagsAcceptIssueRefs(t *testing.T) {
	env, dir := setupCLIEnv(t)
	pid := resolvePIDViaHTTP(t, env.URL, dir)
	createIssue(t, env, pid, "subject")
	createIssue(t, env, pid, "the-parent")

	// Look up the parent's UID so we can pass it as a ref.
	b := fetchIssueViaHTTP(t, env, pid, 2)
	require.NotEmpty(t, b.Issue.UID, "seeded parent must have a UID")

	// Use #N for the URL ref and the full UID for --parent.
	runCLI(t, env, dir, "edit", "#1", "--parent", b.Issue.UID)

	got := fetchIssueViaHTTP(t, env, pid, 1)
	var sawParent bool
	for _, l := range got.Links {
		if l.Type == "parent" && l.FromNumber == 1 && l.ToNumber == 2 {
			sawParent = true
		}
	}
	assert.True(t, sawParent, "parent link by UID-ref must persist")

	// UID prefix on --remove-parent must also resolve. ULIDs minted in the
	// same millisecond share their timestamp prefix and can share random-byte
	// runs too, so derive a prefix that's actually unambiguous across the
	// project's seeded UIDs rather than guessing at a fixed length.
	other := fetchIssueViaHTTP(t, env, pid, 1)
	prefix := unambiguousUIDPrefix(t, b.Issue.UID, []string{other.Issue.UID})
	runCLI(t, env, dir, "edit", "1", "--remove-parent", prefix)
	got = fetchIssueViaHTTP(t, env, pid, 1)
	for _, l := range got.Links {
		assert.NotEqual(t, "parent", l.Type, "parent link must be removable by UID prefix")
	}
}

// TestEdit_RemoveByUIDPrefixWorksAfterPeerSoftDeleted covers an
// iteration-7 roborev finding: --remove-* flags resolve UID/prefix refs
// CLI-side, and the live-only path used to fail with "issue not found"
// once the peer was soft-deleted, even though the daemon-side cleanup
// is soft-delete-tolerant. The CLI now passes include_deleted=true on
// the resolution call for remove-* flags so the link cleanup remains
// usable when the peer is hidden.
func TestEdit_RemoveByUIDPrefixWorksAfterPeerSoftDeleted(t *testing.T) {
	env, dir := setupCLIEnv(t)
	pid := resolvePIDViaHTTP(t, env.URL, dir)
	createIssue(t, env, pid, "subject")
	createIssue(t, env, pid, "peer")

	// Link #1 → blocks → #2.
	runCLI(t, env, dir, "edit", "1", "--blocks", "2")

	// Soft-delete #2. The CLI delete command requires --force + --confirm.
	runCLI(t, env, dir, "delete", "2", "--force", "--confirm", "DELETE #2")

	// Capture #2's UID prefix BEFORE removal — show with --include-deleted.
	deleted := fetchDeletedIssueViaHTTP(t, env, pid, 2)
	require.NotEmpty(t, deleted.Issue.UID, "deleted #2 must still have a UID")
	subject := fetchIssueViaHTTP(t, env, pid, 1)
	prefix := unambiguousUIDPrefix(t, deleted.Issue.UID, []string{subject.Issue.UID})

	// --remove-blocks <uid-prefix> must succeed against the soft-deleted peer.
	runCLI(t, env, dir, "edit", "1", "--remove-blocks", prefix)

	got := fetchIssueViaHTTP(t, env, pid, 1)
	for _, l := range got.Links {
		assert.NotEqualf(t, "blocks", l.Type, "link to soft-deleted peer must be removable: %+v", l)
	}
}

// TestEdit_HumanModePrintsLinkSummary covers an iteration-10 roborev
// finding: the human-mode (`kata edit ... ` without --json/--quiet)
// renderer used to print only `#N <title> [status]` after a link
// mutation, hiding the actual outcome. Now appends a "links: ..."
// segment listing every applied add/remove, and an explicit
// "(no changes applied)" tail when every requested op was a no-op.
func TestEdit_HumanModePrintsLinkSummary(t *testing.T) {
	env, dir := setupCLIEnv(t)
	pid := resolvePIDViaHTTP(t, env.URL, dir)
	createIssue(t, env, pid, "subject")
	createIssue(t, env, pid, "target")

	// Successful add: summary line includes the +blocks segment.
	out := runCLI(t, env, dir, "edit", "1", "--blocks", "2")
	assert.Contains(t, out, "+blocks #2",
		"human-mode edit must report the link mutation: %q", out)

	// Idempotent no-op: same flag again. Daemon returns changed=false
	// with no entries in changes; renderer should print the no-op tail.
	out = runCLI(t, env, dir, "edit", "1", "--blocks", "2")
	assert.Contains(t, out, "(no changes applied)",
		"human-mode no-op edit must say so explicitly: %q", out)
}

// TestEdit_DistinctParentRefsRejected covers a roborev finding from
// kata#1 iteration 4: --parent N --parent M (or --remove-parent) was
// silently last-winning under cobra's StringVar. We now accept these as
// StringSliceVar and explicitly reject distinct values so a typo can't
// mutate the wrong relationship.
func TestEdit_DistinctParentRefsRejected(t *testing.T) {
	env, dir := setupCLIEnv(t)
	pid := resolvePIDViaHTTP(t, env.URL, dir)
	createIssue(t, env, pid, "subject")
	createIssue(t, env, pid, "p1")
	createIssue(t, env, pid, "p2")

	_, err := runCLICapture(t, env, dir, "edit", "1", "--parent", "2", "--parent", "3")
	require.Error(t, err, "two distinct --parent values must error")
	assert.Contains(t, err.Error(), "--parent")

	_, err = runCLICapture(t, env, dir, "edit", "1", "--remove-parent", "2", "--remove-parent", "3")
	require.Error(t, err, "two distinct --remove-parent values must error")
	assert.Contains(t, err.Error(), "--remove-parent")

	// Repeats of the SAME value are allowed (idempotent for the user).
	runCLI(t, env, dir, "edit", "1", "--parent", "2", "--parent", "2")
}

// TestEdit_EquivalentParentRefsAccepted covers an iteration-13 roborev
// finding: at-most-one flags compared by raw string equality were
// rejecting refs that resolve to the same issue (`--parent 2 --parent #2`,
// or a full UID + its prefix). The collapse helper now reconciles
// equivalent refs through the daemon's resolution and rejects only
// when entries resolve to *different* issues.
func TestEdit_EquivalentParentRefsAccepted(t *testing.T) {
	env, dir := setupCLIEnv(t)
	pid := resolvePIDViaHTTP(t, env.URL, dir)
	createIssue(t, env, pid, "subject")
	createIssue(t, env, pid, "the-parent")

	// Mixed numeric forms must reconcile: `2` and `#2` resolve to #2.
	runCLI(t, env, dir, "edit", "1", "--parent", "2", "--parent", "#2")

	got := fetchIssueViaHTTP(t, env, pid, 1)
	var sawParent bool
	for _, l := range got.Links {
		if l.Type == "parent" && l.FromNumber == 1 && l.ToNumber == 2 {
			sawParent = true
		}
	}
	assert.True(t, sawParent, "equivalent --parent refs must reconcile to the same issue")

	// UID + numeric also reconciles.
	parent := fetchIssueViaHTTP(t, env, pid, 2)
	require.NotEmpty(t, parent.Issue.UID)
	runCLI(t, env, dir, "edit", "1", "--remove-parent", "2", "--remove-parent", parent.Issue.UID)

	got = fetchIssueViaHTTP(t, env, pid, 1)
	for _, l := range got.Links {
		assert.NotEqualf(t, "parent", l.Type, "equivalent --remove-parent refs must succeed: %+v", l)
	}
}

// TestEdit_PriorityOnPATCH sets priority via the unified PATCH instead of the
// legacy second HTTP call.
func TestEdit_PriorityOnPATCH(t *testing.T) {
	env, dir := setupCLIEnv(t)
	pid := resolvePIDViaHTTP(t, env.URL, dir)
	createIssue(t, env, pid, "subject")

	runCLI(t, env, dir, "edit", "1", "--priority", "2")

	b := fetchIssueViaHTTP(t, env, pid, 1)
	require.NotNil(t, b.Issue.Priority)
	assert.Equal(t, int64(2), *b.Issue.Priority)

	// Clear via the dash sentinel.
	runCLI(t, env, dir, "edit", "1", "--priority", "-")
	b = fetchIssueViaHTTP(t, env, pid, 1)
	assert.Nil(t, b.Issue.Priority)
}

// TestEdit_EmptyTitle_ValidatedClientSide covers hammer-test
// finding #4: edit --title "" (or whitespace-only) used to forward
// the value to the daemon, which returned a raw SQLite CHECK
// constraint error. Now blocked client-side with a kindValidation
// cliError that points the user at the right action (omit the
// flag to keep the existing title).
func TestEdit_EmptyTitle_ValidatedClientSide(t *testing.T) {
	for _, blank := range []string{"", "   ", "\t\n"} {
		_, err := runCmdOutput(t, nil, "edit", "1", "--title", blank)
		ce := requireCLIError(t, err, ExitValidation)
		assert.Equal(t, kindValidation, ce.Kind)
		assert.Contains(t, ce.Message, "must not be empty",
			"validation message should explain the failure for %q", blank)
	}
}
