package daemon_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wesm/kata/internal/testenv"
)

func TestCreateLink_HappyPath(t *testing.T) {
	env := testenv.New(t)
	pid, a, b := setupTwoIssues(t, env)
	from, err := env.DB.IssueByID(t.Context(), a)
	require.NoError(t, err)
	to, err := env.DB.IssueByID(t.Context(), b)
	require.NoError(t, err)

	out := postLink(t, env, pid, a, "blocks", b)
	assert.Equal(t, "blocks", out.Link.Type)
	assert.Equal(t, from.ShortID, out.Link.From.ShortID)
	assert.Equal(t, to.ShortID, out.Link.To.ShortID)
	project, err := env.DB.ProjectByID(t.Context(), pid)
	require.NoError(t, err)
	assert.Equal(t, from.UID, out.Link.From.UID)
	assert.Equal(t, to.UID, out.Link.To.UID)
	require.NotNil(t, out.Event)
	assert.Equal(t, "issue.linked", out.Event.Type)
	assert.Equal(t, project.UID, out.Event.ProjectUID)
	require.NotNil(t, out.Event.IssueUID)
	require.NotNil(t, out.Event.RelatedIssueUID)
	assert.Equal(t, from.UID, *out.Event.IssueUID)
	assert.Equal(t, to.UID, *out.Event.RelatedIssueUID)
	assert.True(t, out.Changed)
}

func TestCreateLink_DuplicateIsNoop(t *testing.T) {
	env := testenv.New(t)
	pid, a, b := setupTwoIssues(t, env)
	postLink(t, env, pid, a, "blocks", b)

	out := postLink(t, env, pid, a, "blocks", b)
	assert.Nil(t, out.Event, "duplicate link is no-op (event:null)")
	assert.False(t, out.Changed)
}

func TestCreateLink_RelatedCanonicalizesOrder(t *testing.T) {
	env := testenv.New(t)
	pid, a, b := setupTwoIssues(t, env) // a < b
	aIss, err := env.DB.IssueByID(t.Context(), a)
	require.NoError(t, err)
	bIss, err := env.DB.IssueByID(t.Context(), b)
	require.NoError(t, err)
	out := postLink(t, env, pid, b, "related", a) // user passes b → a
	assert.Equal(t, "related", out.Link.Type)
	assert.Equal(t, aIss.ShortID, out.Link.From.ShortID, "canonical: from is lower-numbered side")
	assert.Equal(t, bIss.ShortID, out.Link.To.ShortID)
}

// TestCreateLink_RelatedEventAttributionIsURLIssue verifies that when a user
// POSTs a `related` link from the higher-numbered side and the handler
// canonicalizes storage to (from < to), the resulting event still attributes
// to the URL's issue (not the canonical-from). The link row records the
// canonical relationship; the event records the user's action.
func TestCreateLink_RelatedEventAttributionIsURLIssue(t *testing.T) {
	env := testenv.New(t)
	pid, a, b := setupTwoIssues(t, env) // a < b
	aIss, err := env.DB.IssueByID(t.Context(), a)
	require.NoError(t, err)
	bIss, err := env.DB.IssueByID(t.Context(), b)
	require.NoError(t, err)
	// POST from b (higher-numbered) targeting a. Storage canonicalizes
	// to (a→b), but the event must still be attributed to issue b.
	out := postLink(t, env, pid, b, "related", a)
	require.NotNil(t, out.Event)
	assert.Equal(t, "issue.linked", out.Event.Type)

	// The response carries the canonical link (from=a, to=b).
	assert.Equal(t, aIss.ShortID, out.Link.From.ShortID)
	assert.Equal(t, bIss.ShortID, out.Link.To.ShortID)

	// Query the events table directly: events.issue_uid must be b's UID (URL),
	// and the payload's from_short_id / to_short_id must record what the
	// user did (from b → to a), not the canonical link's columns.
	row := env.DB.QueryRowContext(t.Context(),
		`SELECT issue_uid, payload FROM events
		 WHERE project_id = ? AND type = 'issue.linked'
		 ORDER BY id DESC LIMIT 1`, pid)
	var issueUID, payload string
	require.NoError(t, row.Scan(&issueUID, &payload))
	assert.Equal(t, bIss.UID, issueUID, "event must attribute to URL issue (b), not canonical-from (a)")

	var pl struct {
		FromShortID string `json:"from_short_id"`
		ToShortID   string `json:"to_short_id"`
	}
	require.NoError(t, json.Unmarshal([]byte(payload), &pl))
	assert.Equal(t, bIss.ShortID, pl.FromShortID, "payload from_short_id is the URL issue's short_id")
	assert.Equal(t, aIss.ShortID, pl.ToShortID, "payload to_short_id is the OTHER endpoint")
}

func TestCreateLink_ParentAlreadySetIs409(t *testing.T) {
	env := testenv.New(t)
	pid, child, p1 := setupTwoIssues(t, env)
	p2 := createIssueViaHTTP(t, env, pid, "p2")
	postLink(t, env, pid, child, "parent", p1)

	p2Ref := refForIssue(t, env, p2)
	resp, _ := postLinkRaw(t, env, pid, child, map[string]any{
		"actor":  "tester",
		"type":   "parent",
		"to_ref": p2Ref,
	})
	assert.Equal(t, 409, resp.StatusCode)
}

func TestCreateLink_ParentReplaceSwapsParent(t *testing.T) {
	env := testenv.New(t)
	pid, child, p1 := setupTwoIssues(t, env)
	p2 := createIssueViaHTTP(t, env, pid, "p2")
	postLink(t, env, pid, child, "parent", p1)

	p2Iss, err := env.DB.IssueByID(t.Context(), p2)
	require.NoError(t, err)
	resp, out := postLinkRaw(t, env, pid, child, map[string]any{
		"actor":   "tester",
		"type":    "parent",
		"to_ref":  p2Iss.ShortID,
		"replace": true,
	})
	require.Equal(t, 200, resp.StatusCode)
	assert.Equal(t, p2Iss.ShortID, out.Link.To.ShortID)
}

func TestCreateLink_ParentReplaceUnlinkEventPointsToOldParent(t *testing.T) {
	env := testenv.New(t)
	pid, child, p1 := setupTwoIssues(t, env)
	p2 := createIssueViaHTTP(t, env, pid, "p2")
	postLink(t, env, pid, child, "parent", p1)

	p2Ref := refForIssue(t, env, p2)
	resp, _ := postLinkRaw(t, env, pid, child, map[string]any{
		"actor":   "tester",
		"type":    "parent",
		"to_ref":  p2Ref,
		"replace": true,
	})
	require.Equal(t, 200, resp.StatusCode)

	// The unlink event isn't in the response (response carries only the
	// linked event). Query the events table directly to verify the unlink
	// event references the OLD parent (p1), not the new (p2).
	p1Iss, err := env.DB.IssueByID(t.Context(), p1)
	require.NoError(t, err)
	var pl struct {
		ToShortID string `json:"to_short_id"`
	}
	require.NoError(t, json.Unmarshal([]byte(lastEventPayload(t, env, pid, "issue.unlinked")), &pl))
	assert.Equal(t, p1Iss.ShortID, pl.ToShortID, "unlink event must reference the old parent's short_id")
}

// TestCreateLink_ParentReplaceSelfLinkLeavesNoMutation verifies that a
// self-link rejected on the parent --replace path returns 400 BEFORE deleting
// the existing parent. With the bug, DeleteLinkAndEvent would have committed
// the unlink (row + event) before CreateLinkAndEvent surfaced ErrSelfLink. We
// assert directly against the events and links tables: no issue.unlinked
// event exists, and the original parent link is still attached.
func TestCreateLink_ParentReplaceSelfLinkLeavesNoMutation(t *testing.T) {
	env := testenv.New(t)
	pid, child, p1 := setupTwoIssues(t, env)
	postLink(t, env, pid, child, "parent", p1)

	childRef := refForIssue(t, env, child)
	resp, _ := postLinkRaw(t, env, pid, child, map[string]any{
		"actor":   "tester",
		"type":    "parent",
		"to_ref":  childRef,
		"replace": true,
	})
	require.Equal(t, 400, resp.StatusCode, "self-link must be rejected before mutation")

	// No issue.unlinked event was inserted. The bug's signature was a
	// committed unlink event followed by a 400; the fix's signature is
	// zero unlink events.
	var unlinkedCount int
	require.NoError(t, env.DB.QueryRowContext(t.Context(),
		`SELECT COUNT(*) FROM events WHERE project_id = ? AND type = 'issue.unlinked'`,
		pid).Scan(&unlinkedCount))
	assert.Equal(t, 0, unlinkedCount, "no issue.unlinked event should exist after rejected self-link")

	// And the original parent link row itself is still attached.
	var parentLinks int
	require.NoError(t, env.DB.QueryRowContext(t.Context(),
		`SELECT COUNT(*) FROM links WHERE project_id = ? AND type = 'parent'`,
		pid).Scan(&parentLinks))
	assert.Equal(t, 1, parentLinks, "original parent link must still exist")
}

func TestCreateLink_BlankActorIs400(t *testing.T) {
	env := testenv.New(t)
	pid, a, b := setupTwoIssues(t, env)
	bRef := refForIssue(t, env, b)
	resp, _ := postLinkRaw(t, env, pid, a, map[string]any{
		"actor":  "   ",
		"type":   "blocks",
		"to_ref": bRef,
	})
	assert.Equal(t, 400, resp.StatusCode)
}

func TestDeleteLink_BlankActorIs400(t *testing.T) {
	env := testenv.New(t)
	pid, a, b := setupTwoIssues(t, env)
	created := postLink(t, env, pid, a, "blocks", b)
	resp, _ := deleteLinkAs(t, env, pid, a, "  ", created.Link.ID)
	assert.Equal(t, 400, resp.StatusCode)
}

func TestCreateLink_SelfLinkIs400(t *testing.T) {
	env := testenv.New(t)
	pid, a, _ := setupTwoIssues(t, env)
	aRef := refForIssue(t, env, a)
	resp, _ := postLinkRaw(t, env, pid, a, map[string]any{
		"actor":  "tester",
		"type":   "blocks",
		"to_ref": aRef,
	})
	assert.Equal(t, 400, resp.StatusCode)
}

func TestDeleteLink_RemovesAndEmitsUnlink(t *testing.T) {
	env := testenv.New(t)
	pid, a, b := setupTwoIssues(t, env)
	created := postLink(t, env, pid, a, "blocks", b)

	resp, out := deleteLink(t, env, pid, a, created.Link.ID)
	require.Equal(t, 200, resp.StatusCode)
	require.NotNil(t, out.Event)
	assert.Equal(t, "issue.unlinked", out.Event.Type)
	assert.True(t, out.Changed)
}

// TestDeleteLink_NotAttachedToURLIssueIs404 verifies that a DELETE on
// /issues/{c}/links/{link_id} where the link is between (a, b) — neither of
// which is c — returns 404 instead of mutating the wrong issue's link and
// emitting a misattributed unlink event.
func TestDeleteLink_NotAttachedToURLIssueIs404(t *testing.T) {
	env := testenv.New(t)
	pid, a, b := setupTwoIssues(t, env)
	c := createIssueViaHTTP(t, env, pid, "c")
	created := postLink(t, env, pid, a, "blocks", b)

	resp, _ := deleteLink(t, env, pid, c, created.Link.ID)
	assert.Equal(t, 404, resp.StatusCode)
}

func TestDeleteLink_AbsentIs200NoOp(t *testing.T) {
	env := testenv.New(t)
	pid, a, _ := setupTwoIssues(t, env)
	resp, out := deleteLink(t, env, pid, a, 9999)
	require.Equal(t, 200, resp.StatusCode)
	assert.Nil(t, out.Event)
	assert.False(t, out.Changed)
}
