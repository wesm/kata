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

func seedProject(t *testing.T, env *testenv.Env, name string) db.Project {
	t.Helper()
	p, err := env.DB.CreateProject(t.Context(), name)
	require.NoError(t, err)
	return p
}

func seedRecurrence(t *testing.T, env *testenv.Env, projectID int64, rule, dtstart, tz, title string) db.Recurrence {
	t.Helper()
	rec, err := env.DB.CreateRecurrence(t.Context(), db.CreateRecurrenceIn{
		ProjectID: projectID, Actor: "tester",
		Rule: rule, DTStart: dtstart, Timezone: tz,
		Template: db.RecurrenceTemplate{Title: title},
	})
	require.NoError(t, err)
	return rec
}

func TestPostRecurrence_HappyPath(t *testing.T) {
	env := testenv.New(t, testenv.WithAuthToken("tok"))
	p := seedProject(t, env, "src")

	body := `{
		"actor":"tester",
		"rrule":"FREQ=WEEKLY",
		"dtstart":"2026-05-15",
		"timezone":"America/New_York",
		"template":{"title":"Weekly review"}
	}`
	req, err := http.NewRequest(http.MethodPost,
		fmt.Sprintf("%s/api/v1/projects/%d/recurrences", env.URL, p.ID),
		strings.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer tok")
	req.Header.Set("Content-Type", "application/json")

	resp, err := env.HTTP.Do(req) //nolint:gosec // G704: test server URL, not user-controlled
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	require.Equalf(t, http.StatusCreated, resp.StatusCode, "body: %s", raw)

	var out struct {
		Recurrence struct {
			UID string `json:"uid"`
		} `json:"recurrence"`
	}
	require.NoError(t, json.Unmarshal(raw, &out))
	assert.Len(t, out.Recurrence.UID, 26)
}

func TestPatchRecurrence_RequiresIfMatch(t *testing.T) {
	env := testenv.New(t, testenv.WithAuthToken("tok"))
	p := seedProject(t, env, "src")
	rec := seedRecurrence(t, env, p.ID, "FREQ=WEEKLY", "2026-05-15", "UTC", "old")

	req, err := http.NewRequest(http.MethodPatch,
		fmt.Sprintf("%s/api/v1/projects/%d/recurrences/%s", env.URL, p.ID, rec.UID),
		strings.NewReader(`{"actor":"tester","template":{"title":"new"}}`))
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer tok")
	req.Header.Set("Content-Type", "application/json")
	// No If-Match header — handler must reject with 400.

	resp, err := env.HTTP.Do(req) //nolint:gosec // G704: test server URL, not user-controlled
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestPatchRecurrence_HappyPathReturnsNewETag(t *testing.T) {
	env := testenv.New(t, testenv.WithAuthToken("tok"))
	p := seedProject(t, env, "src")
	rec := seedRecurrence(t, env, p.ID, "FREQ=WEEKLY", "2026-05-15", "UTC", "old")

	req, err := http.NewRequest(http.MethodPatch,
		fmt.Sprintf("%s/api/v1/projects/%d/recurrences/%s", env.URL, p.ID, rec.UID),
		strings.NewReader(`{"actor":"tester","template":{"title":"new"}}`))
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer tok")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("If-Match", `"rev-1"`)

	resp, err := env.HTTP.Do(req) //nolint:gosec // G704: test server URL, not user-controlled
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, `"rev-2"`, resp.Header.Get("ETag"))
}

func TestDeleteRecurrence_SoftDeletes(t *testing.T) {
	env := testenv.New(t, testenv.WithAuthToken("tok"))
	p := seedProject(t, env, "src")
	rec := seedRecurrence(t, env, p.ID, "FREQ=WEEKLY", "2026-05-15", "UTC", "x")

	req, err := http.NewRequest(http.MethodDelete,
		fmt.Sprintf("%s/api/v1/projects/%d/recurrences/%s?actor=tester", env.URL, p.ID, rec.UID), nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer tok")

	resp, err := env.HTTP.Do(req) //nolint:gosec // G704: test server URL, not user-controlled
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	assert.Equal(t, http.StatusNoContent, resp.StatusCode)

	// Listing should be empty now.
	list, err := env.DB.ListRecurrencesByProject(t.Context(), p.ID)
	require.NoError(t, err)
	assert.Empty(t, list)
}

func TestListRecurrences_ByProject(t *testing.T) {
	env := testenv.New(t, testenv.WithAuthToken("tok"))
	p := seedProject(t, env, "src")
	_ = seedRecurrence(t, env, p.ID, "FREQ=WEEKLY", "2026-05-15", "UTC", "a")
	_ = seedRecurrence(t, env, p.ID, "FREQ=MONTHLY", "2026-05-01", "UTC", "b")

	req, err := http.NewRequest(http.MethodGet,
		fmt.Sprintf("%s/api/v1/projects/%d/recurrences", env.URL, p.ID), nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer tok")

	resp, err := env.HTTP.Do(req) //nolint:gosec // G704: test server URL, not user-controlled
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var out struct {
		Recurrences []db.Recurrence `json:"recurrences"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	assert.Len(t, out.Recurrences, 2)
}
