package db_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wesm/kata/internal/db"
	"github.com/wesm/kata/internal/uid"
)

func openTestDB(t *testing.T) *db.DB {
	t.Helper()
	d, err := db.Open(context.Background(), filepath.Join(t.TempDir(), "kata.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = d.Close() })
	return d
}

func TestCreateProject_RoundTrips(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	p, err := d.CreateProject(ctx, "github.com/wesm/kata", "kata")
	require.NoError(t, err)
	assert.Equal(t, "github.com/wesm/kata", p.Identity)
	assert.Equal(t, "kata", p.Name)
	assertValidUID(t, p.UID)
	assert.Equal(t, int64(1), p.NextIssueNumber)
	assert.False(t, p.CreatedAt.IsZero())

	got, err := d.ProjectByIdentity(ctx, "github.com/wesm/kata")
	require.NoError(t, err)
	assert.Equal(t, p.ID, got.ID)
	assert.Equal(t, p.UID, got.UID)
}

func assertValidUID(t *testing.T, got string) {
	t.Helper()
	require.Len(t, got, 26)
	assert.True(t, uid.Valid(got), "invalid UID %q", got)
}

func TestProjectByIdentity_NotFound(t *testing.T) {
	d := openTestDB(t)
	_, err := d.ProjectByIdentity(context.Background(), "missing")
	assert.ErrorIs(t, err, db.ErrNotFound)
}

func TestCreateProject_DuplicateIdentity(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	_, err := d.CreateProject(ctx, "x", "x")
	require.NoError(t, err)
	_, err = d.CreateProject(ctx, "x", "x")
	require.Error(t, err)
}

func TestRenameProject_UpdatesNameOnly(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p, err := d.CreateProject(ctx, "github.com/wesm/kata", "kata")
	require.NoError(t, err)
	alias, err := d.AttachAlias(ctx, p.ID, "github.com/wesm/kata", "git", "/tmp/kata")
	require.NoError(t, err)

	renamed, err := d.RenameProject(ctx, p.ID, "Kata Tracker")
	require.NoError(t, err)
	assert.Equal(t, p.ID, renamed.ID)
	assert.Equal(t, "github.com/wesm/kata", renamed.Identity)
	assert.Equal(t, "Kata Tracker", renamed.Name)
	assert.Equal(t, p.NextIssueNumber, renamed.NextIssueNumber)

	got, err := d.ProjectByID(ctx, p.ID)
	require.NoError(t, err)
	assert.Equal(t, "Kata Tracker", got.Name)
	aliases, err := d.ProjectAliases(ctx, p.ID)
	require.NoError(t, err)
	require.Len(t, aliases, 1)
	assert.Equal(t, alias.ID, aliases[0].ID)
}

func TestRenameProject_MissingReturnsErrNotFound(t *testing.T) {
	d := openTestDB(t)
	_, err := d.RenameProject(context.Background(), 9999, "missing")
	assert.ErrorIs(t, err, db.ErrNotFound)
}

func TestAttachAlias_AndLookup(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p, err := d.CreateProject(ctx, "github.com/wesm/kata", "kata")
	require.NoError(t, err)

	a, err := d.AttachAlias(ctx, p.ID, "github.com/wesm/kata", "git", "/tmp/x")
	require.NoError(t, err)
	assert.Equal(t, p.ID, a.ProjectID)
	assert.Equal(t, "git", a.AliasKind)

	got, err := d.AliasByIdentity(ctx, "github.com/wesm/kata")
	require.NoError(t, err)
	assert.Equal(t, a.ID, got.ID)
}

func TestAliasByIdentity_NotFound(t *testing.T) {
	d := openTestDB(t)
	_, err := d.AliasByIdentity(context.Background(), "missing")
	assert.ErrorIs(t, err, db.ErrNotFound)
}

func TestTouchAlias_UpdatesLastSeen(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p, err := d.CreateProject(ctx, "x", "x")
	require.NoError(t, err)
	a, err := d.AttachAlias(ctx, p.ID, "x", "git", "/tmp/x")
	require.NoError(t, err)

	require.NoError(t, d.TouchAlias(ctx, a.ID, "/tmp/y"))
	got, err := d.AliasByIdentity(ctx, "x")
	require.NoError(t, err)
	assert.Equal(t, "/tmp/y", got.RootPath)
	assert.True(t, !got.LastSeenAt.Before(a.LastSeenAt))
}

func TestTouchAlias_MissingAliasReturnsErrNotFound(t *testing.T) {
	d := openTestDB(t)
	err := d.TouchAlias(context.Background(), 9999, "/tmp/x")
	assert.ErrorIs(t, err, db.ErrNotFound)
}

func TestListProjects_Empty(t *testing.T) {
	d := openTestDB(t)
	got, err := d.ListProjects(context.Background())
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestListProjects_OrdersByIDAsc(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	_, _ = d.CreateProject(ctx, "a", "a")
	_, _ = d.CreateProject(ctx, "b", "b")

	got, err := d.ListProjects(ctx)
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, "a", got[0].Identity)
	assert.Equal(t, "b", got[1].Identity)
}

func TestProjectAliases_ReturnsAllForProject(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p, _ := d.CreateProject(ctx, "p", "p")
	_, _ = d.AttachAlias(ctx, p.ID, "alias-a", "git", "/tmp/a")
	_, _ = d.AttachAlias(ctx, p.ID, "alias-b", "git", "/tmp/b")

	got, err := d.ProjectAliases(ctx, p.ID)
	require.NoError(t, err)
	assert.Len(t, got, 2)
}

func TestMergeProjects_MovesSourceIntoSurvivingTarget(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	kenn, err := d.CreateProject(ctx, "github.com/wesm/kenn", "kenn")
	require.NoError(t, err)
	steward, err := d.CreateProject(ctx, "github.com/wesm/steward", "steward")
	require.NoError(t, err)
	_, err = d.AttachAlias(ctx, kenn.ID, "github.com/wesm/kenn", "git", "/tmp/kenn")
	require.NoError(t, err)
	_, err = d.AttachAlias(ctx, steward.ID, "github.com/wesm/steward", "git", "/tmp/steward")
	require.NoError(t, err)
	parent, _, err := d.CreateIssue(ctx, db.CreateIssueParams{
		ProjectID: kenn.ID, Title: "parent", Author: "tester",
	})
	require.NoError(t, err)
	child, _, err := d.CreateIssue(ctx, db.CreateIssueParams{
		ProjectID: kenn.ID, Title: "child", Author: "tester",
	})
	require.NoError(t, err)
	_, _, err = d.CreateLinkAndEvent(ctx, db.CreateLinkParams{
		ProjectID: kenn.ID, FromIssueID: child.ID, ToIssueID: parent.ID, Type: "parent", Author: "tester",
	}, db.LinkEventParams{
		EventType: "issue.linked", EventIssueID: child.ID, EventIssueNumber: child.Number,
		FromNumber: child.Number, ToNumber: parent.Number, Actor: "tester",
	})
	require.NoError(t, err)

	merged, err := d.MergeProjects(ctx, db.MergeProjectsParams{
		SourceProjectID: kenn.ID,
		TargetProjectID: steward.ID,
	})
	require.NoError(t, err)
	assert.Equal(t, steward.ID, merged.Target.ID)
	assert.Equal(t, "github.com/wesm/steward", merged.Target.Identity)
	assert.Equal(t, "steward", merged.Target.Name)
	assert.Equal(t, int64(2), merged.IssuesMoved)
	assert.Equal(t, int64(1), merged.AliasesMoved)
	assert.Equal(t, int64(3), merged.EventsMoved)
	assert.Equal(t, int64(3), merged.Target.NextIssueNumber)

	gotParent, err := d.IssueByNumber(ctx, steward.ID, 1)
	require.NoError(t, err)
	assert.Equal(t, "parent", gotParent.Title)
	assert.Equal(t, steward.ID, gotParent.ProjectID)
	gotChild, err := d.IssueByNumber(ctx, steward.ID, 2)
	require.NoError(t, err)
	assert.Equal(t, "child", gotChild.Title)
	assert.Equal(t, steward.ID, gotChild.ProjectID)
	_, err = d.IssueByNumber(ctx, kenn.ID, 1)
	assert.ErrorIs(t, err, db.ErrNotFound)
	_, err = d.ProjectByID(ctx, kenn.ID)
	assert.ErrorIs(t, err, db.ErrNotFound)

	aliases, err := d.ProjectAliases(ctx, steward.ID)
	require.NoError(t, err)
	var aliasIdentities []string
	for _, alias := range aliases {
		aliasIdentities = append(aliasIdentities, alias.AliasIdentity)
		assert.Equal(t, steward.ID, alias.ProjectID)
	}
	assert.ElementsMatch(t, []string{"github.com/wesm/kenn", "github.com/wesm/steward"}, aliasIdentities)

	events, err := d.EventsAfter(ctx, db.EventsAfterParams{ProjectID: steward.ID, Limit: 10})
	require.NoError(t, err)
	require.Len(t, events, 3)
	for _, event := range events {
		assert.Equal(t, steward.ID, event.ProjectID)
		assert.Equal(t, "github.com/wesm/steward", event.ProjectIdentity)
	}
}

func TestMergeProjects_PreservesSourceIdentityWhenAliasAlreadyTargetsTarget(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	source, err := d.CreateProject(ctx, "github.com/wesm/old", "old")
	require.NoError(t, err)
	target, err := d.CreateProject(ctx, "github.com/wesm/new", "new")
	require.NoError(t, err)
	_, err = d.AttachAlias(ctx, target.ID, "github.com/wesm/old", "git", "/tmp/old")
	require.NoError(t, err)

	_, err = d.MergeProjects(ctx, db.MergeProjectsParams{
		SourceProjectID: source.ID,
		TargetProjectID: target.ID,
	})
	require.NoError(t, err)

	got, err := d.AliasByIdentity(ctx, "github.com/wesm/old")
	require.NoError(t, err)
	assert.Equal(t, target.ID, got.ProjectID)
}

func TestMergeProjects_RejectsSourceIdentityAliasOwnedByDifferentProject(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	source, err := d.CreateProject(ctx, "github.com/wesm/old", "old")
	require.NoError(t, err)
	target, err := d.CreateProject(ctx, "github.com/wesm/new", "new")
	require.NoError(t, err)
	other, err := d.CreateProject(ctx, "github.com/wesm/other", "other")
	require.NoError(t, err)
	_, err = d.AttachAlias(ctx, other.ID, "github.com/wesm/old", "git", "/tmp/old")
	require.NoError(t, err)

	_, err = d.MergeProjects(ctx, db.MergeProjectsParams{
		SourceProjectID: source.ID,
		TargetProjectID: target.ID,
	})
	require.Error(t, err)

	got, lookupErr := d.ProjectByID(ctx, source.ID)
	require.NoError(t, lookupErr)
	assert.Equal(t, "github.com/wesm/old", got.Identity)
}

// TestMergeProjects_RejectsArchivedSource pins the #24 invariant: an
// archived source can't be merged because that would resurrect its identity
// into the target. Restore-then-merge would be required if/when restore
// ships.
func TestMergeProjects_RejectsArchivedSource(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	source, err := d.CreateProject(ctx, "github.com/wesm/archived-src", "src")
	require.NoError(t, err)
	target, err := d.CreateProject(ctx, "github.com/wesm/live-tgt", "tgt")
	require.NoError(t, err)
	_, _, err = d.RemoveProject(ctx, db.RemoveProjectParams{ProjectID: source.ID, Actor: "tester"})
	require.NoError(t, err)

	_, err = d.MergeProjects(ctx, db.MergeProjectsParams{
		SourceProjectID: source.ID, TargetProjectID: target.ID,
	})
	assert.ErrorIs(t, err, db.ErrProjectMergeArchivedSource)
}

// TestMergeProjects_RejectsArchivedTarget pins the symmetric guard: folding
// live work into an archived project undoes the archive's intent.
func TestMergeProjects_RejectsArchivedTarget(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	source, err := d.CreateProject(ctx, "github.com/wesm/live-src", "src")
	require.NoError(t, err)
	target, err := d.CreateProject(ctx, "github.com/wesm/archived-tgt", "tgt")
	require.NoError(t, err)
	_, _, err = d.RemoveProject(ctx, db.RemoveProjectParams{ProjectID: target.ID, Actor: "tester"})
	require.NoError(t, err)

	_, err = d.MergeProjects(ctx, db.MergeProjectsParams{
		SourceProjectID: source.ID, TargetProjectID: target.ID,
	})
	assert.ErrorIs(t, err, db.ErrProjectMergeArchivedTarget)
}

func TestMergeProjects_IssueNumberCollisionReturnsError(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	source, err := d.CreateProject(ctx, "github.com/wesm/kenn", "kenn")
	require.NoError(t, err)
	target, err := d.CreateProject(ctx, "github.com/wesm/steward", "steward")
	require.NoError(t, err)
	_, _, err = d.CreateIssue(ctx, db.CreateIssueParams{
		ProjectID: source.ID, Title: "source issue", Author: "tester",
	})
	require.NoError(t, err)
	_, _, err = d.CreateIssue(ctx, db.CreateIssueParams{
		ProjectID: target.ID, Title: "target issue", Author: "tester",
	})
	require.NoError(t, err)

	_, err = d.MergeProjects(ctx, db.MergeProjectsParams{
		SourceProjectID: source.ID,
		TargetProjectID: target.ID,
	})
	require.ErrorIs(t, err, db.ErrProjectMergeIssueNumberCollision)

	got, lookupErr := d.ProjectByID(ctx, source.ID)
	require.NoError(t, lookupErr)
	assert.Equal(t, "github.com/wesm/kenn", got.Identity)
}

func TestResetIssueCounter_EmptyProjectMovesCounter(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p, err := d.CreateProject(ctx, "p", "p")
	require.NoError(t, err)

	require.NoError(t, d.ResetIssueCounter(ctx, p.ID, 42))

	p2, err := d.ProjectByID(ctx, p.ID)
	require.NoError(t, err)
	assert.EqualValues(t, 42, p2.NextIssueNumber)
}

func TestResetIssueCounter_ReturnsTypedErrorWithCount(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p, err := d.CreateProject(ctx, "p", "p")
	require.NoError(t, err)
	for range 3 {
		_, _, err := d.CreateIssue(ctx, db.CreateIssueParams{ProjectID: p.ID, Title: "x", Author: "a"})
		require.NoError(t, err)
	}
	before, err := d.ProjectByID(ctx, p.ID)
	require.NoError(t, err)

	err = d.ResetIssueCounter(ctx, p.ID, 1)
	var hasIssues *db.ProjectHasIssuesError
	require.ErrorAs(t, err, &hasIssues)
	assert.EqualValues(t, 3, hasIssues.Count)

	after, err := d.ProjectByID(ctx, p.ID)
	require.NoError(t, err)
	assert.Equal(t, before.NextIssueNumber, after.NextIssueNumber, "counter must not move when gate trips")
}

func TestResetIssueCounter_ProjectNotFound(t *testing.T) {
	d := openTestDB(t)
	err := d.ResetIssueCounter(context.Background(), 9999, 1)
	assert.ErrorIs(t, err, db.ErrNotFound)
}

func TestResetIssueCounter_RejectsInvalidTo(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p, err := d.CreateProject(ctx, "p", "p")
	require.NoError(t, err)

	for _, to := range []int64{0, -1, -42} {
		err := d.ResetIssueCounter(ctx, p.ID, to)
		assert.ErrorIs(t, err, db.ErrInvalidCounterValue, "to=%d", to)
	}
	// Counter must remain at its initial value.
	p2, err := d.ProjectByID(ctx, p.ID)
	require.NoError(t, err)
	assert.EqualValues(t, 1, p2.NextIssueNumber)
}

// Covers the production scenario: project accumulated issues that were all
// purged, then the user resets the counter to start over at 1.
func TestResetIssueCounter_SucceedsAfterPurge(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p, err := d.CreateProject(ctx, "p", "p")
	require.NoError(t, err)

	var issueIDs []int64
	for range 3 {
		issue, _, err := d.CreateIssue(ctx, db.CreateIssueParams{ProjectID: p.ID, Title: "x", Author: "a"})
		require.NoError(t, err)
		issueIDs = append(issueIDs, issue.ID)
	}
	for _, id := range issueIDs {
		_, err := d.PurgeIssue(ctx, id, "tester", nil)
		require.NoError(t, err)
	}

	require.NoError(t, d.ResetIssueCounter(ctx, p.ID, 1))

	p2, err := d.ProjectByID(ctx, p.ID)
	require.NoError(t, err)
	assert.EqualValues(t, 1, p2.NextIssueNumber)
}

// Guards against splitting the gate back into count-then-update — the
// empty-check must be atomic with the write so a concurrent CreateIssue
// can't slip between them.
func TestResetIssueCounter_GateLivesInUpdate(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p, err := d.CreateProject(ctx, "p", "p")
	require.NoError(t, err)
	_, _, err = d.CreateIssue(ctx, db.CreateIssueParams{ProjectID: p.ID, Title: "x", Author: "a"})
	require.NoError(t, err)

	before, err := d.ProjectByID(ctx, p.ID)
	require.NoError(t, err)

	err = d.ResetIssueCounter(ctx, p.ID, 999)
	var hasIssues *db.ProjectHasIssuesError
	require.ErrorAs(t, err, &hasIssues)

	after, err := d.ProjectByID(ctx, p.ID)
	require.NoError(t, err)
	assert.Equal(t, before.NextIssueNumber, after.NextIssueNumber)
}

func TestBatchProjectStats_EmptyProjectReturnsZeroes(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p, err := d.CreateProject(ctx, "github.com/wesm/empty", "empty")
	require.NoError(t, err)

	stats, err := d.BatchProjectStats(ctx)
	require.NoError(t, err)

	require.Contains(t, stats, p.ID)
	s := stats[p.ID]
	assert.Equal(t, 0, s.Open)
	assert.Equal(t, 0, s.Closed)
	assert.Nil(t, s.LastEventAt, "no events → LastEventAt is nil")
}

// TestBatchProjectStats_NoCountInflation pins the spec §6.1 contract:
// the issues-and-events join MUST be pre-aggregated, otherwise N issues
// times M events would inflate counts. Three issues + four events on the
// same project must still report Open=3.
func TestBatchProjectStats_NoCountInflation(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p, err := d.CreateProject(ctx, "github.com/wesm/proj", "proj")
	require.NoError(t, err)
	for i := 0; i < 3; i++ {
		_, _, err := d.CreateIssue(ctx, db.CreateIssueParams{
			ProjectID: p.ID,
			Title:     "i",
			Body:      "",
			Author:    "tester",
		})
		require.NoError(t, err)
	}
	iss, err := d.IssueByNumber(ctx, p.ID, 1)
	require.NoError(t, err)
	_, _, err = d.CreateComment(ctx, db.CreateCommentParams{
		IssueID: iss.ID,
		Author:  "tester",
		Body:    "note",
	})
	require.NoError(t, err)

	stats, err := d.BatchProjectStats(ctx)
	require.NoError(t, err)
	require.Contains(t, stats, p.ID)
	assert.Equal(t, 3, stats[p.ID].Open, "must not inflate by event count")
	assert.Equal(t, 0, stats[p.ID].Closed)
	assert.NotNil(t, stats[p.ID].LastEventAt)
}

// TestBatchProjectStats_ExcludesSoftDeletedIssues pins that issues with
// deleted_at != NULL do not count toward Open/Closed. SoftDeleteIssue is
// the right primitive — PurgeIssue would hard-delete the row and the
// `WHERE deleted_at IS NULL` filter would never get a chance to exercise
// itself. Spec §6.1.
func TestBatchProjectStats_ExcludesSoftDeletedIssues(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p, err := d.CreateProject(ctx, "github.com/wesm/proj", "proj")
	require.NoError(t, err)
	_, _, err = d.CreateIssue(ctx, db.CreateIssueParams{
		ProjectID: p.ID, Title: "live", Body: "", Author: "tester",
	})
	require.NoError(t, err)
	soft, _, err := d.CreateIssue(ctx, db.CreateIssueParams{
		ProjectID: p.ID, Title: "soft", Body: "", Author: "tester",
	})
	require.NoError(t, err)
	_, _, _, err = d.SoftDeleteIssue(ctx, soft.ID, "tester")
	require.NoError(t, err)
	// Sanity: the soft-deleted row still exists with deleted_at set, so
	// the filter is what's actually doing the work.
	got, err := d.IssueByID(ctx, soft.ID)
	require.NoError(t, err)
	require.NotNil(t, got.DeletedAt, "soft-delete must leave row with deleted_at set")

	stats, err := d.BatchProjectStats(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, stats[p.ID].Open, "soft-deleted issue must not count")
}

// TestBatchProjectStats_ExcludesArchivedProjects pins that archived
// projects don't appear in the result map at all. Spec §6.1.
func TestBatchProjectStats_ExcludesArchivedProjects(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	live, err := d.CreateProject(ctx, "github.com/wesm/live", "live")
	require.NoError(t, err)
	arch, err := d.CreateProject(ctx, "github.com/wesm/arch", "arch")
	require.NoError(t, err)
	_, _, err = d.RemoveProject(ctx, db.RemoveProjectParams{ProjectID: arch.ID, Actor: "tester"})
	require.NoError(t, err)

	stats, err := d.BatchProjectStats(ctx)
	require.NoError(t, err)
	assert.Contains(t, stats, live.ID)
	assert.NotContains(t, stats, arch.ID)
}

// TestBatchProjectStats_PartitionsByProject pins that two projects with
// distinct issue counts produce distinct rows; counts are not summed
// across projects. Spec §6.1.
func TestBatchProjectStats_PartitionsByProject(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	a, err := d.CreateProject(ctx, "github.com/wesm/a", "a")
	require.NoError(t, err)
	b, err := d.CreateProject(ctx, "github.com/wesm/b", "b")
	require.NoError(t, err)
	for i := 0; i < 2; i++ {
		_, _, err := d.CreateIssue(ctx, db.CreateIssueParams{
			ProjectID: a.ID, Title: "x", Author: "tester",
		})
		require.NoError(t, err)
	}
	_, _, err = d.CreateIssue(ctx, db.CreateIssueParams{
		ProjectID: b.ID, Title: "y", Author: "tester",
	})
	require.NoError(t, err)

	stats, err := d.BatchProjectStats(ctx)
	require.NoError(t, err)
	assert.Equal(t, 2, stats[a.ID].Open)
	assert.Equal(t, 1, stats[b.ID].Open)
}

// TestBatchProjectStats_ParsesZonedLegacyTimestamp pins that
// parseSQLiteTimestamp accepts the zoned legacy layout
// ("YYYY-MM-DD HH:MM:SS.NNN-07:00") that jsonl.parseExportTime emits on
// the import path. Without this layout, an imported database with a
// zoned offset on events.created_at would 500 the stats endpoint.
//
// The zoned event uses a far-future year (2099) so MAX(created_at)'s
// string comparison deterministically picks this row over the
// CreateIssue-generated issue.created event, whose RFC3339Nano
// timestamp differs at position 10 ('T' vs ' '). Without the
// year-bump, the issue.created row wins the MAX regardless of date
// and parseSQLiteTimestamp's zoned layout is never exercised.
func TestBatchProjectStats_ParsesZonedLegacyTimestamp(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p, err := d.CreateProject(ctx, "github.com/wesm/zoned", "zoned")
	require.NoError(t, err)
	issue, _, err := d.CreateIssue(ctx, db.CreateIssueParams{
		ProjectID: p.ID, Title: "live", Author: "tester",
	})
	require.NoError(t, err)
	// Stamp an event whose created_at uses the zoned legacy layout. The
	// MAX(created_at) over events for this project will surface this row,
	// driving parseSQLiteTimestamp through the new layout slot.
	eventUID, err := uid.New()
	require.NoError(t, err)
	const zonedTS = "2099-05-04 12:34:56.789-07:00"
	_, err = d.ExecContext(ctx, `
		INSERT INTO events (uid, origin_instance_uid, project_id, project_identity, issue_id, issue_number, type, actor, payload, created_at)
		VALUES (?, (SELECT value FROM meta WHERE key='instance_uid'), ?, ?, ?, ?, 'issue.edited', 'tester', '{}', ?)`,
		eventUID, p.ID, p.Identity, issue.ID, issue.Number, zonedTS)
	require.NoError(t, err)

	stats, err := d.BatchProjectStats(ctx)
	require.NoError(t, err)
	require.Contains(t, stats, p.ID)
	require.NotNil(t, stats[p.ID].LastEventAt, "zoned timestamp must parse, not nil")
	// "2099-05-04 12:34:56.789-07:00" → UTC 2099-05-04 19:34:56.789Z.
	// Without parseSQLiteTimestamp's zoned layout, BatchProjectStats would
	// either drop this row or 500. The exact-time assertion prevents a
	// loose nil-check from masking the parser regressing.
	expected := time.Date(2099, 5, 4, 19, 34, 56, 789000000, time.UTC)
	require.True(t, stats[p.ID].LastEventAt.Equal(expected),
		"zoned event time wrong: got %v, want %v", stats[p.ID].LastEventAt, expected)
}

// TestBatchProjectStats_PicksAbsoluteLatestAcrossMixedFormats pins that
// MAX over events.created_at compares by parsed time, not by lex string,
// so the absolute-latest event wins even when the events table contains
// a mix of T-separated RFC3339 and space/offset legacy layouts (which
// parseSQLiteTimestamp accepts on the read path).
//
// Concretely: we stamp two events on the same project, both at the same
// real-world UTC instant. One uses the legacy zoned layout
// ("YYYY-MM-DD HH:MM:SS.SSS-07:00"); the other is a millisecond earlier
// in the current RFC3339Nano layout. Because '<space>' (0x20) sorts
// before 'T' (0x54), a naive lex MAX(created_at) would pick the
// later-T-formatted earlier event. After the julianday() normalization,
// the absolute-latest space-zoned event wins.
func TestBatchProjectStats_PicksAbsoluteLatestAcrossMixedFormats(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p, err := d.CreateProject(ctx, "github.com/wesm/mixed-ts", "mixed-ts")
	require.NoError(t, err)
	issue, _, err := d.CreateIssue(ctx, db.CreateIssueParams{
		ProjectID: p.ID, Title: "live", Author: "tester",
	})
	require.NoError(t, err)

	// Earlier-in-absolute-time but lex-LATER (T separator > space).
	earlierUID, err := uid.New()
	require.NoError(t, err)
	const earlierTRFC = "2050-01-01T00:00:00.000Z"
	_, err = d.ExecContext(ctx, `
		INSERT INTO events (uid, origin_instance_uid, project_id, project_identity, issue_id, issue_number, type, actor, payload, created_at)
		VALUES (?, (SELECT value FROM meta WHERE key='instance_uid'), ?, ?, ?, ?, 'issue.edited', 'tester', '{}', ?)`,
		earlierUID, p.ID, p.Identity, issue.ID, issue.Number, earlierTRFC)
	require.NoError(t, err)

	// Later-in-absolute-time but lex-EARLIER (space separator < T).
	// 2050-01-01 00:00:01-00:00 == 2050-01-01T00:00:01Z which is one
	// second after earlierTRFC. Lex MAX would pick earlierTRFC.
	laterUID, err := uid.New()
	require.NoError(t, err)
	const laterZoned = "2050-01-01 00:00:01.000-00:00"
	_, err = d.ExecContext(ctx, `
		INSERT INTO events (uid, origin_instance_uid, project_id, project_identity, issue_id, issue_number, type, actor, payload, created_at)
		VALUES (?, (SELECT value FROM meta WHERE key='instance_uid'), ?, ?, ?, ?, 'issue.commented', 'tester', '{}', ?)`,
		laterUID, p.ID, p.Identity, issue.ID, issue.Number, laterZoned)
	require.NoError(t, err)

	stats, err := d.BatchProjectStats(ctx)
	require.NoError(t, err)
	require.Contains(t, stats, p.ID)
	require.NotNil(t, stats[p.ID].LastEventAt)
	expected := time.Date(2050, 1, 1, 0, 0, 1, 0, time.UTC)
	assert.True(t, stats[p.ID].LastEventAt.Equal(expected),
		"BatchProjectStats picked the lex-largest row, not the absolute-latest: got %v, want %v",
		stats[p.ID].LastEventAt, expected)
}
