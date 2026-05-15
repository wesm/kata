package daemon

import (
	"context"
	"fmt"
	"log"

	"github.com/wesm/kata/internal/db"
)

// sweepActor is the synthetic author stamped on events emitted by the boot
// sweep. Kept distinct from real user actors so audit-log readers can tell
// the difference between user-driven and recovery materializations.
const sweepActor = "system-sweep"

// RunRecurrenceSweep recovers any recurrence whose latest closed-done instance
// does not match last_materialized_uid — i.e. a previous materialization
// crashed or rolled back before advancing the cursor. Idempotent: safe to call
// on every daemon startup. Soft-deleted recurrences, fresh recurrences with no
// instances, and recurrences whose latest instance is open or closed-skipped
// are all skipped.
func RunRecurrenceSweep(ctx context.Context, d *db.DB) error {
	recs, err := d.ListAllRecurrences(ctx)
	if err != nil {
		return fmt.Errorf("list recurrences: %w", err)
	}
	for _, r := range recs {
		swept, err := sweepOne(ctx, d, r)
		if err != nil {
			return err
		}
		if swept {
			log.Printf("recurrence sweep: materialized next instance for %s", r.UID)
		}
	}
	return nil
}

// sweepOne handles a single recurrence row, returning (true, nil) when a fresh
// instance was materialized so the caller can emit a log line. All skip
// branches return (false, nil); errors propagate verbatim.
func sweepOne(ctx context.Context, d *db.DB, r db.Recurrence) (bool, error) {
	latest, err := d.LatestInstanceForRecurrence(ctx, r.ID)
	if err != nil {
		return false, fmt.Errorf("latest instance for recurrence %s: %w", r.UID, err)
	}
	if latest == nil {
		return false, nil
	}
	if latest.Status != "closed" {
		return false, nil
	}
	if latest.ClosedReason == nil || *latest.ClosedReason != "done" {
		return false, nil
	}
	if r.LastMaterializedUID != nil && *r.LastMaterializedUID == latest.UID {
		return false, nil
	}
	if latest.OccurrenceKey == nil {
		// Legacy or ill-formed row — can't compute the next-after key.
		return false, nil
	}

	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin sweep tx for %s: %w", r.UID, err)
	}
	if _, err := d.MaterializeNext(ctx, tx, r.ID, *latest.OccurrenceKey, sweepActor); err != nil {
		_ = tx.Rollback()
		return false, fmt.Errorf("materialize next for %s: %w", r.UID, err)
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit sweep tx for %s: %w", r.UID, err)
	}
	return true, nil
}
