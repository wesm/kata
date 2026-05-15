package db_test

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wesm/kata/internal/db"
)

// titleSet extracts the Title field from each issue for use in assert.ElementsMatch.
func titleSet(issues []db.Issue) []string {
	out := make([]string, 0, len(issues))
	for _, i := range issues {
		out = append(out, i.Title)
	}
	return out
}

func TestListIssuesByView_TodayIncludesPastScheduledAndPastDeadline(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p, _ := d.CreateProject(ctx, "p")
	seedIssueWithMetadata(t, d, p.ID, "A", `{"scheduled_on":"2026-05-15"}`)
	seedIssueWithMetadata(t, d, p.ID, "B", `{"scheduled_on":"2026-05-10"}`)
	seedIssueWithMetadata(t, d, p.ID, "C", `{"scheduled_on":"2026-06-01"}`)
	seedIssueWithMetadata(t, d, p.ID, "D", `{"deadline_on":"2026-05-15"}`)
	seedIssueWithMetadata(t, d, p.ID, "E", `{"deadline_on":"2026-05-10"}`)
	seedIssueWithMetadata(t, d, p.ID, "F", `{"deadline_on":"2026-06-01"}`)

	issues, err := d.ListIssuesByView(ctx, db.ListIssuesByViewIn{
		View: "today", TodayDate: "2026-05-15",
	})
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"A", "B", "D", "E"}, titleSet(issues))
}

func TestListIssuesByView_UpcomingIsStrictlyFuture(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p, _ := d.CreateProject(ctx, "p")
	seedIssueWithMetadata(t, d, p.ID, "A", `{"scheduled_on":"2026-05-15"}`)
	seedIssueWithMetadata(t, d, p.ID, "B", `{"scheduled_on":"2026-05-16"}`)

	issues, err := d.ListIssuesByView(ctx, db.ListIssuesByViewIn{
		View: "upcoming", TodayDate: "2026-05-15",
	})
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"B"}, titleSet(issues))
}

func TestListIssuesByView_InboxFiltersBySentinelProject(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	inbox, _ := d.CreateProject(ctx, "Inbox")
	other, _ := d.CreateProject(ctx, "other")
	seedIssueInProject(t, d, inbox.ID, "In1", "tester")
	seedIssueInProject(t, d, inbox.ID, "In2", "tester")
	seedIssueInProject(t, d, other.ID, "Elsewhere", "tester")

	issues, err := d.ListIssuesByView(ctx, db.ListIssuesByViewIn{
		View: "inbox", TodayDate: "2026-05-15", InboxProjectID: inbox.ID,
	})
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"In1", "In2"}, titleSet(issues))
}

func TestListIssuesByView_AnytimeExcludesScheduledSomedayAndInbox(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	inbox, _ := d.CreateProject(ctx, "Inbox")
	seedIssueInProject(t, d, inbox.ID, "InboxIssue", "tester")
	p, _ := d.CreateProject(ctx, "p")
	seedIssueWithMetadata(t, d, p.ID, "Sched", `{"scheduled_on":"2026-05-20"}`)
	seedIssueWithMetadata(t, d, p.ID, "Some", `{"someday":true}`)
	seedIssueWithMetadata(t, d, p.ID, "Plain", `{}`)

	issues, err := d.ListIssuesByView(ctx, db.ListIssuesByViewIn{
		View: "anytime", TodayDate: "2026-05-15", InboxProjectID: inbox.ID,
	})
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"Plain"}, titleSet(issues))
}

func TestListIssuesByView_SomedayOnlyTrueFlag(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p, _ := d.CreateProject(ctx, "p")
	seedIssueWithMetadata(t, d, p.ID, "Yes", `{"someday":true}`)
	seedIssueWithMetadata(t, d, p.ID, "No", `{"someday":false}`)
	seedIssueWithMetadata(t, d, p.ID, "None", `{}`)

	issues, err := d.ListIssuesByView(ctx, db.ListIssuesByViewIn{
		View: "someday", TodayDate: "2026-05-15",
	})
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"Yes"}, titleSet(issues))
}

func TestListIssuesByView_LogbookIsClosedOnly(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p, _ := d.CreateProject(ctx, "p")
	seedClosedIssue(t, d, p.ID, "OldOne", "done")
	seedIssueWithMetadata(t, d, p.ID, "StillOpen", `{}`)

	issues, err := d.ListIssuesByView(ctx, db.ListIssuesByViewIn{
		View: "logbook", TodayDate: "2026-05-15",
	})
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"OldOne"}, titleSet(issues))
}

func TestListIssuesByView_UnknownViewRejected(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	_, err := d.ListIssuesByView(ctx, db.ListIssuesByViewIn{View: "bogus"})
	require.Error(t, err)
}

func TestListIssuesByView_AreaFilter(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p1, _ := d.CreateProject(ctx, "p1")
	p2, _ := d.CreateProject(ctx, "p2")
	_, err := d.ExecContext(ctx,
		`UPDATE projects SET metadata = json_set(metadata, '$.area', 'Personal') WHERE id = ?`, p1.ID)
	require.NoError(t, err)
	seedIssueWithMetadata(t, d, p1.ID, "InArea", `{}`)
	seedIssueWithMetadata(t, d, p2.ID, "Outside", `{}`)

	inbox, _ := d.CreateProject(ctx, "Inbox")
	issues, err := d.ListIssuesByView(ctx, db.ListIssuesByViewIn{
		View: "anytime", TodayDate: "2026-05-15",
		InboxProjectID: inbox.ID, Area: "Personal",
	})
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"InArea"}, titleSet(issues))
}

// TestListIssuesByView_TodayPlanUsesAnIndex is a D4 sanity check: the SQLite
// query planner must pick at least one index for the Today predicate rather
// than falling back to a full table scan. If the scheduled_on partial index is
// not chosen, a soft log message is emitted so the reviewer knows to evaluate
// the secondary-index decision; the hard assertion only requires any index use.
func TestListIssuesByView_TodayPlanUsesAnIndex(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	rows, err := d.QueryContext(ctx, `EXPLAIN QUERY PLAN
        SELECT id FROM issues
         WHERE deleted_at IS NULL
           AND status = 'open'
           AND json_extract(metadata,'$.scheduled_on') <= '2026-05-15'`)
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()

	sawAnyIndex := false
	sawScheduledIndex := false
	for rows.Next() {
		var id, parent, notUsed int
		var detail string
		require.NoError(t, rows.Scan(&id, &parent, &notUsed, &detail))
		lower := strings.ToLower(detail)
		if strings.Contains(lower, "using index") || strings.Contains(lower, "search using index") {
			sawAnyIndex = true
		}
		if strings.Contains(lower, "scheduled_on") {
			sawScheduledIndex = true
		}
	}
	require.NoError(t, rows.Err())

	if !sawScheduledIndex {
		t.Logf("planner did not use the scheduled_on partial index; review §D4 secondary-index decision")
	}
	assert.True(t, sawAnyIndex, "planner must use some index, not a full table scan")
}

func TestExpressionIndexesPresent(t *testing.T) {
	d := openTestDB(t)
	want := []string{
		"issues_project_scheduled_on_open",
		"issues_project_deadline_on_open",
		"issues_project_someday_open",
		"projects_area",
	}
	for _, name := range want {
		var n int
		err := d.QueryRow(`SELECT COUNT(*) FROM sqlite_master
			WHERE type='index' AND name=?`, name).Scan(&n)
		require.NoError(t, err)
		assert.Equal(t, 1, n, "index %s missing", name)
	}
}
