package daemon_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wesm/kata/internal/testenv"
)

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

	since := time.Now().Add(-1 * time.Hour).UTC().Format(time.RFC3339)
	until := time.Now().Add(1 * time.Hour).UTC().Format(time.RFC3339)
	digestURL := env.URL +
		"/api/v1/projects/" + strconv.FormatInt(pid, 10) +
		"/digest?since=" + url.QueryEscape(since) + "&until=" + url.QueryEscape(until)

	resp, err := env.HTTP.Get(digestURL)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, 200, resp.StatusCode)

	var body struct {
		EventCount int   `json:"event_count"`
		ProjectID  int64 `json:"project_id"`
		Totals     struct {
			Created   int `json:"created"`
			Closed    int `json:"closed"`
			Commented int `json:"commented"`
			Labeled   int `json:"labeled"`
			Linked    int `json:"linked"`
			Unlinked  int `json:"unlinked"`
			Unblocked int `json:"unblocked"`
		} `json:"totals"`
		Actors []struct {
			Actor  string `json:"actor"`
			Totals struct {
				Created   int `json:"created"`
				Closed    int `json:"closed"`
				Commented int `json:"commented"`
				Unblocked int `json:"unblocked"`
			} `json:"totals"`
			Issues []struct {
				IssueNumber int64    `json:"issue_number"`
				Actions     []string `json:"actions"`
			} `json:"issues"`
		} `json:"actors"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))

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
	var aliceFirst []string
	for _, iss := range body.Actors[0].Issues {
		if iss.IssueNumber == a {
			aliceFirst = iss.Actions
			break
		}
	}
	require.NotNil(t, aliceFirst)
	assert.Equal(t, []string{"created", "commented:2", "closed:done"}, aliceFirst)

	// Bob's actions on issue b should include the labeled token and the
	// unblocks-credit referencing issue b.
	var bobOnB []string
	for _, iss := range body.Actors[1].Issues {
		if iss.IssueNumber == b {
			bobOnB = iss.Actions
			break
		}
	}
	require.NotNil(t, bobOnB)
	assert.Contains(t, bobOnB, "labeled:bug")
}

// TestDigest_ActorFilter only returns events for the named actors.
func TestDigest_ActorFilter(t *testing.T) {
	env := testenv.New(t)
	pid := initWorkspaceViaHTTP(t, env, "https://github.com/wesm/kata.git")
	_ = createIssueAs(t, env, pid, "alice", "x")
	_ = createIssueAs(t, env, pid, "bob", "y")

	since := time.Now().Add(-1 * time.Hour).UTC().Format(time.RFC3339)
	resp, err := env.HTTP.Get(env.URL +
		"/api/v1/projects/" + strconv.FormatInt(pid, 10) +
		"/digest?since=" + url.QueryEscape(since) + "&actor=alice")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, 200, resp.StatusCode)

	var body struct {
		Actors []struct {
			Actor string `json:"actor"`
		} `json:"actors"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
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
	body, _ := json.Marshal(map[string]any{
		"actor":  "alice",
		"title":  "rich",
		"labels": []string{"bug"},
		"owner":  "bob",
		"links": []map[string]any{
			{"type": "blocks", "to_number": target},
		},
	})
	resp, err := env.HTTP.Post(
		env.URL+"/api/v1/projects/"+strconv.FormatInt(pid, 10)+"/issues",
		"application/json", bytes.NewReader(body))
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, 200, resp.StatusCode)
	var created struct {
		Issue struct {
			Number int64 `json:"number"`
		} `json:"issue"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&created))
	rich := created.Issue.Number

	since := time.Now().Add(-1 * time.Hour).UTC().Format(time.RFC3339)
	until := time.Now().Add(1 * time.Hour).UTC().Format(time.RFC3339)
	dresp, err := env.HTTP.Get(env.URL +
		"/api/v1/projects/" + strconv.FormatInt(pid, 10) +
		"/digest?since=" + url.QueryEscape(since) + "&until=" + url.QueryEscape(until))
	require.NoError(t, err)
	defer func() { _ = dresp.Body.Close() }()
	require.Equal(t, 200, dresp.StatusCode)

	var digest struct {
		Totals struct {
			Created  int `json:"created"`
			Labeled  int `json:"labeled"`
			Assigned int `json:"assigned"`
			Linked   int `json:"linked"`
		} `json:"totals"`
		Actors []struct {
			Actor  string `json:"actor"`
			Totals struct {
				Created  int `json:"created"`
				Labeled  int `json:"labeled"`
				Assigned int `json:"assigned"`
				Linked   int `json:"linked"`
			} `json:"totals"`
			Issues []struct {
				IssueNumber int64    `json:"issue_number"`
				Actions     []string `json:"actions"`
			} `json:"issues"`
		} `json:"actors"`
	}
	require.NoError(t, json.NewDecoder(dresp.Body).Decode(&digest))

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

	var actions []string
	for _, iss := range digest.Actors[0].Issues {
		if iss.IssueNumber == rich {
			actions = iss.Actions
			break
		}
	}
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
	resp, err := env.HTTP.Get(env.URL +
		"/api/v1/projects/" + strconv.FormatInt(pid, 10) + "/digest?since=not-a-time")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	assert.Equal(t, 400, resp.StatusCode)
}

// TestDigest_GlobalEndpoint covers the cross-project route.
func TestDigest_GlobalEndpoint(t *testing.T) {
	env := testenv.New(t)
	pid := initWorkspaceViaHTTP(t, env, "https://github.com/wesm/kata.git")
	_ = createIssueAs(t, env, pid, "alice", "x")

	since := time.Now().Add(-1 * time.Hour).UTC().Format(time.RFC3339)
	resp, err := env.HTTP.Get(env.URL + "/api/v1/digest?since=" + url.QueryEscape(since))
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, 200, resp.StatusCode)

	var body struct {
		ProjectID int64 `json:"project_id"`
		Totals    struct {
			Created int `json:"created"`
		} `json:"totals"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	assert.Equal(t, int64(0), body.ProjectID, "global digest reports project_id=0")
	assert.GreaterOrEqual(t, body.Totals.Created, 1)
}

// --- helpers below are local to digest tests; they parallel the ones in
// handlers_links_test.go but accept an explicit actor so cross-actor scenarios
// can be set up. ---

func createIssueAs(t *testing.T, env *testenv.Env, pid int64, actor, title string) int64 {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"actor": actor, "title": title})
	resp, err := env.HTTP.Post(
		env.URL+"/api/v1/projects/"+strconv.FormatInt(pid, 10)+"/issues",
		"application/json", bytes.NewReader(body))
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, 200, resp.StatusCode)
	var out struct {
		Issue struct {
			Number int64 `json:"number"`
		} `json:"issue"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	return out.Issue.Number
}

func postCommentAs(t *testing.T, env *testenv.Env, pid, num int64, actor, txt string) {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"actor": actor, "body": txt})
	resp, err := env.HTTP.Post(
		env.URL+"/api/v1/projects/"+strconv.FormatInt(pid, 10)+
			"/issues/"+strconv.FormatInt(num, 10)+"/comments",
		"application/json", bytes.NewReader(body))
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
}

func closeIssueAs(t *testing.T, env *testenv.Env, pid, num int64, actor, reason string) {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"actor": actor, "reason": reason})
	resp, err := env.HTTP.Post(
		env.URL+"/api/v1/projects/"+strconv.FormatInt(pid, 10)+
			"/issues/"+strconv.FormatInt(num, 10)+"/actions/close",
		"application/json", bytes.NewReader(body))
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
}

func postLabelAs(t *testing.T, env *testenv.Env, pid, num int64, actor, label string) {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"actor": actor, "label": label})
	resp, err := env.HTTP.Post(
		env.URL+"/api/v1/projects/"+strconv.FormatInt(pid, 10)+
			"/issues/"+strconv.FormatInt(num, 10)+"/labels",
		"application/json", bytes.NewReader(body))
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
}

func postLinkAs(t *testing.T, env *testenv.Env, pid, fromNum int64, actor, linkType string, toNum int64) int64 {
	t.Helper()
	body, _ := json.Marshal(map[string]any{
		"actor": actor, "type": linkType, "to_number": toNum,
	})
	resp, err := env.HTTP.Post(
		env.URL+"/api/v1/projects/"+strconv.FormatInt(pid, 10)+
			"/issues/"+strconv.FormatInt(fromNum, 10)+"/links",
		"application/json", bytes.NewReader(body))
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equalf(t, 200, resp.StatusCode, "postLinkAs status")
	var out struct {
		Link struct {
			ID int64 `json:"id"`
		} `json:"link"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	return out.Link.ID
}

// deleteLinkBlocksAs removes a blocks link by re-creating it just to read its
// id, then DELETEs it. The DELETE attribution is to `actor`, which is the
// person who gets credit for the unblock.
func deleteLinkBlocksAs(t *testing.T, env *testenv.Env, pid, fromNum int64, actor string, toNum int64) {
	t.Helper()
	// Look up the existing link id by reading the blocker's links list. The
	// daemon doesn't expose a /links GET, so we rely on the issue show
	// payload, which includes Links with their ids.
	resp, err := env.HTTP.Get(env.URL + "/api/v1/projects/" +
		strconv.FormatInt(pid, 10) + "/issues/" + strconv.FormatInt(fromNum, 10))
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	var view struct {
		Links []struct {
			ID       int64  `json:"id"`
			Type     string `json:"type"`
			ToNumber int64  `json:"to_number"`
		} `json:"links"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&view))
	var linkID int64
	for _, l := range view.Links {
		if l.Type == "blocks" && l.ToNumber == toNum {
			linkID = l.ID
			break
		}
	}
	require.NotZero(t, linkID, "no blocks link to %d found on issue %d", toNum, fromNum)

	delURL := env.URL + "/api/v1/projects/" + strconv.FormatInt(pid, 10) +
		"/issues/" + strconv.FormatInt(fromNum, 10) + "/links/" +
		strconv.FormatInt(linkID, 10) + "?actor=" + url.QueryEscape(actor)
	req, err := http.NewRequest(http.MethodDelete, delURL, nil)
	require.NoError(t, err)
	dresp, err := env.HTTP.Do(req)
	require.NoError(t, err)
	require.NoError(t, dresp.Body.Close())
	require.Equal(t, 200, dresp.StatusCode)
}
