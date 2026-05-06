package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func mouseLeftClick(x, y int) tea.MouseMsg {
	return tea.MouseMsg{X: x, Y: y, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft}
}

func mouseWheelDown() tea.MouseMsg {
	return mouseWheelDownAt(0)
}

func mouseWheelDownAt(x int) tea.MouseMsg {
	return tea.MouseMsg{X: x, Action: tea.MouseActionPress, Button: tea.MouseButtonWheelDown}
}

func mouseWheelUp() tea.MouseMsg {
	return tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonWheelUp}
}

func TestProgramOpts_MouseDisabledByDefault(t *testing.T) {
	without := programOpts(t.Context(), Options{})
	with := programOpts(t.Context(), Options{Mouse: true})
	if len(with) != len(without)+1 {
		t.Fatalf("programOpts with mouse len=%d, want disabled len %d + 1", len(with), len(without))
	}
}

func TestMouseDisabledIgnoresMouseMsg(t *testing.T) {
	m := resizeModel(newTestModel(), 120, 30)
	m.list.issues = makeTestIssues(3)
	m.list.cursor = 0

	nm, cmd := updateModel(m, mouseLeftClick(4, 6))
	if cmd != nil {
		t.Fatalf("disabled mouse returned cmd %T, want nil", cmd())
	}
	if nm.list.cursor != 0 {
		t.Fatalf("disabled mouse moved cursor to %d, want 0", nm.list.cursor)
	}
}

func TestMouseClickSelectsIssueListRow(t *testing.T) {
	m := resizeModel(newTestModel(), 120, 30)
	m.opts.Mouse = true
	m.list.issues = makeTestIssues(5)

	nm, cmd := updateModel(m, mouseLeftClick(4, 6)) // stacked list row 3: title, stats, header, rule, row0...
	if cmd != nil {
		t.Fatalf("single click returned cmd %T, want nil", cmd())
	}
	if nm.list.cursor != 2 || nm.list.selectedNumber != 3 {
		t.Fatalf("cursor=%d selected=%d, want cursor=2 selected #3", nm.list.cursor, nm.list.selectedNumber)
	}
}

func TestMouseSplitClickFirstIssueRowSelectsFirstVisibleRow(t *testing.T) {
	m := resizeModel(newTestModel(), 160, 30)
	m.opts.Mouse = true
	m.layout = layoutSplit
	m.list.issues = makeTestIssues(5)
	m.list.cursor = 0

	nm, _ := updateModel(m, mouseLeftClick(4, 4)) // title, pane border, table header, rule, first row
	if nm.list.cursor != 0 || nm.list.selectedNumber != 1 {
		t.Fatalf("cursor=%d selected=%d, want first visible issue", nm.list.cursor, nm.list.selectedNumber)
	}
}

func TestMouseFullScreenHelpIgnoresSplitPaneMouse(t *testing.T) {
	m := resizeModel(newTestModel(), 160, 30)
	m.opts.Mouse = true
	m.layout = layoutSplit
	m.view = viewHelp
	m.list.issues = makeTestIssues(5)
	m.list.cursor = 0

	nm, _ := updateModel(m, mouseWheelDownAt(4))
	if nm.list.cursor != 0 {
		t.Fatalf("help-view mouse wheel moved list cursor to %d, want no-op", nm.list.cursor)
	}
}

func TestMouseWheelScrollsIssueDetail(t *testing.T) {
	m := setupDetailScenario(t, 120, 30, "line\nline\nline\nline\nline\nline\nline\nline\nline\nline\nline\nline\nline\nline\nline\nTAIL")
	m.opts.Mouse = true
	m.detail.scroll = 0

	nm, _ := updateModel(m, mouseWheelDown())
	if nm.detail.scroll == 0 {
		t.Fatal("wheel down did not scroll detail body")
	}
	nm, _ = updateModel(nm, mouseWheelUp())
	if nm.detail.scroll != 0 {
		t.Fatalf("wheel up scroll=%d, want 0", nm.detail.scroll)
	}
}

func TestMouseClickSelectsProjectsRow(t *testing.T) {
	m := setupProjectsView(
		mockProject{ID: 1, Name: "alpha", Ident: "github.com/acme/alpha"},
		mockProject{ID: 2, Name: "beta", Ident: "github.com/acme/beta"},
	)
	m.opts.Mouse = true

	nm, cmd := updateModel(m, mouseLeftClick(2, 5)) // projects row after title/count/blank/header
	if cmd != nil {
		t.Fatalf("single click returned cmd %T, want nil", cmd())
	}
	if nm.projectsCursor != 1 {
		t.Fatalf("projectsCursor=%d, want first concrete project row", nm.projectsCursor)
	}
}
