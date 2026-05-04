package tui

import (
	"context"
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
