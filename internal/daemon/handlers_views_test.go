package daemon_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wesm/kata/internal/daemon"
	"github.com/wesm/kata/internal/db"
	"github.com/wesm/kata/internal/testenv"
)

// seedIssueWithMetadataHTTP creates an issue and overwrites its metadata JSON
// directly via the DB handle. Mirrors the seedIssueWithMetadata helper used in
// the db package's view tests so view handler tests can pre-set scheduled_on,
// deadline_on, and someday flags without round-tripping through the metadata
// patch endpoint.
func seedIssueWithMetadataHTTP(t *testing.T, env *testenv.Env, projectID int64, title, metadataJSON string) db.Issue {
	t.Helper()
	iss, _, err := env.DB.CreateIssue(context.Background(), db.CreateIssueParams{
		ProjectID: projectID, Title: title, Author: "tester",
	})
	require.NoError(t, err)
	_, err = env.DB.ExecContext(context.Background(),
		`UPDATE issues SET metadata = ? WHERE id = ?`, metadataJSON, iss.ID)
	require.NoError(t, err)
	return iss
}

// doGetView issues GET /api/v1/issues with the given query string and
// X-Kata-Client-TZ header. Returns the response paired with the buffered body.
func doGetView(t *testing.T, env *testenv.Env, query, clientTZ string) (*http.Response, []byte) {
	t.Helper()
	url := env.URL + "/api/v1/issues"
	if query != "" {
		url += "?" + query
	}
	req, err := http.NewRequest(http.MethodGet, url, nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer tok")
	if clientTZ != "" {
		req.Header.Set("X-Kata-Client-TZ", clientTZ)
	}
	resp, err := env.HTTP.Do(req) //nolint:gosec // test request to loopback URL
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	bs, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp, bs
}

// viewResponse decodes the {"issues": [...]} envelope.
type viewResponse struct {
	Issues []db.Issue `json:"issues"`
}

// dateOffset returns time.Now() shifted by n days in UTC formatted as YYYY-MM-DD.
func dateOffset(n int) string {
	return time.Now().UTC().AddDate(0, 0, n).Format("2006-01-02")
}

// TestGetIssuesView_Today_IncludesPastAndCurrent seeds yesterday/today/tomorrow
// in a project, then verifies the "today" view returns only the past and
// current scheduled issues.
func TestGetIssuesView_Today_IncludesPastAndCurrent(t *testing.T) {
	env := testenv.New(t, testenv.WithAuthToken("tok"))
	p, err := env.DB.CreateProject(t.Context(), "work")
	require.NoError(t, err)

	seedIssueWithMetadataHTTP(t, env, p.ID, "Yesterday",
		fmt.Sprintf(`{"scheduled_on":%q}`, dateOffset(-1)))
	seedIssueWithMetadataHTTP(t, env, p.ID, "Today",
		fmt.Sprintf(`{"scheduled_on":%q}`, dateOffset(0)))
	seedIssueWithMetadataHTTP(t, env, p.ID, "Tomorrow",
		fmt.Sprintf(`{"scheduled_on":%q}`, dateOffset(1)))

	resp, bs := doGetView(t, env, "view=today", "UTC")
	require.Equalf(t, http.StatusOK, resp.StatusCode, "body: %s", bs)

	var out viewResponse
	require.NoError(t, json.Unmarshal(bs, &out))

	titles := make([]string, 0, len(out.Issues))
	for _, iss := range out.Issues {
		titles = append(titles, iss.Title)
	}
	assert.ElementsMatch(t, []string{"Yesterday", "Today"}, titles,
		"today view must include past+current scheduled issues, not future")
}

// TestGetIssuesView_Upcoming_StrictlyFuture seeds today + tomorrow and
// verifies the "upcoming" view returns only the future-scheduled issue.
func TestGetIssuesView_Upcoming_StrictlyFuture(t *testing.T) {
	env := testenv.New(t, testenv.WithAuthToken("tok"))
	p, err := env.DB.CreateProject(t.Context(), "work")
	require.NoError(t, err)

	seedIssueWithMetadataHTTP(t, env, p.ID, "Today",
		fmt.Sprintf(`{"scheduled_on":%q}`, dateOffset(0)))
	seedIssueWithMetadataHTTP(t, env, p.ID, "Tomorrow",
		fmt.Sprintf(`{"scheduled_on":%q}`, dateOffset(1)))

	resp, bs := doGetView(t, env, "view=upcoming", "UTC")
	require.Equalf(t, http.StatusOK, resp.StatusCode, "body: %s", bs)

	var out viewResponse
	require.NoError(t, json.Unmarshal(bs, &out))

	require.Len(t, out.Issues, 1)
	assert.Equal(t, "Tomorrow", out.Issues[0].Title)
}

// TestGetIssuesView_Inbox_ReturnsSentinelProject verifies the handler invokes
// EnsureInbox so an issue created against the Inbox project surfaces in the
// "inbox" view without the caller having to know its ID.
func TestGetIssuesView_Inbox_ReturnsSentinelProject(t *testing.T) {
	env := testenv.New(t, testenv.WithAuthToken("tok"))

	// First request bootstraps Inbox via EnsureInbox; from then on we can
	// look it up by name to seed against it.
	resp, _ := doGetView(t, env, "view=inbox", "UTC")
	require.Equal(t, http.StatusOK, resp.StatusCode)

	inbox, err := env.DB.ProjectByName(t.Context(), daemon.InboxProjectName)
	require.NoError(t, err)

	seedIssueWithMetadataHTTP(t, env, inbox.ID, "Triage me", `{}`)

	resp, bs := doGetView(t, env, "view=inbox", "UTC")
	require.Equalf(t, http.StatusOK, resp.StatusCode, "body: %s", bs)

	var out viewResponse
	require.NoError(t, json.Unmarshal(bs, &out))
	require.Len(t, out.Issues, 1)
	assert.Equal(t, "Triage me", out.Issues[0].Title)
	assert.Equal(t, inbox.ID, out.Issues[0].ProjectID)
}

// TestGetIssuesView_UnknownView_400 rejects views outside the documented set
// with a 400 envelope.
func TestGetIssuesView_UnknownView_400(t *testing.T) {
	env := testenv.New(t, testenv.WithAuthToken("tok"))

	resp, bs := doGetView(t, env, "view=banana", "UTC")
	assertAPIError(t, resp.StatusCode, bs, http.StatusBadRequest, "invalid_view")
}

// TestGetIssuesView_InvalidTZ_400 rejects an IANA timezone the runtime can't
// load with a 400 envelope (not 500).
func TestGetIssuesView_InvalidTZ_400(t *testing.T) {
	env := testenv.New(t, testenv.WithAuthToken("tok"))

	resp, bs := doGetView(t, env, "view=today", "Mars/Phobos")
	assertAPIError(t, resp.StatusCode, bs, http.StatusBadRequest, "invalid_tz")
}

// TestGetIssuesView_ProjectFilter_FiltersByProject seeds two projects each
// with one "anytime" issue (no scheduled_on, not someday, outside inbox) and
// verifies that ?project_id=<A> returns only project A's issue.
func TestGetIssuesView_ProjectFilter_FiltersByProject(t *testing.T) {
	env := testenv.New(t, testenv.WithAuthToken("tok"))

	pA, err := env.DB.CreateProject(t.Context(), "alpha")
	require.NoError(t, err)
	pB, err := env.DB.CreateProject(t.Context(), "beta")
	require.NoError(t, err)

	seedIssueWithMetadataHTTP(t, env, pA.ID, "AlphaTask", `{}`)
	seedIssueWithMetadataHTTP(t, env, pB.ID, "BetaTask", `{}`)

	resp, bs := doGetView(t, env,
		fmt.Sprintf("view=anytime&project_id=%d", pA.ID), "UTC")
	require.Equalf(t, http.StatusOK, resp.StatusCode, "body: %s", bs)

	var out viewResponse
	require.NoError(t, json.Unmarshal(bs, &out))
	require.Len(t, out.Issues, 1)
	assert.Equal(t, "AlphaTask", out.Issues[0].Title)
	assert.Equal(t, pA.ID, out.Issues[0].ProjectID)
}
