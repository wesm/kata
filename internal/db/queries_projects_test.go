package db_test

import (
	"context"
	"path/filepath"
	"testing"

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
