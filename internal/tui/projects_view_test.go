package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestProjectsView_RendersWithoutPanic confirms the view renders a
// non-empty frame for a model in viewProjects state, even with no
// projects loaded yet. Required for boot landing where the fetch is
// still in flight.
func TestProjectsView_RendersWithoutPanic(t *testing.T) {
	m := initialModel(Options{})
	m.view = viewProjects
	m.width = 80
	m.height = 24

	out := m.View()
	if out == "" {
		t.Fatal("viewProjects must render a non-empty frame")
	}
	if !strings.Contains(out, "projects") {
		t.Errorf("expected 'projects' in viewProjects output:\n%s", out)
	}
}

// TestProjectsRows_SentinelSumsAndPinsFirst pins spec §1.6: the All-
// projects sentinel row's Open/Closed are the sum of per-row counts and
// LastEventAt is the row-max. The sentinel is always at index 0.
func TestProjectsRows_SentinelSumsAndPinsFirst(t *testing.T) {
	t1 := time.Date(2026, 5, 4, 10, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC) // newer
	byID := map[int64]string{1: "kata", 2: "roborev", 3: "msgvault"}
	idents := map[int64]string{1: "github.com/wesm/kata", 2: "...", 3: "..."}
	stats := map[int64]ProjectStatsSummary{
		1: {Open: 5, Closed: 2, LastEventAt: &t2},
		2: {Open: 3, Closed: 1, LastEventAt: &t1},
		3: {Open: 0, Closed: 0, LastEventAt: nil},
	}
	rows := projectsRows(byID, idents, stats)
	require.Len(t, rows, 4) // sentinel + 3 projects
	assert.True(t, rows[0].sentinel, "row 0 must be the sentinel")
	assert.Equal(t, 8, rows[0].stats.Open, "sentinel open = 5+3+0")
	assert.Equal(t, 3, rows[0].stats.Closed, "sentinel closed = 2+1+0")
	require.NotNil(t, rows[0].stats.LastEventAt)
	assert.True(t, rows[0].stats.LastEventAt.Equal(t2), "sentinel last = max(t1, t2) = t2")
}

// TestProjectsRows_SortByLastEventDesc pins spec §5.3: rows after the
// sentinel are sorted by last_event_at desc with name asc as the
// tiebreak. A row with no events sinks to the bottom.
func TestProjectsRows_SortByLastEventDesc(t *testing.T) {
	t1 := time.Date(2026, 5, 4, 10, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC)
	byID := map[int64]string{1: "older", 2: "newer", 3: "noevents"}
	idents := map[int64]string{1: "...", 2: "...", 3: "..."}
	stats := map[int64]ProjectStatsSummary{
		1: {LastEventAt: &t1},
		2: {LastEventAt: &t2},
		3: {LastEventAt: nil},
	}
	rows := projectsRows(byID, idents, stats)
	assert.True(t, rows[0].sentinel)
	assert.Equal(t, "newer", rows[1].name)
	assert.Equal(t, "older", rows[2].name)
	assert.Equal(t, "noevents", rows[3].name)
}

// TestProjectsView_RendersTable confirms the table renders with the
// expected column headers and row content for a fixture model. Wide
// terminal so all columns fit.
func TestProjectsView_RendersTable(t *testing.T) {
	t1 := time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC)
	m := initialModel(Options{})
	m.view = viewProjects
	m.width = 120
	m.height = 24
	m.projectsByID = map[int64]string{1: "kata", 2: "roborev"}
	m.projectIdentByID = map[int64]string{1: "github.com/wesm/kata", 2: "github.com/wesm/roborev"}
	m.projectStats = map[int64]ProjectStatsSummary{
		1: {Open: 12, Closed: 3, LastEventAt: &t1},
		2: {Open: 7, Closed: 2, LastEventAt: &t1},
	}

	out := m.View()
	for _, want := range []string{
		"kata / projects", "Project", "Open", "Closed", "Total", "Updated",
		"All projects", "kata", "roborev",
	} {
		assert.Contains(t, out, want, "missing %q in viewProjects output", want)
	}
}

// TestProjectsView_DashWhenNoEvents pins spec §6.1: a row with
// LastEventAt=nil renders "—" in the Updated column.
func TestProjectsView_DashWhenNoEvents(t *testing.T) {
	m := initialModel(Options{})
	m.view = viewProjects
	m.width = 120
	m.height = 24
	m.projectsByID = map[int64]string{1: "fresh"}
	m.projectIdentByID = map[int64]string{1: "github.com/wesm/fresh"}
	m.projectStats = map[int64]ProjectStatsSummary{
		1: {Open: 0, Closed: 0, LastEventAt: nil},
	}
	out := m.View()
	assert.Contains(t, out, "—", "nil LastEventAt must render as em-dash")
}

// TestProjectsView_IdentityFooterOnHighlight pins spec §5.1:
// highlighting a real project renders its identity URL beneath the
// table; highlighting the sentinel renders the description.
func TestProjectsView_IdentityFooterOnHighlight(t *testing.T) {
	m := initialModel(Options{})
	m.view = viewProjects
	m.width = 120
	m.height = 24
	m.projectsByID = map[int64]string{1: "kata"}
	m.projectIdentByID = map[int64]string{1: "github.com/wesm/kata"}
	m.projectStats = map[int64]ProjectStatsSummary{1: {}}

	m.projectsCursor = 0 // sentinel row
	out := m.View()
	assert.Contains(t, out, "issue queue across every registered project")

	m.projectsCursor = 1 // kata row
	out = m.View()
	assert.Contains(t, out, "identity: github.com/wesm/kata")
}
