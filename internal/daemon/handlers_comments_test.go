package daemon_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCommentEndpoint_AppendsAndEmitsEvent(t *testing.T) {
	_, ts, pid, num := bootstrapProjectWithIssue(t)

	resp, bs := postJSON(t, ts, issueURL(pid, num, "comments"),
		map[string]any{"actor": "agent", "body": "first comment"})
	require.Equal(t, 200, resp.StatusCode, string(bs))
	assert.Contains(t, string(bs), `"body":"first comment"`)
	assert.Contains(t, string(bs), `"type":"issue.commented"`)
}

func TestActionsClose_ReopenRoundtrip(t *testing.T) {
	_, ts, pid, num := bootstrapProjectWithIssue(t)

	resp, bs := postJSON(t, ts, issueURL(pid, num, "actions/close"),
		map[string]any{"actor": "agent", "reason": "wontfix"})
	require.Equal(t, 200, resp.StatusCode, string(bs))
	assert.Contains(t, string(bs), `"status":"closed"`)
	assert.Contains(t, string(bs), `"closed_reason":"wontfix"`)

	resp2, bs2 := postJSON(t, ts, issueURL(pid, num, "actions/reopen"),
		map[string]any{"actor": "agent"})
	require.Equal(t, 200, resp2.StatusCode, string(bs2))
	assert.Contains(t, string(bs2), `"status":"open"`)
}

func TestActionsClose_RejectsUnsupportedReason(t *testing.T) {
	_, ts, pid, num := bootstrapProjectWithIssue(t)

	resp, bs := postJSON(t, ts, issueURL(pid, num, "actions/close"),
		map[string]any{"actor": "agent", "reason": "obsolete"})
	assertAPIError(t, resp.StatusCode, bs, 400, "validation")
}

func TestActionsClose_AlreadyClosedIsNoOpEnvelope(t *testing.T) {
	_, ts, pid, num := bootstrapProjectWithIssue(t)
	_, _ = postJSON(t, ts, issueURL(pid, num, "actions/close"),
		map[string]any{"actor": "agent"})

	resp, bs := postJSON(t, ts, issueURL(pid, num, "actions/close"),
		map[string]any{"actor": "agent"})
	require.Equal(t, 200, resp.StatusCode, string(bs))
	assert.Contains(t, string(bs), `"changed":false`)
	assert.Contains(t, string(bs), `"event":null`)
}

func TestCreateComment_BlankActorIs400(t *testing.T) {
	_, ts, pid, num := bootstrapProjectWithIssue(t)

	resp, bs := postJSON(t, ts, issueURL(pid, num, "comments"),
		map[string]any{"actor": "   ", "body": "hi"})
	assertAPIError(t, resp.StatusCode, bs, 400, "validation")
}

func TestCloseIssue_BlankActorIs400(t *testing.T) {
	_, ts, pid, num := bootstrapProjectWithIssue(t)

	resp, bs := postJSON(t, ts, issueURL(pid, num, "actions/close"),
		map[string]any{"actor": "   "})
	assertAPIError(t, resp.StatusCode, bs, 400, "validation")
}

func TestReopenIssue_BlankActorIs400(t *testing.T) {
	_, ts, pid, num := bootstrapProjectWithIssue(t)

	resp, bs := postJSON(t, ts, issueURL(pid, num, "actions/reopen"),
		map[string]any{"actor": "   "})
	assertAPIError(t, resp.StatusCode, bs, 400, "validation")
}
