package tui

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestClient stands up an httptest server with handler, registers
// cleanup, and returns a daemon Client pointed at it.
func newTestClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return NewClient(srv.URL, srv.Client())
}

// respondJSON writes body as a JSON response with the right Content-Type.
func respondJSON(t *testing.T, w http.ResponseWriter, body any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(body); err != nil {
		t.Fatalf("encode response: %v", err)
	}
}

func TestClient_ListIssues_BuildsExpectedURLAndDecodes(t *testing.T) {
	var gotURL string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotURL = r.URL.String()
		respondJSON(t, w, map[string]any{
			"issues": []map[string]any{
				{"number": 1, "title": "a", "status": "open"},
				{"number": 2, "title": "b", "status": "open"},
			},
		})
	})
	got, err := c.ListIssues(context.Background(), 7, ListFilter{Status: "open"})
	require.NoError(t, err)
	if !strings.Contains(gotURL, "/api/v1/projects/7/issues") {
		t.Fatalf("unexpected URL: %s", gotURL)
	}
	if !strings.Contains(gotURL, "status=open") {
		t.Fatalf("status filter missing: %s", gotURL)
	}
	if len(got) != 2 {
		t.Fatalf("got %d issues, want 2", len(got))
	}
}

func TestClient_ListIssues_SendsLimit(t *testing.T) {
	var gotQuery string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"issues":[]}`))
	})
	_, err := c.ListIssues(context.Background(), 7, ListFilter{Limit: 2001})
	require.NoError(t, err)
	if !strings.Contains(gotQuery, "limit=2001") {
		t.Fatalf("limit not sent: %q", gotQuery)
	}
	if strings.Contains(gotQuery, "status=") {
		t.Fatalf("empty status must not be sent: %q", gotQuery)
	}
}

func TestModel_FetchInitialUsesQueueFetchFilter(t *testing.T) {
	var gotQuery string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"issues":[]}`))
	})
	m := Model{
		api:   c,
		scope: scope{projectID: 7},
		list:  listModel{filter: ListFilter{Status: "closed"}},
	}
	msg := m.fetchInitial()()
	fetched, ok := msg.(initialFetchMsg)
	if !ok {
		t.Fatalf("fetchInitial msg = %T, want initialFetchMsg", msg)
	}
	require.NoError(t, fetched.err)
	if !strings.Contains(gotQuery, "limit=2001") {
		t.Fatalf("limit not sent: %q", gotQuery)
	}
	if strings.Contains(gotQuery, "status=") {
		t.Fatalf("render status leaked into initial fetch: %q", gotQuery)
	}
	if !cacheKeysEqual(fetched.dispatchKey, cacheKey{projectID: 7, limit: queueFetchLimit}) {
		t.Fatalf("dispatchKey = %+v", fetched.dispatchKey)
	}
}

func TestClient_GetIssueDetail_DecodesWrappedEnvelope(t *testing.T) {
	var gotPath string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		respondJSON(t, w, map[string]any{
			"issue": map[string]any{
				"uid": "01JZ0000000000000000000001", "project_uid": "01JZ0000000000000000000002",
				"number": 42, "title": "fix", "status": "open",
			},
			"comments": []any{},
			"links": []map[string]any{
				{
					"id": 1, "type": "blocks", "from_number": 42, "to_number": 7,
					"from_issue_uid": "01JZ0000000000000000000001",
					"to_issue_uid":   "01JZ0000000000000000000003",
				},
			},
			"labels": []any{},
		})
	})
	detail, err := c.GetIssueDetail(context.Background(), 7, 42)
	require.NoError(t, err)
	if gotPath != "/api/v1/projects/7/issues/42" {
		t.Fatalf("unexpected path: %s", gotPath)
	}
	got := detail.Issue
	if got == nil || got.Number != 42 || got.Title != "fix" {
		t.Fatalf("unexpected issue: %+v", got)
	}
	if got.UID != "01JZ0000000000000000000001" {
		t.Fatalf("issue UID = %q", got.UID)
	}
	if got.ProjectUID != "01JZ0000000000000000000002" {
		t.Fatalf("project UID = %q", got.ProjectUID)
	}
	links, err := c.ListLinks(context.Background(), 7, 42)
	require.NoError(t, err)
	if len(links) != 1 || links[0].FromIssueUID != "01JZ0000000000000000000001" ||
		links[0].ToIssueUID != "01JZ0000000000000000000003" {
		t.Fatalf("link UIDs not decoded: %+v", links)
	}
}

func TestClient_ShowIssue_DecodesHierarchy(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		respondJSON(t, w, map[string]any{
			"issue": map[string]any{"number": 42, "title": "fix", "status": "open"},
			"parent": map[string]any{
				"number": 12, "title": "workspace polish", "status": "open",
			},
			"children": []map[string]any{
				{
					"number": 43, "title": "child", "status": "open",
					"labels": []string{"bug", "ux"},
					"child_counts": map[string]any{
						"open": 1, "total": 2,
					},
				},
			},
			"comments": []any{},
			"links":    []any{},
			"labels": []map[string]any{
				{"issue_id": 1, "label": "prio-1", "author": "a"},
				{"issue_id": 1, "label": "bug", "author": "a"},
			},
		})
	})
	got, err := c.GetIssueDetail(context.Background(), 7, 42)
	require.NoError(t, err)
	if got.Issue == nil || got.Issue.Number != 42 {
		t.Fatalf("unexpected issue: %+v", got.Issue)
	}
	if got.Parent == nil || got.Parent.Number != 12 || got.Parent.Title != "workspace polish" {
		t.Fatalf("unexpected parent: %+v", got.Parent)
	}
	if len(got.Children) != 1 || got.Children[0].Number != 43 {
		t.Fatalf("unexpected children: %+v", got.Children)
	}
	if len(got.Children[0].Labels) != 2 || got.Children[0].Labels[0] != "bug" {
		t.Fatalf("child labels not decoded: %+v", got.Children[0].Labels)
	}
	if got.Children[0].ChildCounts == nil || got.Children[0].ChildCounts.Total != 2 {
		t.Fatalf("child counts not decoded: %+v", got.Children[0].ChildCounts)
	}
	if len(got.Issue.Labels) != 2 || got.Issue.Labels[0] != "bug" || got.Issue.Labels[1] != "prio-1" {
		t.Fatalf("issue labels not sorted: %+v", got.Issue.Labels)
	}
}

func TestClient_CreateIssue_SendsIdempotencyHeader(t *testing.T) {
	var gotKey string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("Idempotency-Key")
		respondJSON(t, w, map[string]any{
			"issue":   map[string]any{"number": 1, "title": "t", "status": "open"},
			"changed": true,
		})
	})
	_, err := c.CreateIssue(context.Background(), 7, CreateIssueBody{
		Title: "t", Actor: "alice", IdempotencyKey: "my-key",
	})
	require.NoError(t, err)
	if gotKey != "my-key" {
		t.Fatalf("Idempotency-Key not forwarded: %q", gotKey)
	}
}

func TestClient_DecodeError_ReturnsAPIError(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(
			`{"status":404,"error":{"code":"project_not_initialized",` +
				`"message":"no .kata.toml ancestor","hint":"run kata init"}}`))
	})
	_, err := c.GetIssueDetail(context.Background(), 7, 42)
	if err == nil {
		t.Fatal("expected error")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected *APIError, got %T: %v", err, err)
	}
	if apiErr.Code != "project_not_initialized" {
		t.Fatalf("Code = %q, want project_not_initialized", apiErr.Code)
	}
	if apiErr.Status != http.StatusNotFound {
		t.Fatalf("Status = %d, want 404", apiErr.Status)
	}
	if apiErr.Hint != "run kata init" {
		t.Fatalf("Hint = %q, want run kata init", apiErr.Hint)
	}
	if !strings.Contains(apiErr.Error(), "project_not_initialized") {
		t.Fatalf("Error() = %q, want it to mention the code", apiErr.Error())
	}
}

func TestClient_RemoveLabel_PathEscapesLabel(t *testing.T) {
	var gotRawURI, gotMethod, gotActor string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotRawURI = r.RequestURI
		gotMethod = r.Method
		gotActor = r.URL.Query().Get("actor")
		respondJSON(t, w, map[string]any{
			"issue":   map[string]any{"number": 1, "title": "t", "status": "open"},
			"changed": true,
		})
	})
	_, err := c.RemoveLabel(context.Background(), 7, 42, "team/backend", "alice")
	require.NoError(t, err)
	if gotMethod != http.MethodDelete {
		t.Fatalf("method = %s, want DELETE", gotMethod)
	}
	if !strings.Contains(gotRawURI, "labels/team%2Fbackend") {
		t.Fatalf("label not path-escaped, raw URI = %s", gotRawURI)
	}
	if gotActor != "alice" {
		t.Fatalf("actor query missing: %q", gotActor)
	}
}

func TestClient_ListComments_RoutesThroughShowIssue(t *testing.T) {
	var gotPath string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		respondJSON(t, w, map[string]any{
			"issue":    map[string]any{"number": 42, "title": "t", "status": "open"},
			"comments": []map[string]any{{"id": 1, "author": "a", "body": "hi"}},
			"links":    []any{},
			"labels":   []any{},
		})
	})
	got, err := c.ListComments(context.Background(), 7, 42)
	require.NoError(t, err)
	if gotPath != "/api/v1/projects/7/issues/42" {
		t.Fatalf("unexpected path: %s", gotPath)
	}
	if len(got) != 1 || got[0].Body != "hi" {
		t.Fatalf("unexpected comments: %+v", got)
	}
}

func TestClient_AssignEmptyOwnerRoutesToUnassign(t *testing.T) {
	var gotPath string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		respondJSON(t, w, map[string]any{
			"issue":   map[string]any{"number": 1, "title": "t", "status": "open"},
			"changed": true,
		})
	})
	_, err := c.Assign(context.Background(), 7, 42, "", "alice")
	require.NoError(t, err)
	if !strings.HasSuffix(gotPath, "/actions/unassign") {
		t.Fatalf("expected unassign path, got %s", gotPath)
	}
}

func TestClient_ListEvents_FiltersByIssueClientSide(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/projects/7/events" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		respondJSON(t, w, map[string]any{
			"events": []map[string]any{
				{
					"event_id": 1, "type": "issue.commented", "issue_number": 42, "actor": "a",
					"project_uid": "01JZ0000000000000000000002",
					"issue_uid":   "01JZ0000000000000000000001",
				},
				{"event_id": 2, "type": "issue.commented", "issue_number": 99, "actor": "a"},
				{
					"event_id": 3, "type": "issue.labeled", "issue_number": 42, "actor": "a",
					"project_uid":          "01JZ0000000000000000000002",
					"issue_uid":            "01JZ0000000000000000000001",
					"related_issue_uid":    "01JZ0000000000000000000004",
					"related_issue_number": 84,
				},
			},
			"next_after_id":  3,
			"reset_required": false,
		})
	})
	got, err := c.ListEvents(context.Background(), 7, 42)
	require.NoError(t, err)
	if len(got) != 2 {
		t.Fatalf("got %d events for #42, want 2", len(got))
	}
	for _, e := range got {
		if e.Type != "issue.commented" && e.Type != "issue.labeled" {
			t.Fatalf("unexpected event leaked through filter: %+v", e)
		}
	}
	if got[0].ProjectUID != "01JZ0000000000000000000002" ||
		got[0].IssueUID != "01JZ0000000000000000000001" {
		t.Fatalf("event UIDs not decoded: %+v", got[0])
	}
	if got[1].RelatedIssueUID != "01JZ0000000000000000000004" {
		t.Fatalf("related issue UID not decoded: %+v", got[1])
	}
}

// TestClient_ListIssues_NotNilOnSuccess guards the bug where listIssuesAt
// returned resp.Issues evaluated *before* c.do filled it (the do call was
// the second operand of the comma-statement, so resp was nil at capture).
func TestClient_ListIssues_NotNilOnSuccess(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"issues":[{"number":1,"title":"a","status":"open"}]}`))
	})
	got, err := c.ListIssues(context.Background(), 7, ListFilter{})
	require.NoError(t, err)
	if len(got) != 1 || got[0].Number != 1 {
		t.Fatalf("got %+v, want one issue with number=1", got)
	}
}

// TestClient_ListAllIssues_NotNilOnSuccess covers the same regression on
// the cross-project endpoint.
func TestClient_ListAllIssues_NotNilOnSuccess(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"issues":[{"number":2,"title":"b","status":"open"}]}`))
	})
	got, err := c.ListAllIssues(context.Background(), ListFilter{})
	require.NoError(t, err)
	if len(got) != 1 || got[0].Number != 2 {
		t.Fatalf("got %+v, want one issue with number=2", got)
	}
}

// TestClient_ListProjects_NotNilOnSuccess is the analogue for ListProjects.
func TestClient_ListProjects_NotNilOnSuccess(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"projects":[{"id":7,"identity":"x","name":"k"}]}`))
	})
	got, err := c.ListProjects(context.Background())
	require.NoError(t, err)
	if len(got) != 1 || got[0].ID != 7 {
		t.Fatalf("got %+v, want one project with id=7", got)
	}
}

// TestClient_ListIssues_FilterShape asserts only the daemon-honored
// query params land on the wire. Owner/Author/Search/Labels are kept on
// the struct for client-side filtering but must not leak as URL params.
func TestClient_ListIssues_FilterShape(t *testing.T) {
	var gotURL string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotURL = r.URL.String()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"issues":[]}`))
	})
	_, err := c.ListIssues(context.Background(), 7, ListFilter{
		Status: "open",
		Owner:  "alice",
		Author: "bob",
		Search: "foo",
		Labels: []string{"x"},
	})
	require.NoError(t, err)
	if !strings.Contains(gotURL, "status=open") {
		t.Fatalf("status not sent: %s", gotURL)
	}
	for _, leaked := range []string{"owner=", "author=", "q=", "label=", "include_deleted="} {
		if strings.Contains(gotURL, leaked) {
			t.Fatalf("client leaked %q to wire (daemon ignores it): %s", leaked, gotURL)
		}
	}
}

// TestListIssues_TUIDecodePopulatesLabels (Plan 8 commit 5b): the
// daemon's list response now embeds a labels slice per row (api.IssueOut
// = db.Issue + Labels). The TUI's Issue struct already has the right
// `json:"labels,omitempty"` tag (commit 1), so the wire→struct mapping
// is automatic — this test pins that promise so a future struct-tag
// removal doesn't silently drop labels from the list view.
func TestListIssues_TUIDecodePopulatesLabels(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		respondJSON(t, w, map[string]any{
			"issues": []map[string]any{
				{
					"number": 1, "title": "first", "status": "open",
					"labels": []string{"bug", "prio-1"},
				},
				{
					"number": 2, "title": "second", "status": "open",
					"labels": []string{"enhancement"},
				},
				{
					"number": 3, "title": "third", "status": "open",
					// no labels field — omitempty on the wire.
				},
			},
		})
	})
	got, err := c.ListIssues(context.Background(), 7, ListFilter{})
	require.NoError(t, err)
	if len(got) != 3 {
		t.Fatalf("got %d issues, want 3", len(got))
	}
	wantPerNumber := map[int64][]string{
		1: {"bug", "prio-1"},
		2: {"enhancement"},
		3: nil,
	}
	for _, iss := range got {
		want := wantPerNumber[iss.Number]
		if len(iss.Labels) != len(want) {
			t.Fatalf("issue #%d labels = %v, want %v", iss.Number, iss.Labels, want)
		}
		for i := range want {
			if iss.Labels[i] != want[i] {
				t.Fatalf("issue #%d labels[%d] = %q, want %q",
					iss.Number, i, iss.Labels[i], want[i])
			}
		}
	}
}

// TestShowIssue_PopulatesLabelsFromTopLevel: the daemon ships labels as
// a sibling slice on the show envelope (one IssueLabel per row, no
// guaranteed order). showIssue extracts the label names, sorts them
// alphabetically, and assigns them to resp.Issue.Labels so downstream
// rendering doesn't have to re-sort. Wire absence (omitempty on the
// Issue struct) means a show response with no labels leaves a
// previously-populated Labels slice empty — covered by other tests.
func TestShowIssue_PopulatesLabelsFromTopLevel(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		respondJSON(t, w, map[string]any{
			"issue":    map[string]any{"number": 42, "title": "fix", "status": "open"},
			"comments": []any{},
			"links":    []any{},
			"labels": []map[string]any{
				{"issue_id": 1, "label": "prio-1", "author": "a"},
				{"issue_id": 1, "label": "bug", "author": "a"},
				{"issue_id": 1, "label": "needs-design", "author": "a"},
			},
		})
	})
	resp, err := c.showIssue(context.Background(), 7, 42)
	require.NoError(t, err)
	got := resp.Issue.Labels
	want := []string{"bug", "needs-design", "prio-1"}
	if len(got) != len(want) {
		t.Fatalf("labels = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("labels[%d] = %q, want %q (full: %v)", i, got[i], want[i], got)
		}
	}
}

// TestAPIError_EmptyBodyFallback covers the 404 with no body case where
// Code and Message are both blank. Without the fallback, Error() would
// return ": ".
func TestAPIError_EmptyBodyFallback(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	_, err := c.GetIssueDetail(context.Background(), 7, 42)
	if err == nil {
		t.Fatal("expected error")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected *APIError, got %T", err)
	}
	msg := apiErr.Error()
	if !strings.Contains(msg, "HTTP 404") {
		t.Fatalf("Error() = %q, want it to mention HTTP 404", msg)
	}
	if !strings.Contains(msg, "/api/v1/projects/7/issues/42") {
		t.Fatalf("Error() = %q, want it to mention the path", msg)
	}
}

// TestClient_ListProjectsWithStats_Decodes pins that the typed client
// decodes the ?include=stats wire shape into ProjectSummaryWithStats,
// including the optional Stats field. Spec §7.3.
func TestClient_ListProjectsWithStats_Decodes(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/api/v1/projects", r.URL.Path)
		require.Equal(t, "stats", r.URL.Query().Get("include"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
            "projects": [
                {"id": 7, "identity": "github.com/wesm/x", "name": "x",
                 "stats": {"open": 3, "closed": 1, "last_event_at": "2026-05-04T12:00:00.000Z"}},
                {"id": 9, "identity": "github.com/wesm/empty", "name": "empty",
                 "stats": {"open": 0, "closed": 0, "last_event_at": null}}
            ]
        }`))
	})

	got, err := c.ListProjectsWithStats(t.Context())
	require.NoError(t, err)
	require.Len(t, got, 2)

	require.NotNil(t, got[0].Stats)
	assert.Equal(t, 3, got[0].Stats.Open)
	assert.Equal(t, 1, got[0].Stats.Closed)
	require.NotNil(t, got[0].Stats.LastEventAt)

	require.NotNil(t, got[1].Stats)
	assert.Equal(t, 0, got[1].Stats.Open)
	assert.Nil(t, got[1].Stats.LastEventAt, "null wire → nil pointer")
}

// TestClient_ListProjectsWithStats_NotNilOnSuccess pins the same
// regression covered for ListIssues / ListAllIssues: a 200 with an empty
// array returns []ProjectSummaryWithStats{}, never nil — callers iterate
// without nil-checks. Spec §7.3.
func TestClient_ListProjectsWithStats_NotNilOnSuccess(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"projects": []}`))
	})

	got, err := c.ListProjectsWithStats(t.Context())
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Len(t, got, 0)
}

// TestClient_ResolveProject_PropagatesParseError guards against a
// malformed .kata.toml silently falling through to a start_path
// request. In remote-client mode the daemon cannot stat the
// client's path, so the failure mode is a confusing "no such file"
// rather than the actual broken-config error the user can fix.
func TestClient_ResolveProject_PropagatesParseError(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".kata.toml"), //nolint:gosec // test fixture
		[]byte("not = valid = toml ==="), 0o644))

	var called atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		called.Add(1)
	}))
	defer srv.Close()
	c := NewClient(srv.URL, srv.Client())

	_, err := c.ResolveProject(t.Context(), dir)
	require.Error(t, err)
	assert.Zero(t, called.Load(), "TUI must reject parse errors before reaching the daemon")
}

// TestClient_ResolveProject_FallsBackOnMissingConfig confirms the
// missing case still works: no .kata.toml means start_path is sent
// for daemon-side filesystem resolution.
func TestClient_ResolveProject_FallsBackOnMissingConfig(t *testing.T) {
	dir := t.TempDir() // no .kata.toml

	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bs, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(bs, &got)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"project":{"id":42}}`))
	}))
	defer srv.Close()
	c := NewClient(srv.URL, srv.Client())

	_, err := c.ResolveProject(t.Context(), dir)
	require.NoError(t, err)
	assert.Equal(t, dir, got["start_path"])
	_, hasIdentity := got["project_identity"]
	assert.False(t, hasIdentity)
}
