package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

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
