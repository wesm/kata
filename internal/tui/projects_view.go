package tui

import (
	"context"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// fetchProjectsWithStats issues GET /api/v1/projects?include=stats and
// dispatches a projectsLoadedMsg carrying the stats map. The boot
// fetchProjects cmd is the no-stats variant used by the list view's
// project-name cache; this cmd is dispatched by every transition into
// viewProjects per spec §6.2.
//
// Failures populate err so the message handler can surface a toast
// without leaving the table empty.
func (m Model) fetchProjectsWithStats() tea.Cmd {
	api := m.api
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		rows, err := api.ListProjectsWithStats(ctx)
		if err != nil {
			return projectsLoadedMsg{err: err}
		}
		names := make(map[int64]string, len(rows))
		idents := make(map[int64]string, len(rows))
		stats := make(map[int64]ProjectStatsSummary, len(rows))
		for _, r := range rows {
			names[r.ID] = r.Name
			idents[r.ID] = r.Identity
			if r.Stats != nil {
				stats[r.ID] = *r.Stats
			}
		}
		return projectsLoadedMsg{projects: names, idents: idents, stats: stats}
	}
}

// projectsRow is one row of the projects view. Sentinel=true marks the
// implicit "All projects" entry at index 0; otherwise the row carries a
// concrete projectID.
type projectsRow struct {
	sentinel  bool
	projectID int64
	name      string
	identity  string
	stats     ProjectStatsSummary
}

// projectsRows builds the row list rendered by viewProjects. The
// sentinel row is always at index 0; remaining rows are sorted by
// last_event_at desc with name asc as the tiebreak. Spec §5.3.
//
// The sentinel's totals are client-summed from the per-row stats (spec
// §1.6) so the "All projects" Open/Closed/Total are guaranteed
// consistent with the rows on the same frame, and last_event_at is the
// max across rows.
func projectsRows(byID map[int64]string, identByID map[int64]string, stats map[int64]ProjectStatsSummary) []projectsRow {
	rows := []projectsRow{}
	for id, name := range byID {
		rows = append(rows, projectsRow{
			projectID: id,
			name:      name,
			identity:  identByID[id],
			stats:     stats[id],
		})
	}
	sort.SliceStable(rows, func(i, j int) bool {
		ti, tj := timeOrZero(rows[i].stats.LastEventAt), timeOrZero(rows[j].stats.LastEventAt)
		if !ti.Equal(tj) {
			return ti.After(tj)
		}
		return strings.ToLower(rows[i].name) < strings.ToLower(rows[j].name)
	})
	sentinel := projectsRow{sentinel: true, name: "All projects"}
	var maxT time.Time
	for _, r := range rows {
		sentinel.stats.Open += r.stats.Open
		sentinel.stats.Closed += r.stats.Closed
		if r.stats.LastEventAt != nil && r.stats.LastEventAt.After(maxT) {
			maxT = *r.stats.LastEventAt
		}
	}
	if !maxT.IsZero() {
		sentinel.stats.LastEventAt = &maxT
	}
	return append([]projectsRow{sentinel}, rows...)
}

// timeOrZero unwraps an optional time pointer, returning the zero value
// for nil. Sort uses this so a project with no events sinks to the end
// of the descending order.
func timeOrZero(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return *t
}
