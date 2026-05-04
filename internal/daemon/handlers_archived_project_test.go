package daemon_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wesm/kata/internal/db"
)

// TestArchivedProject_SurfaceHandlersReturn404 covers every project-scoped
// surface handler that calls activeProjectByID. Each must hide an archived
// project behind a project_not_found 404, matching the behavior already
// pinned for createIssue.
//
// Internal callers (merge, restore, alias resolve) deliberately use
// store.ProjectByID directly to operate on archived rows; those paths are
// not exercised here.
func TestArchivedProject_SurfaceHandlersReturn404(t *testing.T) {
	h, projectID := bootstrapProject(t)
	ts := h.ts.(*httptest.Server)

	// Archive the project directly via the DB layer. The HTTP DELETE path is
	// covered by handlers_projects_test; here we only need the projects row
	// in archived state so we can probe each surface endpoint.
	_, _, err := h.DB().RemoveProject(context.Background(), db.RemoveProjectParams{
		ProjectID: projectID, Actor: "tester",
	})
	require.NoError(t, err)

	pid := strconv.FormatInt(projectID, 10)

	cases := []struct {
		name   string
		method string
		path   string
		body   any
	}{
		{"showProject", http.MethodGet, "/api/v1/projects/" + pid, nil},
		{"renameProject", http.MethodPatch, "/api/v1/projects/" + pid,
			map[string]any{"name": "renamed"}},
		{"resetIssueCounter", http.MethodPost,
			"/api/v1/projects/" + pid + "/reset-counter",
			map[string]any{"to": 1}},
		{"createIssue", http.MethodPost, "/api/v1/projects/" + pid + "/issues",
			map[string]any{"actor": "tester", "title": "x", "body": ""}},
		{"listIssues", http.MethodGet, "/api/v1/projects/" + pid + "/issues", nil},
		{"listAllIssues_byProject", http.MethodGet, "/api/v1/issues?project_id=" + pid, nil},
		{"pollProjectEvents", http.MethodGet, "/api/v1/projects/" + pid + "/events", nil},
		{"digestProject", http.MethodGet,
			"/api/v1/projects/" + pid + "/digest?since=" +
				url.QueryEscape(time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)), nil},
		{"readyIssues", http.MethodGet, "/api/v1/projects/" + pid + "/ready", nil},
		{"listLabels", http.MethodGet, "/api/v1/projects/" + pid + "/labels", nil},
		{"searchIssues", http.MethodGet, "/api/v1/projects/" + pid + "/search?q=anything", nil},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var bodyReader io.Reader
			if tc.body != nil {
				js, err := json.Marshal(tc.body)
				require.NoError(t, err)
				bodyReader = bytes.NewReader(js)
			}
			r, err := http.NewRequest(tc.method, ts.URL+tc.path, bodyReader)
			require.NoError(t, err)
			if tc.body != nil {
				r.Header.Set("Content-Type", "application/json")
			}
			resp, err := http.DefaultClient.Do(r) //nolint:gosec // test loopback
			require.NoError(t, err)
			defer func() { _ = resp.Body.Close() }()
			bs, _ := io.ReadAll(resp.Body)

			assert.Equal(t, http.StatusNotFound, resp.StatusCode, string(bs))
			assert.Contains(t, string(bs), "project_not_found", string(bs))
		})
	}
}

// TestListAllIssues_NegativeProjectIDReturns400 pins that
// GET /api/v1/issues?project_id=-1 is a validation error rather than
// silently falling through the > 0 guard and returning all projects'
// issues.
func TestListAllIssues_NegativeProjectIDReturns400(t *testing.T) {
	h, _ := bootstrapProject(t)
	ts := h.ts.(*httptest.Server)

	resp, err := http.Get(ts.URL + "/api/v1/issues?project_id=-1") //nolint:gosec,noctx // test loopback
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	bs, _ := io.ReadAll(resp.Body)

	assert.Equal(t, http.StatusBadRequest, resp.StatusCode, string(bs))
	assert.Contains(t, string(bs), "validation", string(bs))
	assert.Contains(t, string(bs), "project_id", string(bs))
}

// TestArchivedProject_SSEStreamRejects covers the SSE handler path that
// is wired through the raw mux instead of huma. An archived project_id
// must short-circuit to project_not_found before opening a stream.
func TestArchivedProject_SSEStreamRejects(t *testing.T) {
	h, projectID := bootstrapProject(t)
	ts := h.ts.(*httptest.Server)

	_, _, err := h.DB().RemoveProject(context.Background(), db.RemoveProjectParams{
		ProjectID: projectID, Actor: "tester",
	})
	require.NoError(t, err)

	pid := strconv.FormatInt(projectID, 10)
	r, err := http.NewRequest(http.MethodGet, ts.URL+"/api/v1/events/stream?project_id="+pid, nil)
	require.NoError(t, err)
	r.Header.Set("Accept", "text/event-stream")
	resp, err := http.DefaultClient.Do(r) //nolint:gosec // test loopback
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	bs, _ := io.ReadAll(resp.Body)

	assert.Equal(t, http.StatusNotFound, resp.StatusCode, string(bs))
	assert.Contains(t, string(bs), "project_not_found", string(bs))
}
