package daemon_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wesm/kata/internal/db"
	"github.com/wesm/kata/internal/testenv"
)

func seedProjectAndIssue(t *testing.T, env *testenv.Env) (db.Project, db.Issue) {
	t.Helper()
	p, err := env.DB.CreateProject(t.Context(), "mp")
	require.NoError(t, err)
	iss, _, err := env.DB.CreateIssue(t.Context(), db.CreateIssueParams{
		ProjectID: p.ID, Title: "x", Author: "tester",
	})
	require.NoError(t, err)
	return p, iss
}

func doPostWithIfMatch(t *testing.T, env *testenv.Env, url, body, ifMatch string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer tok")
	req.Header.Set("Content-Type", "application/json")
	if ifMatch != "" {
		req.Header.Set("If-Match", ifMatch)
	}
	resp, err := env.HTTP.Do(req) //nolint:gosec // G704: test server URL, not user-controlled
	require.NoError(t, err)
	return resp
}

func TestPatchIssueMetadata_HappyPath_200(t *testing.T) {
	env := testenv.New(t, testenv.WithAuthToken("tok"))
	p, iss := seedProjectAndIssue(t, env)

	url := fmt.Sprintf("%s/api/v1/projects/%d/issues/%s/metadata", env.URL, p.ID, iss.ShortID)
	ifMatch := fmt.Sprintf(`"rev-%d"`, iss.Revision)
	resp := doPostWithIfMatch(t, env, url, `{"actor":"tester","patch":{"scheduled_on":"2026-05-20"}}`, ifMatch)
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	require.Equalf(t, http.StatusOK, resp.StatusCode, "body: %s", raw)
	assert.Equal(t, fmt.Sprintf(`"rev-%d"`, iss.Revision+1), resp.Header.Get("ETag"))

	var out struct {
		Issue struct {
			Metadata string `json:"metadata"`
			Revision int64  `json:"revision"`
		} `json:"issue"`
		Changed bool `json:"changed"`
	}
	require.NoError(t, json.Unmarshal(raw, &out))
	assert.True(t, out.Changed)
	assert.Equal(t, iss.Revision+1, out.Issue.Revision)
	assert.Contains(t, out.Issue.Metadata, `"scheduled_on":"2026-05-20"`)
}

func TestPatchIssueMetadata_StaleIfMatch_412(t *testing.T) {
	env := testenv.New(t, testenv.WithAuthToken("tok"))
	p, iss := seedProjectAndIssue(t, env)

	url := fmt.Sprintf("%s/api/v1/projects/%d/issues/%s/metadata", env.URL, p.ID, iss.ShortID)
	resp := doPostWithIfMatch(t, env, url, `{"actor":"tester","patch":{"scheduled_on":"2026-05-20"}}`, `"rev-99"`)
	defer func() { _ = resp.Body.Close() }()
	assert.Equal(t, http.StatusPreconditionFailed, resp.StatusCode)
}

func TestPatchIssueMetadata_UnknownKey_400(t *testing.T) {
	env := testenv.New(t, testenv.WithAuthToken("tok"))
	p, iss := seedProjectAndIssue(t, env)

	url := fmt.Sprintf("%s/api/v1/projects/%d/issues/%s/metadata", env.URL, p.ID, iss.ShortID)
	ifMatch := fmt.Sprintf(`"rev-%d"`, iss.Revision)
	resp := doPostWithIfMatch(t, env, url, `{"actor":"tester","patch":{"banana":"yellow"}}`, ifMatch)
	defer func() { _ = resp.Body.Close() }()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestPatchIssueMetadata_MissingIfMatch_400(t *testing.T) {
	env := testenv.New(t, testenv.WithAuthToken("tok"))
	p, iss := seedProjectAndIssue(t, env)

	url := fmt.Sprintf("%s/api/v1/projects/%d/issues/%s/metadata", env.URL, p.ID, iss.ShortID)
	resp := doPostWithIfMatch(t, env, url, `{"actor":"tester","patch":{"scheduled_on":"2026-05-20"}}`, "")
	defer func() { _ = resp.Body.Close() }()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestPatchProjectMetadata_HappyPath_200(t *testing.T) {
	env := testenv.New(t, testenv.WithAuthToken("tok"))
	p, err := env.DB.CreateProject(t.Context(), "proj")
	require.NoError(t, err)

	url := fmt.Sprintf("%s/api/v1/projects/%d/metadata", env.URL, p.ID)
	ifMatch := fmt.Sprintf(`"rev-%d"`, p.Revision)
	resp := doPostWithIfMatch(t, env, url, `{"actor":"tester","patch":{"area":"Personal"}}`, ifMatch)
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	require.Equalf(t, http.StatusOK, resp.StatusCode, "body: %s", raw)
	assert.Equal(t, fmt.Sprintf(`"rev-%d"`, p.Revision+1), resp.Header.Get("ETag"))

	var out struct {
		Project struct {
			Metadata string `json:"metadata"`
			Revision int64  `json:"revision"`
		} `json:"project"`
		Changed bool `json:"changed"`
	}
	require.NoError(t, json.Unmarshal(raw, &out))
	assert.True(t, out.Changed)
	assert.Equal(t, p.Revision+1, out.Project.Revision)
	assert.Contains(t, out.Project.Metadata, `"area":"Personal"`)
}

func TestPatchProjectMetadata_StaleIfMatch_412(t *testing.T) {
	env := testenv.New(t, testenv.WithAuthToken("tok"))
	p, err := env.DB.CreateProject(t.Context(), "proj2")
	require.NoError(t, err)

	url := fmt.Sprintf("%s/api/v1/projects/%d/metadata", env.URL, p.ID)
	resp := doPostWithIfMatch(t, env, url, `{"actor":"tester","patch":{"area":"Work"}}`, `"rev-99"`)
	defer func() { _ = resp.Body.Close() }()
	assert.Equal(t, http.StatusPreconditionFailed, resp.StatusCode)
}

func TestPatchProjectMetadata_UnknownKey_400(t *testing.T) {
	env := testenv.New(t, testenv.WithAuthToken("tok"))
	p, err := env.DB.CreateProject(t.Context(), "proj3")
	require.NoError(t, err)

	url := fmt.Sprintf("%s/api/v1/projects/%d/metadata", env.URL, p.ID)
	ifMatch := fmt.Sprintf(`"rev-%d"`, p.Revision)
	resp := doPostWithIfMatch(t, env, url, `{"actor":"tester","patch":{"banana":"yellow"}}`, ifMatch)
	defer func() { _ = resp.Body.Close() }()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

// TestPatchIssueMetadata_InvalidValueOnKnownKey_400 covers the validator path
// where the key is registered but the value fails type-specific validation.
// Validator errors must wrap metadata.ErrInvalidValue so the handler maps
// them to 400 (not 500).
func TestPatchIssueMetadata_InvalidValueOnKnownKey_400(t *testing.T) {
	env := testenv.New(t, testenv.WithAuthToken("tok"))
	p, iss := seedProjectAndIssue(t, env)

	url := fmt.Sprintf("%s/api/v1/projects/%d/issues/%s/metadata", env.URL, p.ID, iss.ShortID)
	ifMatch := fmt.Sprintf(`"rev-%d"`, iss.Revision)
	// "scheduled_on" is registered, but 123 is not a JSON string in YYYY-MM-DD.
	resp := doPostWithIfMatch(t, env, url, `{"actor":"tester","patch":{"scheduled_on":123}}`, ifMatch)
	defer func() { _ = resp.Body.Close() }()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

// TestPatchProjectMetadata_InvalidValueOnKnownKey_400 mirrors the issue test
// for project metadata: a value with the wrong JSON type must produce 400.
func TestPatchProjectMetadata_InvalidValueOnKnownKey_400(t *testing.T) {
	env := testenv.New(t, testenv.WithAuthToken("tok"))
	p, err := env.DB.CreateProject(t.Context(), "proj4")
	require.NoError(t, err)

	url := fmt.Sprintf("%s/api/v1/projects/%d/metadata", env.URL, p.ID)
	ifMatch := fmt.Sprintf(`"rev-%d"`, p.Revision)
	// "area" is TypeString — a number must be rejected as 400.
	resp := doPostWithIfMatch(t, env, url, `{"actor":"tester","patch":{"area":123}}`, ifMatch)
	defer func() { _ = resp.Body.Close() }()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}
