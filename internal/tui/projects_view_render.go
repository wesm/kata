package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/mattn/go-runewidth"
)

// renderProjects draws the projects-view body: a 5-column table
// (Project / Open / Closed / Total / Updated), an All-projects sentinel
// pinned at row 0, and a 1-line identity footer for the highlighted
// row. Spec §5.
func renderProjects(m Model) string {
	rows := projectsRows(m.projectsByID, m.projectIdentByID, m.projectStats)
	cursor := m.projectsCursor
	if cursor >= len(rows) {
		cursor = len(rows) - 1
	}
	if cursor < 0 {
		cursor = 0
	}

	headerCells := []string{"Project", "Open", "Closed", "Total", "Updated"}
	body := []string{
		titleStyle.Render("kata / projects"),
		subtleStyle.Render(fmt.Sprintf("%d projects", len(rows)-1)),
		"",
		renderProjectsHeader(headerCells, m.width),
	}
	for i, r := range rows {
		body = append(body, renderProjectsRow(r, i == cursor, m.width))
	}
	body = append(body, "")
	if cursor >= 0 && cursor < len(rows) {
		body = append(body, subtleStyle.Render(footerForRow(rows[cursor], m.width)))
	}
	body = append(body, "")
	body = append(body, subtleStyle.Render(
		"[↑/↓ k/j] move  [enter] open  [esc] back  [r] refresh  [q] quit  [?] help"))

	if m.width <= 0 || m.height <= 0 {
		return strings.Join(body, "\n")
	}
	return strings.Join(body, "\n")
}

func renderProjectsHeader(cells []string, width int) string {
	// Fixed-width numeric columns; flexible Project column.
	return projectsRowLayout(cells[0], cells[1], cells[2], cells[3], cells[4], width, false)
}

func renderProjectsRow(r projectsRow, highlight bool, width int) string {
	name := r.name
	if r.sentinel {
		name = "All projects"
	} else {
		name = sanitizeForDisplay(name)
	}
	open := fmt.Sprintf("%d", r.stats.Open)
	closed := fmt.Sprintf("%d", r.stats.Closed)
	total := fmt.Sprintf("%d", r.stats.Open+r.stats.Closed)
	updated := updatedColumn(r.stats.LastEventAt)
	return projectsRowLayout(name, open, closed, total, updated, width, highlight)
}

// projectsRowLayout lays out the five columns with the Project column
// flexing and the four numeric/time columns fixed-width and right- or
// left-aligned per spec §5.2.
func projectsRowLayout(project, open, closed, total, updated string, width int, highlight bool) string {
	const (
		openW    = 6
		closedW  = 7
		totalW   = 6
		updatedW = 12
		gap      = 2
	)
	projectW := width - (openW + closedW + totalW + updatedW + 4*gap) - 2
	if projectW < 8 {
		projectW = 8
	}
	cursor := "  "
	if highlight {
		cursor = "▶ "
	}
	line := cursor + padToWidth(project, projectW) +
		strings.Repeat(" ", gap) + padL(open, openW) +
		strings.Repeat(" ", gap) + padL(closed, closedW) +
		strings.Repeat(" ", gap) + padL(total, totalW) +
		strings.Repeat(" ", gap) + padToWidth(updated, updatedW)
	if highlight {
		line = lipgloss.NewStyle().Bold(true).Render(line)
	}
	return line
}

// padL right-aligns s in a width of `w` cells. Counterpart to
// padToWidth (which left-aligns); used for numeric columns where
// the eye scans down right-aligned digits. ANSI-aware on both the
// measure and the truncate paths.
func padL(s string, w int) string {
	width := runewidth.StringWidth(stripANSI(s))
	if width == w {
		return s
	}
	if width > w {
		return ansi.Truncate(s, w, "…")
	}
	return strings.Repeat(" ", w-width) + s
}

// updatedColumn renders the Updated cell. nil (project with zero
// events) is the em-dash per spec §6.1; otherwise we delegate to
// humanizeRelative so the per-bucket format ("30s ago" / "2h ago" /
// "3d ago" / "1w ago") and renderNow injection match the rest of
// the TUI.
func updatedColumn(t *time.Time) string {
	if t == nil {
		return "—"
	}
	return humanizeRelative(*t)
}

// footerForRow renders the 1-line identity footer for a highlighted row.
// Sentinel: a description; concrete project: the identity URL truncated
// to width-2 if longer. Spec §5.1, §9.
func footerForRow(r projectsRow, width int) string {
	if r.sentinel {
		return "issue queue across every registered project"
	}
	label := "identity: " + sanitizeForDisplay(r.identity)
	if width > 0 && runewidth.StringWidth(label) > width-2 {
		label = runewidth.Truncate(label, width-2, "…")
	}
	return label
}
