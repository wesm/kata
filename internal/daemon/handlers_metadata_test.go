package daemon_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wesm/kata/internal/db"
	"github.com/wesm/kata/internal/testenv"
)

func seedProjectAndIssue(t *testing.T, env *testenv.Env) (db.Project, db.Issue) {
	t.Helper()
	p := seedProject(t, env, "mp")
	iss, _, err := env.DB.CreateIssue(t.Context(), db.CreateIssueParams{
		ProjectID: p.ID, Title: "x", Author: "tester",
	})
	require.NoError(t, err)
	return p, iss
}

// metadataSubject parameterises the issue + project metadata tests over a
// common setup that returns the patch URL and starting revision for the
// subject entity. The PATCH wire shape is identical across both subjects;
// envKey is the top-level response JSON key ("issue" or "project") under
// which the entity's metadata and revision are nested so the test body
// can decode both response shapes without hand-rolling per-subject structs.
type metadataSubject struct {
	name   string
	envKey string
	setup  func(t *testing.T, env *testenv.Env) (url string, rev int64)
}

func metadataSubjects() []metadataSubject {
	return []metadataSubject{
		{
			"issue", "issue",
			func(t *testing.T, env *testenv.Env) (string, int64) {
				p, iss := seedProjectAndIssue(t, env)
				return fmt.Sprintf("%s/api/v1/projects/%d/issues/%s/metadata",
					env.URL, p.ID, iss.ShortID), iss.Revision
			},
		},
		{
			"project", "project",
			func(t *testing.T, env *testenv.Env) (string, int64) {
				p := seedProject(t, env, "proj")
				return fmt.Sprintf("%s/api/v1/projects/%d/metadata",
					env.URL, p.ID), p.Revision
			},
		},
	}
}

// decodeMetadataEnvelope pulls the {metadata, revision} pair from the
// subject-specific envelope key in a PATCH response body. The metadata blob
// is stored as a JSON string, so this returns the decoded inner string ready
// for substring assertions.
func decodeMetadataEnvelope(t *testing.T, raw []byte, envKey string) (metadata string, rev int64) {
	t.Helper()
	var envelope map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(raw, &envelope))
	var inner struct {
		Metadata string `json:"metadata"`
		Revision int64  `json:"revision"`
	}
	require.NoError(t, json.Unmarshal(envelope[envKey], &inner))
	return inner.Metadata, inner.Revision
}

// TestPatchMetadata_HappyPath_200 covers the happy-path PATCH for both the
// issue and project metadata endpoints. Each subject supplies its own URL,
// starting revision, a registered key/value to patch, and the substring to
// assert in the persisted metadata blob.
func TestPatchMetadata_HappyPath_200(t *testing.T) {
	cases := []struct {
		subject metadataSubject
		patch   string // value inside `"patch": {...}`
		expect  string // substring required in the decoded metadata blob
	}{
		{metadataSubjects()[0], `{"scheduled_on":"2026-05-20"}`, `"scheduled_on":"2026-05-20"`},
		{metadataSubjects()[1], `{"area":"Personal"}`, `"area":"Personal"`},
	}
	for _, c := range cases {
		t.Run(c.subject.name, func(t *testing.T) {
			env := testenv.New(t, testenv.WithAuthToken("tok"))
			url, rev := c.subject.setup(t, env)
			ifMatch := fmt.Sprintf(`"rev-%d"`, rev)
			body := fmt.Sprintf(`{"actor":"tester","patch":%s}`, c.patch)
			resp := doPostWithIfMatch(t, env, url, body, ifMatch)
			defer func() { _ = resp.Body.Close() }()
			raw, _ := io.ReadAll(resp.Body)
			require.Equalf(t, http.StatusOK, resp.StatusCode, "body: %s", raw)
			assert.Equal(t, fmt.Sprintf(`"rev-%d"`, rev+1), resp.Header.Get("ETag"))

			metadata, newRev := decodeMetadataEnvelope(t, raw, c.subject.envKey)
			assert.Equal(t, rev+1, newRev)
			assert.Contains(t, metadata, c.expect)
			assert.Contains(t, string(raw), `"changed":true`)
		})
	}
}

// TestPatchMetadata_NullTopLevelPatchRejected pins the wire contract for the
// patch field on both subjects: it must be a JSON object, never null. The
// Huma schema validator rejects `"patch":null` upfront with 400 — this test
// locks that behavior so a future tri-state refactor cannot accidentally
// loosen the schema and let null reach the handler (where a nil map would
// silently no-op). Per-key delete is `{"k":null}` inside the object; null at
// the top level is always invalid.
func TestPatchMetadata_NullTopLevelPatchRejected(t *testing.T) {
	for _, subject := range metadataSubjects() {
		t.Run(subject.name, func(t *testing.T) {
			env := testenv.New(t, testenv.WithAuthToken("tok"))
			url, rev := subject.setup(t, env)
			ifMatch := fmt.Sprintf(`"rev-%d"`, rev)
			resp := doPostWithIfMatch(t, env, url, `{"actor":"tester","patch":null}`, ifMatch)
			defer func() { _ = resp.Body.Close() }()
			assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
		})
	}
}

// TestPatchMetadata_StaleIfMatch_412 covers stale If-Match for both subjects.
func TestPatchMetadata_StaleIfMatch_412(t *testing.T) {
	cases := []struct {
		subject metadataSubject
		patch   string
	}{
		{metadataSubjects()[0], `{"scheduled_on":"2026-05-20"}`},
		{metadataSubjects()[1], `{"area":"Work"}`},
	}
	for _, c := range cases {
		t.Run(c.subject.name, func(t *testing.T) {
			env := testenv.New(t, testenv.WithAuthToken("tok"))
			url, _ := c.subject.setup(t, env)
			body := fmt.Sprintf(`{"actor":"tester","patch":%s}`, c.patch)
			resp := doPostWithIfMatch(t, env, url, body, `"rev-99"`)
			defer func() { _ = resp.Body.Close() }()
			assert.Equal(t, http.StatusPreconditionFailed, resp.StatusCode)
		})
	}
}

// TestPatchMetadata_InvalidValueOnKnownKey_400 covers the validator path for
// both subjects: a registered key with a wrong JSON type must produce a 400
// envelope, not a 500.
func TestPatchMetadata_InvalidValueOnKnownKey_400(t *testing.T) {
	cases := []struct {
		subject metadataSubject
		patch   string // registered key with a value of the wrong JSON type
	}{
		{metadataSubjects()[0], `{"scheduled_on":123}`}, // scheduled_on expects a date string
		{metadataSubjects()[1], `{"area":123}`},         // area is TypeString
	}
	for _, c := range cases {
		t.Run(c.subject.name, func(t *testing.T) {
			env := testenv.New(t, testenv.WithAuthToken("tok"))
			url, rev := c.subject.setup(t, env)
			ifMatch := fmt.Sprintf(`"rev-%d"`, rev)
			body := fmt.Sprintf(`{"actor":"tester","patch":%s}`, c.patch)
			resp := doPostWithIfMatch(t, env, url, body, ifMatch)
			defer func() { _ = resp.Body.Close() }()
			assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
		})
	}
}

// TestPatchIssueMetadata_UnknownKey_Accepted: the daemon doesn't enforce a
// closed metadata schema. Unknown keys are written through as opaque values
// so consumers can carry their own UI hints without daemon releases.
func TestPatchIssueMetadata_UnknownKey_Accepted(t *testing.T) {
	env := testenv.New(t, testenv.WithAuthToken("tok"))
	p, iss := seedProjectAndIssue(t, env)

	url := fmt.Sprintf("%s/api/v1/projects/%d/issues/%s/metadata", env.URL, p.ID, iss.ShortID)
	ifMatch := fmt.Sprintf(`"rev-%d"`, iss.Revision)
	resp := doPostWithIfMatch(t, env, url, `{"actor":"tester","patch":{"definitely_not_a_key":"yellow"}}`, ifMatch)
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
	assert.Contains(t, out.Issue.Metadata, `"definitely_not_a_key":"yellow"`,
		"unknown key must round-trip into the persisted metadata blob")

	// GET-after view confirms the key is durably stored and surfaced.
	getReq, err := http.NewRequest(http.MethodGet, url[:len(url)-len("/metadata")], nil)
	require.NoError(t, err)
	getReq.Header.Set("Authorization", "Bearer tok")
	getResp, err := env.HTTP.Do(getReq) //nolint:gosec // G704: test server URL, not user-controlled
	require.NoError(t, err)
	defer func() { _ = getResp.Body.Close() }()
	getBody, _ := io.ReadAll(getResp.Body)
	require.Equalf(t, http.StatusOK, getResp.StatusCode, "GET body: %s", getBody)

	var view struct {
		Issue struct {
			Metadata string `json:"metadata"`
		} `json:"issue"`
	}
	require.NoError(t, json.Unmarshal(getBody, &view))
	assert.Contains(t, view.Issue.Metadata, `"definitely_not_a_key":"yellow"`,
		"GET-after must surface the opaque key alongside the reserved ones")
}

func TestPatchIssueMetadata_MissingIfMatch_400(t *testing.T) {
	env := testenv.New(t, testenv.WithAuthToken("tok"))
	p, iss := seedProjectAndIssue(t, env)

	url := fmt.Sprintf("%s/api/v1/projects/%d/issues/%s/metadata", env.URL, p.ID, iss.ShortID)
	resp := doPostWithIfMatch(t, env, url, `{"actor":"tester","patch":{"scheduled_on":"2026-05-20"}}`, "")
	defer func() { _ = resp.Body.Close() }()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

// TestPatchProjectMetadata_UnknownKey_Accepted mirrors the issue test: the
// project metadata blob also accepts unknown keys opaquely. The ShowProject
// wire shape does not project the metadata blob, so durable persistence is
// verified by re-reading the project row directly.
func TestPatchProjectMetadata_UnknownKey_Accepted(t *testing.T) {
	env := testenv.New(t, testenv.WithAuthToken("tok"))
	p, err := env.DB.CreateProject(t.Context(), "proj3")
	require.NoError(t, err)

	url := fmt.Sprintf("%s/api/v1/projects/%d/metadata", env.URL, p.ID)
	ifMatch := fmt.Sprintf(`"rev-%d"`, p.Revision)
	resp := doPostWithIfMatch(t, env, url, `{"actor":"tester","patch":{"definitely_not_a_key":"yellow"}}`, ifMatch)
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
	assert.Contains(t, out.Project.Metadata, `"definitely_not_a_key":"yellow"`,
		"unknown key must round-trip into the persisted metadata blob")

	// DB-side check: confirm the key is durably stored (the ShowProject wire
	// shape doesn't expose the metadata blob, so we re-read the row).
	stored, err := env.DB.ProjectByID(t.Context(), p.ID)
	require.NoError(t, err)
	assert.Contains(t, string(stored.Metadata), `"definitely_not_a_key":"yellow"`,
		"opaque key must survive a fresh DB read")
}
