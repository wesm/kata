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

// scopeFixtureMultiProject extends the single-project fixture with a
// projectsByID cache holding two projects so the picker has rows to
// render and select from.
func scopeFixtureMultiProject() Model {
	m := scopeFixtureSingle()
	m.projectsByID = map[int64]string{7: "kata", 9: "other"}
	return m
}

// TestProjectPicker_PKeyOpensModal pins the P binding: the top-level
// dispatcher routes P through openProjectPicker which sets the modal
// kind so subsequent keys are owned by the picker.
func TestProjectPicker_PKeyOpensModal(t *testing.T) {
	m := scopeFixtureMultiProject()
	out, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'P'}})
	nm := out.(Model)
	if nm.modal != modalProjectPicker {
		t.Fatalf("modal = %v, want modalProjectPicker", nm.modal)
	}
	if len(nm.projectPicker.items) != 3 {
		t.Fatalf("picker items = %d, want 3 (All projects + 2 projects)", len(nm.projectPicker.items))
	}
	if !nm.projectPicker.items[0].allProjects {
		t.Fatalf("first row must be the All projects sentinel")
	}
}

// TestProjectPicker_OpensOnActiveScope pins the cursor-positioning
// rule: opening the picker lands the highlight on whichever scope is
// currently active so a no-op re-open doesn't move focus.
func TestProjectPicker_OpensOnActiveScope(t *testing.T) {
	m := scopeFixtureMultiProject()
	// scope.projectID=7 → cursor lands on the row whose projectID is 7.
	out, _ := m.openProjectPicker()
	want := -1
	for i, item := range out.projectPicker.items {
		if !item.allProjects && item.projectID == 7 {
			want = i
			break
		}
	}
	if out.projectPicker.cursor != want {
		t.Fatalf("cursor = %d, want %d (active scope row)", out.projectPicker.cursor, want)
	}
}

// TestProjectPicker_AllProjectsSelection pins the "All projects" row:
// Enter on it flips scope.allProjects=true and dispatches a refetch.
func TestProjectPicker_AllProjectsSelection(t *testing.T) {
	m := scopeFixtureMultiProject()
	m, _ = m.openProjectPicker()
	m.projectPicker.cursor = 0 // All projects row
	out, cmd := m.routeProjectPickerKey(tea.KeyMsg{Type: tea.KeyEnter})
	if !out.scope.allProjects {
		t.Fatal("Enter on All projects must flip scope.allProjects")
	}
	if out.scope.projectID != 0 {
		t.Fatalf("projectID = %d, want 0 in all-projects mode", out.scope.projectID)
	}
	if out.modal != modalNone {
		t.Fatalf("modal must close after selection: %v", out.modal)
	}
	if cmd == nil {
		t.Fatal("selection must dispatch a refetch command")
	}
}

// TestProjectPicker_SwitchesToOtherProject pins the cross-project hop:
// Enter on a non-home project row updates scope.projectID and
// scope.projectName to the selected project.
func TestProjectPicker_SwitchesToOtherProject(t *testing.T) {
	m := scopeFixtureMultiProject()
	m, _ = m.openProjectPicker()
	// Find the "other" project row.
	target := -1
	for i, item := range m.projectPicker.items {
		if !item.allProjects && item.projectID == 9 {
			target = i
			break
		}
	}
	if target < 0 {
		t.Fatal("test setup: project 9 not in picker items")
	}
	m.projectPicker.cursor = target
	out, cmd := m.routeProjectPickerKey(tea.KeyMsg{Type: tea.KeyEnter})
	if out.scope.allProjects {
		t.Fatal("specific-project selection must not leave allProjects=true")
	}
	if out.scope.projectID != 9 {
		t.Fatalf("projectID = %d, want 9", out.scope.projectID)
	}
	if out.scope.projectName != "other" {
		t.Fatalf("projectName = %q, want \"other\"", out.scope.projectName)
	}
	if cmd == nil {
		t.Fatal("selection must dispatch a refetch command")
	}
}

// TestProjectPicker_EscCancels pins the cancel path: Esc closes the
// modal without changing scope.
func TestProjectPicker_EscCancels(t *testing.T) {
	m := scopeFixtureMultiProject()
	m, _ = m.openProjectPicker()
	m.projectPicker.cursor = 1 // not the active scope
	out, _ := m.routeProjectPickerKey(tea.KeyMsg{Type: tea.KeyEsc})
	if out.modal != modalNone {
		t.Fatalf("Esc must close modal: %v", out.modal)
	}
	if out.scope.projectID != 7 {
		t.Fatalf("scope.projectID = %d, want 7 unchanged after cancel", out.scope.projectID)
	}
}

// TestProjectPicker_NoProjectsRefuses pins the empty-state path: with
// no projects in the cache the picker surfaces a toast instead of
// opening an empty modal.
func TestProjectPicker_NoProjectsRefuses(t *testing.T) {
	m := scopeFixtureSingle()
	m.projectsByID = map[int64]string{}
	out, cmd := m.openProjectPicker()
	if out.modal == modalProjectPicker {
		t.Fatal("picker must not open with no projects to choose from")
	}
	if out.toast == nil {
		t.Fatal("expected a toast explaining why the picker won't open")
	}
	if cmd == nil {
		t.Fatal("toast must come with an expiry cmd")
	}
}

// TestProjectPicker_GatedByInputting: P while the inline command bar
// is open must reach the prompt instead of opening the picker.
func TestProjectPicker_GatedByInputting(t *testing.T) {
	m := scopeFixtureMultiProject()
	m.input = newSearchBar(ListFilter{})
	out, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'P'}})
	nm := out.(Model)
	if nm.modal == modalProjectPicker {
		t.Fatal("P opened picker while bar was active; should reach the prompt")
	}
	if v := nm.input.activeField().value(); v != "P" {
		t.Fatalf("bar buffer = %q, want %q (rune must reach prompt)", v, "P")
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
