package daemon_test

import (
	"net/url"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wesm/kata/internal/testenv"
)

// digestActor is the per-actor slice in the digest response: totals plus a
// per-issue action sequence.
type digestActor struct {
	Actor  string `json:"actor"`
	Totals struct {
		Created   int `json:"created"`
		Closed    int `json:"closed"`
		Commented int `json:"commented"`
		Labeled   int `json:"labeled"`
		Assigned  int `json:"assigned"`
		Linked    int `json:"linked"`
		Unblocked int `json:"unblocked"`
	} `json:"totals"`
	Issues []struct {
		IssueNumber int64    `json:"issue_number"`
		Actions     []string `json:"actions"`
	} `json:"issues"`
}

// digestBody is the decoded shape used by the digest tests. Fields are a
// superset of what each individual test asserts; missing fields decode as
// zero values.
type digestBody struct {
	EventCount int   `json:"event_count"`
	ProjectID  int64 `json:"project_id"`
	Totals     struct {
		Created   int `json:"created"`
		Closed    int `json:"closed"`
		Commented int `json:"commented"`
		Labeled   int `json:"labeled"`
		Assigned  int `json:"assigned"`
		Linked    int `json:"linked"`
		Unlinked  int `json:"unlinked"`
		Unblocked int `json:"unblocked"`
	} `json:"totals"`
	Actors []digestActor `json:"actors"`
}

// actionsFor returns the action sequence the digest recorded for actor on
// issueNumber, or nil when no entry exists for that pair.
func (d *digestBody) actionsFor(actor string, issueNumber int64) []string {
	for _, a := range d.Actors {
		if a.Actor != actor {
			continue
		}
		for _, iss := range a.Issues {
			if iss.IssueNumber == issueNumber {
				return iss.Actions
			}
		}
	}
	return nil
}

// projectDigestPath builds the per-project digest URL with optional
// since/until/actor filters. Empty values are omitted.
func projectDigestPath(projectID int64, since, until, actor string) string {
	q := url.Values{}
	if since != "" {
		q.Set("since", since)
	}
	if until != "" {
		q.Set("until", until)
	}
	if actor != "" {
		q.Set("actor", actor)
	}
	path := projectPath(projectID) + "/digest"
	if encoded := q.Encode(); encoded != "" {
		path += "?" + encoded
	}
	return path
}

// fetchDigest GETs the per-project digest with the supplied since/until window
// and returns the decoded body.
func fetchDigest(t *testing.T, env *testenv.Env, projectID int64, since, until string) digestBody {
	t.Helper()
	var out digestBody
	envGetJSON(t, env, projectDigestPath(projectID, since, until, ""), &out)
	return out
}

// TestDigest_AggregatesByActor exercises the end-to-end digest path: two
// actors create, comment, label, close, and explicitly unblock issues; the
// digest then surfaces per-actor totals and per-issue action sequences.
func TestDigest_AggregatesByActor(t *testing.T) {
	env := testenv.New(t)
	pid := initWorkspaceViaHTTP(t, env, "https://github.com/wesm/kata.git")

	// Alice creates two issues, comments on the first twice, closes it.
	a := createIssueAs(t, env, pid, "alice", "first")
	_ = createIssueAs(t, env, pid, "alice", "second")
	postCommentAs(t, env, pid, a, "alice", "looking now")
	postCommentAs(t, env, pid, a, "alice", "found a repro")
	closeIssueAs(t, env, pid, a, "alice", "done")

	// Bob creates an issue, labels it, then bob blocks-his-own-issue with
	// alice's first issue, then unblocks (the unlink credits bob with
	// "unblocks").
	b := createIssueAs(t, env, pid, "bob", "third")
	postLabelAs(t, env, pid, b, "bob", "bug")
	postLinkAs(t, env, pid, a, "bob", "blocks", b) // a blocks b
	deleteLinkBlocksAs(t, env, pid, a, "bob", b)   // bob unblocks b

	body := fetchDigest(t, env, pid, rfc3339Offset(-time.Hour), rfc3339Offset(time.Hour))

	assert.Equal(t, pid, body.ProjectID)
	assert.GreaterOrEqual(t, body.EventCount, 9)
	assert.Equal(t, 3, body.Totals.Created)
	assert.Equal(t, 1, body.Totals.Closed)
	assert.Equal(t, 2, body.Totals.Commented)
	assert.Equal(t, 1, body.Totals.Labeled)
	assert.Equal(t, 1, body.Totals.Linked)
	assert.Equal(t, 1, body.Totals.Unlinked)
	assert.Equal(t, 1, body.Totals.Unblocked, "unlink of type=blocks should bump unblocked")

	// Actors are sorted alphabetically.
	require.Len(t, body.Actors, 2)
	assert.Equal(t, "alice", body.Actors[0].Actor)
	assert.Equal(t, "bob", body.Actors[1].Actor)
	assert.Equal(t, 2, body.Actors[0].Totals.Created)
	assert.Equal(t, 1, body.Actors[0].Totals.Closed)
	assert.Equal(t, 2, body.Actors[0].Totals.Commented)
	assert.Equal(t, 1, body.Actors[1].Totals.Unblocked)

	// Alice's first issue should show created → commented:2 → closed:done in
	// that canonical order.
	aliceFirst := body.actionsFor("alice", a)
	require.NotNil(t, aliceFirst)
	assert.Equal(t, []string{"created", "commented:2", "closed:done"}, aliceFirst)

	// Bob's actions on issue b should include the labeled token and the
	// unblocks-credit referencing issue b.
	bobOnB := body.actionsFor("bob", b)
	require.NotNil(t, bobOnB)
	assert.Contains(t, bobOnB, "labeled:bug")
}

// TestDigest_ActorFilter only returns events for the named actors.
func TestDigest_ActorFilter(t *testing.T) {
	env := testenv.New(t)
	pid := initWorkspaceViaHTTP(t, env, "https://github.com/wesm/kata.git")
	_ = createIssueAs(t, env, pid, "alice", "x")
	_ = createIssueAs(t, env, pid, "bob", "y")

	var body digestBody
	envGetJSON(t, env, projectDigestPath(pid, rfc3339Offset(-time.Hour), "", "alice"), &body)
	require.Len(t, body.Actors, 1)
	assert.Equal(t, "alice", body.Actors[0].Actor)
}

// TestDigest_CountsCreateTimeLabelsOwnerLinks ensures that initial labels,
// owner, and links supplied at issue creation are folded into digest totals
// and per-issue actions. CreateIssue does not emit separate
// issue.labeled/assigned/linked events for create-time state — without payload
// mining the digest would silently undercount common `kata create --label
// bug --owner alice --blocks 7` activity.
func TestDigest_CountsCreateTimeLabelsOwnerLinks(t *testing.T) {
	env := testenv.New(t)
	pid := initWorkspaceViaHTTP(t, env, "https://github.com/wesm/kata.git")

	// Seed a target issue alice's later issue can block.
	target := createIssueAs(t, env, pid, "alice", "target")

	// Alice creates a richer issue with a label, an owner, and a blocks link.
	var created struct {
		Issue struct {
			Number int64 `json:"number"`
		} `json:"issue"`
	}
	envPostJSON(t, env, projectPath(pid)+"/issues", map[string]any{
		"actor":  "alice",
		"title":  "rich",
		"labels": []string{"bug"},
		"owner":  "bob",
		"links": []map[string]any{
			{"type": "blocks", "to_number": target},
		},
	}, &created)
	rich := created.Issue.Number

	digest := fetchDigest(t, env, pid, rfc3339Offset(-time.Hour), rfc3339Offset(time.Hour))

	// Two issues created total. The richer one bumps labeled / assigned /
	// linked even though no separate events were emitted for them.
	assert.Equal(t, 2, digest.Totals.Created)
	assert.Equal(t, 1, digest.Totals.Labeled, "create-time label must fold into digest totals")
	assert.Equal(t, 1, digest.Totals.Assigned, "create-time owner must fold into digest totals")
	assert.Equal(t, 1, digest.Totals.Linked, "create-time link must fold into digest totals")

	require.Len(t, digest.Actors, 1)
	assert.Equal(t, "alice", digest.Actors[0].Actor)
	assert.Equal(t, 1, digest.Actors[0].Totals.Labeled)
	assert.Equal(t, 1, digest.Actors[0].Totals.Assigned)
	assert.Equal(t, 1, digest.Actors[0].Totals.Linked)

	actions := digest.actionsFor("alice", rich)
	require.NotNil(t, actions, "rich issue not present in alice's per-issue digest")
	assert.Contains(t, actions, "created")
	assert.Contains(t, actions, "labeled:bug")
	assert.Contains(t, actions, "assigned:bob")
	assert.Contains(t, actions, "blocks:#"+strconv.FormatInt(target, 10))
}

// TestDigest_RejectsBadSince validates the since parameter.
func TestDigest_RejectsBadSince(t *testing.T) {
	env := testenv.New(t)
	pid := initWorkspaceViaHTTP(t, env, "https://github.com/wesm/kata.git")
	resp := envDoJSON(t, env, "GET", projectPath(pid)+"/digest?since=not-a-time", nil, nil)
	assert.Equal(t, 400, resp.StatusCode)
}

// TestDigest_GlobalEndpoint covers the cross-project route.
func TestDigest_GlobalEndpoint(t *testing.T) {
	env := testenv.New(t)
	pid := initWorkspaceViaHTTP(t, env, "https://github.com/wesm/kata.git")
	_ = createIssueAs(t, env, pid, "alice", "x")

	var body struct {
		ProjectID int64 `json:"project_id"`
		Totals    struct {
			Created int `json:"created"`
		} `json:"totals"`
	}
	envGetJSON(t, env, "/api/v1/digest?since="+url.QueryEscape(rfc3339Offset(-time.Hour)), &body)
	assert.Equal(t, int64(0), body.ProjectID, "global digest reports project_id=0")
	assert.GreaterOrEqual(t, body.Totals.Created, 1)
}
