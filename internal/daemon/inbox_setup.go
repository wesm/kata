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

// EnsureInbox guarantees the sentinel Inbox project exists. It is
// idempotent: calling it on every daemon startup is cheap and safe.
//
// On first call, creates a project named "Inbox". On subsequent calls,
// returns the existing project.
func EnsureInbox(ctx context.Context, d *db.DB) (db.Project, error) {
	p, err := d.ProjectByName(ctx, InboxProjectName)
	if err == nil {
		return p, nil
	}
	if !errors.Is(err, db.ErrNotFound) {
		return db.Project{}, fmt.Errorf("look up Inbox project: %w", err)
	}
	p, err = d.CreateProject(ctx, InboxProjectName)
	if err != nil {
		return db.Project{}, fmt.Errorf("create Inbox project: %w", err)
	}
	return p, nil
}
