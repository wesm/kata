package tui

import "testing"

func int64Ptr(v int64) *int64 { return &v }

func TestBuildQueueRows_CollapsedShowsTopLevelOnly(t *testing.T) {
	issues := []Issue{
		{ProjectID: 7, Number: 1, Title: "parent", ChildCounts: &ChildCounts{Open: 1, Total: 1}},
		{ProjectID: 7, Number: 2, Title: "child", ParentNumber: int64Ptr(1)},
		{ProjectID: 7, Number: 3, Title: "unrelated"},
	}

	rows := buildQueueRows(issues, ListFilter{}, nil)
	assertQueueNumbers(t, rows, []int64{1, 3})
	if !rows[0].hasChildren {
		t.Fatal("parent row should report children")
	}
}

func TestBuildQueueRows_ExpandedShowsDirectChildren(t *testing.T) {
	issues := []Issue{
		{ProjectID: 7, Number: 1, Title: "parent", ChildCounts: &ChildCounts{Open: 2, Total: 2}},
		{ProjectID: 7, Number: 2, Title: "child 1", ParentNumber: int64Ptr(1), ChildCounts: &ChildCounts{Open: 1, Total: 1}},
		{ProjectID: 7, Number: 3, Title: "child 2", ParentNumber: int64Ptr(1)},
		{ProjectID: 7, Number: 4, Title: "grandchild", ParentNumber: int64Ptr(2)},
	}

	rows := buildQueueRows(issues, ListFilter{}, expansionSet{{projectID: 7, number: 1}: true})
	assertQueueNumbers(t, rows, []int64{1, 2, 3})
	if rows[1].depth != 1 || rows[2].depth != 1 {
		t.Fatalf("children depths = %d/%d, want 1/1", rows[1].depth, rows[2].depth)
	}

	rows = buildQueueRows(issues, ListFilter{}, expansionSet{
		{projectID: 7, number: 1}: true,
		{projectID: 7, number: 2}: true,
	})
	assertQueueNumbers(t, rows, []int64{1, 2, 4, 3})
	if rows[2].depth != 2 {
		t.Fatalf("grandchild depth = %d, want 2", rows[2].depth)
	}
}

func TestBuildQueueRows_DefaultsExpandedChildrenToTopologicalOrder(t *testing.T) {
	issues := []Issue{
		{ProjectID: 7, Number: 1, Title: "parent", ChildCounts: &ChildCounts{Open: 3, Total: 3}},
		{ProjectID: 7, Number: 2, Title: "blocked child", ParentNumber: int64Ptr(1)},
		{ProjectID: 7, Number: 3, Title: "blocker child", ParentNumber: int64Ptr(1), Blocks: []int64{2}},
		{ProjectID: 7, Number: 4, Title: "unrelated child", ParentNumber: int64Ptr(1)},
	}

	rows := buildQueueRows(issues, ListFilter{}, expansionSet{{projectID: 7, number: 1}: true})
	assertQueueNumbers(t, rows, []int64{1, 3, 2, 4})
}

func TestBuildQueueRows_TemporalChildSortPreservesFetchOrder(t *testing.T) {
	issues := []Issue{
		{ProjectID: 7, Number: 1, Title: "parent", ChildCounts: &ChildCounts{Open: 3, Total: 3}},
		{ProjectID: 7, Number: 2, Title: "blocked child", ParentNumber: int64Ptr(1)},
		{ProjectID: 7, Number: 3, Title: "blocker child", ParentNumber: int64Ptr(1), Blocks: []int64{2}},
		{ProjectID: 7, Number: 4, Title: "unrelated child", ParentNumber: int64Ptr(1)},
	}

	rows := buildQueueRowsWithSort(
		issues,
		ListFilter{},
		expansionSet{{projectID: 7, number: 1}: true},
		childSortTemporal,
	)
	assertQueueNumbers(t, rows, []int64{1, 2, 3, 4})
}

func TestBuildQueueRows_FilteredChildAutoShowsAncestorContext(t *testing.T) {
	issues := []Issue{
		{ProjectID: 7, Number: 1, Title: "parent"},
		{ProjectID: 7, Number: 2, Title: "detail hint bars incomplete", ParentNumber: int64Ptr(1)},
		{ProjectID: 7, Number: 3, Title: "unrelated"},
	}

	rows := buildQueueRows(issues, ListFilter{Search: "hint bars"}, nil)
	assertQueueNumbers(t, rows, []int64{1, 2})
	if !rows[0].context {
		t.Fatal("non-matching ancestor should be a context row")
	}
	if rows[1].context {
		t.Fatal("matching child should not be a context row")
	}
	if !rows[0].expanded {
		t.Fatal("ancestor context row should render as expanded while showing a matched child")
	}
}

func TestBuildQueueRows_StatusFilterIsClientSide(t *testing.T) {
	issues := []Issue{
		{ProjectID: 7, Number: 1, Status: "open"},
		{ProjectID: 7, Number: 2, Status: "closed"},
	}

	rows := buildQueueRows(issues, ListFilter{Status: "closed"}, nil)
	assertQueueNumbers(t, rows, []int64{2})
}

func TestBuildQueueRows_LabelsFilterAnyOf(t *testing.T) {
	issues := []Issue{
		{ProjectID: 7, Number: 1, Labels: []string{"bug", "ux"}},
		{ProjectID: 7, Number: 2, Labels: []string{"daemon"}},
	}

	rows := buildQueueRows(issues, ListFilter{Labels: []string{"ux", "docs"}}, nil)
	assertQueueNumbers(t, rows, []int64{1})
}

func assertQueueNumbers(t *testing.T, rows []queueRow, want []int64) {
	t.Helper()
	if len(rows) != len(want) {
		t.Fatalf("rows len = %d, want %d: %+v", len(rows), len(want), rows)
	}
	for i, wantNumber := range want {
		if rows[i].issue.Number != wantNumber {
			t.Fatalf("row %d = #%d, want #%d (rows=%+v)", i, rows[i].issue.Number, wantNumber, rows)
		}
	}
}
