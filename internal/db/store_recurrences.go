package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/wesm/kata/internal/recurrence"
	"github.com/wesm/kata/internal/shortid"
	katauid "github.com/wesm/kata/internal/uid"
	sqlite3 "modernc.org/sqlite/lib"
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

	// Compute the initial cursor: the first occurrence on or after dtstart.
	// recurrence.Next returns nil for trivially-empty rules (e.g. UNTIL in the
	// past); in that case the recurrence is born "exhausted" with cursor=NULL.
	firstNext, err := recurrence.Next(in.Rule, in.DTStart, in.Timezone)
	if err != nil {
		return rec, fmt.Errorf("compute first occurrence: %w", err)
	}

	res, err := tx.ExecContext(ctx, `
		INSERT INTO recurrences
		  (uid, project_id, rrule, dtstart, timezone,
		   template_title, template_body, template_owner, template_priority,
		   template_labels, template_metadata, next_occurrence_key,
		   author, revision)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1)`,
		recUID, in.ProjectID, in.Rule, in.DTStart, in.Timezone,
		in.Template.Title, in.Template.Body,
		in.Template.Owner, in.Template.Priority,
		labelsJSON, metaJSON, firstNext, // *string → NULL when nil
		in.Actor)
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

// MaterializeNextOut carries the results of a successful MaterializeNext call.
type MaterializeNextOut struct {
	// NewIssueID is the row id of the newly inserted issue (zero when Skipped).
	NewIssueID int64
	// NewIssueUID is the UID of the inserted or already-existing issue.
	NewIssueUID string
	// OccurrenceKey is the occurrence date that was materialized.
	OccurrenceKey string
	// Skipped is true when the occurrence already existed (race with another writer).
	Skipped bool
}

// MaterializeNext walks the recurrence's RRULE past afterKey, inserts the next
// issue instance in the same tx (seeded from the template), and emits
// issue.created + recurrence.materialized events. If the new issue's
// (recurrence_id, occurrence_key) collides with an existing row (race with
// another writer), it emits recurrence.materialization_skipped instead and
// advances next_occurrence_key one step past the duplicate so future
// materializations don't loop on the same key. When the rule is exhausted on
// transition, emits recurrence.exhausted.
func (d *DB) MaterializeNext(
	ctx context.Context, tx *sql.Tx, recurrenceID int64, afterKey, actor string,
) (MaterializeNextOut, error) {
	var out MaterializeNextOut

	var (
		r           Recurrence
		projectName string
	)
	err := tx.QueryRowContext(ctx, `
		SELECT r.id, r.uid, r.project_id, p.name,
		       r.rrule, r.dtstart, r.timezone,
		       r.template_title, r.template_body, r.template_owner, r.template_priority,
		       r.template_labels, r.template_metadata, r.next_occurrence_key,
		       r.last_materialized_uid, r.author, r.revision,
		       r.created_at, r.updated_at, r.deleted_at
		  FROM recurrences r JOIN projects p ON p.id = r.project_id
		 WHERE r.id = ?`, recurrenceID,
	).Scan(&r.ID, &r.UID, &r.ProjectID, &projectName,
		&r.RRule, &r.DTStart, &r.Timezone,
		&r.TemplateTitle, &r.TemplateBody, &r.TemplateOwner, &r.TemplatePriority,
		&r.TemplateLabels, &r.TemplateMetadata, &r.NextOccurrenceKey,
		&r.LastMaterializedUID, &r.Author, &r.Revision,
		&r.CreatedAt, &r.UpdatedAt, &r.DeletedAt)
	if err != nil {
		return out, err
	}
	if r.DeletedAt != nil {
		return out, nil
	}

	next, err := recurrence.Walk(r.RRule, r.DTStart, r.Timezone, afterKey)
	if err != nil {
		return out, fmt.Errorf("walk rrule: %w", err)
	}

	if next == nil {
		// Exhausted — emit transition event only when previously non-empty.
		if r.NextOccurrenceKey != nil && *r.NextOccurrenceKey != "" {
			if _, err := tx.ExecContext(ctx,
				`UPDATE recurrences
				    SET next_occurrence_key = NULL,
				        revision             = revision + 1,
				        updated_at           = strftime('%Y-%m-%dT%H:%M:%fZ','now')
				  WHERE id = ?`, recurrenceID,
			); err != nil {
				return out, err
			}
			payload, mErr := json.Marshal(map[string]string{"recurrence_uid": r.UID})
			if mErr != nil {
				return out, fmt.Errorf("marshal exhausted payload: %w", mErr)
			}
			if _, err := d.insertEventTx(ctx, tx, eventInsert{
				ProjectID:   r.ProjectID,
				ProjectName: projectName,
				Type:        "recurrence.exhausted",
				Actor:       actor,
				Payload:     string(payload),
			}); err != nil {
				return out, err
			}
		}
		return out, nil
	}
	nextKey := *next

	// Compose new issue metadata: template_metadata merged with scheduled_on.
	var tmplMeta map[string]json.RawMessage
	if err := json.Unmarshal([]byte(r.TemplateMetadata), &tmplMeta); err != nil {
		return out, fmt.Errorf("parse template_metadata: %w", err)
	}
	if tmplMeta == nil {
		tmplMeta = map[string]json.RawMessage{}
	}
	scheduledJSON, _ := json.Marshal(nextKey)
	tmplMeta["scheduled_on"] = scheduledJSON
	issueMetadata, err := json.Marshal(tmplMeta)
	if err != nil {
		return out, fmt.Errorf("marshal issue metadata: %w", err)
	}

	newUID, err := katauid.New()
	if err != nil {
		return out, fmt.Errorf("generate uid: %w", err)
	}
	newShortID, err := assignShortIDIn(ctx, tx, []int64{r.ProjectID}, newUID, shortid.MinLength)
	if err != nil {
		return out, fmt.Errorf("assign short_id: %w", err)
	}

	_, err = tx.ExecContext(ctx, `
		INSERT INTO issues
		  (uid, project_id, short_id, title, body, status,
		   owner, priority, author, metadata, revision,
		   recurrence_id, occurrence_key, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, 'open', ?, ?, ?, ?, 1, ?, ?,
		        strftime('%Y-%m-%dT%H:%M:%fZ','now'),
		        strftime('%Y-%m-%dT%H:%M:%fZ','now'))`,
		newUID, r.ProjectID, newShortID, r.TemplateTitle, r.TemplateBody,
		r.TemplateOwner, r.TemplatePriority, actor, string(issueMetadata),
		r.ID, nextKey,
	)
	if err != nil {
		if isUniqueConstraint(err) {
			return d.handleMaterializeCollision(ctx, tx, r, projectName, nextKey, actor)
		}
		return out, err
	}

	var newIssueID int64
	if err := tx.QueryRowContext(ctx,
		`SELECT id FROM issues WHERE uid = ?`, newUID,
	).Scan(&newIssueID); err != nil {
		return out, err
	}

	// Seed labels from template_labels.
	var labels []string
	_ = json.Unmarshal([]byte(r.TemplateLabels), &labels)
	for _, lbl := range labels {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO issue_labels (issue_id, label, author) VALUES (?, ?, ?)`,
			newIssueID, lbl, actor,
		); err != nil {
			return out, err
		}
	}

	// Advance recurrence cursor to the key strictly after nextKey.
	afterNext, err := recurrence.Walk(r.RRule, r.DTStart, r.Timezone, nextKey)
	if err != nil {
		return out, fmt.Errorf("walk after next: %w", err)
	}
	var nextNext *string
	if afterNext != nil {
		v := *afterNext
		nextNext = &v
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE recurrences
		    SET next_occurrence_key   = ?,
		        last_materialized_uid = ?,
		        revision              = revision + 1,
		        updated_at            = strftime('%Y-%m-%dT%H:%M:%fZ','now')
		  WHERE id = ?`,
		nextNext, newUID, r.ID,
	); err != nil {
		return out, err
	}

	// Emit issue.created (with recurrence linkage) and recurrence.materialized.
	issueCreatedPayload, err := json.Marshal(map[string]any{
		"title":          r.TemplateTitle,
		"body":           r.TemplateBody,
		"recurrence_uid": r.UID,
		"occurrence_key": nextKey,
		"labels":         labels,
	})
	if err != nil {
		return out, fmt.Errorf("marshal issue.created payload: %w", err)
	}
	if _, err := d.insertEventTx(ctx, tx, eventInsert{
		ProjectID:   r.ProjectID,
		ProjectName: projectName,
		IssueID:     &newIssueID,
		IssueUID:    &newUID,
		Type:        "issue.created",
		Actor:       actor,
		Payload:     string(issueCreatedPayload),
	}); err != nil {
		return out, err
	}

	matPayload, err := json.Marshal(map[string]string{
		"recurrence_uid": r.UID,
		"occurrence_key": nextKey,
		"issue_uid":      newUID,
	})
	if err != nil {
		return out, fmt.Errorf("marshal materialized payload: %w", err)
	}
	if _, err := d.insertEventTx(ctx, tx, eventInsert{
		ProjectID:   r.ProjectID,
		ProjectName: projectName,
		IssueID:     &newIssueID,
		IssueUID:    &newUID,
		Type:        "recurrence.materialized",
		Actor:       actor,
		Payload:     string(matPayload),
	}); err != nil {
		return out, err
	}

	// If we just materialized the LAST occurrence (cursor transitioned to NULL),
	// emit recurrence.exhausted in the same tx — this is the only opportunity:
	// subsequent close attempts will see cursor=NULL and skip the no-next guard.
	if r.NextOccurrenceKey != nil && *r.NextOccurrenceKey != "" && nextNext == nil {
		exhPayload, mErr := json.Marshal(map[string]string{"recurrence_uid": r.UID})
		if mErr != nil {
			return out, fmt.Errorf("marshal exhausted payload: %w", mErr)
		}
		if _, err := d.insertEventTx(ctx, tx, eventInsert{
			ProjectID:   r.ProjectID,
			ProjectName: projectName,
			IssueID:     &newIssueID,
			IssueUID:    &newUID,
			Type:        "recurrence.exhausted",
			Actor:       actor,
			Payload:     string(exhPayload),
		}); err != nil {
			return out, err
		}
	}

	out.NewIssueID = newIssueID
	out.NewIssueUID = newUID
	out.OccurrenceKey = nextKey
	return out, nil
}

// handleMaterializeCollision handles the race where (recurrence_id, occurrence_key)
// already exists. It advances next_occurrence_key one step past the duplicate,
// emits recurrence.materialization_skipped, and (if exhausted) recurrence.exhausted.
// Returns a MaterializeNextOut with Skipped=true, or an error.
func (d *DB) handleMaterializeCollision(
	ctx context.Context, tx *sql.Tx, r Recurrence, projectName, nextKey, actor string,
) (MaterializeNextOut, error) {
	var out MaterializeNextOut

	var existingUID string
	if err := tx.QueryRowContext(ctx,
		`SELECT uid FROM issues WHERE recurrence_id = ? AND occurrence_key = ?`,
		r.ID, nextKey,
	).Scan(&existingUID); err != nil {
		return out, err
	}

	// Advance cursor PAST nextKey so future materializations don't loop on the
	// duplicate. If afterNext is nil (exhausted), set NULL.
	afterNext, err := recurrence.Walk(r.RRule, r.DTStart, r.Timezone, nextKey)
	if err != nil {
		return out, fmt.Errorf("walk after conflict: %w", err)
	}
	var nextNext *string
	if afterNext != nil {
		v := *afterNext
		nextNext = &v
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE recurrences
		    SET last_materialized_uid = ?,
		        next_occurrence_key   = ?,
		        revision              = revision + 1,
		        updated_at            = strftime('%Y-%m-%dT%H:%M:%fZ','now')
		  WHERE id = ?`,
		existingUID, nextNext, r.ID,
	); err != nil {
		return out, err
	}

	skipPayload, mErr := json.Marshal(map[string]string{
		"recurrence_uid":     r.UID,
		"occurrence_key":     nextKey,
		"existing_issue_uid": existingUID,
		"reason":             "already_exists",
	})
	if mErr != nil {
		return out, fmt.Errorf("marshal skipped payload: %w", mErr)
	}
	if _, err := d.insertEventTx(ctx, tx, eventInsert{
		ProjectID:   r.ProjectID,
		ProjectName: projectName,
		Type:        "recurrence.materialization_skipped",
		Actor:       actor,
		Payload:     string(skipPayload),
	}); err != nil {
		return out, err
	}

	if afterNext == nil {
		exhPayload, mErr := json.Marshal(map[string]string{"recurrence_uid": r.UID})
		if mErr != nil {
			return out, fmt.Errorf("marshal exhausted payload: %w", mErr)
		}
		if _, err := d.insertEventTx(ctx, tx, eventInsert{
			ProjectID:   r.ProjectID,
			ProjectName: projectName,
			Type:        "recurrence.exhausted",
			Actor:       actor,
			Payload:     string(exhPayload),
		}); err != nil {
			return out, err
		}
	}

	out.Skipped = true
	out.OccurrenceKey = nextKey
	out.NewIssueUID = existingUID
	return out, nil
}

// isUniqueConstraint reports whether err is a SQLite UNIQUE constraint violation.
func isUniqueConstraint(err error) bool {
	var coded sqliteCodeError
	if !errors.As(err, &coded) {
		return false
	}
	return coded.Code() == sqlite3.SQLITE_CONSTRAINT_UNIQUE
}
