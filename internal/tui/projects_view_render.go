package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// renderProjects draws the project-table view body. The full layout
// (table rows, sentinel, footer) lands in Task 6; this scaffold renders
// a recognizable placeholder so the view can be entered without crashing.
func renderProjects(m Model) string {
	body := strings.Join([]string{
		titleStyle.Render("kata / projects"),
		"",
		subtleStyle.Render("(table renders in Task 6)"),
	}, "\n")
	if m.width <= 0 || m.height <= 0 {
		return body
	}
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, body)
}
