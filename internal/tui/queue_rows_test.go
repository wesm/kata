package tui

import "testing"

func TestBuildQueueRows_CollapsedShowsTopLevelOnly(t *testing.T) {
	issues := []Issue{
		testIssue(1, withCounts(1, 1)),
		testIssue(2, withParent(1)),
		testIssue(3),
	}

	rows := buildQueueRows(issues, ListFilter{}, nil)
	assertQueueNumbers(t, rows, []int64{1, 3})
	if !rows[0].hasChildren {
		t.Fatal("parent row should report children")
	}
}

func TestBuildQueueRows_ExpandedShowsDirectChildren(t *testing.T) {
	issues := []Issue{
		testIssue(1, withCounts(2, 2)),
		testIssue(2, withParent(1), withCounts(1, 1)),
		testIssue(3, withParent(1)),
		testIssue(4, withParent(2)),
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
		testIssue(1, withCounts(3, 3)),
		testIssue(2, withParent(1)),
		testIssue(3, withParent(1), withBlocks(2)),
		testIssue(4, withParent(1)),
	}

	rows := buildQueueRows(issues, ListFilter{}, expansionSet{{projectID: 7, number: 1}: true})
	assertQueueNumbers(t, rows, []int64{1, 3, 2, 4})
}

func TestBuildQueueRows_TemporalChildSortPreservesFetchOrder(t *testing.T) {
	issues := []Issue{
		testIssue(1, withCounts(3, 3)),
		testIssue(2, withParent(1)),
		testIssue(3, withParent(1), withBlocks(2)),
		testIssue(4, withParent(1)),
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
		testIssue(1),
		testIssue(2, withTitle("detail hint bars incomplete"), withParent(1)),
		testIssue(3),
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
		testIssue(1, withStatus("open")),
		testIssue(2, withStatus("closed")),
	}

	rows := buildQueueRows(issues, ListFilter{Status: "closed"}, nil)
	assertQueueNumbers(t, rows, []int64{2})
}

func TestBuildQueueRows_LabelsFilterAnyOf(t *testing.T) {
	issues := []Issue{
		testIssue(1, withLabels("bug", "ux")),
		testIssue(2, withLabels("daemon")),
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
