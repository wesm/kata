package tui

import (
	"strings"
	"testing"

	"github.com/mattn/go-runewidth"
)

func TestQueueHelpRows_ConditionalItems(t *testing.T) {
	parentNum := int64(1)
	withChildren := Model{list: listModel{issues: []Issue{
		{ProjectID: 7, Number: parentNum, Title: "parent", Status: "open"},
		{ProjectID: 7, Number: 2, ParentNumber: &parentNum, Title: "child", Status: "open"},
	}}}
	assertHelpItemPresent(t, flattenHelpRows(withChildren.queueHelpRows()),
		helpItem{key: "space", desc: "expand"})
	assertHelpItemPresent(t, flattenHelpRows(withChildren.queueHelpRows()),
		helpItem{key: "N", desc: "child"})
	assertHelpItemPresent(t, flattenHelpRows(withChildren.queueHelpRows()),
		helpItem{key: "o", desc: "order"})

	leaf := Model{list: listModel{issues: []Issue{
		{ProjectID: 7, Number: 1, Title: "leaf", Status: "open"},
	}}}
	assertHelpItemAbsent(t, flattenHelpRows(leaf.queueHelpRows()),
		helpItem{key: "space", desc: "expand"})
	assertHelpItemPresent(t, flattenHelpRows(leaf.queueHelpRows()),
		helpItem{key: "N", desc: "child"})

	empty := Model{}
	assertHelpItemAbsent(t, flattenHelpRows(empty.queueHelpRows()),
		helpItem{key: "N", desc: "child"})
}

// TestDetailHelpRows_Contexts: the persistent detail footer is
// comprehensive — every detail-mode binding handled by the Update
// loop appears so the user is not stranded looking for an action.
// The activity-focus row carries navigation primitives plus the
// full mutation surface (edit/comment/label/owner/parent/blocker/
// link/close/reopen/quit). Children focus swaps the navigation
// header (↑↓ child / ↵ open child) but keeps the action surface.
func TestDetailHelpRows_Contexts(t *testing.T) {
	activity := Model{detail: detailModel{
		issue:       &Issue{Number: 1, Title: "issue", Status: "open"},
		detailFocus: focusActivity,
		activeTab:   tabComments,
	}}
	for _, want := range []helpItem{
		{key: "↑↓", desc: "move"},
		{key: "↹", desc: "section"},
		{key: "↵", desc: "open"},
		{key: "e", desc: "edit"},
		{key: "c", desc: "comment"},
		{key: "+", desc: "label"},
		{key: "a", desc: "owner"},
		{key: "x", desc: "close"},
		{key: "r", desc: "reopen"},
		{key: "p", desc: "parent"},
		{key: "b", desc: "block"},
		{key: "l", desc: "link"},
		{key: "L", desc: "layout"},
		{key: "esc", desc: "back"},
		{key: "?", desc: "help"},
		{key: "q", desc: "quit"},
	} {
		assertHelpItemPresent(t, flattenHelpRows(activity.detailHelpRows()), want)
	}

	children := Model{detail: detailModel{
		issue:       &Issue{Number: 1, Title: "parent", Status: "open"},
		children:    []Issue{{Number: 2, Title: "child", Status: "open"}},
		detailFocus: focusChildren,
	}}
	for _, want := range []helpItem{
		{key: "↑↓", desc: "child"},
		{key: "↵", desc: "open child"},
		{key: "N", desc: "child"},
		{key: "e", desc: "edit"},
		{key: "x", desc: "close"},
		{key: "?", desc: "help"},
		{key: "q", desc: "quit"},
	} {
		assertHelpItemPresent(t, flattenHelpRows(children.detailHelpRows()), want)
	}
}

func TestHelpRows_InputAndModalContexts(t *testing.T) {
	tests := []struct {
		name string
		m    Model
		want []helpItem
	}{
		{
			name: "search bar",
			m:    Model{input: inputState{kind: inputSearchBar}},
			want: []helpItem{
				{key: "enter", desc: "commit"},
				{key: "esc", desc: "cancel"},
				{key: "ctrl+u", desc: "clear"},
			},
		},
		{
			name: "filter form",
			m:    Model{input: inputState{kind: inputFilterForm}},
			want: []helpItem{
				{key: "ctrl+s", desc: "apply"},
				{key: "esc", desc: "cancel"},
				{key: "ctrl+r", desc: "reset"},
			},
		},
		{
			name: "quit modal",
			m:    Model{modal: modalQuitConfirm},
			want: []helpItem{
				{key: "y", desc: "confirm"},
				{key: "n/esc", desc: "cancel"},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := flattenHelpRows(tt.m.helpRows())
			for _, want := range tt.want {
				assertHelpItemPresent(t, got, want)
			}
		})
	}
}

func TestPersistentHelpRowsPreferArrowNotation(t *testing.T) {
	parentNum := int64(1)
	m := Model{
		list: listModel{issues: []Issue{
			{ProjectID: 7, Number: parentNum, Title: "parent", Status: "open"},
			{ProjectID: 7, Number: 2, ParentNumber: &parentNum, Title: "child", Status: "open"},
		}},
		detail: detailModel{
			issue:    &Issue{Number: 1, Title: "parent", Status: "open"},
			children: []Issue{{Number: 2, Title: "child", Status: "open"}},
		},
	}
	for _, rows := range [][][]helpItem{m.queueHelpRows(), m.detailHelpRows()} {
		for _, item := range flattenHelpRows(rows) {
			if strings.Contains(item.key, "j/k") {
				t.Fatalf("persistent footer keys should use arrows, got %+v", item)
			}
		}
	}
}

func TestRenderHelpTable_ReflowsToFitWidth80(t *testing.T) {
	rows := [][]helpItem{{
		{key: "↑↓", desc: "move"},
		{key: "↵", desc: "open"},
		{key: "space", desc: "expand"},
		{key: "N", desc: "child"},
		{key: "/", desc: "search"},
		{key: "f", desc: "filter"},
		{key: "s", desc: "status"},
		{key: "c", desc: "clear"},
		{key: "x", desc: "close"},
		{key: "?", desc: "help"},
		{key: "q", desc: "quit"},
	}}
	got := stripANSI(renderHelpTable(rows, 80))
	assertLinesFitWidth(t, got, 80)
	assertStringContains(t, got, "▕")
	assertStringContains(t, got, "space expand")
	assertStringContains(t, got, "q quit")
}

func TestReflowHelpRows_ExtremeNarrowFallsBackToOneItemPerRow(t *testing.T) {
	rows := [][]helpItem{{
		{key: "↑↓", desc: "move"},
		{key: "↵", desc: "open"},
		{key: "space", desc: "expand"},
	}}
	got := reflowHelpRows(rows, 8)
	for _, row := range got {
		if len(row) > 1 {
			t.Fatalf("narrow reflow should use one item per row, got %+v", got)
		}
	}
}

func TestListViewFooterUsesAdaptiveHelpTable(t *testing.T) {
	parentNum := int64(1)
	lm := listModel{issues: []Issue{
		{ProjectID: 7, Number: parentNum, Title: "parent", Status: "open"},
		{ProjectID: 7, Number: 2, ParentNumber: &parentNum, Title: "child", Status: "open"},
	}}
	got := stripANSI(lm.View(80, 14, viewChrome{}))
	assertLineCount(t, got, 14)
	assertLinesFitWidth(t, got, 80)
	assertStringContains(t, got, "space expand")
	assertStringContains(t, got, "N child")
	assertStringContains(t, got, "▕")
}

func TestDetailViewFooterUsesAdaptiveChildrenFocusHints(t *testing.T) {
	dm := detailModel{
		issue:       &Issue{Number: 1, Title: "parent", Status: "open"},
		children:    []Issue{{Number: 2, Title: "child", Status: "open"}},
		detailFocus: focusChildren,
	}
	got := stripANSI(dm.View(80, 18, viewChrome{}))
	assertLineCount(t, got, 18)
	assertLinesFitWidth(t, got, 80)
	assertStringContains(t, got, "open child")
	assertStringContains(t, got, "N child")
}

func flattenHelpRows(rows [][]helpItem) []helpItem {
	out := []helpItem{}
	for _, row := range rows {
		out = append(out, row...)
	}
	return out
}

func assertHelpItemPresent(t *testing.T, rows []helpItem, want helpItem) {
	t.Helper()
	for _, row := range rows {
		if row == want {
			return
		}
	}
	t.Fatalf("help rows missing %+v in %+v", want, rows)
}

func assertHelpItemAbsent(t *testing.T, rows []helpItem, deny helpItem) {
	t.Helper()
	for _, row := range rows {
		if row == deny {
			t.Fatalf("help rows unexpectedly contain %+v in %+v", deny, rows)
		}
	}
}

func assertStringContains(t *testing.T, got, want string) {
	t.Helper()
	if !strings.Contains(got, want) {
		t.Fatalf("output missing %q:\n%s", want, got)
	}
}

func assertLineCount(t *testing.T, got string, want int) {
	t.Helper()
	if lines := strings.Split(got, "\n"); len(lines) != want {
		t.Fatalf("got %d lines, want %d:\n%s", len(lines), want, got)
	}
}

func assertLinesFitWidth(t *testing.T, got string, width int) {
	t.Helper()
	for i, line := range strings.Split(got, "\n") {
		if w := runewidth.StringWidth(line); w > width {
			t.Fatalf("line %d width=%d exceeds %d:\n%s", i+1, w, width, got)
		}
	}
}
