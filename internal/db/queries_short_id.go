package db

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/wesm/kata/internal/shortid"
)

// assignShortID returns the smallest-length short_id (>= shortid.MinLength)
// derived from ulid that does not collide with any existing issue in the
// project, including soft-deleted rows. Soft-deleted issues retain their
// short_ids so `kata restore` is stable; purged rows are gone from the table
// and free their suffixes for reuse.
func assignShortID(ctx context.Context, tx *sql.Tx, projectID int64, ulid string) (string, error) {
	for length := shortid.MinLength; length <= shortid.MaxLength; length++ {
		candidate, err := shortid.Derive(ulid, length)
		if err != nil {
			return "", fmt.Errorf("derive short_id at length %d: %w", length, err)
		}
		var n int
		if err := tx.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM issues WHERE project_id = ? AND short_id = ?`,
			projectID, candidate,
		).Scan(&n); err != nil {
			return "", fmt.Errorf("collision check at length %d: %w", length, err)
		}
		if n == 0 {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("short_id auto-extend exhausted for ulid %s", ulid)
}
