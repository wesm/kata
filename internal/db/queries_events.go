package db

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// MaxEventID returns the highest events.id, or 0 when the table is empty. The
// SSE handler uses this as the high-water mark snapshot after Subscribe.
func (d *DB) MaxEventID(ctx context.Context) (int64, error) {
	var n sql.NullInt64
	err := d.QueryRowContext(ctx, `SELECT MAX(id) FROM events`).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("max event id: %w", err)
	}
	if !n.Valid {
		return 0, nil
	}
	return n.Int64, nil
}

// EventsAfterParams selects events with id strictly greater than AfterID,
// optionally bounded above by ThroughID and filtered by ProjectID. Limit is
// applied verbatim; callers are responsible for clamping (the polling
// endpoint clamps to [1, 1000]; the SSE drain passes 10001).
type EventsAfterParams struct {
	AfterID   int64
	ProjectID int64 // 0 = cross-project; nonzero adds AND project_id = ?
	ThroughID int64 // 0 = no upper bound; nonzero adds AND id <= ?
	Limit     int
}

// EventsAfter returns up to Limit events ordered by id ASC. The issue and
// related_issue short_ids are joined from the live `issues` table so events
// render with display ids that stay current even after `kata projects merge`
// or a future federation merge shifts a peer's short_id. UIDs remain stable.
func (d *DB) EventsAfter(ctx context.Context, p EventsAfterParams) ([]Event, error) {
	var (
		conds []string
		args  []any
	)
	conds = append(conds, "e.id > ?")
	args = append(args, p.AfterID)
	if p.ProjectID != 0 {
		conds = append(conds, "e.project_id = ?")
		args = append(args, p.ProjectID)
	}
	if p.ThroughID != 0 {
		conds = append(conds, "e.id <= ?")
		args = append(args, p.ThroughID)
	}
	q := `SELECT e.id, e.uid, e.origin_instance_uid, e.project_id, p.uid, e.project_name,
	             e.issue_id, e.issue_uid, i.short_id, e.related_issue_id, e.related_issue_uid, ri.short_id,
	             e.type, e.actor, e.payload, e.created_at
	      FROM events e
	      JOIN projects p ON p.id = e.project_id
	      LEFT JOIN issues i ON i.id = e.issue_id
	      LEFT JOIN issues ri ON ri.id = e.related_issue_id
	      WHERE ` + strings.Join(conds, " AND ") + ` ORDER BY e.id ASC LIMIT ?`
	args = append(args, p.Limit)
	rows, err := d.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("events after: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Event
	for rows.Next() {
		var e Event
		if err := rows.Scan(&e.ID, &e.UID, &e.OriginInstanceUID, &e.ProjectID, &e.ProjectUID, &e.ProjectName,
			&e.IssueID, &e.IssueUID, &e.IssueShortID,
			&e.RelatedIssueID, &e.RelatedIssueUID, &e.RelatedIssueShortID,
			&e.Type, &e.Actor, &e.Payload, &e.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan event: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// EventsInWindowParams selects events whose created_at lies in the closed
// window [Since, Until]. ProjectID = 0 disables the project filter; an empty
// Actors slice disables actor filtering. Results are ordered by id ASC so the
// digest aggregator can rely on chronological ordering for per-issue
// "actions" sequencing.
//
// Both bounds are inclusive. SQLite stores created_at at millisecond
// precision; an exclusive upper bound silently excludes events emitted in the
// same millisecond as Until, which happens often when Until defaults to
// time.Now() right after a mutation. Inclusive matches what humans typing
// "since 24h" expect anyway.
type EventsInWindowParams struct {
	Since     string // string, inclusive lower bound on created_at
	Until     string // string, inclusive upper bound on created_at
	ProjectID int64  // 0 = cross-project
	Actors    []string
}

// EventsInWindow returns every event in the requested window. There is no row
// cap: digest is a one-shot read and the caller has already chosen a finite
// window. Callers are expected to pass a sane window (typically <= 7 days).
func (d *DB) EventsInWindow(ctx context.Context, p EventsInWindowParams) ([]Event, error) {
	var (
		conds []string
		args  []any
	)
	conds = append(conds, "e.created_at >= ?")
	args = append(args, p.Since)
	conds = append(conds, "e.created_at <= ?")
	args = append(args, p.Until)
	if p.ProjectID != 0 {
		conds = append(conds, "e.project_id = ?")
		args = append(args, p.ProjectID)
	}
	if len(p.Actors) > 0 {
		placeholders := make([]string, len(p.Actors))
		for i, a := range p.Actors {
			placeholders[i] = "?"
			args = append(args, a)
		}
		conds = append(conds, "e.actor IN ("+strings.Join(placeholders, ",")+")")
	}
	q := `SELECT e.id, e.project_id, e.project_name, e.issue_id, e.issue_uid, i.short_id,
	             e.related_issue_id, e.related_issue_uid, ri.short_id,
	             e.type, e.actor, e.payload, e.created_at
	      FROM events e
	      LEFT JOIN issues i ON i.id = e.issue_id
	      LEFT JOIN issues ri ON ri.id = e.related_issue_id
	      WHERE ` + strings.Join(conds, " AND ") + ` ORDER BY e.id ASC`
	rows, err := d.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("events in window: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Event
	for rows.Next() {
		var e Event
		if err := rows.Scan(&e.ID, &e.ProjectID, &e.ProjectName, &e.IssueID, &e.IssueUID, &e.IssueShortID,
			&e.RelatedIssueID, &e.RelatedIssueUID, &e.RelatedIssueShortID,
			&e.Type, &e.Actor, &e.Payload, &e.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan event: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// PurgeResetCheck returns the maximum purge_reset_after_event_id strictly
// greater than afterID, optionally constrained to a project. Returns 0 when
// no matching purge_log row exists. The strict > semantics align with the
// spec §2.6 reservation: every reserved cursor is greater than every real
// events.id at the moment of the purge, so cursor == reservedID means the
// client is already past it and does not need a reset.
//
// projectID == 0 = cross-project (no filter).
func (d *DB) PurgeResetCheck(ctx context.Context, afterID, projectID int64) (int64, error) {
	q := `SELECT MAX(purge_reset_after_event_id) FROM purge_log
	      WHERE purge_reset_after_event_id IS NOT NULL AND purge_reset_after_event_id > ?`
	args := []any{afterID}
	if projectID != 0 {
		q += ` AND project_id = ?`
		args = append(args, projectID)
	}
	var n sql.NullInt64
	if err := d.QueryRowContext(ctx, q, args...).Scan(&n); err != nil {
		return 0, fmt.Errorf("purge reset check: %w", err)
	}
	if !n.Valid {
		return 0, nil
	}
	return n.Int64, nil
}
