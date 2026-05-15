package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/wesm/kata/internal/metadata"
)

// PatchIssueMetadataIn carries inputs for PatchIssueMetadata.
type PatchIssueMetadataIn struct {
	IssueID    int64
	IfMatchRev int64
	Actor      string
	Patch      map[string]json.RawMessage
}

// PatchIssueMetadataOut carries results from a successful PatchIssueMetadata call.
type PatchIssueMetadataOut struct {
	Issue       Issue
	Event       Event
	Changed     bool
	NewRevision int64
}

// PatchIssueMetadata applies a per-key patch to issues.metadata inside a single
// transaction. It validates all patch keys against metadata.IssueRegistry before
// opening a transaction, enforces If-Match (revision gate), and emits an
// issue.metadata_updated event whose payload carries the per-key diff plus
// revision_new. No event is emitted and the revision is not bumped when the
// patch produces no actual change (empty diff).
func (d *DB) PatchIssueMetadata(ctx context.Context, in PatchIssueMetadataIn) (PatchIssueMetadataOut, error) {
	var out PatchIssueMetadataOut

	// Validate all patch keys before opening a tx. A bad key/value never starts a tx.
	for key, raw := range in.Patch {
		if err := metadata.Validate(metadata.IssueRegistry, key, raw); err != nil {
			return out, fmt.Errorf("validate %q: %w", key, err)
		}
	}

	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return out, err
	}
	defer func() { _ = tx.Rollback() }()

	var (
		curMetadata string
		curRevision int64
		projectID   int64
		projectName string
	)
	err = tx.QueryRowContext(ctx, `
		SELECT i.metadata, i.revision, i.project_id, p.name
		  FROM issues i JOIN projects p ON p.id = i.project_id
		 WHERE i.id = ? AND i.deleted_at IS NULL`,
		in.IssueID,
	).Scan(&curMetadata, &curRevision, &projectID, &projectName)
	if errors.Is(err, sql.ErrNoRows) {
		return out, fmt.Errorf("issue %d not found", in.IssueID)
	}
	if err != nil {
		return out, err
	}

	if in.IfMatchRev != curRevision {
		return out, &RevisionConflictError{CurrentRevision: curRevision}
	}

	// Apply the patch onto the current metadata to produce the new blob,
	// then diff old vs new to detect no-ops and build the event payload.
	newBlob, err := applyMetadataPatch(json.RawMessage(curMetadata), in.Patch)
	if err != nil {
		return out, fmt.Errorf("apply patch: %w", err)
	}

	diff, err := metadata.Diff(json.RawMessage(curMetadata), newBlob)
	if err != nil {
		return out, fmt.Errorf("compute diff: %w", err)
	}

	if len(diff) == 0 {
		// No-op: commit (no writes) and return Changed=false. Revision unchanged.
		if err := tx.Commit(); err != nil {
			return out, err
		}
		issue, err := d.IssueByID(ctx, in.IssueID)
		if err != nil {
			return out, err
		}
		out.Issue = issue
		out.Changed = false
		out.NewRevision = curRevision
		return out, nil
	}

	newRev := curRevision + 1
	if _, err := tx.ExecContext(ctx, `
		UPDATE issues
		   SET metadata   = ?,
		       revision   = ?,
		       updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now')
		 WHERE id = ?`,
		string(newBlob), newRev, in.IssueID,
	); err != nil {
		return out, fmt.Errorf("update issue metadata: %w", err)
	}

	// Build a serializable diff for the event payload: {key: {from, to}}.
	type keyDiffPayload struct {
		From json.RawMessage `json:"from"`
		To   json.RawMessage `json:"to"`
	}
	diffPayload := make(map[string]keyDiffPayload, len(diff))
	for k, kd := range diff {
		diffPayload[k] = keyDiffPayload{From: kd.From, To: kd.To}
	}
	payload, err := json.Marshal(struct {
		Diff        map[string]keyDiffPayload `json:"diff"`
		RevisionNew int64                     `json:"revision_new"`
	}{diffPayload, newRev})
	if err != nil {
		return out, fmt.Errorf("marshal event payload: %w", err)
	}

	ev, err := d.insertEventTx(ctx, tx, eventInsert{
		ProjectID:   projectID,
		ProjectName: projectName,
		IssueID:     &in.IssueID,
		Type:        "issue.metadata_updated",
		Actor:       in.Actor,
		Payload:     string(payload),
	})
	if err != nil {
		return out, err
	}

	if err := tx.Commit(); err != nil {
		return out, err
	}

	issue, err := d.IssueByID(ctx, in.IssueID)
	if err != nil {
		return out, err
	}
	out.Issue = issue
	out.Event = ev
	out.Changed = true
	out.NewRevision = newRev
	return out, nil
}

// applyMetadataPatch merges patch keys into oldBlob, producing a new JSON
// object. Null values in the patch delete the corresponding key from the result.
// oldBlob may be empty or "null", treated as {}.
func applyMetadataPatch(oldBlob json.RawMessage, patch map[string]json.RawMessage) (json.RawMessage, error) {
	var m map[string]json.RawMessage
	if len(oldBlob) > 0 && string(oldBlob) != "null" {
		if err := json.Unmarshal(oldBlob, &m); err != nil {
			return nil, fmt.Errorf("unmarshal current metadata: %w", err)
		}
	}
	if m == nil {
		m = make(map[string]json.RawMessage)
	}
	for k, v := range patch {
		if string(v) == "null" {
			delete(m, k)
		} else {
			m[k] = v
		}
	}
	out, err := json.Marshal(m)
	if err != nil {
		return nil, fmt.Errorf("marshal new metadata: %w", err)
	}
	return out, nil
}
