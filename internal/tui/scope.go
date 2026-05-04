package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// handleScopeToggle implements the R binding: flip between the home
// project (single-project mode) and the all-projects feed. The list
// refetches under the new scope; the cache slot keys on allProjects so
// the previous slot stays warm for a fast toggle back. SSE keeps flowing
// — eventAffectsView() filters client-side so every cached event shows
// up under whichever scope we land in.
//
// When the home project is unset (boot landed in the empty state) the
// toggle has nowhere to go, so it shows a toast instead of leaving the
// user stuck on an empty list with no project to return to.
func (m Model) handleScopeToggle() (Model, tea.Cmd) {
	if !m.scope.allProjects && m.scope.homeProjectID == 0 {
		return m.toastScopeUnavailable()
	}
	if m.scope.allProjects {
		m.scope.allProjects = false
		m.scope.projectID = m.scope.homeProjectID
		m.scope.projectName = m.scope.homeProjectName
	} else {
		m.scope.allProjects = true
		m.scope.projectID = 0
		m.scope.projectName = ""
	}
	m.list = listModel{}
	m.cache.markStale()
	return m, m.fetchInitial()
}

// toastScopeUnavailable surfaces a hint that scope can't be toggled —
// e.g. the TUI booted in the empty state with no home project to
// return to.
func (m Model) toastScopeUnavailable() (Model, tea.Cmd) {
	m.toast = &toast{
		text:      "no project to toggle from (run kata init in a project first)",
		level:     toastError,
		expiresAt: m.toastNow().Add(toastNoBindingTTL),
	}
	return m, toastExpireCmd(toastNoBindingTTL)
}

// renderEmpty draws the centered onboarding hint shown when the daemon
// has zero registered projects. lipgloss.Place handles vertical and
// horizontal centering inside width × height; small terminals fall back
// to top-left placement (lipgloss caps the offsets) so the message
// remains visible.
func renderEmpty(width, height int) string {
	body := strings.Join([]string{
		titleStyle.Render("no kata projects registered yet"),
		"",
		subtleStyle.Render("run `kata init` in a repo to get started."),
		subtleStyle.Render("press q to quit."),
	}, "\n")
	if width <= 0 || height <= 0 {
		return body
	}
	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, body)
}

// renderTooNarrow is the degraded full-screen hint shown when the
// terminal width can't fit readable table columns + chip strip.
//
// q / ctrl+c still route through Model.routeGlobalKey, so the user
// can quit from the hint screen without resizing first.
func renderTooNarrow(width, height int) string {
	msg := strings.Join([]string{
		"kata tui needs more space",
		"",
		">=80 columns wide",
		"resize your terminal and try again",
		"",
		"press q to quit",
	}, "\n")
	body := lipgloss.NewStyle().
		Border(lipgloss.NormalBorder()).
		BorderForeground(panelActiveBorder).
		Padding(1, 2).
		Render(msg)
	if width <= 0 || height <= 0 {
		return body
	}
	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, body)
}
