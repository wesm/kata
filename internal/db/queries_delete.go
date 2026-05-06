package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	katauid "github.com/wesm/kata/internal/uid"
)

// SoftDeleteIssue sets deleted_at on the issue and emits issue.soft_deleted.
// Already-deleted issues are returned as a no-op envelope (nil event,
// changed=false). Unknown issues return ErrNotFound.
func (d *DB) SoftDeleteIssue(ctx context.Context, issueID int64, actor string) (Issue, *Event, bool, error) {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return Issue{}, nil, false, err
	}
	defer func() { _ = tx.Rollback() }()

	issue, projectIdentity, err := lookupIssueIncludingDeleted(ctx, tx, issueID)
	if err != nil {
		return Issue{}, nil, false, err
	}
	if issue.DeletedAt != nil {
		// Already soft-deleted; commit so the read-side state is consistent
		// (no-op tx is harmless) and return the no-op envelope.
		if err := tx.Commit(); err != nil {
			return Issue{}, nil, false, err
		}
		return issue, nil, false, nil
	}
	// Conditional UPDATE — gated on deleted_at IS NULL — closes the
	// read-then-write race: a concurrent SoftDeleteIssue between our lookup
	// and our UPDATE would otherwise let both transactions emit events.
	res, err := tx.ExecContext(ctx,
		`UPDATE issues
		 SET deleted_at = strftime('%Y-%m-%dT%H:%M:%fZ','now'),
		     updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now')
		 WHERE id = ? AND deleted_at IS NULL`, issueID)
	if err != nil {
		return Issue{}, nil, false, fmt.Errorf("soft delete issue: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return Issue{}, nil, false, fmt.Errorf("soft delete rows affected: %w", err)
	}
	if n == 0 {
		// Lost the race — another tx soft-deleted this issue. No event.
		if err := tx.Commit(); err != nil {
			return Issue{}, nil, false, err
		}
		updated, err := d.IssueByID(ctx, issueID)
		if err != nil {
			return Issue{}, nil, false, err
		}
		return updated, nil, false, nil
	}
	evt, err := d.insertEventTx(ctx, tx, eventInsert{
		ProjectID:       issue.ProjectID,
		ProjectIdentity: projectIdentity,
		IssueID:         &issue.ID,
		IssueNumber:     &issue.Number,
		Type:            "issue.soft_deleted",
		Actor:           actor,
		Payload:         "{}",
	})
	if err != nil {
		return Issue{}, nil, false, err
	}
	if err := tx.Commit(); err != nil {
		return Issue{}, nil, false, err
	}
	updated, err := d.IssueByID(ctx, issueID)
	if err != nil {
		return Issue{}, nil, false, err
	}
	return updated, &evt, true, nil
}

// RestoreIssue clears deleted_at and emits issue.restored. Not-deleted issues
// are returned as a no-op envelope. Unknown issues return ErrNotFound.
func (d *DB) RestoreIssue(ctx context.Context, issueID int64, actor string) (Issue, *Event, bool, error) {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return Issue{}, nil, false, err
	}
	defer func() { _ = tx.Rollback() }()

	issue, projectIdentity, err := lookupIssueIncludingDeleted(ctx, tx, issueID)
	if err != nil {
		return Issue{}, nil, false, err
	}
	if issue.DeletedAt == nil {
		if err := tx.Commit(); err != nil {
			return Issue{}, nil, false, err
		}
		return issue, nil, false, nil
	}
	// Conditional UPDATE — gated on deleted_at IS NOT NULL — closes the
	// read-then-write race symmetric to SoftDeleteIssue.
	res, err := tx.ExecContext(ctx,
		`UPDATE issues
		 SET deleted_at = NULL,
		     updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now')
		 WHERE id = ? AND deleted_at IS NOT NULL`, issueID)
	if err != nil {
		return Issue{}, nil, false, fmt.Errorf("restore issue: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return Issue{}, nil, false, fmt.Errorf("restore rows affected: %w", err)
	}
	if n == 0 {
		// Lost the race — another tx restored this issue. No event.
		if err := tx.Commit(); err != nil {
			return Issue{}, nil, false, err
		}
		updated, err := d.IssueByID(ctx, issueID)
		if err != nil {
			return Issue{}, nil, false, err
		}
		return updated, nil, false, nil
	}
	evt, err := d.insertEventTx(ctx, tx, eventInsert{
		ProjectID:       issue.ProjectID,
		ProjectIdentity: projectIdentity,
		IssueID:         &issue.ID,
		IssueNumber:     &issue.Number,
		Type:            "issue.restored",
		Actor:           actor,
		Payload:         "{}",
	})
	if err != nil {
		return Issue{}, nil, false, err
	}
	if err := tx.Commit(); err != nil {
		return Issue{}, nil, false, err
	}
	updated, err := d.IssueByID(ctx, issueID)
	if err != nil {
		return Issue{}, nil, false, err
	}
	return updated, &evt, true, nil
}

// sqlReader is the subset of *sql.Conn / *sql.Tx used by helpers that need to
// run the same SELECT under either a connection-scoped manual transaction
// (BEGIN IMMEDIATE) or a database/sql-managed *sql.Tx.
type sqlReader interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// PurgeIssue runs the seven-step transaction from spec §3.5: cascade-deletes
// every dependent (events, comments, links, labels), reserves an SSE cursor by
// bumping sqlite_sequence above the deleted events' ids, writes a purge_log
// audit row, and finally removes the issues row (which fires the FTS deletion
// trigger). Uses BEGIN IMMEDIATE so the count snapshots in step 3 are stable
// against concurrent writers — no other writer can slip a comment/link/label
// in between counting and deleting.
//
// No issue.purged event is persisted; purge_log is the only audit record.
// Returns ErrNotFound if the issue does not exist (whether or not it had been
// soft-deleted first).
func (d *DB) PurgeIssue(ctx context.Context, issueID int64, actor string, reason *string) (PurgeLog, error) {
	conn, err := d.Conn(ctx)
	if err != nil {
		return PurgeLog{}, fmt.Errorf("acquire conn: %w", err)
	}
	defer func() { _ = conn.Close() }()

	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE TRANSACTION"); err != nil {
		return PurgeLog{}, fmt.Errorf("begin immediate: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			// Use a detached context so rollback runs even if the caller's
			// ctx is already canceled — otherwise the conn may return to the
			// pool with an open tx after a mid-flight cancellation.
			_, _ = conn.ExecContext(context.WithoutCancel(ctx), "ROLLBACK")
		}
	}()

	issue, projectIdentity, err := lookupIssueIncludingDeleted(ctx, conn, issueID)
	if err != nil {
		return PurgeLog{}, err
	}

	purgeLogID, err := purgeCascade(ctx, conn, issue, projectIdentity, actor, reason, d.instanceUID)
	if err != nil {
		return PurgeLog{}, err
	}

	pl, err := scanPurgeLog(ctx, conn, purgeLogID)
	if err != nil {
		return PurgeLog{}, fmt.Errorf("re-fetch purge_log: %w", err)
	}

	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return PurgeLog{}, fmt.Errorf("commit: %w", err)
	}
	committed = true
	return pl, nil
}

// connExec is the subset of *sql.Conn / *sql.Tx that purgeCascade needs:
// both reads and writes inside a manual transaction.
type connExec interface {
	sqlReader
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// purgeCascade is steps 2-7 of PurgeIssue. It runs inside the BEGIN IMMEDIATE
// transaction held by the caller and returns the purge_log row id of the audit
// row it inserted. Split out of PurgeIssue to keep the public method's body
// readable and to bound its cyclomatic complexity.
func purgeCascade(
	ctx context.Context,
	c connExec,
	issue Issue,
	projectIdentity string,
	actor string,
	reason *string,
	originInstanceUID string,
) (int64, error) {
	// Step 2: capture the events.id range about to be cascade-deleted so the
	// audit row records what the SSE reset cursor is reserving past.
	var minEventID, maxEventID sql.NullInt64
	if err := c.QueryRowContext(ctx,
		`SELECT MIN(id), MAX(id) FROM events WHERE issue_id = ? OR related_issue_id = ?`,
		issue.ID, issue.ID).Scan(&minEventID, &maxEventID); err != nil {
		return 0, fmt.Errorf("scan event id range: %w", err)
	}

	// Step 3: count snapshots — stable under BEGIN IMMEDIATE.
	commentCount, err := scanCount(ctx, c,
		`SELECT count(*) FROM comments WHERE issue_id = ?`, issue.ID)
	if err != nil {
		return 0, fmt.Errorf("count comments: %w", err)
	}
	linkCount, err := scanCount(ctx, c,
		`SELECT count(*) FROM links WHERE from_issue_id = ? OR to_issue_id = ?`,
		issue.ID, issue.ID)
	if err != nil {
		return 0, fmt.Errorf("count links: %w", err)
	}
	labelCount, err := scanCount(ctx, c,
		`SELECT count(*) FROM issue_labels WHERE issue_id = ?`, issue.ID)
	if err != nil {
		return 0, fmt.Errorf("count labels: %w", err)
	}
	eventCount, err := scanCount(ctx, c,
		`SELECT count(*) FROM events WHERE issue_id = ? OR related_issue_id = ?`,
		issue.ID, issue.ID)
	if err != nil {
		return 0, fmt.Errorf("count events: %w", err)
	}

	// Step 4: cascade-delete dependents. Order is bounded by foreign keys —
	// events (which can reference issues via issue_id/related_issue_id) and
	// the relationship rows (comments, links, labels) all reference issues, so
	// they must go before the issues row in step 7. Mutual ordering between
	// the four below is otherwise free.
	if _, err := c.ExecContext(ctx,
		`DELETE FROM events WHERE issue_id = ? OR related_issue_id = ?`,
		issue.ID, issue.ID); err != nil {
		return 0, fmt.Errorf("delete events: %w", err)
	}
	if _, err := c.ExecContext(ctx,
		`DELETE FROM comments WHERE issue_id = ?`, issue.ID); err != nil {
		return 0, fmt.Errorf("delete comments: %w", err)
	}
	if _, err := c.ExecContext(ctx,
		`DELETE FROM links WHERE from_issue_id = ? OR to_issue_id = ?`,
		issue.ID, issue.ID); err != nil {
		return 0, fmt.Errorf("delete links: %w", err)
	}
	if _, err := c.ExecContext(ctx,
		`DELETE FROM issue_labels WHERE issue_id = ?`, issue.ID); err != nil {
		return 0, fmt.Errorf("delete labels: %w", err)
	}

	// Step 5: reserve an SSE cursor by bumping sqlite_sequence past the
	// max events.id we just deleted. Skip when no events were attached —
	// there's nothing for subscribers to skip past.
	reservedCursor, err := reserveEventSequence(ctx, c, minEventID.Valid)
	if err != nil {
		return 0, err
	}

	purgeUID, err := katauid.New()
	if err != nil {
		return 0, fmt.Errorf("generate purge uid: %w", err)
	}
	// Step 6: write the audit row. sql.NullInt64 carries through as either
	// INTEGER or NULL; database/sql handles the marshaling.
	res, err := c.ExecContext(ctx,
		`INSERT INTO purge_log(
		   uid, origin_instance_uid,
		   project_id, purged_issue_id, issue_uid, project_uid, project_identity, issue_number,
		   issue_title, issue_author, comment_count, link_count, label_count,
		   event_count, events_deleted_min_id, events_deleted_max_id,
		   purge_reset_after_event_id, actor, reason)
		 VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		purgeUID, originInstanceUID,
		issue.ProjectID, issue.ID, issue.UID, issue.ProjectUID, projectIdentity, issue.Number,
		issue.Title, issue.Author, commentCount, linkCount, labelCount,
		eventCount, minEventID, maxEventID, reservedCursor, actor, reason)
	if err != nil {
		return 0, fmt.Errorf("insert purge_log: %w", err)
	}
	purgeLogID, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("purge_log last id: %w", err)
	}

	// Step 7: remove the issues row. The issues_ad_fts trigger fires here and
	// drops the matching FTS row.
	if _, err := c.ExecContext(ctx,
		`DELETE FROM issues WHERE id = ?`, issue.ID); err != nil {
		return 0, fmt.Errorf("delete issue: %w", err)
	}
	return purgeLogID, nil
}

// scanCount runs a `SELECT count(*) ...` statement and returns the result.
func scanCount(ctx context.Context, r sqlReader, query string, args ...any) (int64, error) {
	var n int64
	if err := r.QueryRowContext(ctx, query, args...).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

// reserveEventSequence advances sqlite_sequence for events past the current
// seq, returning the reserved value as a NullInt64 (Valid=true) for the
// purge_log row's purge_reset_after_event_id column. If hadEvents is false,
// returns NullInt64{} so the column stores NULL (no SSE reset needed).
func reserveEventSequence(ctx context.Context, c connExec, hadEvents bool) (sql.NullInt64, error) {
	if !hadEvents {
		return sql.NullInt64{}, nil
	}
	var seq int64
	if err := c.QueryRowContext(ctx,
		`SELECT seq FROM sqlite_sequence WHERE name = 'events'`).Scan(&seq); err != nil {
		return sql.NullInt64{}, fmt.Errorf("read events seq: %w", err)
	}
	seq++
	if _, err := c.ExecContext(ctx,
		`UPDATE sqlite_sequence SET seq = ? WHERE name = 'events'`, seq); err != nil {
		return sql.NullInt64{}, fmt.Errorf("bump events seq: %w", err)
	}
	return sql.NullInt64{Int64: seq, Valid: true}, nil
}

// scanPurgeLog re-reads the purge_log row inserted by purgeCascade so the
// caller receives a typed PurgeLog with nullable fields decoded as *int64.
// Returns ErrNotFound when no row matches; callers in PurgeIssue see this only
// if the just-inserted row is missing (which would indicate a DB-level bug).
func scanPurgeLog(ctx context.Context, r sqlReader, id int64) (PurgeLog, error) {
	const q = `
		SELECT id, uid, origin_instance_uid, project_id, purged_issue_id, issue_uid, project_uid,
		       project_identity, issue_number, issue_title, issue_author, comment_count, link_count, label_count,
		       event_count, events_deleted_min_id, events_deleted_max_id,
		       purge_reset_after_event_id, actor, reason, purged_at
		FROM purge_log WHERE id = ?`
	var pl PurgeLog
	err := r.QueryRowContext(ctx, q, id).Scan(
		&pl.ID, &pl.UID, &pl.OriginInstanceUID, &pl.ProjectID, &pl.PurgedIssueID, &pl.IssueUID,
		&pl.ProjectUID, &pl.ProjectIdentity, &pl.IssueNumber, &pl.IssueTitle, &pl.IssueAuthor, &pl.CommentCount,
		&pl.LinkCount, &pl.LabelCount, &pl.EventCount,
		&pl.EventsDeletedMinID, &pl.EventsDeletedMaxID,
		&pl.PurgeResetAfterEventID, &pl.Actor, &pl.Reason, &pl.PurgedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return PurgeLog{}, ErrNotFound
	}
	if err != nil {
		return PurgeLog{}, fmt.Errorf("scan purge_log: %w", err)
	}
	return pl, nil
}

// lookupIssueIncludingDeleted fetches an issue + its project's identity for
// event snapshotting. Unlike lookupIssueForEvent (queries.go), this version
// does NOT filter out soft-deleted rows — it's the right primitive for the
// destructive ladder verbs that need to operate on deleted issues.
func lookupIssueIncludingDeleted(ctx context.Context, r sqlReader, issueID int64) (Issue, string, error) {
	const q = `
		SELECT i.id, i.uid, i.project_id, p.uid, i.number, i.title, i.body, i.status,
		       i.closed_reason, i.owner, i.priority, i.author, i.created_at, i.updated_at,
		       i.closed_at, i.deleted_at, p.identity
		FROM issues i
		JOIN projects p ON p.id = i.project_id
		WHERE i.id = ?`
	var (
		i        Issue
		identity string
	)
	err := r.QueryRowContext(ctx, q, issueID).
		Scan(&i.ID, &i.UID, &i.ProjectID, &i.ProjectUID, &i.Number, &i.Title, &i.Body, &i.Status,
			&i.ClosedReason, &i.Owner, &i.Priority, &i.Author, &i.CreatedAt, &i.UpdatedAt,
			&i.ClosedAt, &i.DeletedAt, &identity)
	if errors.Is(err, sql.ErrNoRows) {
		return Issue{}, "", ErrNotFound
	}
	if err != nil {
		return Issue{}, "", fmt.Errorf("lookup issue including deleted: %w", err)
	}
	return i, identity, nil
}
