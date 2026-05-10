package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestShow_RendersLabelsAndLinksSections(t *testing.T) {
	env, dir, pid := setupCLIWorkspace(t)
	parent := createIssue(t, env, pid, "parent")
	child := createIssue(t, env, pid, "child")
	// Two labels so we exercise the comma-join.
	for _, label := range []string{"bug", "priority:high"} {
		runCLI(t, env, dir, "label", "add", child, label)
	}
	createLinkViaHTTP(t, env, pid, child, "parent", parent)

	out := runCLI(t, env, dir, "show", child)
	// Exact section headers and comma-joined label rendering.
	assert.Contains(t, out, "--- labels ---")
	assert.Contains(t, out, "bug, priority:high")
	// Links section: viewer (child) is on the "from" side of (from=child parent to=parent)
	// so it reads "parent: <parent_short_id>" — its parent is the parent issue.
	assert.Contains(t, out, "--- links ---")
	assert.Contains(t, out, "parent: "+parent)
}

// TestShow_LinkLabelInvertsOnToSide verifies that when show runs against
// the link's "to" side, the rendered LABEL inverts to read from the
// viewer's perspective: the parent slot's "to" end is the parent of
// the "from" end, so from the parent's POV (parent of child), the link
// reads "child: <child_short_id>".
func TestShow_LinkLabelInvertsOnToSide(t *testing.T) {
	env, dir, pid := setupCLIWorkspace(t)
	parent := createIssue(t, env, pid, "parent")
	child := createIssue(t, env, pid, "child")
	// child → parent stores (from=child, to=parent). Showing parent puts
	// us on the to side.
	createLinkViaHTTP(t, env, pid, child, "parent", parent)

	out := runCLI(t, env, dir, "show", parent)
	assert.Contains(t, out, "child: "+child,
		"showing the parent issue must label the link as `child` from its POV")
}

func TestShow_AcceptsBareUIDAndQualified(t *testing.T) {
	env, dir, pid := setupCLIWorkspace(t)
	created := createIssueViaHTTPFull(t, env, dir, "uid target")
	_ = pid // pid not needed; we resolve via created.ShortID

	for _, ref := range []string{created.ShortID, "kata#" + created.ShortID, created.UID} {
		out := runCLI(t, env, dir, "show", ref)
		assert.Contains(t, out, "uid target", "ref %s", ref)
	}
}

// TestShow_LegacyNumberFails pins that bare numeric refs no longer resolve.
// The ResolveRef helper rejects them up-front with a guidance message.
func TestShow_LegacyNumberFails(t *testing.T) {
	env, dir, _ := setupCLIWorkspace(t)
	_, err := runCLICapture(t, env, dir, "show", "1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "legacy issue number")
}
