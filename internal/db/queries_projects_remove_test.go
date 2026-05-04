package db_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wesm/kata/internal/db"
	"github.com/wesm/kata/internal/uid"
)

// TestRemoveProject_ArchivesAndDropsAliases pins the happy path: a project
// with no open issues + one alias is archived (deleted_at set), the alias
// row is removed, and a project.removed event is emitted.
func TestRemoveProject_ArchivesAndDropsAliases(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p, err := d.CreateProject(ctx, "github.com/wesm/proj-archive", "archive-me")
	require.NoError(t, err)
	_, err = d.AttachAlias(ctx, p.ID, "github.com/wesm/proj-archive", "git", "/tmp/archive")
	require.NoError(t, err)

	got, evt, err := d.RemoveProject(ctx, db.RemoveProjectParams{
		ProjectID: p.ID, Actor: "tester",
	})
	require.NoError(t, err)
	require.NotNil(t, got.DeletedAt, "deleted_at must be set")
	require.NotNil(t, evt)
	assert.Equal(t, "project.removed", evt.Type)
	assert.Equal(t, p.ID, evt.ProjectID)
	assert.True(t, uid.Valid(evt.UID))
	assert.Equal(t, d.InstanceUID(), evt.OriginInstanceUID)

	var aliasCount int
	require.NoError(t, d.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM project_aliases WHERE project_id = ?`, p.ID).Scan(&aliasCount))
	assert.Equal(t, 0, aliasCount, "aliases must be hard-deleted")
}

// TestRemoveProject_RefusesWhenOpenIssues pins the safety gate: open issues
// block archival unless Force=true.
func TestRemoveProject_RefusesWhenOpenIssues(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p, err := d.CreateProject(ctx, "github.com/wesm/proj-busy", "busy")
	require.NoError(t, err)
	_, _, err = d.CreateIssue(ctx, db.CreateIssueParams{
		ProjectID: p.ID, Title: "still open", Author: "tester",
	})
	require.NoError(t, err)

	_, _, err = d.RemoveProject(ctx, db.RemoveProjectParams{ProjectID: p.ID, Actor: "tester"})
	require.Error(t, err)
	assert.True(t, errors.Is(err, db.ErrProjectHasOpenIssues))

	var detail *db.ProjectHasOpenIssuesError
	require.True(t, errors.As(err, &detail), "ErrProjectHasOpenIssues must wrap a count")
	assert.Equal(t, int64(1), detail.OpenIssues)
}

// TestRemoveProject_ForceOverridesOpenIssues pins that --force archives even
// with open issues. The issue rows themselves stay open in the DB; archival
// is about the project surface, not bulk-closing tickets.
func TestRemoveProject_ForceOverridesOpenIssues(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p, err := d.CreateProject(ctx, "github.com/wesm/proj-force", "force")
	require.NoError(t, err)
	_, _, err = d.CreateIssue(ctx, db.CreateIssueParams{
		ProjectID: p.ID, Title: "still open under force", Author: "tester",
	})
	require.NoError(t, err)

	got, evt, err := d.RemoveProject(ctx, db.RemoveProjectParams{
		ProjectID: p.ID, Actor: "tester", Force: true,
	})
	require.NoError(t, err)
	require.NotNil(t, got.DeletedAt)
	require.NotNil(t, evt)

	var stillOpen int
	require.NoError(t, d.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM issues WHERE project_id = ? AND status = 'open'`,
		p.ID).Scan(&stillOpen))
	assert.Equal(t, 1, stillOpen, "force does not bulk-close issues")
}

// TestRemoveProject_AlreadyArchived covers the idempotency-rejection: a
// second RemoveProject on an archived project surfaces a clean error rather
// than silently re-archiving.
func TestRemoveProject_AlreadyArchived(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p, err := d.CreateProject(ctx, "github.com/wesm/proj-twice", "twice")
	require.NoError(t, err)
	_, _, err = d.RemoveProject(ctx, db.RemoveProjectParams{ProjectID: p.ID, Actor: "tester"})
	require.NoError(t, err)

	_, _, err = d.RemoveProject(ctx, db.RemoveProjectParams{ProjectID: p.ID, Actor: "tester"})
	require.Error(t, err)
	assert.True(t, errors.Is(err, db.ErrProjectAlreadyArchived))
}

// TestRemoveProject_ExcludedFromListAndResolve pins the read-path filter:
// after archival the project no longer surfaces in ListProjects /
// ProjectByIdentity.
func TestRemoveProject_ExcludedFromListAndResolve(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	keep, err := d.CreateProject(ctx, "github.com/wesm/proj-keep", "keep")
	require.NoError(t, err)
	gone, err := d.CreateProject(ctx, "github.com/wesm/proj-gone", "gone")
	require.NoError(t, err)
	_, _, err = d.RemoveProject(ctx, db.RemoveProjectParams{ProjectID: gone.ID, Actor: "tester"})
	require.NoError(t, err)

	list, err := d.ListProjects(ctx)
	require.NoError(t, err)
	require.Len(t, list, 1)
	assert.Equal(t, keep.ID, list[0].ID)

	_, err = d.ProjectByIdentity(ctx, "github.com/wesm/proj-gone")
	assert.ErrorIs(t, err, db.ErrNotFound)

	// Including-archived variant still finds it.
	got, err := d.ProjectByIdentityIncludingArchived(ctx, "github.com/wesm/proj-gone")
	require.NoError(t, err)
	require.NotNil(t, got.DeletedAt)
}

// TestDetachProjectAlias_RemovesOneAndEmitsEvent pins the happy path: with
// two aliases, one detaches cleanly and emits project.alias_removed.
func TestDetachProjectAlias_RemovesOneAndEmitsEvent(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p, err := d.CreateProject(ctx, "github.com/wesm/proj-detach", "detach")
	require.NoError(t, err)
	a1, err := d.AttachAlias(ctx, p.ID, "github.com/wesm/proj-detach", "git", "/tmp/a")
	require.NoError(t, err)
	a2, err := d.AttachAlias(ctx, p.ID, "local:///tmp/elsewhere", "local", "/tmp/elsewhere")
	require.NoError(t, err)

	got, evt, err := d.DetachProjectAlias(ctx, db.DetachAliasParams{
		ProjectID: p.ID, AliasID: a2.ID, Actor: "tester",
	})
	require.NoError(t, err)
	assert.Equal(t, a2.AliasIdentity, got.AliasIdentity)
	require.NotNil(t, evt)
	assert.Equal(t, "project.alias_removed", evt.Type)
	assert.Equal(t, p.ID, evt.ProjectID)
	assert.True(t, uid.Valid(evt.UID))

	var remaining int
	require.NoError(t, d.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM project_aliases WHERE project_id = ?`, p.ID).Scan(&remaining))
	assert.Equal(t, 1, remaining)

	// The other alias is still resolvable.
	resolved, err := d.AliasByIdentity(ctx, a1.AliasIdentity)
	require.NoError(t, err)
	assert.Equal(t, a1.ID, resolved.ID)
}

// TestDetachProjectAlias_RefusesWhenLast pins the safety gate: the last
// alias for a project requires Force=true to drop. Without it the resolve
// flow would lose the workspace→project link silently.
func TestDetachProjectAlias_RefusesWhenLast(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p, err := d.CreateProject(ctx, "github.com/wesm/proj-only", "only")
	require.NoError(t, err)
	a, err := d.AttachAlias(ctx, p.ID, "github.com/wesm/proj-only", "git", "/tmp/only")
	require.NoError(t, err)

	_, _, err = d.DetachProjectAlias(ctx, db.DetachAliasParams{
		ProjectID: p.ID, AliasID: a.ID, Actor: "tester",
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, db.ErrAliasIsLast))
}

// TestDetachProjectAlias_ForceDropsLast pins that Force overrides the
// last-alias refusal — the operator has explicitly accepted that the
// project will no longer resolve from any workspace.
func TestDetachProjectAlias_ForceDropsLast(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p, err := d.CreateProject(ctx, "github.com/wesm/proj-force-last", "force-last")
	require.NoError(t, err)
	a, err := d.AttachAlias(ctx, p.ID, "github.com/wesm/proj-force-last", "git", "/tmp/force-last")
	require.NoError(t, err)

	_, evt, err := d.DetachProjectAlias(ctx, db.DetachAliasParams{
		ProjectID: p.ID, AliasID: a.ID, Actor: "tester", Force: true,
	})
	require.NoError(t, err)
	require.NotNil(t, evt)

	var remaining int
	require.NoError(t, d.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM project_aliases WHERE project_id = ?`, p.ID).Scan(&remaining))
	assert.Equal(t, 0, remaining)
}

// TestDetachProjectAlias_RejectsCrossProject pins the atomic
// (project_id, alias_id) safety: if a stale handler preflight resolves
// to project A but the alias_id belongs to project B, the delete must
// refuse rather than silently dropping B's alias. The transaction's
// SELECT keys on both columns so a reassignment between any preflight
// and the delete cannot mis-target.
func TestDetachProjectAlias_RejectsCrossProject(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	pA, err := d.CreateProject(ctx, "github.com/wesm/proj-a", "a")
	require.NoError(t, err)
	pB, err := d.CreateProject(ctx, "github.com/wesm/proj-b", "b")
	require.NoError(t, err)
	// Two aliases on B so detaching one doesn't trip the last-alias gate
	// (we want the cross-project check to be the only refusal path).
	_, err = d.AttachAlias(ctx, pB.ID, "github.com/wesm/proj-b", "git", "/tmp/b-primary")
	require.NoError(t, err)
	aB, err := d.AttachAlias(ctx, pB.ID, "local:///tmp/b-extra", "local", "/tmp/b-extra")
	require.NoError(t, err)

	_, _, err = d.DetachProjectAlias(ctx, db.DetachAliasParams{
		ProjectID: pA.ID, AliasID: aB.ID, Actor: "tester",
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, db.ErrNotFound),
		"cross-project detach must refuse with ErrNotFound, got %v", err)

	// Both of B's aliases are intact.
	var remaining int
	require.NoError(t, d.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM project_aliases WHERE project_id = ?`, pB.ID).Scan(&remaining))
	assert.Equal(t, 2, remaining)
}
