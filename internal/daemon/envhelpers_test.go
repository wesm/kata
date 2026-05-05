package daemon_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os/exec"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wesm/kata/internal/testenv"
)

// defaultActor is the actor string used by the un-parameterized helpers
// (postLink, postLabel, createIssueViaHTTP). Tests that need cross-actor
// scenarios should use the *As variants.
const defaultActor = "tester"

// envDoJSON sends a JSON-bodied request against env's daemon. When body is
// non-nil it is JSON-encoded and Content-Type is set automatically. When out
// is non-nil and the response is 2xx the body is decoded into out; otherwise
// the body is drained. The response is returned for status assertions.
func envDoJSON(t *testing.T, env *testenv.Env, method, path string, body, out any) *http.Response {
	t.Helper()
	var bodyReader io.Reader
	if body != nil {
		js, err := json.Marshal(body)
		require.NoError(t, err)
		bodyReader = bytes.NewReader(js)
	}
	req, err := http.NewRequestWithContext(context.Background(), method, env.URL+path, bodyReader)
	require.NoError(t, err)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := env.HTTP.Do(req) //nolint:gosec // test request to loopback URL
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	if out != nil && resp.StatusCode >= 200 && resp.StatusCode < 300 {
		require.NoError(t, json.NewDecoder(resp.Body).Decode(out))
	} else {
		_, _ = io.Copy(io.Discard, resp.Body)
	}
	return resp
}

// envDoRaw is the raw-body counterpart to envDoJSON. It builds an arbitrary
// request (optional JSON body, optional headers), executes it, and returns the
// response paired with the buffered body bytes. Use this when the test needs
// to assert on the raw payload (substring checks, error envelopes) instead of
// decoding into a typed struct. resp.Body is closed before return.
func envDoRaw(t *testing.T, env *testenv.Env, method, path string, body any, headers map[string]string) (*http.Response, []byte) {
	t.Helper()
	var bodyReader io.Reader
	if body != nil {
		js, err := json.Marshal(body)
		require.NoError(t, err)
		bodyReader = bytes.NewReader(js)
	}
	req, err := http.NewRequestWithContext(context.Background(), method, env.URL+path, bodyReader)
	require.NoError(t, err)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := env.HTTP.Do(req) //nolint:gosec // test request to loopback URL
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	bs, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp, bs
}

// envGetRaw is the GET shorthand for envDoRaw — no body, no extra headers.
func envGetRaw(t *testing.T, env *testenv.Env, path string) (*http.Response, []byte) {
	t.Helper()
	return envDoRaw(t, env, http.MethodGet, path, nil, nil)
}

// envPostJSON POSTs body and decodes the response into out, asserting 200.
// For non-2xx assertions, use envDoJSON directly.
func envPostJSON(t *testing.T, env *testenv.Env, path string, body, out any) {
	t.Helper()
	resp := envDoJSON(t, env, http.MethodPost, path, body, out)
	require.Equalf(t, http.StatusOK, resp.StatusCode, "POST %s expected 200, got %d", path, resp.StatusCode)
}

// envGetJSON GETs path and decodes the response into out, asserting 200.
func envGetJSON(t *testing.T, env *testenv.Env, path string, out any) {
	t.Helper()
	resp := envDoJSON(t, env, http.MethodGet, path, nil, out)
	require.Equalf(t, http.StatusOK, resp.StatusCode, "GET %s expected 200, got %d", path, resp.StatusCode)
}

// rfc3339Offset returns now+d formatted as RFC3339 in UTC; convenient for
// digest tests building ?since= / ?until= query params.
func rfc3339Offset(d time.Duration) string {
	return time.Now().Add(d).UTC().Format(time.RFC3339)
}

// projectPath returns the URL prefix for resources scoped to a project rowid.
func projectPath(projectID int64) string {
	return "/api/v1/projects/" + strconv.FormatInt(projectID, 10)
}

// issuePath returns the URL for a specific issue under a project. When suffix
// is non-empty it is appended after the issue number (e.g. "comments" or
// "actions/close"); when empty the resource URL itself is returned.
func issuePath(projectID, issueNumber int64, suffix string) string {
	base := projectPath(projectID) + "/issues/" + strconv.FormatInt(issueNumber, 10)
	if suffix == "" {
		return base
	}
	return base + "/" + suffix
}

// initWorkspaceViaHTTP runs git init in a temp dir, adds origin, posts to
// /api/v1/projects, and returns the resolved project_id.
func initWorkspaceViaHTTP(t *testing.T, env *testenv.Env, origin string) int64 {
	t.Helper()
	dir := t.TempDir()
	mustRun(t, dir, "git", "init", "--quiet")
	mustRun(t, dir, "git", "remote", "add", "origin", origin)

	envPostJSON(t, env, "/api/v1/projects", map[string]string{"start_path": dir}, nil)
	var out struct {
		Project struct {
			ID int64 `json:"id"`
		} `json:"project"`
	}
	envPostJSON(t, env, "/api/v1/projects/resolve", map[string]string{"start_path": dir}, &out)
	return out.Project.ID
}

// mustRun runs a command in dir, failing the test on error.
func mustRun(t *testing.T, dir, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...) //nolint:gosec // G204: test-controlled args
	cmd.Dir = dir
	require.NoErrorf(t, cmd.Run(), "%s %v", name, args)
}

// setupOneIssue creates a workspace + one issue, returns (project_id, issue_number).
func setupOneIssue(t *testing.T, env *testenv.Env) (int64, int64) {
	t.Helper()
	pid := initWorkspaceViaHTTP(t, env, "https://github.com/wesm/kata.git")
	n := createIssueViaHTTP(t, env, pid, "x")
	return pid, n
}

// setupTwoIssues creates a workspace + two issues, returns (project_id, a_number, b_number).
func setupTwoIssues(t *testing.T, env *testenv.Env) (int64, int64, int64) {
	t.Helper()
	pid := initWorkspaceViaHTTP(t, env, "https://github.com/wesm/kata.git")
	a := createIssueViaHTTP(t, env, pid, "a")
	b := createIssueViaHTTP(t, env, pid, "b")
	return pid, a, b
}

// createIssueViaHTTP creates an issue with the default actor and returns its number.
func createIssueViaHTTP(t *testing.T, env *testenv.Env, projectID int64, title string) int64 {
	t.Helper()
	return createIssueAs(t, env, projectID, defaultActor, title)
}

// createIssueAs creates an issue attributed to actor and returns its number.
func createIssueAs(t *testing.T, env *testenv.Env, projectID int64, actor, title string) int64 {
	t.Helper()
	var out struct {
		Issue struct {
			Number int64 `json:"number"`
		} `json:"issue"`
	}
	envPostJSON(t, env, projectPath(projectID)+"/issues",
		map[string]string{"actor": actor, "title": title}, &out)
	return out.Issue.Number
}

// postCommentAs posts a comment attributed to actor.
func postCommentAs(t *testing.T, env *testenv.Env, projectID, issueNumber int64, actor, body string) {
	t.Helper()
	envPostJSON(t, env, issuePath(projectID, issueNumber, "comments"),
		map[string]string{"actor": actor, "body": body}, nil)
}

// closeIssueAs closes an issue attributed to actor.
func closeIssueAs(t *testing.T, env *testenv.Env, projectID, issueNumber int64, actor, reason string) {
	t.Helper()
	envPostJSON(t, env, issuePath(projectID, issueNumber, "actions/close"),
		map[string]string{"actor": actor, "reason": reason}, nil)
}

// labelResp is the decoded shape of an AddLabelResponse body.
type labelResp struct {
	Issue struct {
		Number int64 `json:"number"`
	} `json:"issue"`
	Label struct {
		Label string `json:"label"`
	} `json:"label"`
	Event *struct {
		Type string `json:"type"`
	} `json:"event"`
	Changed bool `json:"changed"`
}

// postLabel calls POST /labels with the default actor and returns the decoded response.
func postLabel(t *testing.T, env *testenv.Env, projectID, issueNumber int64, label string) labelResp {
	t.Helper()
	return postLabelAs(t, env, projectID, issueNumber, defaultActor, label)
}

// postLabelAs calls POST /labels attributed to actor and returns the decoded response.
func postLabelAs(t *testing.T, env *testenv.Env, projectID, issueNumber int64, actor, label string) labelResp {
	t.Helper()
	var out labelResp
	envPostJSON(t, env, issuePath(projectID, issueNumber, "labels"),
		map[string]string{"actor": actor, "label": label}, &out)
	return out
}

// deleteLabel calls DELETE /labels/{label} with the default actor and returns
// the response paired with the decoded labelResp. The decoded body is only
// populated for 2xx responses; callers asserting on error envelopes should
// inspect resp.StatusCode and ignore the labelResp.
func deleteLabel(t *testing.T, env *testenv.Env, projectID, issueNumber int64, label string) (*http.Response, labelResp) {
	t.Helper()
	return deleteLabelAs(t, env, projectID, issueNumber, defaultActor, label)
}

// deleteLabelAs calls DELETE /labels/{label} attributed to actor.
func deleteLabelAs(t *testing.T, env *testenv.Env, projectID, issueNumber int64, actor, label string) (*http.Response, labelResp) {
	t.Helper()
	path := issuePath(projectID, issueNumber, "labels/"+url.PathEscape(label)) +
		"?actor=" + url.QueryEscape(actor)
	var out labelResp
	resp := envDoJSON(t, env, http.MethodDelete, path, nil, &out)
	return resp, out
}

// linkResp is the decoded shape of a CreateLinkResponse body. The Event UID
// fields are populated only by handlers that emit them (e.g. issue.linked);
// other paths leave them nil.
type linkResp struct {
	Issue struct {
		Number int64 `json:"number"`
	} `json:"issue"`
	Link struct {
		ID           int64  `json:"id"`
		Type         string `json:"type"`
		FromNumber   int64  `json:"from_number"`
		FromIssueUID string `json:"from_issue_uid"`
		ToNumber     int64  `json:"to_number"`
		ToIssueUID   string `json:"to_issue_uid"`
	} `json:"link"`
	Event *struct {
		Type            string  `json:"type"`
		ProjectUID      string  `json:"project_uid"`
		IssueUID        *string `json:"issue_uid"`
		RelatedIssueUID *string `json:"related_issue_uid"`
	} `json:"event"`
	Changed bool `json:"changed"`
}

// postLink calls POST /links with the default actor and returns the decoded response.
func postLink(t *testing.T, env *testenv.Env, projectID, fromNumber int64, linkType string, toNumber int64) linkResp {
	t.Helper()
	return postLinkAs(t, env, projectID, fromNumber, defaultActor, linkType, toNumber)
}

// postLinkAs calls POST /links attributed to actor and returns the decoded response.
func postLinkAs(t *testing.T, env *testenv.Env, projectID, fromNumber int64, actor, linkType string, toNumber int64) linkResp {
	t.Helper()
	var out linkResp
	envPostJSON(t, env, issuePath(projectID, fromNumber, "links"),
		map[string]any{"actor": actor, "type": linkType, "to_number": toNumber}, &out)
	return out
}

// postLinkRaw calls POST /links with an arbitrary payload (e.g. replace=true,
// blank actor) and returns the response paired with the decoded body. The
// decoded body is only populated for 2xx responses; callers asserting on error
// envelopes should inspect resp.StatusCode and ignore the linkResp.
func postLinkRaw(t *testing.T, env *testenv.Env, projectID, fromNumber int64, payload map[string]any) (*http.Response, linkResp) {
	t.Helper()
	var out linkResp
	resp := envDoJSON(t, env, http.MethodPost, issuePath(projectID, fromNumber, "links"), payload, &out)
	return resp, out
}

// deleteLink calls DELETE /links/{id} with the default actor and returns the
// response paired with the decoded linkResp.
func deleteLink(t *testing.T, env *testenv.Env, projectID, issueNumber, linkID int64) (*http.Response, linkResp) {
	t.Helper()
	return deleteLinkAs(t, env, projectID, issueNumber, defaultActor, linkID)
}

// deleteLinkAs calls DELETE /links/{id} attributed to actor.
func deleteLinkAs(t *testing.T, env *testenv.Env, projectID, issueNumber int64, actor string, linkID int64) (*http.Response, linkResp) {
	t.Helper()
	path := issuePath(projectID, issueNumber, "links/"+strconv.FormatInt(linkID, 10)) +
		"?actor=" + url.QueryEscape(actor)
	var out linkResp
	resp := envDoJSON(t, env, http.MethodDelete, path, nil, &out)
	return resp, out
}

// ownerResp is the decoded shape of an Assign/Unassign response body.
type ownerResp struct {
	Issue struct {
		Owner *string `json:"owner"`
	} `json:"issue"`
	Event *struct {
		Type string `json:"type"`
	} `json:"event"`
	Changed bool `json:"changed"`
}

// postAssign POSTs to /actions/assign and returns the response paired with the
// decoded body. The body is only populated for 2xx responses; callers asserting
// on error envelopes should inspect resp.StatusCode and ignore ownerResp.
func postAssign(t *testing.T, env *testenv.Env, projectID, issueNumber int64, actor, owner string) (*http.Response, ownerResp) {
	t.Helper()
	var out ownerResp
	resp := envDoJSON(t, env, http.MethodPost, issuePath(projectID, issueNumber, "actions/assign"),
		map[string]string{"actor": actor, "owner": owner}, &out)
	return resp, out
}

// postUnassign POSTs to /actions/unassign and returns the response paired with
// the decoded body.
func postUnassign(t *testing.T, env *testenv.Env, projectID, issueNumber int64, actor string) (*http.Response, ownerResp) {
	t.Helper()
	var out ownerResp
	resp := envDoJSON(t, env, http.MethodPost, issuePath(projectID, issueNumber, "actions/unassign"),
		map[string]string{"actor": actor}, &out)
	return resp, out
}

// lastEventPayload returns the JSON payload of the most recently inserted event
// of eventType for projectID. Used by tests verifying side-effect events that
// aren't surfaced in the HTTP response (e.g. the trailing unlink emitted by a
// parent --replace).
func lastEventPayload(t *testing.T, env *testenv.Env, projectID int64, eventType string) string {
	t.Helper()
	var payload string
	require.NoError(t, env.DB.QueryRowContext(t.Context(),
		`SELECT payload FROM events
		 WHERE project_id = ? AND type = ?
		 ORDER BY id DESC LIMIT 1`, projectID, eventType).Scan(&payload))
	return payload
}

// deleteLinkBlocksAs removes a blocks link from fromNumber → toNumber. The
// daemon doesn't expose a /links GET, so the link id is recovered from the
// issue show payload before the DELETE. The DELETE attribution is to actor,
// who gets credit for the unblock event.
func deleteLinkBlocksAs(t *testing.T, env *testenv.Env, projectID, fromNumber int64, actor string, toNumber int64) {
	t.Helper()
	var view struct {
		Links []struct {
			ID       int64  `json:"id"`
			Type     string `json:"type"`
			ToNumber int64  `json:"to_number"`
		} `json:"links"`
	}
	envGetJSON(t, env, issuePath(projectID, fromNumber, ""), &view)
	var linkID int64
	for _, l := range view.Links {
		if l.Type == "blocks" && l.ToNumber == toNumber {
			linkID = l.ID
			break
		}
	}
	require.NotZerof(t, linkID, "no blocks link to %d found on issue %d", toNumber, fromNumber)

	delPath := issuePath(projectID, fromNumber, "links/"+strconv.FormatInt(linkID, 10)) +
		"?actor=" + url.QueryEscape(actor)
	resp := envDoJSON(t, env, http.MethodDelete, delPath, nil, nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
}
