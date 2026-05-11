package main

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// wontfixMessage is the canned ≥60-char wontfix message used by the
// audit tests. Centralizing it keeps the per-test lines under 100
// chars (which the close prose alone otherwise blows past).
const wontfixMessage = "Decided not to fix; out of scope for this milestone and product direction."

// TestAuditCloses_ListsAllClosesInWindow verifies the happy path: every
// close event in the default (open) window surfaces in the JSON output
// with its reason intact.
func TestAuditCloses_ListsAllClosesInWindow(t *testing.T) {
	env, dir, pid, refOne := setupWorkspaceWithIssue(t, "issue one")
	refTwo := createIssue(t, env, pid, "issue two")
	runCLI(t, env, dir, "close", refOne, "--done",
		"--message", "Fixed first issue and ran the auth tests.",
		"--commit", "abc1234")
	runCLI(t, env, dir, "close", refTwo, "--wontfix",
		"--message", wontfixMessage)

	out := runCLI(t, env, dir, "audit", "closes", "--json")
	assert.Contains(t, out, `"issue":"`+refOne+`"`)
	assert.Contains(t, out, `"issue":"`+refTwo+`"`)
	assert.Contains(t, out, `"reason":"done"`)
	assert.Contains(t, out, `"reason":"wontfix"`)
}

// TestAuditCloses_TextOutputRendersRows pins the non-JSON path: a row
// per close must appear under the header. Regression test for an
// unmarshal mismatch where the CLI decoded into the full
// AuditClosesResponse shape (with nested Body.Rows) while huma emits
// the body content directly, leaving Rows empty and the table blank.
func TestAuditCloses_TextOutputRendersRows(t *testing.T) {
	env, dir, _, ref := setupWorkspaceWithIssue(t, "issue one")
	runCLI(t, env, dir, "close", ref, "--done",
		"--message", "Fixed the issue and ran the auth tests thoroughly.",
		"--commit", "abc1234")

	out := runCLI(t, env, dir, "audit", "closes")
	assert.Contains(t, out, "TIME")
	assert.Contains(t, out, ref,
		"text output must include the closed issue's short_id")
	assert.Contains(t, out, "done",
		"text output must include the close reason")
}

// TestAuditCloses_FilterByActor verifies --actor narrows results to a
// single actor's closes.
func TestAuditCloses_FilterByActor(t *testing.T) {
	env, dir, pid, refOne := setupWorkspaceWithIssue(t, "issue one")
	refTwo := createIssue(t, env, pid, "issue two")
	runCLIAs(t, env, dir, "alice", "close", refOne, "--done",
		"--message", "Fixed the issue alice owned and ran the auth tests.",
		"--commit", "abc1234")
	runCLIAs(t, env, dir, "bob", "close", refTwo, "--done",
		"--message", "Fixed the issue bob owned and ran the cache tests.",
		"--commit", "def5678")

	out := runCLI(t, env, dir, "audit", "closes", "--actor", "alice", "--json")
	assert.Contains(t, out, `"actor":"alice"`)
	assert.NotContains(t, out, `"actor":"bob"`)
}

// TestAuditCloses_FilterByReason verifies --reason narrows to a single
// close reason (here, the wontfix close is filtered out).
func TestAuditCloses_FilterByReason(t *testing.T) {
	env, dir, pid, refOne := setupWorkspaceWithIssue(t, "issue one")
	refTwo := createIssue(t, env, pid, "issue two")
	runCLI(t, env, dir, "close", refOne, "--done",
		"--message", "Fixed first issue and ran the auth tests.",
		"--commit", "abc1234")
	runCLI(t, env, dir, "close", refTwo, "--wontfix",
		"--message", wontfixMessage)

	out := runCLI(t, env, dir, "audit", "closes", "--reason", "done", "--json")
	assert.Contains(t, out, `"issue":"`+refOne+`"`)
	assert.NotContains(t, out, `"issue":"`+refTwo+`"`)
	assert.Contains(t, out, `"reason":"done"`)
	assert.NotContains(t, out, `"reason":"wontfix"`)
}

// TestAuditCloses_WontfixHasNoNoEvidenceFlag verifies that a wontfix
// close (which is admissible without evidence) does NOT get the
// "no-evidence" flag — that flag is reserved for closes that should
// have carried evidence and didn't.
func TestAuditCloses_WontfixHasNoNoEvidenceFlag(t *testing.T) {
	env, dir, _, ref := setupWorkspaceWithIssue(t, "issue one")
	runCLI(t, env, dir, "close", ref, "--wontfix",
		"--message", wontfixMessage)

	out := runCLI(t, env, dir, "audit", "closes", "--json")
	assert.Contains(t, out, `"reason":"wontfix"`)
	assert.NotContains(t, out, `"no-evidence"`)
}

// TestAuditCloses_FilterByNoEvidenceEmptyByDefault verifies that when
// every close in the window carries evidence, --no-evidence returns no
// rows (the projection's Rows field is an empty list rather than
// missing or nil).
func TestAuditCloses_FilterByNoEvidenceEmptyByDefault(t *testing.T) {
	env, dir, _, ref := setupWorkspaceWithIssue(t, "issue one")
	runCLI(t, env, dir, "close", ref, "--done",
		"--message", "Fixed first issue and ran the auth tests.",
		"--commit", "abc1234")

	out := runCLI(t, env, dir, "audit", "closes", "--no-evidence", "--json")
	assert.NotContains(t, out, `"issue":"`+ref+`"`)
	assert.Contains(t, out, `"rows":[]`)
}

// TestAuditCloses_EvidenceTypesSurfaceInRow verifies that the
// evidence_types projection lists the evidence kinds attached to a
// close (here, "commit" from --commit abc1234).
func TestAuditCloses_EvidenceTypesSurfaceInRow(t *testing.T) {
	env, dir, _, ref := setupWorkspaceWithIssue(t, "issue one")
	runCLI(t, env, dir, "close", ref, "--done",
		"--message", "Fixed first issue and ran the auth tests.",
		"--commit", "abc1234")

	out := runCLI(t, env, dir, "audit", "closes", "--json")
	assert.Contains(t, out, `"evidence_types":["commit"]`)
}

// TestAuditCloses_FilterByParent verifies --parent narrows to closes
// of children of the given parent issue.
func TestAuditCloses_FilterByParent(t *testing.T) {
	env, dir, pid, parent := setupWorkspaceWithIssue(t, "parent issue")
	childOfParent := createIssue(t, env, pid, "child of parent")
	runCLI(t, env, dir, "edit", childOfParent, "--parent", parent)
	unrelated := createIssue(t, env, pid, "unrelated issue")
	runCLI(t, env, dir, "close", childOfParent, "--done",
		"--message", "Fixed the child of the parent issue and ran the unit tests.",
		"--commit", "abc1234")
	runCLI(t, env, dir, "close", unrelated, "--done",
		"--message", "Fixed the unrelated issue and ran the integration tests.",
		"--commit", "def5678")

	out := runCLI(t, env, dir, "audit", "closes", "--parent", parent, "--json")
	assert.Contains(t, out, `"issue":"`+childOfParent+`"`)
	assert.NotContains(t, out, `"issue":"`+unrelated+`"`)
}

// TestAuditCloses_ThrottledFlagIgnoresLaterThrottle pins the temporal
// rule: a close.throttled event whose id is GREATER than the successful
// close it could naively be matched against must not flag that close.
// The throttle there is a later retry against the same key, not a guard
// the original close tripped — flagging it would mislead a reviewer.
func TestAuditCloses_ThrottledFlagIgnoresLaterThrottle(t *testing.T) {
	env, dir, pid, parent := setupWorkspaceWithIssue(t, "parent issue")
	siblingA := createIssue(t, env, pid, "sibling a")
	siblingB := createIssue(t, env, pid, "sibling b")
	runCLI(t, env, dir, "edit", siblingA, "--parent", parent)
	runCLI(t, env, dir, "edit", siblingB, "--parent", parent)
	// Establish a successful close on siblingB with message M so a later
	// attempt to close siblingA with the same message trips the
	// repeated-message guard. Event 1 ≈ close (siblingB, agent-a).
	msg := "Verified consistent state and confirmed no regressions in tests."
	runCLIAs(t, env, dir, "agent-a", "close", siblingB, "--done",
		"--message", msg,
		"--commit", "abc1234")
	// Close siblingA successfully with a distinct message. Event 2 ≈
	// close (siblingA, agent-a). This is the row that must NOT be
	// flagged throttled — the throttle later in the timeline is
	// unrelated to this close.
	runCLIAs(t, env, dir, "agent-a", "close", siblingA, "--done",
		"--message", "Different prose for sibling A; independent verification.",
		"--commit", "def5678")
	// Reopen siblingA so we can attempt to close it again. Event 3.
	runCLIAs(t, env, dir, "agent-a", "reopen", siblingA)
	// Attempt to close siblingA with the duplicate message M. The
	// repeated-message guard fires because siblingB's prior close used
	// the same msg under the same parent. Event 4 ≈ close.throttled
	// (siblingA, agent-a).
	_, _, _ = runCLIWithErr(t, env, dir, "close", siblingA, "--as", "agent-a",
		"--done",
		"--message", msg,
		"--commit", "abc1234")

	out := runCLI(t, env, dir, "audit", "closes", "--actor", "agent-a", "--json")
	// The successful close of siblingA (event 2) must NOT carry a
	// throttled flag — the only close.throttled for (siblingA, agent-a)
	// is event 4, which post-dates the close.
	type row struct {
		Issue string   `json:"issue"`
		Flags []string `json:"flags"`
	}
	var got struct {
		Rows []row `json:"rows"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &got))
	var seenA bool
	for _, r := range got.Rows {
		if r.Issue == siblingA {
			seenA = true
			for _, f := range r.Flags {
				assert.NotEqual(t, "throttled", f,
					"close of %s must not be flagged throttled by a later throttle", siblingA)
			}
		}
	}
	assert.True(t, seenA, "audit should include the close of %s", siblingA)
}

// TestAuditCloses_ThrottledFlagSurfaces verifies that a close that
// previously hit the repeated-message guard (and thus emitted a
// close.throttled event) is flagged "throttled" in the audit row once
// the same actor retries successfully with a different message.
func TestAuditCloses_ThrottledFlagSurfaces(t *testing.T) {
	env, dir, pid, parent := setupWorkspaceWithIssue(t, "parent issue")
	childA := createIssue(t, env, pid, "child a")
	childB := createIssue(t, env, pid, "child b")
	runCLI(t, env, dir, "edit", childA, "--parent", parent)
	runCLI(t, env, dir, "edit", childB, "--parent", parent)
	msg := "Schema review complete; table remains metadata-only and unchanged."
	runCLIAs(t, env, dir, "agent-a", "close", childA, "--audit-no-change",
		"--message", msg,
		"--evidence", "no-change-audit:metadata-only")
	// Second close with identical message trips the repeated-message
	// guard and emits a close.throttled event tagged with agent-a/childB.
	_, _, _ = runCLIWithErr(t, env, dir, "close", childB, "--as", "agent-a",
		"--audit-no-change",
		"--message", msg,
		"--evidence", "no-change-audit:metadata-only")
	// Retry with a distinct message — the close succeeds; the prior
	// close.throttled event for agent-a/childB stays in the audit window.
	runCLIAs(t, env, dir, "agent-a", "close", childB, "--audit-no-change",
		"--message", "Reviewed sibling B; same conclusion under a fresh narrative.",
		"--evidence", "no-change-audit:metadata-only")

	out := runCLI(t, env, dir, "audit", "closes", "--actor", "agent-a", "--json")
	assert.Contains(t, out, `"throttled"`)
}
