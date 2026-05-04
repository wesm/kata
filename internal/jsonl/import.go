package jsonl

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/wesm/kata/internal/db"
	katauid "github.com/wesm/kata/internal/uid"
)

// ImportOptions controls optional import behaviors.
type ImportOptions struct {
	// NewInstance preserves the target's meta.instance_uid (the value db.Open
	// wrote on first open) instead of overwriting it with the source's. The
	// imported events.origin_instance_uid and purge_log.origin_instance_uid
	// columns are NOT rewritten — they preserve the original origins so a
	// future federation loop-detector can tell which events came from the
	// cloned-from instance versus the new local one.
	NewInstance bool
}

// Import reads JSONL records from r and inserts them into target.
func Import(ctx context.Context, r io.Reader, target *db.DB) error {
	return ImportWithOptions(ctx, r, target, ImportOptions{})
}

// ImportWithOptions is like Import but applies opts to control behavior.
func ImportWithOptions(ctx context.Context, r io.Reader, target *db.DB, opts ImportOptions) error {
	envs, err := NewDecoder(r).ReadAll(ctx)
	if err != nil {
		return err
	}
	exportVersion, err := validateExportVersion(envs)
	if err != nil {
		return err
	}
	tx, err := target.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin import: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `PRAGMA defer_foreign_keys=ON`); err != nil {
		return fmt.Errorf("defer foreign keys: %w", err)
	}

	// Capture the target's meta.instance_uid (set by db.Open) before the
	// envelope loop has a chance to overwrite it. This value is the LOCAL
	// origin used by v2→v3 fill rules even when the source's later upserts
	// replace meta.instance_uid in the same transaction (default-mode v3
	// import).
	localInstanceUID, err := readMetaInstanceUID(ctx, tx)
	if err != nil {
		return err
	}

	for _, env := range envs {
		if err := importEnvelope(ctx, tx, env, exportVersion, localInstanceUID, opts); err != nil {
			return err
		}
	}
	if err := recordImportSchemaVersion(ctx, tx); err != nil {
		return err
	}
	if err := reconcileSequences(ctx, tx); err != nil {
		return err
	}
	if err := validateBeforeCommit(ctx, tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit import: %w", err)
	}
	// Default mode may have overwritten meta.instance_uid with the source's
	// value; the target's cached InstanceUID() must follow suit so subsequent
	// inserts on this handle stamp the right origin. (--new-instance leaves
	// the row at db.Open's value, so the refresh is a no-op there.)
	if err := target.RefreshInstanceUID(ctx); err != nil {
		return err
	}
	return nil
}

func readMetaInstanceUID(ctx context.Context, tx *sql.Tx) (string, error) {
	var v string
	err := tx.QueryRowContext(ctx,
		`SELECT value FROM meta WHERE key='instance_uid'`).Scan(&v)
	if err != nil {
		return "", fmt.Errorf("read target instance_uid: %w", err)
	}
	return v, nil
}

func validateExportVersion(envs []Envelope) (int, error) {
	var rec metaRecord
	if err := decodeData(envs[0], &rec); err != nil {
		return 0, err
	}
	version, err := strconv.Atoi(rec.Value)
	if err != nil {
		return 0, fmt.Errorf("invalid export_version %q: %w", rec.Value, err)
	}
	if version > db.CurrentSchemaVersion() {
		return 0, fmt.Errorf("unsupported export_version %d for current schema version %d", version, db.CurrentSchemaVersion())
	}
	if version < 1 {
		return 0, fmt.Errorf("invalid export_version %d", version)
	}
	return version, nil
}

func importEnvelope(ctx context.Context, tx *sql.Tx, env Envelope, exportVersion int, localInstanceUID string, opts ImportOptions) error {
	switch env.Kind {
	case KindMeta:
		var rec metaRecord
		if err := decodeData(env, &rec); err != nil {
			return err
		}
		if rec.Key == "export_version" {
			return nil
		}
		if rec.Key == "schema_version" && exportVersion < db.CurrentSchemaVersion() {
			return nil
		}
		// --new-instance: skip the source's meta.instance_uid record so the
		// target keeps the value db.Open wrote. The imported events and
		// purge_log rows still carry the source's origin_instance_uid (per
		// §5.2): they were authored elsewhere and the new clone needs to know
		// that for future federation loop detection.
		if rec.Key == "instance_uid" && opts.NewInstance {
			return nil
		}
		_, err := tx.ExecContext(ctx,
			`INSERT INTO meta(key, value) VALUES(?, ?)
			 ON CONFLICT(key) DO UPDATE SET value=excluded.value`,
			rec.Key, rec.Value)
		return wrapImportErr(env.Kind, err)
	case KindProject:
		var rec projectRecord
		if err := decodeData(env, &rec); err != nil {
			return err
		}
		if err := fillProjectUID(&rec, exportVersion); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx,
			`INSERT INTO projects(id, uid, identity, name, created_at, next_issue_number, deleted_at)
			 VALUES(?, ?, ?, ?, ?, ?, ?)`,
			rec.ID, rec.UID, rec.Identity, rec.Name, rec.CreatedAt, rec.NextIssueNumber, rec.DeletedAt)
		return wrapImportErr(env.Kind, err)
	case KindProjectAlias:
		var rec projectAliasRecord
		if err := decodeData(env, &rec); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx,
			`INSERT INTO project_aliases(id, project_id, alias_identity, alias_kind, root_path, created_at, last_seen_at)
			 VALUES(?, ?, ?, ?, ?, ?, ?)`,
			rec.ID, rec.ProjectID, rec.AliasIdentity, rec.AliasKind, rec.RootPath, rec.CreatedAt, rec.LastSeenAt)
		return wrapImportErr(env.Kind, err)
	case KindIssue:
		var rec issueRecord
		if err := decodeData(env, &rec); err != nil {
			return err
		}
		if err := fillIssueUID(&rec, exportVersion); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx,
			`INSERT INTO issues(id, uid, project_id, number, title, body, status, closed_reason, owner, author,
			                    created_at, updated_at, closed_at, deleted_at)
			 VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			rec.ID, rec.UID, rec.ProjectID, rec.Number, rec.Title, rec.Body, rec.Status, rec.ClosedReason,
			rec.Owner, rec.Author, rec.CreatedAt, rec.UpdatedAt, rec.ClosedAt, rec.DeletedAt)
		return wrapImportErr(env.Kind, err)
	case KindComment:
		var rec commentRecord
		if err := decodeData(env, &rec); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx,
			`INSERT INTO comments(id, issue_id, author, body, created_at) VALUES(?, ?, ?, ?, ?)`,
			rec.ID, rec.IssueID, rec.Author, rec.Body, rec.CreatedAt)
		return wrapImportErr(env.Kind, err)
	case KindIssueLabel:
		var rec issueLabelRecord
		if err := decodeData(env, &rec); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx,
			`INSERT INTO issue_labels(issue_id, label, author, created_at) VALUES(?, ?, ?, ?)`,
			rec.IssueID, rec.Label, rec.Author, rec.CreatedAt)
		return wrapImportErr(env.Kind, err)
	case KindLink:
		var rec linkRecord
		if err := decodeData(env, &rec); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx,
			`INSERT INTO links(id, project_id, from_issue_id, from_issue_uid, to_issue_id, to_issue_uid, type, author, created_at)
			 VALUES(
			   ?, ?, ?,
			   COALESCE(NULLIF(?, ''), (SELECT uid FROM issues WHERE id = ?)),
			   ?,
			   COALESCE(NULLIF(?, ''), (SELECT uid FROM issues WHERE id = ?)),
			   ?, ?, ?
			 )`,
			rec.ID, rec.ProjectID, rec.FromIssueID, rec.FromIssueUID, rec.FromIssueID,
			rec.ToIssueID, rec.ToIssueUID, rec.ToIssueID, rec.Type, rec.Author, rec.CreatedAt)
		return wrapImportErr(env.Kind, err)
	case KindEvent:
		var rec eventRecord
		if err := decodeData(env, &rec); err != nil {
			return err
		}
		if err := fillEventUIDs(ctx, tx, &rec); err != nil {
			return err
		}
		if err := fillEventV3Identity(&rec, exportVersion, localInstanceUID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx,
			`INSERT INTO events(id, uid, origin_instance_uid, project_id, project_identity, issue_id, issue_uid, issue_number, related_issue_id, related_issue_uid,
			                    type, actor, payload, created_at)
			 VALUES(
			   ?, ?, ?, ?, ?, ?,
			   COALESCE(?, (SELECT uid FROM issues WHERE id = ?)),
			   ?, ?,
			   COALESCE(?, (SELECT uid FROM issues WHERE id = ?)),
			   ?, ?, ?, ?
			 )`,
			rec.ID, rec.UID, rec.OriginInstanceUID,
			rec.ProjectID, rec.ProjectIdentity, rec.IssueID,
			stringPtrValue(rec.IssueUID), rec.IssueID,
			rec.IssueNumber, rec.RelatedIssueID,
			stringPtrValue(rec.RelatedIssueUID), rec.RelatedIssueID,
			rec.Type, rec.Actor, string(rec.Payload), rec.CreatedAt)
		return wrapImportErr(env.Kind, err)
	case KindPurgeLog:
		var rec purgeLogRecord
		if err := decodeData(env, &rec); err != nil {
			return err
		}
		if err := fillPurgeLogV3Identity(&rec, exportVersion, localInstanceUID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx,
			`INSERT INTO purge_log(id, uid, origin_instance_uid, project_id, purged_issue_id, issue_uid, project_uid, project_identity, issue_number, issue_title,
			                       issue_author, comment_count, link_count, label_count, event_count,
			                       events_deleted_min_id, events_deleted_max_id, purge_reset_after_event_id,
			                       actor, reason, purged_at)
			 VALUES(
			   ?, ?, ?, ?, ?,
			   COALESCE(?, (SELECT uid FROM issues WHERE id = ?)),
			   COALESCE(?, (SELECT uid FROM projects WHERE id = ?)),
			   ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?
			 )`,
			rec.ID, rec.UID, rec.OriginInstanceUID,
			rec.ProjectID, rec.PurgedIssueID,
			stringPtrValue(rec.IssueUID), rec.PurgedIssueID,
			stringPtrValue(rec.ProjectUID), rec.ProjectID,
			rec.ProjectIdentity, rec.IssueNumber,
			rec.IssueTitle, rec.IssueAuthor, rec.CommentCount, rec.LinkCount, rec.LabelCount,
			rec.EventCount, rec.EventsDeletedMinID, rec.EventsDeletedMaxID, rec.PurgeResetAfterEventID,
			rec.Actor, rec.Reason, rec.PurgedAt)
		return wrapImportErr(env.Kind, err)
	case KindSQLiteSequence:
		var rec sqliteSequenceRecord
		if err := decodeData(env, &rec); err != nil {
			return err
		}
		return upsertSequence(ctx, tx, rec.Name, rec.Seq)
	default:
		return fmt.Errorf("import %s: unsupported kind", env.Kind)
	}
}

func recordImportSchemaVersion(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx,
		`INSERT INTO meta(key, value) VALUES('schema_version', ?)
		 ON CONFLICT(key) DO UPDATE SET value=excluded.value`,
		strconv.Itoa(db.CurrentSchemaVersion()))
	if err != nil {
		return fmt.Errorf("record import schema version: %w", err)
	}
	return nil
}

func decodeData(env Envelope, dst any) error {
	if err := json.Unmarshal(env.Data, dst); err != nil {
		return fmt.Errorf("decode %s data: %w", env.Kind, err)
	}
	return nil
}

func wrapImportErr(kind Kind, err error) error {
	if err != nil {
		return fmt.Errorf("import %s: %w", kind, err)
	}
	return nil
}

func fillProjectUID(rec *projectRecord, exportVersion int) error {
	if exportVersion >= 2 || rec.UID != "" {
		return nil
	}
	t, err := parseExportTime(rec.CreatedAt)
	if err != nil {
		return fmt.Errorf("fill project uid: %w", err)
	}
	uid, err := katauid.FromStableSeed([]byte(fmt.Sprintf("project:%d:%s", rec.ID, rec.Identity)), t)
	if err != nil {
		return fmt.Errorf("fill project uid: %w", err)
	}
	rec.UID = uid
	return nil
}

func fillIssueUID(rec *issueRecord, exportVersion int) error {
	if exportVersion >= 2 || rec.UID != "" {
		return nil
	}
	t, err := parseExportTime(rec.CreatedAt)
	if err != nil {
		return fmt.Errorf("fill issue uid: %w", err)
	}
	uid, err := katauid.FromStableSeed([]byte(fmt.Sprintf("issue:%d:%d", rec.ProjectID, rec.Number)), t)
	if err != nil {
		return fmt.Errorf("fill issue uid: %w", err)
	}
	rec.UID = uid
	return nil
}

func fillEventUIDs(ctx context.Context, tx *sql.Tx, rec *eventRecord) error {
	if rec.IssueID != nil && rec.IssueUID == nil {
		issueUID, err := lookupIssueUID(ctx, tx, *rec.IssueID)
		if err != nil {
			return fmt.Errorf("corrupt_event_fk: event %d issue_id %d: %w", rec.ID, *rec.IssueID, err)
		}
		rec.IssueUID = &issueUID
	}
	if rec.RelatedIssueID != nil && rec.RelatedIssueUID == nil {
		issueUID, err := lookupIssueUID(ctx, tx, *rec.RelatedIssueID)
		if err != nil {
			return fmt.Errorf("corrupt_event_fk: event %d related_issue_id %d: %w", rec.ID, *rec.RelatedIssueID, err)
		}
		rec.RelatedIssueUID = &issueUID
	}
	return nil
}

// fillEventV3Identity backfills events.uid + events.origin_instance_uid for
// pre-v3 sources per spec §5.3. The event UID is deterministic across reruns
// (FromStableSeed of project_id+id+created_at). The origin_instance_uid is the
// destination's local instance UID — intentionally non-deterministic across
// reruns: re-cutover from the same v2 source produces a different LOCAL and
// therefore different origins on every backfilled event. v3 sources carry both
// fields verbatim.
func fillEventV3Identity(rec *eventRecord, exportVersion int, localInstanceUID string) error {
	if exportVersion >= 3 {
		return nil
	}
	if rec.UID == "" {
		t, err := parseExportTime(rec.CreatedAt)
		if err != nil {
			return fmt.Errorf("fill event uid: %w", err)
		}
		uid, err := katauid.FromStableSeed([]byte(fmt.Sprintf("event:%d:%d", rec.ProjectID, rec.ID)), t)
		if err != nil {
			return fmt.Errorf("fill event uid: %w", err)
		}
		rec.UID = uid
	}
	if rec.OriginInstanceUID == "" {
		rec.OriginInstanceUID = localInstanceUID
	}
	return nil
}

// fillPurgeLogV3Identity backfills purge_log.uid + purge_log.origin_instance_uid
// for pre-v3 sources per spec §5.3. Mirrors fillEventV3Identity.
func fillPurgeLogV3Identity(rec *purgeLogRecord, exportVersion int, localInstanceUID string) error {
	if exportVersion >= 3 {
		return nil
	}
	if rec.UID == "" {
		t, err := parseExportTime(rec.PurgedAt)
		if err != nil {
			return fmt.Errorf("fill purge_log uid: %w", err)
		}
		uid, err := katauid.FromStableSeed([]byte(fmt.Sprintf("purge:%d:%d", rec.ProjectID, rec.ID)), t)
		if err != nil {
			return fmt.Errorf("fill purge_log uid: %w", err)
		}
		rec.UID = uid
	}
	if rec.OriginInstanceUID == "" {
		rec.OriginInstanceUID = localInstanceUID
	}
	return nil
}

func lookupIssueUID(ctx context.Context, tx *sql.Tx, issueID int64) (string, error) {
	var issueUID string
	if err := tx.QueryRowContext(ctx, `SELECT uid FROM issues WHERE id = ?`, issueID).Scan(&issueUID); err != nil {
		if err == sql.ErrNoRows {
			return "", db.ErrNotFound
		}
		return "", err
	}
	return issueUID, nil
}

func parseExportTime(s string) (time.Time, error) {
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05.999999999-07:00", "2006-01-02 15:04:05"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("parse timestamp %q", s)
}

func stringPtrValue(s *string) any {
	if s == nil {
		return nil
	}
	return *s
}

type projectRecord struct {
	ID              int64   `json:"id"`
	UID             string  `json:"uid"`
	Identity        string  `json:"identity"`
	Name            string  `json:"name"`
	CreatedAt       string  `json:"created_at"`
	NextIssueNumber int64   `json:"next_issue_number"`
	DeletedAt       *string `json:"deleted_at,omitempty"`
}

type projectAliasRecord struct {
	ID            int64  `json:"id"`
	ProjectID     int64  `json:"project_id"`
	AliasIdentity string `json:"alias_identity"`
	AliasKind     string `json:"alias_kind"`
	RootPath      string `json:"root_path"`
	CreatedAt     string `json:"created_at"`
	LastSeenAt    string `json:"last_seen_at"`
}

type issueRecord struct {
	ID           int64   `json:"id"`
	UID          string  `json:"uid"`
	ProjectID    int64   `json:"project_id"`
	Number       int64   `json:"number"`
	Title        string  `json:"title"`
	Body         string  `json:"body"`
	Status       string  `json:"status"`
	ClosedReason *string `json:"closed_reason"`
	Owner        *string `json:"owner"`
	Author       string  `json:"author"`
	CreatedAt    string  `json:"created_at"`
	UpdatedAt    string  `json:"updated_at"`
	ClosedAt     *string `json:"closed_at"`
	DeletedAt    *string `json:"deleted_at"`
}

type commentRecord struct {
	ID        int64  `json:"id"`
	IssueID   int64  `json:"issue_id"`
	Author    string `json:"author"`
	Body      string `json:"body"`
	CreatedAt string `json:"created_at"`
}

type issueLabelRecord struct {
	IssueID   int64  `json:"issue_id"`
	Label     string `json:"label"`
	Author    string `json:"author"`
	CreatedAt string `json:"created_at"`
}

type linkRecord struct {
	ID           int64  `json:"id"`
	ProjectID    int64  `json:"project_id"`
	FromIssueID  int64  `json:"from_issue_id"`
	FromIssueUID string `json:"from_issue_uid"`
	ToIssueID    int64  `json:"to_issue_id"`
	ToIssueUID   string `json:"to_issue_uid"`
	Type         string `json:"type"`
	Author       string `json:"author"`
	CreatedAt    string `json:"created_at"`
}

type eventRecord struct {
	ID                int64           `json:"id"`
	UID               string          `json:"uid"`
	OriginInstanceUID string          `json:"origin_instance_uid"`
	ProjectID         int64           `json:"project_id"`
	ProjectIdentity   string          `json:"project_identity"`
	IssueID           *int64          `json:"issue_id"`
	IssueUID          *string         `json:"issue_uid"`
	IssueNumber       *int64          `json:"issue_number"`
	RelatedIssueID    *int64          `json:"related_issue_id"`
	RelatedIssueUID   *string         `json:"related_issue_uid"`
	Type              string          `json:"type"`
	Actor             string          `json:"actor"`
	Payload           json.RawMessage `json:"payload"`
	CreatedAt         string          `json:"created_at"`
}

type purgeLogRecord struct {
	ID                     int64   `json:"id"`
	UID                    string  `json:"uid"`
	OriginInstanceUID      string  `json:"origin_instance_uid"`
	ProjectID              int64   `json:"project_id"`
	PurgedIssueID          int64   `json:"purged_issue_id"`
	IssueUID               *string `json:"issue_uid"`
	ProjectUID             *string `json:"project_uid"`
	ProjectIdentity        string  `json:"project_identity"`
	IssueNumber            int64   `json:"issue_number"`
	IssueTitle             string  `json:"issue_title"`
	IssueAuthor            string  `json:"issue_author"`
	CommentCount           int64   `json:"comment_count"`
	LinkCount              int64   `json:"link_count"`
	LabelCount             int64   `json:"label_count"`
	EventCount             int64   `json:"event_count"`
	EventsDeletedMinID     *int64  `json:"events_deleted_min_id"`
	EventsDeletedMaxID     *int64  `json:"events_deleted_max_id"`
	PurgeResetAfterEventID *int64  `json:"purge_reset_after_event_id"`
	Actor                  string  `json:"actor"`
	Reason                 *string `json:"reason"`
	PurgedAt               string  `json:"purged_at"`
}

type sqliteSequenceRecord struct {
	Name string `json:"name"`
	Seq  int64  `json:"seq"`
}

func upsertSequence(ctx context.Context, tx *sql.Tx, name string, seq int64) error {
	res, err := tx.ExecContext(ctx, `UPDATE sqlite_sequence SET seq = ? WHERE name = ?`, seq, name)
	if err != nil {
		return fmt.Errorf("update sqlite_sequence %s: %w", name, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("sqlite_sequence rows affected: %w", err)
	}
	if n == 0 {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO sqlite_sequence(name, seq) VALUES(?, ?)`, name, seq); err != nil {
			return fmt.Errorf("insert sqlite_sequence %s: %w", name, err)
		}
	}
	return nil
}

func reconcileSequences(ctx context.Context, tx *sql.Tx) error {
	for _, table := range []string{"projects", "project_aliases", "issues", "comments", "links", "events", "purge_log"} {
		var maxID int64
		if err := tx.QueryRowContext(ctx,
			`SELECT COALESCE(MAX(id), 0) FROM `+table).Scan(&maxID); err != nil {
			return fmt.Errorf("max id for %s: %w", table, err)
		}
		var stored sql.NullInt64
		if err := tx.QueryRowContext(ctx,
			`SELECT MAX(seq) FROM sqlite_sequence WHERE name = ?`, table).Scan(&stored); err != nil {
			return fmt.Errorf("read sqlite_sequence %s: %w", table, err)
		}
		seq := maxID
		if stored.Valid && stored.Int64 > seq {
			seq = stored.Int64
		}
		if seq > 0 {
			if err := upsertSequence(ctx, tx, table, seq); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateBeforeCommit(ctx context.Context, tx *sql.Tx) error {
	fkRows, err := tx.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		return fmt.Errorf("foreign_key_check: %w", err)
	}
	defer func() { _ = fkRows.Close() }()
	if fkRows.Next() {
		return fmt.Errorf("foreign_key_check: violations found")
	}
	if err := fkRows.Err(); err != nil {
		return fmt.Errorf("foreign_key_check rows: %w", err)
	}

	rows, err := tx.QueryContext(ctx, `PRAGMA integrity_check`)
	if err != nil {
		return fmt.Errorf("integrity_check: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var msg string
		if err := rows.Scan(&msg); err != nil {
			return fmt.Errorf("integrity_check scan: %w", err)
		}
		if !strings.EqualFold(msg, "ok") {
			return fmt.Errorf("integrity_check: %s", msg)
		}
	}
	return rows.Err()
}
