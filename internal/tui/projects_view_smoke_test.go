package tui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSmoke_ProjectsViewLoop covers the end-to-end happy path of
// spec §8.7: an unbound cwd lands on viewProjects, the table is
// ordered by last_event_at desc, Enter on the sentinel transitions to
// viewList in all-projects scope, and P from viewList returns to
// viewProjects.
func TestSmoke_ProjectsViewLoop(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/projects/resolve":
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status": 404,
				"error":  map[string]any{"code": "project_not_initialized"},
			})
		case "/api/v1/projects":
			require.Equal(t, "stats", r.URL.Query().Get("include"))
			_, _ = w.Write([]byte(`{"projects":[
                {"id":1,"identity":"github.com/wesm/proj-a","name":"proj-a",
                 "stats":{"open":2,"closed":1,"last_event_at":"2026-05-04T10:00:00.000Z"}},
                {"id":2,"identity":"github.com/wesm/proj-b","name":"proj-b",
                 "stats":{"open":5,"closed":2,"last_event_at":"2026-05-04T12:00:00.000Z"}},
                {"id":3,"identity":"github.com/wesm/proj-c","name":"proj-c",
                 "stats":{"open":10,"closed":3,"last_event_at":"2026-05-04T11:00:00.000Z"}}
            ]}`))
		case "/api/v1/issues":
			// Cross-project list — the all-projects drill-in fetches from here.
			_, _ = w.Write([]byte(`{"issues":[]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c := NewClient(srv.URL, srv.Client())

	// 1. Boot resolves to viewProjects.
	bi, err := bootResolveScope(t.Context(), c, "/tmp/unbound")
	require.NoError(t, err)
	require.Equal(t, viewProjects, bi.view)

	// 2. Build the model and seed the projects cache from boot's
	//    pre-fetched rows (matches buildRunModel).
	m := initialModel(Options{})
	m.api = c
	m.scope = bi.scope
	m.view = bi.view
	m.projectsByID = map[int64]string{}
	m.projectIdentByID = map[int64]string{}
	m.projectStats = map[int64]ProjectStatsSummary{}
	for _, r := range bi.projects {
		m.projectsByID[r.ID] = r.Name
		m.projectIdentByID[r.ID] = r.Identity
		if r.Stats != nil {
			m.projectStats[r.ID] = *r.Stats
		}
	}

	// 3. Rows ordered by last_event_at desc: proj-b (12:00), proj-c (11:00), proj-a (10:00).
	rows := projectsRows(m.projectsByID, m.projectIdentByID, m.projectStats)
	require.Len(t, rows, 4)
	assert.True(t, rows[0].sentinel)
	assert.Equal(t, "proj-b", rows[1].name)
	assert.Equal(t, "proj-c", rows[2].name)
	assert.Equal(t, "proj-a", rows[3].name)

	// 4. Enter on the sentinel transitions to viewList in all-projects scope.
	m.projectsCursor = 0
	nextModel, cmd := m.routeProjectsViewKey(tea.KeyMsg{Type: tea.KeyEnter})
	assert.Equal(t, viewList, nextModel.view)
	assert.True(t, nextModel.scope.allProjects)
	require.NotNil(t, cmd, "drill-in must dispatch fetchInitial")

	// 5. P from the resulting viewList returns to viewProjects with scope preserved.
	out2, cmd2 := nextModel.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'P'}})
	nm2 := out2.(Model)
	assert.Equal(t, viewProjects, nm2.view)
	assert.True(t, nm2.scope.allProjects, "scope preserved on P-back")
	require.NotNil(t, cmd2, "P must dispatch fetchProjectsWithStats")
}
