package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// scopeFixtureSingle returns a Model in single-project scope with a
// home project bound. The cache holds a mock entry so the toggle's
// drop() has something to clear; tests assert it became empty.
func scopeFixtureSingle() Model {
	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	m := Model{
		view:     viewList,
		keymap:   newKeymap(),
		cache:    newIssueCache(),
		toastNow: func() time.Time { return now },
		scope: scope{
			projectID:       7,
			projectName:     "kata",
			homeProjectID:   7,
			homeProjectName: "kata",
		},
		list: listModel{actor: "wesm"},
	}
	m.cache.put(cacheKey{projectID: 7}, []Issue{{Number: 1}})
	return m
}

// TestScopeToggle_FlipsToAllProjects pins the R binding's contract: in
// single-project scope with a home project bound, R flips to all-projects
// mode and clears the per-project list cache so the next fetch lands a
// fresh all-projects page.
func TestScopeToggle_FlipsToAllProjects(t *testing.T) {
	m := scopeFixtureSingle()
	next, cmd := m.handleScopeToggle()
	if !next.scope.allProjects {
		t.Fatal("R must flip scope.allProjects from false → true")
	}
	if next.scope.projectID != 0 {
		t.Fatalf("projectID = %d, want 0 in all-projects mode", next.scope.projectID)
	}
	if next.scope.homeProjectID != 7 {
		t.Fatal("homeProjectID must persist across toggle so we can flip back")
	}
	if cmd == nil {
		t.Fatal("toggle must dispatch a refetch command")
	}
}

// TestScopeToggle_FlipsBackToHome pins the inverse: from all-projects mode,
// R restores the home project as the new scope.
func TestScopeToggle_FlipsBackToHome(t *testing.T) {
	m := scopeFixtureSingle()
	m.scope.allProjects = true
	m.scope.projectID = 0
	m.scope.projectName = ""

	next, cmd := m.handleScopeToggle()
	if next.scope.allProjects {
		t.Fatal("second R must flip scope.allProjects back to false")
	}
	if next.scope.projectID != 7 {
		t.Fatalf("projectID = %d, want 7 (home project)", next.scope.projectID)
	}
	if next.scope.projectName != "kata" {
		t.Fatalf("projectName = %q, want \"kata\"", next.scope.projectName)
	}
	if cmd == nil {
		t.Fatal("toggle-back must dispatch a refetch command")
	}
}

// TestScopeToggle_RKeyDispatch pins that the R binding at the top level
// routes through handleScopeToggle and actually flips scope.
func TestScopeToggle_RKeyDispatch(t *testing.T) {
	m := scopeFixtureSingle()
	out, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'R'}})
	nm := out.(Model)
	if !nm.scope.allProjects {
		t.Fatal("R via Update must flip scope.allProjects")
	}
}

// TestScopeToggle_GatedByInputting: pressing R while the M3a inline
// command bar is open must reach the bar's textinput buffer instead
// of toggling scope. canQuit gates global keys via m.input.kind.
func TestScopeToggle_GatedByInputting(t *testing.T) {
	m := scopeFixtureSingle()
	m.input = newSearchBar(ListFilter{})
	out, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'R'}})
	nm := out.(Model)
	if nm.scope.allProjects {
		t.Fatal("R toggled scope while bar was active; should reach the prompt")
	}
	if v := nm.input.activeField().value(); v != "R" {
		t.Fatalf("bar buffer = %q, want %q (rune must reach prompt)", v, "R")
	}
}

// TestScopeToggle_NoHomeRefuses pins the empty-state guard: when boot
// landed in viewEmpty (no project resolved) the toggle has nowhere to
// go, so it surfaces a toast instead of leaving the user on a blank
// list with no way back.
func TestScopeToggle_NoHomeRefuses(t *testing.T) {
	m := scopeFixtureSingle()
	m.scope = scope{empty: true}
	next, cmd := m.handleScopeToggle()
	if next.scope.allProjects {
		t.Fatal("R must not flip into all-projects when there's no home project")
	}
	if next.toast == nil {
		t.Fatal("toggle must surface a toast when there's no project to toggle from")
	}
	if cmd == nil {
		t.Fatal("toast must come with an expiry cmd")
	}
}

// TestEmptyState_RendersHint: viewEmpty renders the onboarding hint
// containing both the "no kata projects" line and the kata init hint.
func TestEmptyState_RendersHint(t *testing.T) {
	m := initialModel(Options{})
	m.view = viewEmpty
	m.width = 80
	m.height = 24
	out := m.View()
	for _, want := range []string{
		"no kata projects registered yet",
		"kata init",
		"press q to quit",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("empty view missing %q in output\n%s", want, out)
		}
	}
}

// TestEmptyState_QuitsOnQ: q from viewEmpty opens the M3.5b
// quit-confirm modal. Y from there commits to quit. ctrl+c remains
// the immediate-quit escape hatch so the user is never trapped.
func TestEmptyState_QuitsOnQ(t *testing.T) {
	m := initialModel(Options{})
	m.view = viewEmpty
	out, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	if out.(Model).modal != modalQuitConfirm {
		t.Fatalf("q from viewEmpty did not open quit-confirm: %v", out.(Model).modal)
	}
	out, cmd := out.(Model).Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	_ = out
	if cmd == nil {
		t.Fatal("y in modal produced nil cmd, want tea.Quit")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatalf("y in modal cmd = %T, want tea.QuitMsg", cmd())
	}
}

// TestEmptyState_OtherKeysIgnored: j, ?, R in viewEmpty are no-ops so an
// unbound user can't accidentally fall into a partially-functional help
// or list view from a state with no projects.
func TestEmptyState_OtherKeysIgnored(t *testing.T) {
	m := initialModel(Options{})
	m.view = viewEmpty
	for _, k := range []rune{'j', '?', 'R', 'k', 's'} {
		out, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{k}})
		if out.(Model).view != viewEmpty {
			t.Fatalf("key %q changed view from empty to %v", k, out.(Model).view)
		}
		if cmd != nil {
			t.Fatalf("key %q in viewEmpty returned non-nil cmd", k)
		}
	}
}

// TestRenderEmpty_ZeroDims: a zero-sized terminal renders the message
// without panicking inside lipgloss.Place. Defensive: the model emits
// width/height from WindowSizeMsg, which can lag on first frame.
func TestRenderEmpty_ZeroDims(t *testing.T) {
	out := renderEmpty(0, 0)
	if !strings.Contains(out, "no kata projects registered yet") {
		t.Fatalf("zero-dim render missing hint:\n%s", out)
	}
}
