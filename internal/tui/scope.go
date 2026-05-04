package tui

import (
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// projectPickerState carries the cursor + the sorted project list shown
// in the modal. The "All projects" entry is implicit at index 0; project
// IDs >= 1 fill the remainder. Rebuilt from m.projectsByID every time
// the picker is opened so a project added since the last open shows up.
type projectPickerState struct {
	cursor int
	items  []projectPickerItem
}

// projectPickerItem is one row in the picker. allProjects=true marks
// the implicit "All projects" entry; otherwise projectID names a real
// project.
type projectPickerItem struct {
	allProjects bool
	projectID   int64
	name        string
}

// openProjectPicker opens the modal listing every project plus an
// "All projects" entry so the user can navigate to any scope from any
// scope. The cursor lands on the currently-active scope so a no-op
// re-open doesn't move focus. When no projects are known yet (boot
// race or empty state) a toast surfaces instead of an empty modal.
func (m Model) openProjectPicker() (Model, tea.Cmd) {
	items := buildProjectPickerItems(m.projectsByID)
	if len(items) <= 1 && len(m.projectsByID) == 0 {
		return m.toastPickerUnavailable()
	}
	m.projectPicker = projectPickerState{
		cursor: pickerCursorForScope(items, m.scope),
		items:  items,
	}
	m.modal = modalProjectPicker
	return m, nil
}

// toastPickerUnavailable surfaces a hint when the picker can't open
// because no projects are known (e.g. the boot fetch failed or the
// daemon really has no projects).
func (m Model) toastPickerUnavailable() (Model, tea.Cmd) {
	m.toast = &toast{
		text:      "no projects to switch to (run kata init in a project first)",
		level:     toastError,
		expiresAt: m.toastNow().Add(toastNoBindingTTL),
	}
	return m, toastExpireCmd(toastNoBindingTTL)
}

// buildProjectPickerItems composes the modal's row list: an
// "All projects" sentinel followed by every project from the cache,
// sorted alphabetically by name (case-insensitive) so the order is
// stable across re-opens.
func buildProjectPickerItems(byID map[int64]string) []projectPickerItem {
	items := []projectPickerItem{{allProjects: true, name: "All projects"}}
	keys := make([]int64, 0, len(byID))
	for id := range byID {
		keys = append(keys, id)
	}
	sort.Slice(keys, func(i, j int) bool {
		ni, nj := strings.ToLower(byID[keys[i]]), strings.ToLower(byID[keys[j]])
		if ni != nj {
			return ni < nj
		}
		return keys[i] < keys[j]
	})
	for _, id := range keys {
		items = append(items, projectPickerItem{projectID: id, name: byID[id]})
	}
	return items
}

// pickerCursorForScope picks the initial cursor row so the picker
// opens on whatever scope is currently active. Defaults to row 0
// ("All projects") when the active scope's project isn't in the
// items list (e.g. it was archived since boot).
func pickerCursorForScope(items []projectPickerItem, sc scope) int {
	if sc.allProjects {
		return 0
	}
	for i, item := range items {
		if !item.allProjects && item.projectID == sc.projectID {
			return i
		}
	}
	return 0
}

// routeProjectPickerKey delivers a key to the active picker modal.
// j/k or up/down move the cursor (clamped); Enter selects the
// highlighted row and switches scope; Esc cancels without changing
// scope. Other keys are absorbed.
func (m Model) routeProjectPickerKey(msg tea.KeyMsg) (Model, tea.Cmd) {
	switch msg.String() {
	case "j", "down":
		if m.projectPicker.cursor < len(m.projectPicker.items)-1 {
			m.projectPicker.cursor++
		}
		return m, nil
	case "k", "up":
		if m.projectPicker.cursor > 0 {
			m.projectPicker.cursor--
		}
		return m, nil
	case "g", "home":
		m.projectPicker.cursor = 0
		return m, nil
	case "G", "end":
		m.projectPicker.cursor = len(m.projectPicker.items) - 1
		return m, nil
	case "esc":
		m.modal = modalNone
		m.projectPicker = projectPickerState{}
		return m, nil
	case "enter":
		return m.applyProjectPickerSelection()
	}
	return m, nil
}

// applyProjectPickerSelection commits the highlighted row's choice:
// "All projects" flips scope.allProjects=true, a specific project sets
// projectID/projectName. The list cache is dropped so the next fetch
// lands a fresh page under the new scope.
func (m Model) applyProjectPickerSelection() (Model, tea.Cmd) {
	if m.projectPicker.cursor < 0 || m.projectPicker.cursor >= len(m.projectPicker.items) {
		m.modal = modalNone
		m.projectPicker = projectPickerState{}
		return m, nil
	}
	item := m.projectPicker.items[m.projectPicker.cursor]
	if item.allProjects {
		m.scope.allProjects = true
		m.scope.projectID = 0
		m.scope.projectName = ""
	} else if item.projectID != m.scope.projectID || m.scope.allProjects {
		m.scope.allProjects = false
		m.scope.projectID = item.projectID
		m.scope.projectName = item.name
	} else {
		// No-op selection (already in this project's scope).
		m.modal = modalNone
		m.projectPicker = projectPickerState{}
		return m, nil
	}
	m.modal = modalNone
	m.projectPicker = projectPickerState{}
	m.list = listModel{}
	m.cache.markStale()
	return m, m.fetchInitial()
}

// renderProjectPickerModal renders the centered project-switcher modal.
// The active row is highlighted; the "All projects" sentinel sits at the
// top so a single press of Enter from that row gives the user the full
// cross-project feed.
func renderProjectPickerModal(state projectPickerState) string {
	rows := make([]string, 0, len(state.items)+4)
	rows = append(rows, "switch project", "")
	for i, item := range state.items {
		marker := "  "
		if i == state.cursor {
			marker = "▶ "
		}
		label := item.name
		if !item.allProjects && label == "" {
			label = "(unnamed project)"
		}
		line := padToWidth(marker+label, modalProjectPickerWidth)
		if i == state.cursor {
			line = lipgloss.NewStyle().Bold(true).Render(line)
		}
		rows = append(rows, line)
	}
	rows = append(rows, "", "[↑/k] up  [↓/j] down  [enter] select  [esc] cancel")
	return modalBoxStyle.Render(strings.Join(rows, "\n"))
}

const modalProjectPickerWidth = 32

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
