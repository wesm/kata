package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/exp/teatest"
)

// ptrString is a test-only helper for taking the address of a string
// literal inline. The production model exposes pointer fields (Owner,
// DeletedAt) and Go forbids &"literal" so the fixtures need this.
func ptrString(s string) *string { return &s }

// ptrTime is the time.Time companion to ptrString.
func ptrTime(t time.Time) *time.Time { return &t }

// listFixture is the on-screen seed for the list tests. Three rows cover
// the open, closed, and soft-deleted statusChip branches without booting
// a real daemon. The deleted row keeps statusChip's DeletedAt branch
// under test even after the Task 4 fixture moved into this file.
func listFixture() []Issue {
	deleted := time.Now().Add(-2 * time.Hour)
	return []Issue{
		{
			Number: 1, Title: "fix login bug on Safari",
			Status: "open", Owner: ptrString("claude-4.7"),
			UpdatedAt: time.Now().Add(-3 * time.Hour),
		},
		{
			Number: 2, Title: "rebuild search index",
			Status: "closed", Owner: ptrString("wesm"),
			UpdatedAt: time.Now().Add(-1 * time.Hour),
		},
		{
			Number: 3, Title: "purge stale tokens",
			Status: "open", DeletedAt: ptrTime(deleted),
			UpdatedAt: deleted,
		},
	}
}

// setupListTeatest boots a teatest model at 120x30, seeds the standard
// listFixture, and registers a ctrl+c fast-quit cleanup. q opens the
// quit-confirm modal in M3.5b; ctrl+c bypasses the confirm so tests
// terminate without an extra `y` keystroke + race on the modal.
func setupListTeatest(t *testing.T) *teatest.TestModel {
	t.Helper()
	m := initialModel(Options{})
	tm := teatest.NewTestModel(t, m, teatest.WithInitialTermSize(120, 30))
	tm.Send(tea.WindowSizeMsg{Width: 120, Height: 30})
	tm.Send(initialFetchMsg{dispatchKey: cacheKey{limit: queueFetchLimit}, issues: listFixture()})
	t.Cleanup(func() {
		tm.Send(tea.KeyMsg{Type: tea.KeyCtrlC})
		tm.WaitFinished(t)
	})
	return tm
}

// TestList_Render_Fixture confirms the seed reaches the screen so the
// rendering layer can be reviewed independent of the network layer. The
// [deleted] assertion guards statusChip's soft-delete branch.
func TestList_Render_Fixture(t *testing.T) {
	tm := setupListTeatest(t)
	teatest.WaitFor(t, tm.Output(), func(b []byte) bool {
		s := string(b)
		return strings.Contains(s, "fix login bug on Safari") &&
			strings.Contains(s, "[deleted]")
	}, teatest.WithDuration(2*time.Second))
}

// TestList_Cursor_DownAndUp drives j/j/k against the three-row fixture
// and asserts the marker glyph lands on the row containing #2. The third
// row gives the down-clamp room to move; with two rows j/j/k would land
// on index 0 because cursor never reaches 2. lipgloss/table pads between
// columns, so we scan output line-by-line for one that contains both the
// marker and the row's issue number.
func TestList_Cursor_DownAndUp(t *testing.T) {
	tm := setupListTeatest(t)
	teatest.WaitFor(t, tm.Output(), func(b []byte) bool {
		return strings.Contains(string(b), "purge stale tokens")
	}, teatest.WithDuration(2*time.Second))
	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
	teatest.WaitFor(t, tm.Output(), func(b []byte) bool {
		for _, line := range strings.Split(string(b), "\n") {
			if strings.Contains(line, "▶") && strings.Contains(line, "#2") {
				return true
			}
		}
		return false
	}, teatest.WithDuration(2*time.Second))
}
