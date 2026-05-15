package daemon_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wesm/kata/internal/daemon"
	"github.com/wesm/kata/internal/db"
)

func openInboxTestDB(t *testing.T) *db.DB {
	t.Helper()
	d, err := db.Open(context.Background(), filepath.Join(t.TempDir(), "kata.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = d.Close() })
	return d
}

func TestEnsureInbox_CreatesOnFirstCall(t *testing.T) {
	d := openInboxTestDB(t)
	ctx := context.Background()
	inbox, err := daemon.EnsureInbox(ctx, d)
	require.NoError(t, err)
	assert.Equal(t, "Inbox", inbox.Name)
	assert.NotEmpty(t, inbox.UID)
	assert.NotZero(t, inbox.ID)

	// Sanity: ProjectByName returns the same row.
	looked, err := d.ProjectByName(ctx, "Inbox")
	require.NoError(t, err)
	assert.Equal(t, inbox.ID, looked.ID)
}

func TestEnsureInbox_IdempotentOnSecondCall(t *testing.T) {
	d := openInboxTestDB(t)
	ctx := context.Background()
	a, err := daemon.EnsureInbox(ctx, d)
	require.NoError(t, err)
	b, err := daemon.EnsureInbox(ctx, d)
	require.NoError(t, err)
	assert.Equal(t, a.ID, b.ID, "second call must return the same project")
	assert.Equal(t, a.UID, b.UID)
}

// TestEnsureInbox_RestoresArchivedSentinel guards against a startup deadlock
// where an operator archived the Inbox sentinel and the next daemon boot would
// otherwise crash trying to recreate it (project names are globally unique).
func TestEnsureInbox_RestoresArchivedSentinel(t *testing.T) {
	d := openInboxTestDB(t)
	ctx := context.Background()

	original, err := daemon.EnsureInbox(ctx, d)
	require.NoError(t, err)

	_, err = d.ExecContext(ctx,
		`UPDATE projects SET deleted_at = CURRENT_TIMESTAMP WHERE id = ?`, original.ID)
	require.NoError(t, err)

	restored, err := daemon.EnsureInbox(ctx, d)
	require.NoError(t, err)
	assert.Equal(t, original.ID, restored.ID, "should restore the same row, not create a new one")
	assert.Nil(t, restored.DeletedAt, "DeletedAt must be cleared after restore")

	looked, err := d.ProjectByName(ctx, "Inbox")
	require.NoError(t, err)
	assert.Equal(t, original.ID, looked.ID)
}
