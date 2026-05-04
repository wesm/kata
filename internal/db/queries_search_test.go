package db_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wesm/kata/internal/db"
)

func TestFTS_IssueInsertIsIndexed(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p, err := d.CreateProject(ctx, "p", "p")
	require.NoError(t, err)
	_, _, err = d.CreateIssue(ctx, db.CreateIssueParams{
		ProjectID: p.ID, Title: "fix login crash", Body: "stack trace here", Author: "tester",
	})
	require.NoError(t, err)

	var hits int
	require.NoError(t, d.QueryRowContext(ctx,
		`SELECT count(*) FROM issues_fts WHERE issues_fts MATCH 'login'`).Scan(&hits))
	assert.Equal(t, 1, hits, "FTS should index issue.title on insert")

	require.NoError(t, d.QueryRowContext(ctx,
		`SELECT count(*) FROM issues_fts WHERE issues_fts MATCH 'trace'`).Scan(&hits))
	assert.Equal(t, 1, hits, "FTS should index issue.body on insert")
}

func TestFTS_IssueUpdateReindexes(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p, err := d.CreateProject(ctx, "p", "p")
	require.NoError(t, err)
	issue, _, err := d.CreateIssue(ctx, db.CreateIssueParams{
		ProjectID: p.ID, Title: "old title", Body: "initial body content", Author: "tester",
	})
	require.NoError(t, err)

	newTitle := "fresh title"
	_, _, _, err = d.EditIssue(ctx, db.EditIssueParams{
		IssueID: issue.ID, Title: &newTitle, Actor: "tester",
	})
	require.NoError(t, err)

	var oldHits, newHits int
	require.NoError(t, d.QueryRowContext(ctx,
		`SELECT count(*) FROM issues_fts WHERE issues_fts MATCH 'old'`).Scan(&oldHits))
	require.NoError(t, d.QueryRowContext(ctx,
		`SELECT count(*) FROM issues_fts WHERE issues_fts MATCH 'fresh'`).Scan(&newHits))
	assert.Equal(t, 0, oldHits, "old title tokens must be gone after edit")
	assert.Equal(t, 1, newHits, "new title tokens must be searchable after edit")
}

func TestFTS_CommentInsertReindexes(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p, err := d.CreateProject(ctx, "p", "p")
	require.NoError(t, err)
	issue, _, err := d.CreateIssue(ctx, db.CreateIssueParams{
		ProjectID: p.ID, Title: "boring", Body: "body", Author: "tester",
	})
	require.NoError(t, err)

	_, _, err = d.CreateComment(ctx, db.CreateCommentParams{
		IssueID: issue.ID, Author: "tester", Body: "watermelon",
	})
	require.NoError(t, err)

	var hits int
	require.NoError(t, d.QueryRowContext(ctx,
		`SELECT count(*) FROM issues_fts WHERE issues_fts MATCH 'watermelon'`).Scan(&hits))
	assert.Equal(t, 1, hits, "comment body must be searchable after insert")
}

func TestSearchFTS_RanksByBM25(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p, err := d.CreateProject(ctx, "p", "p")
	require.NoError(t, err)

	// Three issues. Only the first two mention "login"; the second has it
	// many more times. Issue 1's body is padded with unrelated text so token
	// density makes issue 2's BM25 win unambiguous regardless of column-length
	// normalization quirks.
	_, _, err = d.CreateIssue(ctx, db.CreateIssueParams{
		ProjectID: p.ID, Title: "fix login crash",
		Body:   "stack trace from a totally unrelated incident with many tokens to dilute density and dominate length normalization here",
		Author: "tester",
	})
	require.NoError(t, err)
	_, _, err = d.CreateIssue(ctx, db.CreateIssueParams{
		ProjectID: p.ID, Title: "login is broken on login screen",
		Body: "login fails twice login login login", Author: "tester",
	})
	require.NoError(t, err)
	_, _, err = d.CreateIssue(ctx, db.CreateIssueParams{
		ProjectID: p.ID, Title: "unrelated issue", Body: "no match here", Author: "tester",
	})
	require.NoError(t, err)

	got, err := d.SearchFTS(ctx, p.ID, "login", 20, false)
	require.NoError(t, err)
	assert.Len(t, got, 2)
	// The doubly-mentioned issue should outrank the singly-mentioned one.
	assert.Equal(t, int64(2), got[0].Issue.Number, "more matches → higher rank")
	assert.Equal(t, int64(1), got[1].Issue.Number)
	// Issue 2 has "login" in both title and body; issue 1 has it only in title.
	assert.Equal(t, []string{"title", "body"}, got[0].MatchedIn,
		"issue 2 has login in title AND body")
	assert.Equal(t, []string{"title"}, got[1].MatchedIn,
		"issue 1 has login only in title")
}

func TestSearchFTS_MatchedIn_Comments(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p, err := d.CreateProject(ctx, "p", "p")
	require.NoError(t, err)

	// Issue body does NOT contain the search term; only the comment does.
	issue, _, err := d.CreateIssue(ctx, db.CreateIssueParams{
		ProjectID: p.ID, Title: "unrelated", Body: "nothing here", Author: "tester",
	})
	require.NoError(t, err)
	_, _, err = d.CreateComment(ctx, db.CreateCommentParams{
		IssueID: issue.ID, Author: "tester", Body: "watermelon found",
	})
	require.NoError(t, err)

	got, err := d.SearchFTS(ctx, p.ID, "watermelon", 20, false)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, []string{"comments"}, got[0].MatchedIn,
		"term appears only in a comment")
}

func TestSearchFTS_MatchedIn_AllThreeColumns(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p, err := d.CreateProject(ctx, "p", "p")
	require.NoError(t, err)

	issue, _, err := d.CreateIssue(ctx, db.CreateIssueParams{
		ProjectID: p.ID, Title: "watermelon title", Body: "watermelon body", Author: "tester",
	})
	require.NoError(t, err)
	_, _, err = d.CreateComment(ctx, db.CreateCommentParams{
		IssueID: issue.ID, Author: "tester", Body: "watermelon comment",
	})
	require.NoError(t, err)

	got, err := d.SearchFTS(ctx, p.ID, "watermelon", 20, false)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, []string{"title", "body", "comments"}, got[0].MatchedIn,
		"matched_in must list columns in title/body/comments order")
}

func TestSearchFTS_FiltersByProject(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p1, err := d.CreateProject(ctx, "p1", "p1")
	require.NoError(t, err)
	p2, err := d.CreateProject(ctx, "p2", "p2")
	require.NoError(t, err)

	_, _, err = d.CreateIssue(ctx, db.CreateIssueParams{
		ProjectID: p1.ID, Title: "login bug", Body: "", Author: "tester",
	})
	require.NoError(t, err)
	_, _, err = d.CreateIssue(ctx, db.CreateIssueParams{
		ProjectID: p2.ID, Title: "login bug", Body: "", Author: "tester",
	})
	require.NoError(t, err)

	got, err := d.SearchFTS(ctx, p1.ID, "login", 20, false)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, p1.ID, got[0].Issue.ProjectID)
}

func TestSearchFTS_ExcludesDeletedByDefault(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p, err := d.CreateProject(ctx, "p", "p")
	require.NoError(t, err)
	keep, _, err := d.CreateIssue(ctx, db.CreateIssueParams{
		ProjectID: p.ID, Title: "keep login", Body: "", Author: "tester",
	})
	require.NoError(t, err)
	gone, _, err := d.CreateIssue(ctx, db.CreateIssueParams{
		ProjectID: p.ID, Title: "deleted login", Body: "", Author: "tester",
	})
	require.NoError(t, err)
	// Mark the second issue soft-deleted directly via SQL — Task 5 ships
	// SoftDeleteIssue but this test runs before that, and the DB-layer
	// behavior we want to verify is "search query filters deleted_at IS NULL".
	_, err = d.ExecContext(ctx,
		`UPDATE issues SET deleted_at = strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id = ?`,
		gone.ID)
	require.NoError(t, err)

	got, err := d.SearchFTS(ctx, p.ID, "login", 20, false)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, keep.ID, got[0].Issue.ID, "soft-deleted issue must be filtered")

	// includeDeleted=true returns both.
	got, err = d.SearchFTS(ctx, p.ID, "login", 20, true)
	require.NoError(t, err)
	assert.Len(t, got, 2)
}

func TestSearchFTS_EmptyQueryReturnsEmpty(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p, err := d.CreateProject(ctx, "p", "p")
	require.NoError(t, err)
	_, _, err = d.CreateIssue(ctx, db.CreateIssueParams{
		ProjectID: p.ID, Title: "anything", Body: "", Author: "tester",
	})
	require.NoError(t, err)

	got, err := d.SearchFTS(ctx, p.ID, "   ", 20, false)
	require.NoError(t, err)
	assert.Empty(t, got, "blank query → empty result, not an error")
}

func TestSearchFTS_LimitCappedAt200(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p, err := d.CreateProject(ctx, "p", "p")
	require.NoError(t, err)
	// Create 250 matching issues. The cap must clamp the result set to 200
	// regardless of how large a limit the caller passes; without the cap, this
	// test would return 250 and fail.
	for i := 0; i < 250; i++ {
		_, _, err = d.CreateIssue(ctx, db.CreateIssueParams{
			ProjectID: p.ID, Title: "login bug", Body: "", Author: "tester",
		})
		require.NoError(t, err)
	}
	got, err := d.SearchFTS(ctx, p.ID, "login", 1_000_000, false)
	require.NoError(t, err)
	assert.Len(t, got, 200, "limit must be capped at 200")
}

func TestSearchFTS_MultiTermImplicitAND(t *testing.T) {
	// Multi-term queries split on whitespace and AND the terms. "login Safari"
	// must match an issue that contains both terms even when they aren't
	// adjacent — the previous single-phrase wrap missed this.
	d := openTestDB(t)
	ctx := context.Background()
	p, err := d.CreateProject(ctx, "p", "p")
	require.NoError(t, err)

	_, _, err = d.CreateIssue(ctx, db.CreateIssueParams{
		ProjectID: p.ID, Title: "fix login crash on Safari", Author: "tester",
	})
	require.NoError(t, err)
	_, _, err = d.CreateIssue(ctx, db.CreateIssueParams{
		ProjectID: p.ID, Title: "login bug", Body: "", Author: "tester",
	})
	require.NoError(t, err)
	_, _, err = d.CreateIssue(ctx, db.CreateIssueParams{
		ProjectID: p.ID, Title: "Safari preferences", Body: "", Author: "tester",
	})
	require.NoError(t, err)

	got, err := d.SearchFTS(ctx, p.ID, "login Safari", 20, false)
	require.NoError(t, err)
	require.Len(t, got, 1, "only the issue containing both terms must match")
	assert.Equal(t, "fix login crash on Safari", got[0].Issue.Title)
}

func TestSearchFTS_QueryEscaping(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p, err := d.CreateProject(ctx, "p", "p")
	require.NoError(t, err)
	_, _, err = d.CreateIssue(ctx, db.CreateIssueParams{
		ProjectID: p.ID, Title: `fix "login" crash`, Body: "", Author: "tester",
	})
	require.NoError(t, err)

	// FTS5 syntax characters must not surface as syntax errors.
	got, err := d.SearchFTS(ctx, p.ID, `"login"`, 20, false)
	require.NoError(t, err)
	assert.Len(t, got, 1)
}

// TestSearchFTS_MatchedIn_CrossColumn pins the matched_in fix from roborev
// 16789: when the query is multi-term implicit-AND and the tokens land in
// different columns (title="login", body="Safari"), the per-column subqueries
// must use OR semantics so each column reports as matched. With the old
// AND-based per-column subqueries this returned matched_in=[].
func TestSearchFTS_MatchedIn_CrossColumn(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p, err := d.CreateProject(ctx, "p", "p")
	require.NoError(t, err)
	_, _, err = d.CreateIssue(ctx, db.CreateIssueParams{
		ProjectID: p.ID, Title: "login bug", Body: "Safari issue", Author: "tester",
	})
	require.NoError(t, err)

	got, err := d.SearchFTS(ctx, p.ID, "login Safari", 20, false)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.ElementsMatch(t, []string{"title", "body"}, got[0].MatchedIn)
}

// TestSearchFTS_OperatorWordsAsLiterals pins #23: FTS5 reserved words and
// special characters in the user query must be treated as literal phrase
// content, not as FTS5 syntax. The escaping pass quotes each whitespace-split
// token and doubles embedded quotes, which neutralizes NEAR/OR/AND/NOT and
// the `*` prefix-match marker.
func TestSearchFTS_OperatorWordsAsLiterals(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p, err := d.CreateProject(ctx, "p", "p")
	require.NoError(t, err)
	_, _, err = d.CreateIssue(ctx, db.CreateIssueParams{
		ProjectID: p.ID, Title: "do NOT merge yet", Body: "blocked OR waiting", Author: "tester",
	})
	require.NoError(t, err)
	_, _, err = d.CreateIssue(ctx, db.CreateIssueParams{
		ProjectID: p.ID, Title: "merge after review", Author: "tester",
	})
	require.NoError(t, err)
	// Seeded so AND/NEAR-as-literals can be exercised: each operator-bearing
	// row pairs with a competitor that has the surrounding tokens but lacks
	// the operator word itself. Under operator semantics ("build" AND
	// "deploy") both rows would match; under correct literal semantics only
	// the row containing the literal operator word matches. The test asserts
	// exactly one match, so a regression that lost the FTS5 phrase-quoting
	// fails here.
	_, _, err = d.CreateIssue(ctx, db.CreateIssueParams{
		ProjectID: p.ID, Title: "build AND deploy pipeline", Author: "tester",
	})
	require.NoError(t, err)
	_, _, err = d.CreateIssue(ctx, db.CreateIssueParams{
		ProjectID: p.ID, Title: "build deploy pipeline", Author: "tester",
	})
	require.NoError(t, err)
	_, _, err = d.CreateIssue(ctx, db.CreateIssueParams{
		ProjectID: p.ID, Title: "place item NEAR exit", Author: "tester",
	})
	require.NoError(t, err)
	_, _, err = d.CreateIssue(ctx, db.CreateIssueParams{
		ProjectID: p.ID, Title: "place item by exit", Author: "tester",
	})
	require.NoError(t, err)

	cases := []struct {
		name, query, wantTitle string
	}{
		{"NOT as literal", "NOT merge", "do NOT merge yet"},
		{"OR as literal", "blocked OR", "do NOT merge yet"},
		{"AND as literal", "build AND deploy", "build AND deploy pipeline"},
		{"NEAR as literal", "item NEAR exit", "place item NEAR exit"},
		{"star is literal not prefix", "merg*", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := d.SearchFTS(ctx, p.ID, tc.query, 20, false)
			require.NoError(t, err, "query %q must parse without FTS5 syntax error", tc.query)
			if tc.wantTitle == "" {
				assert.Len(t, got, 0, "wildcard must not act as prefix-match")
				return
			}
			require.Len(t, got, 1, "query %q must match exactly one row", tc.query)
			assert.Equal(t, tc.wantTitle, got[0].Issue.Title)
		})
	}
}

// TestSearchFTSAny_FindsNearDuplicates pins the look-alike fix from roborev
// 16791: implicit-AND search misses a row that's a near-duplicate but lacks
// one of the query tokens; the OR variant retrieves it so similarity.Score
// can decide. With AND, "login crash Safari critical" excludes a row that
// only contains "login crash Safari" (no "critical").
func TestSearchFTSAny_FindsNearDuplicates(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p, err := d.CreateProject(ctx, "p", "p")
	require.NoError(t, err)
	_, _, err = d.CreateIssue(ctx, db.CreateIssueParams{
		ProjectID: p.ID, Title: "login crash on Safari", Body: "stack trace", Author: "tester",
	})
	require.NoError(t, err)

	// Implicit-AND: query has an extra token ("critical") that the row lacks
	// — the row is filtered out before similarity scoring would see it.
	gotAnd, err := d.SearchFTS(ctx, p.ID, "login crash Safari critical", 20, false)
	require.NoError(t, err)
	assert.Len(t, gotAnd, 0, "AND form must filter rows missing any token")

	// OR variant: same query, but the row matches because it contains at
	// least one of the tokens. similarity.Score in the handler decides
	// whether to actually treat this as a duplicate.
	gotOr, err := d.SearchFTSAny(ctx, p.ID, "login crash Safari critical", 20, false)
	require.NoError(t, err)
	require.Len(t, gotOr, 1, "OR form must retrieve near-duplicates")
	assert.Equal(t, "login crash on Safari", gotOr[0].Issue.Title)
}
