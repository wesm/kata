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
	return assignShortIDIn(ctx, tx, []int64{projectID}, ulid)
}

// assignShortIDIn is the generalized form of assignShortID that returns the
// smallest-length short_id (>= shortid.MinLength) derived from ulid that
// doesn't collide with any issue in the given project set. Rows whose uid
// matches ulid are excluded from the collision count, so re-keying an issue
// in place doesn't count its own row as a self-collision. Used by project
// merge (across (source, target)) and single-project creates alike.
func assignShortIDIn(ctx context.Context, tx *sql.Tx, projectIDs []int64, ulid string) (string, error) {
	if len(projectIDs) == 0 {
		return "", fmt.Errorf("assignShortIDIn: empty projectIDs")
	}
	placeholders, args := projectIDPlaceholders(projectIDs)
	for length := shortid.MinLength; length <= shortid.MaxLength; length++ {
		candidate, err := shortid.Derive(ulid, length)
		if err != nil {
			return "", fmt.Errorf("derive short_id at length %d: %w", length, err)
		}
		var n int
		queryArgs := append([]any{}, args...)
		queryArgs = append(queryArgs, candidate, ulid)
		query := `SELECT COUNT(*) FROM issues
			WHERE project_id IN (` + placeholders + `)
			  AND short_id = ?
			  AND uid <> ?`
		if err := tx.QueryRowContext(ctx, query, queryArgs...).Scan(&n); err != nil {
			return "", fmt.Errorf("collision check at length %d: %w", length, err)
		}
		if n == 0 {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("short_id auto-extend exhausted for ulid %s", ulid)
}

// projectIDPlaceholders returns a comma-separated "?"-list and the matching
// args slice for use in a SQL IN-clause.
func projectIDPlaceholders(ids []int64) (string, []any) {
	out := make([]byte, 0, 2*len(ids))
	args := make([]any, 0, len(ids))
	for i, id := range ids {
		if i > 0 {
			out = append(out, ',')
		}
		out = append(out, '?')
		args = append(args, id)
	}
	return string(out), args
}
