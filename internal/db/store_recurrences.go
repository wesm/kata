package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	katauid "github.com/wesm/kata/internal/uid"
)

// RecurrenceTemplate carries the issue-template fields for a recurrence row.
// Owner and Priority are optional; Labels and Metadata default to empty
// collections when nil.
type RecurrenceTemplate struct {
	Title    string
	Body     string
	Owner    *string
	Priority *int64
	Labels   []string
	Metadata json.RawMessage
}

// CreateRecurrenceIn holds the inputs for CreateRecurrence.
type CreateRecurrenceIn struct {
	ProjectID int64
	Actor     string
	Rule      string
	DTStart   string
	Timezone  string
	Template  RecurrenceTemplate
}

// CreateRecurrence inserts a new recurrence row, emits a recurrence.created
// event, and returns the freshly-read row.
func (d *DB) CreateRecurrence(ctx context.Context, in CreateRecurrenceIn) (Recurrence, error) {
	var rec Recurrence
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return rec, err
	}
	defer func() { _ = tx.Rollback() }()

	// events.project_name is NOT NULL — load it before inserting the event.
	var projectName string
	if err := tx.QueryRowContext(ctx,
		`SELECT name FROM projects WHERE id = ? AND deleted_at IS NULL`,
		in.ProjectID,
	).Scan(&projectName); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return rec, fmt.Errorf("project %d not found", in.ProjectID)
		}
		return rec, err
	}

	recUID, err := katauid.New()
	if err != nil {
		return rec, fmt.Errorf("generate recurrence uid: %w", err)
	}

	labelsJSON := "[]"
	if len(in.Template.Labels) > 0 {
		b, merr := json.Marshal(in.Template.Labels)
		if merr != nil {
			return rec, fmt.Errorf("marshal labels: %w", merr)
		}
		labelsJSON = string(b)
	}
	metaJSON := "{}"
	if len(in.Template.Metadata) > 0 {
		metaJSON = string(in.Template.Metadata)
	}

	res, err := tx.ExecContext(ctx, `
		INSERT INTO recurrences
		  (uid, project_id, rrule, dtstart, timezone,
		   template_title, template_body, template_owner, template_priority,
		   template_labels, template_metadata, author, revision)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1)`,
		recUID, in.ProjectID, in.Rule, in.DTStart, in.Timezone,
		in.Template.Title, in.Template.Body,
		in.Template.Owner, in.Template.Priority,
		labelsJSON, metaJSON, in.Actor)
	if err != nil {
		return rec, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return rec, err
	}

	payload, err := json.Marshal(map[string]any{
		"recurrence_uid": recUID,
		"rrule":          in.Rule,
		"dtstart":        in.DTStart,
		"timezone":       in.Timezone,
		"template_title": in.Template.Title,
		"template_body":  in.Template.Body,
	})
	if err != nil {
		return rec, fmt.Errorf("marshal event payload: %w", err)
	}
	if _, err := d.insertEventTx(ctx, tx, eventInsert{
		ProjectID:   in.ProjectID,
		ProjectName: projectName,
		Type:        "recurrence.created",
		Actor:       in.Actor,
		Payload:     string(payload),
	}); err != nil {
		return rec, err
	}

	if err := tx.Commit(); err != nil {
		return rec, err
	}
	return d.GetRecurrenceByID(ctx, id)
}

// RecurrenceUpdate holds the optional fields for PatchRecurrence. A nil field
// means "leave unchanged"; a non-nil field means "set to this value".
type RecurrenceUpdate struct {
	Rule             *string
	DTStart          *string
	Timezone         *string
	TemplateTitle    *string
	TemplateBody     *string
	TemplateOwner    *string
	TemplatePriority *int64
	TemplateLabels   *[]string
	TemplateMetadata *json.RawMessage
}

// PatchRecurrenceIn holds the inputs for PatchRecurrence.
type PatchRecurrenceIn struct {
	RecurrenceID int64
	IfMatchRev   int64
	Actor        string
	Update       RecurrenceUpdate
}

// PatchRecurrenceOut carries results from a successful PatchRecurrence call.
type PatchRecurrenceOut struct {
	Recurrence  Recurrence
	NewRevision int64
	Changed     bool
}

// PatchRecurrence runs an If-Match-guarded UPDATE comparing each supplied
// field against the current row. It builds a per-field {from, to} diff in
// JSON, emits a recurrence.updated event with that diff, and bumps revision.
// A patch where no fields change is a no-op: no event is emitted and revision
// is not bumped.
func (d *DB) PatchRecurrence(ctx context.Context, in PatchRecurrenceIn) (PatchRecurrenceOut, error) {
	var out PatchRecurrenceOut
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return out, err
	}
	defer func() { _ = tx.Rollback() }()

	cur, err := d.getRecurrenceTx(ctx, tx, in.RecurrenceID)
	if errors.Is(err, sql.ErrNoRows) {
		return out, fmt.Errorf("recurrence %d not found", in.RecurrenceID)
	}
	if err != nil {
		return out, err
	}
	if cur.DeletedAt != nil {
		return out, fmt.Errorf("recurrence %d soft-deleted", in.RecurrenceID)
	}
	if in.IfMatchRev != cur.Revision {
		return out, &RevisionConflictError{CurrentRevision: cur.Revision}
	}

	type diffEntry struct {
		From json.RawMessage `json:"from"`
		To   json.RawMessage `json:"to"`
	}
	diff := map[string]diffEntry{}
	var sets []string
	var args []any

	addDiff := func(field string, from, to any) {
		fromJSON, _ := json.Marshal(from)
		toJSON, _ := json.Marshal(to)
		diff[field] = diffEntry{From: fromJSON, To: toJSON}
	}

	if in.Update.Rule != nil && *in.Update.Rule != cur.RRule {
		addDiff("rrule", cur.RRule, *in.Update.Rule)
		sets = append(sets, "rrule = ?")
		args = append(args, *in.Update.Rule)
	}
	if in.Update.DTStart != nil && *in.Update.DTStart != cur.DTStart {
		addDiff("dtstart", cur.DTStart, *in.Update.DTStart)
		sets = append(sets, "dtstart = ?")
		args = append(args, *in.Update.DTStart)
	}
	if in.Update.Timezone != nil && *in.Update.Timezone != cur.Timezone {
		addDiff("timezone", cur.Timezone, *in.Update.Timezone)
		sets = append(sets, "timezone = ?")
		args = append(args, *in.Update.Timezone)
	}
	if in.Update.TemplateTitle != nil && *in.Update.TemplateTitle != cur.TemplateTitle {
		addDiff("template_title", cur.TemplateTitle, *in.Update.TemplateTitle)
		sets = append(sets, "template_title = ?")
		args = append(args, *in.Update.TemplateTitle)
	}
	if in.Update.TemplateBody != nil && *in.Update.TemplateBody != cur.TemplateBody {
		addDiff("template_body", cur.TemplateBody, *in.Update.TemplateBody)
		sets = append(sets, "template_body = ?")
		args = append(args, *in.Update.TemplateBody)
	}
	if in.Update.TemplateOwner != nil {
		var curOwner string
		if cur.TemplateOwner != nil {
			curOwner = *cur.TemplateOwner
		}
		if *in.Update.TemplateOwner != curOwner {
			addDiff("template_owner", curOwner, *in.Update.TemplateOwner)
			sets = append(sets, "template_owner = ?")
			args = append(args, *in.Update.TemplateOwner)
		}
	}
	if in.Update.TemplatePriority != nil {
		if cur.TemplatePriority == nil || *cur.TemplatePriority != *in.Update.TemplatePriority {
			addDiff("template_priority", cur.TemplatePriority, *in.Update.TemplatePriority)
			sets = append(sets, "template_priority = ?")
			args = append(args, *in.Update.TemplatePriority)
		}
	}
	if in.Update.TemplateLabels != nil {
		nextLabels, merr := json.Marshal(*in.Update.TemplateLabels)
		if merr != nil {
			return out, fmt.Errorf("marshal labels: %w", merr)
		}
		if string(nextLabels) != cur.TemplateLabels {
			addDiff("template_labels",
				json.RawMessage(cur.TemplateLabels),
				json.RawMessage(nextLabels))
			sets = append(sets, "template_labels = ?")
			args = append(args, string(nextLabels))
		}
	}
	if in.Update.TemplateMetadata != nil {
		if string(*in.Update.TemplateMetadata) != cur.TemplateMetadata {
			addDiff("template_metadata",
				json.RawMessage(cur.TemplateMetadata),
				json.RawMessage(*in.Update.TemplateMetadata))
			sets = append(sets, "template_metadata = ?")
			args = append(args, string(*in.Update.TemplateMetadata))
		}
	}

	if len(diff) == 0 {
		// No-op: no changed fields — commit (nothing to write) and return unchanged.
		if err := tx.Commit(); err != nil {
			return out, err
		}
		out.Recurrence = cur
		out.NewRevision = cur.Revision
		out.Changed = false
		return out, nil
	}

	newRev := cur.Revision + 1
	sets = append(sets, "revision = ?", "updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now')")
	args = append(args, newRev, in.RecurrenceID)
	// sets only contains column-name literals chosen above; user values are
	// parameterized via args. Safe to concatenate.
	q := "UPDATE recurrences SET " + strings.Join(sets, ", ") + " WHERE id = ?" // #nosec G202
	if _, err := tx.ExecContext(ctx, q, args...); err != nil {
		return out, err
	}

	var projectName string
	if err := tx.QueryRowContext(ctx,
		`SELECT name FROM projects WHERE id = ?`, cur.ProjectID,
	).Scan(&projectName); err != nil {
		return out, err
	}

	eventPayload, merr := json.Marshal(struct {
		RecurrenceUID string               `json:"recurrence_uid"`
		Diff          map[string]diffEntry `json:"diff"`
		RevisionNew   int64                `json:"revision_new"`
	}{cur.UID, diff, newRev})
	if merr != nil {
		return out, fmt.Errorf("marshal event payload: %w", merr)
	}

	if _, err := d.insertEventTx(ctx, tx, eventInsert{
		ProjectID:   cur.ProjectID,
		ProjectName: projectName,
		Type:        "recurrence.updated",
		Actor:       in.Actor,
		Payload:     string(eventPayload),
	}); err != nil {
		return out, err
	}

	if err := tx.Commit(); err != nil {
		return out, err
	}
	next, err := d.GetRecurrenceByID(ctx, in.RecurrenceID)
	if err != nil {
		return out, err
	}
	out.Recurrence = next
	out.NewRevision = newRev
	out.Changed = true
	return out, nil
}

// SoftDeleteRecurrence sets deleted_at on the recurrence row and emits a
// recurrence.deleted event. Returns an error if the row is already deleted
// or does not exist.
func (d *DB) SoftDeleteRecurrence(ctx context.Context, id int64, actor string) error {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	var pid int64
	var recUID, projectName string
	if err := tx.QueryRowContext(ctx, `
		SELECT r.project_id, r.uid, p.name
		  FROM recurrences r JOIN projects p ON p.id = r.project_id
		 WHERE r.id = ? AND r.deleted_at IS NULL`,
		id,
	).Scan(&pid, &recUID, &projectName); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("recurrence %d not found or already deleted", id)
		}
		return err
	}

	if _, err := tx.ExecContext(ctx, `
		UPDATE recurrences
		   SET deleted_at = strftime('%Y-%m-%dT%H:%M:%fZ','now'),
		       revision   = revision + 1
		 WHERE id = ?`, id,
	); err != nil {
		return err
	}

	payload, merr := json.Marshal(map[string]string{"recurrence_uid": recUID})
	if merr != nil {
		return fmt.Errorf("marshal event payload: %w", merr)
	}
	if _, err := d.insertEventTx(ctx, tx, eventInsert{
		ProjectID:   pid,
		ProjectName: projectName,
		Type:        "recurrence.deleted",
		Actor:       actor,
		Payload:     string(payload),
	}); err != nil {
		return err
	}
	return tx.Commit()
}

// recurrenceSelectFields is the canonical SELECT column list for recurrences.
const recurrenceSelectFields = `id, uid, project_id, rrule, dtstart, timezone,
    template_title, template_body, template_owner, template_priority,
    template_labels, template_metadata, next_occurrence_key,
    last_materialized_uid, author, revision, created_at, updated_at, deleted_at`

// scanner is the common interface satisfied by both *sql.Row and *sql.Rows.
type scanner interface {
	Scan(dest ...any) error
}

func scanRecurrence(row scanner) (Recurrence, error) {
	var r Recurrence
	err := row.Scan(
		&r.ID, &r.UID, &r.ProjectID, &r.RRule, &r.DTStart, &r.Timezone,
		&r.TemplateTitle, &r.TemplateBody, &r.TemplateOwner, &r.TemplatePriority,
		&r.TemplateLabels, &r.TemplateMetadata, &r.NextOccurrenceKey,
		&r.LastMaterializedUID, &r.Author, &r.Revision,
		&r.CreatedAt, &r.UpdatedAt, &r.DeletedAt,
	)
	return r, err
}

// GetRecurrenceByID returns the recurrence with the given row id.
func (d *DB) GetRecurrenceByID(ctx context.Context, id int64) (Recurrence, error) {
	return scanRecurrence(d.QueryRowContext(ctx,
		"SELECT "+recurrenceSelectFields+" FROM recurrences WHERE id = ?", id))
}

func (d *DB) getRecurrenceTx(ctx context.Context, tx *sql.Tx, id int64) (Recurrence, error) {
	return scanRecurrence(tx.QueryRowContext(ctx,
		"SELECT "+recurrenceSelectFields+" FROM recurrences WHERE id = ?", id))
}

// GetRecurrenceByUID returns the recurrence with the given UID.
func (d *DB) GetRecurrenceByUID(ctx context.Context, recUID string) (Recurrence, error) {
	return scanRecurrence(d.QueryRowContext(ctx,
		"SELECT "+recurrenceSelectFields+" FROM recurrences WHERE uid = ?", recUID))
}

// ListRecurrencesByProject returns all non-deleted recurrences for projectID,
// ordered by created_at DESC.
func (d *DB) ListRecurrencesByProject(ctx context.Context, projectID int64) ([]Recurrence, error) {
	rows, err := d.QueryContext(ctx,
		"SELECT "+recurrenceSelectFields+
			" FROM recurrences WHERE project_id = ? AND deleted_at IS NULL ORDER BY created_at DESC",
		projectID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Recurrence
	for rows.Next() {
		r, err := scanRecurrence(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
