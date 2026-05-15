package daemon

import (
	"context"
	"errors"
	"fmt"

	"github.com/wesm/kata/internal/db"
)

// InboxProjectName is the canonical name of the sentinel project for
// captures that haven't been triaged into another project.
const InboxProjectName = "Inbox"

// EnsureInbox guarantees the sentinel Inbox project exists and is active.
// It is idempotent: calling it on every daemon startup is cheap and safe.
//
// Project names are globally unique, so if a previous operator archived the
// sentinel via a soft-delete, a fresh CreateProject would collide on the
// unique-name constraint and crash startup. To stay robust against that, the
// lookup goes through ProjectByNameIncludingArchived; an archived sentinel is
// restored in place rather than recreated.
func EnsureInbox(ctx context.Context, d *db.DB) (db.Project, error) {
	p, err := d.ProjectByNameIncludingArchived(ctx, InboxProjectName)
	switch {
	case err == nil && p.DeletedAt == nil:
		return p, nil
	case err == nil:
		if _, err := d.ExecContext(ctx,
			`UPDATE projects SET deleted_at = NULL WHERE id = ?`, p.ID); err != nil {
			return db.Project{}, fmt.Errorf("restore archived Inbox project: %w", err)
		}
		restored, err := d.ProjectByID(ctx, p.ID)
		if err != nil {
			return db.Project{}, fmt.Errorf("reload restored Inbox project: %w", err)
		}
		return restored, nil
	case errors.Is(err, db.ErrNotFound):
		created, err := d.CreateProject(ctx, InboxProjectName)
		if err != nil {
			return db.Project{}, fmt.Errorf("create Inbox project: %w", err)
		}
		return created, nil
	default:
		return db.Project{}, fmt.Errorf("look up Inbox project: %w", err)
	}
}
