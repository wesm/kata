package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	katauid "github.com/wesm/kata/internal/uid"
)

// ErrNotFound is returned when a single-row lookup matches zero rows.
var ErrNotFound = errors.New("not found")

// CreateProject inserts a new projects row with default next_issue_number=1.
func (d *DB) CreateProject(ctx context.Context, identity, name string) (Project, error) {
	projectUID, err := katauid.New()
	if err != nil {
		return Project{}, fmt.Errorf("generate project uid: %w", err)
	}
	res, err := d.ExecContext(ctx,
		`INSERT INTO projects(uid, identity, name) VALUES(?, ?, ?)`, projectUID, identity, name)
	if err != nil {
		return Project{}, fmt.Errorf("insert project: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Project{}, fmt.Errorf("last id: %w", err)
	}
	return d.ProjectByID(ctx, id)
}

// ProjectByID fetches one project by its rowid. Archived (deleted_at != NULL)
// projects are returned as-is so callers like the merge / restore paths can
// see them; surface-level callers (HTTP handlers, CLI) inspect DeletedAt
// themselves.
func (d *DB) ProjectByID(ctx context.Context, id int64) (Project, error) {
	row := d.QueryRowContext(ctx, projectSelect+` WHERE id = ?`, id)
	return scanProject(row)
}

// ProjectByIdentity fetches one project by its UNIQUE identity. Archived
// projects are excluded — resolve flow uses this and an archived project
// must look gone from the active surface. Callers needing the row even when
// archived (e.g. to surface a "this identity was archived" error) can
// follow up with ProjectByIdentityIncludingArchived.
func (d *DB) ProjectByIdentity(ctx context.Context, identity string) (Project, error) {
	row := d.QueryRowContext(ctx,
		projectSelect+` WHERE identity = ? AND deleted_at IS NULL`, identity)
	return scanProject(row)
}

// ProjectByIdentityIncludingArchived returns the project even when archived.
// Used by error-message paths that want to distinguish "no project at all"
// from "project was archived".
func (d *DB) ProjectByIdentityIncludingArchived(ctx context.Context, identity string) (Project, error) {
	row := d.QueryRowContext(ctx, projectSelect+` WHERE identity = ?`, identity)
	return scanProject(row)
}

// RenameProject updates a project's display name without changing its stable
// identity, aliases, or issue numbering.
func (d *DB) RenameProject(ctx context.Context, id int64, name string) (Project, error) {
	res, err := d.ExecContext(ctx, `UPDATE projects SET name = ? WHERE id = ?`, name, id)
	if err != nil {
		return Project{}, fmt.Errorf("rename project: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return Project{}, fmt.Errorf("rename project rows affected: %w", err)
	}
	if n == 0 {
		return Project{}, ErrNotFound
	}
	return d.ProjectByID(ctx, id)
}

// ListProjects returns every active project ordered by id ASC. Archived
// projects (deleted_at != NULL) are excluded; callers needing them too can
// use ListProjectsIncludingArchived.
func (d *DB) ListProjects(ctx context.Context) ([]Project, error) {
	return d.listProjects(ctx, false)
}

// ListProjectsIncludingArchived returns every project including archived
// rows. Used by surfaces that want to render archived state explicitly
// (e.g. operator inspection or restore tooling).
func (d *DB) ListProjectsIncludingArchived(ctx context.Context) ([]Project, error) {
	return d.listProjects(ctx, true)
}

func (d *DB) listProjects(ctx context.Context, includeArchived bool) ([]Project, error) {
	q := projectSelect
	if !includeArchived {
		q += ` WHERE deleted_at IS NULL`
	}
	q += ` ORDER BY id ASC`
	rows, err := d.QueryContext(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Project
	for rows.Next() {
		p, err := scanProject(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// BatchProjectStats returns aggregate stats for every active project. The
// result includes projects with zero issues (Open=0, Closed=0) and zero
// events (LastEventAt=nil), driven by LEFT JOINs onto pre-aggregated
// subqueries. Pre-aggregation matters: the naive
// projects⋈issues⋈events GROUP BY shape would multiply each issue row by
// each event row and inflate counts. Spec §6.1.
func (d *DB) BatchProjectStats(ctx context.Context) (map[int64]ProjectStats, error) {
	const q = `
WITH
  issue_counts AS (
    SELECT
      project_id,
      SUM(CASE WHEN status = 'open'   THEN 1 ELSE 0 END) AS open_count,
      SUM(CASE WHEN status = 'closed' THEN 1 ELSE 0 END) AS closed_count
    FROM issues
    WHERE deleted_at IS NULL
    GROUP BY project_id
  ),
  event_max AS (
    -- julianday() normalizes both T-separated RFC3339 and space/offset
    -- legacy layouts to a numeric julian day, so MAX picks the
    -- absolute-latest event regardless of which text format was stored.
    -- strftime() formats it back to RFC3339Nano with a 'Z' zone, matching
    -- the layout the rest of the code emits via strftime() on insert.
    SELECT project_id,
           strftime('%Y-%m-%dT%H:%M:%fZ', MAX(julianday(created_at))) AS last_event_at
    FROM events
    GROUP BY project_id
  )
SELECT
  p.id,
  COALESCE(ic.open_count,   0),
  COALESCE(ic.closed_count, 0),
  em.last_event_at
FROM projects p
LEFT JOIN issue_counts ic ON ic.project_id = p.id
LEFT JOIN event_max    em ON em.project_id = p.id
WHERE p.deleted_at IS NULL
ORDER BY p.id`
	rows, err := d.QueryContext(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("batch project stats: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := map[int64]ProjectStats{}
	for rows.Next() {
		var (
			id     int64
			open   int
			closed int
			ts     sql.NullString
		)
		if err := rows.Scan(&id, &open, &closed, &ts); err != nil {
			return nil, fmt.Errorf("scan project stats: %w", err)
		}
		s := ProjectStats{Open: open, Closed: closed}
		if ts.Valid {
			t, err := parseSQLiteTimestamp(ts.String)
			if err != nil {
				return nil, fmt.Errorf("parse last_event_at %q: %w", ts.String, err)
			}
			s.LastEventAt = &t
		}
		out[id] = s
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows: %w", err)
	}
	return out, nil
}

// parseSQLiteTimestamp parses a TIMESTAMP-typed column value returned as a
// driver string. The current schema's strftime('%Y-%m-%dT%H:%M:%fZ','now')
// produces RFC3339 with millisecond precision and a 'Z' zone, but databases
// imported from older snapshots may carry SQLite's other supported text
// layouts: bare ("YYYY-MM-DD HH:MM:SS[.SSS]") or zoned with an explicit
// offset suffix (matching jsonl.parseExportTime). Fall through the layouts
// in order; surface the original error when none match so a corrupt value
// still returns an actionable wrap.
func parseSQLiteTimestamp(s string) (time.Time, error) {
	layouts := []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02 15:04:05.999999999-07:00",
		"2006-01-02 15:04:05.999999999",
		"2006-01-02 15:04:05",
	}
	var firstErr error
	for _, layout := range layouts {
		t, err := time.Parse(layout, s)
		if err == nil {
			return t, nil
		}
		if firstErr == nil {
			firstErr = err
		}
	}
	return time.Time{}, firstErr
}

// ProjectHasIssuesError is returned by ResetIssueCounter when the target
// project still has at least one row in the issues table. Carries the count
// observed at rejection time so callers can surface it without re-querying.
type ProjectHasIssuesError struct{ Count int64 }

func (e *ProjectHasIssuesError) Error() string {
	return fmt.Sprintf("project has %d issues", e.Count)
}

// CountIssues returns the number of rows currently in the issues table for
// projectID. Purged rows are gone from the table entirely (queries_delete.go),
// so a zero count means a clean slate.
func (d *DB) CountIssues(ctx context.Context, projectID int64) (int64, error) {
	var n int64
	if err := d.QueryRowContext(ctx,
		`SELECT count(*) FROM issues WHERE project_id = ?`, projectID).Scan(&n); err != nil {
		return 0, fmt.Errorf("count issues: %w", err)
	}
	return n, nil
}

// ErrInvalidCounterValue is returned by ResetIssueCounter when `to < 1`.
// The CLI and HTTP handler also enforce this; the DB-layer check is
// defense-in-depth so a buggy future caller can't quietly set the counter
// to zero or negative.
var ErrInvalidCounterValue = errors.New("counter value must be >= 1")

// ResetIssueCounter sets projects.next_issue_number to `to` for projectID,
// but only when the project has no rows in the issues table. The empty-check
// is folded into the UPDATE's WHERE clause (NOT EXISTS) so the gate is atomic
// with the write — SQLite's default DEFERRED transaction would not have
// taken a write lock for a separate count() query, leaving a window for a
// concurrent CreateIssue between count and update. Returns ErrNotFound when
// the project does not exist; *ProjectHasIssuesError carrying the observed
// count when the project is non-empty.
func (d *DB) ResetIssueCounter(ctx context.Context, projectID, to int64) error {
	if to < 1 {
		return ErrInvalidCounterValue
	}
	res, err := d.ExecContext(ctx,
		`UPDATE projects SET next_issue_number = ?
		 WHERE id = ?
		   AND NOT EXISTS (SELECT 1 FROM issues WHERE project_id = projects.id)`,
		to, projectID)
	if err != nil {
		return fmt.Errorf("update counter: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected: %w", err)
	}
	if n == 1 {
		return nil
	}
	// Update affected zero rows: either the project doesn't exist or it has
	// at least one issue. Disambiguate so callers map to 404 vs 409 — at this
	// point we've already failed the gate, so any drift in the count comes
	// from concurrent activity that postdated the rejection and is not
	// load-bearing.
	if _, perr := d.ProjectByID(ctx, projectID); errors.Is(perr, ErrNotFound) {
		return ErrNotFound
	} else if perr != nil {
		return fmt.Errorf("post-update project lookup: %w", perr)
	}
	count, cerr := d.CountIssues(ctx, projectID)
	if cerr != nil {
		return fmt.Errorf("post-update count: %w", cerr)
	}
	// A concurrent purge between the failed UPDATE and this lookup can drop
	// the count to zero, but the UPDATE's NOT EXISTS proved the project was
	// non-empty at the moment we tried to write. Clamp to 1 so the error
	// payload doesn't claim "has 0 issues" — that would be both confusing
	// and structurally inconsistent with the rejection itself.
	if count < 1 {
		count = 1
	}
	return &ProjectHasIssuesError{Count: count}
}

// AttachAlias inserts a project_aliases row.
func (d *DB) AttachAlias(ctx context.Context, projectID int64, identity, kind, rootPath string) (ProjectAlias, error) {
	res, err := d.ExecContext(ctx,
		`INSERT INTO project_aliases(project_id, alias_identity, alias_kind, root_path)
		 VALUES(?, ?, ?, ?)`, projectID, identity, kind, rootPath)
	if err != nil {
		return ProjectAlias{}, fmt.Errorf("insert alias: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return ProjectAlias{}, err
	}
	return d.AliasByID(ctx, id)
}

// AliasByIdentity returns the alias for a given alias_identity.
func (d *DB) AliasByIdentity(ctx context.Context, identity string) (ProjectAlias, error) {
	row := d.QueryRowContext(ctx, aliasSelect+` WHERE alias_identity = ?`, identity)
	return scanAlias(row)
}

// AliasByID returns the project_aliases row with the given id.
func (d *DB) AliasByID(ctx context.Context, id int64) (ProjectAlias, error) {
	row := d.QueryRowContext(ctx, aliasSelect+` WHERE id = ?`, id)
	return scanAlias(row)
}

// TouchAlias updates last_seen_at to now and rewrites root_path. Returns
// ErrNotFound when no alias has the given id.
func (d *DB) TouchAlias(ctx context.Context, aliasID int64, rootPath string) error {
	res, err := d.ExecContext(ctx,
		`UPDATE project_aliases
		 SET last_seen_at = strftime('%Y-%m-%dT%H:%M:%fZ','now'),
		     root_path    = ?
		 WHERE id = ?`, rootPath, aliasID)
	if err != nil {
		return fmt.Errorf("touch alias: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("touch alias rows affected: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// ProjectAliases returns every alias attached to a project ordered by id ASC.
func (d *DB) ProjectAliases(ctx context.Context, projectID int64) ([]ProjectAlias, error) {
	rows, err := d.QueryContext(ctx, aliasSelect+` WHERE project_id = ? ORDER BY id ASC`, projectID)
	if err != nil {
		return nil, fmt.Errorf("list aliases: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []ProjectAlias
	for rows.Next() {
		a, err := scanAlias(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// projectSelect is the canonical SELECT list for projects rows.
const projectSelect = `SELECT id, uid, identity, name, created_at, next_issue_number, deleted_at FROM projects`

// rowScanner is the subset of *sql.Row / *sql.Rows used by scan helpers.
type rowScanner interface {
	Scan(...any) error
}

func scanProject(r rowScanner) (Project, error) {
	var p Project
	err := r.Scan(&p.ID, &p.UID, &p.Identity, &p.Name, &p.CreatedAt, &p.NextIssueNumber, &p.DeletedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Project{}, ErrNotFound
	}
	if err != nil {
		return Project{}, fmt.Errorf("scan project: %w", err)
	}
	return p, nil
}

// aliasSelect is the canonical SELECT list for project_aliases rows.
const aliasSelect = `SELECT id, project_id, alias_identity, alias_kind, root_path, created_at, last_seen_at FROM project_aliases`

func scanAlias(r rowScanner) (ProjectAlias, error) {
	var a ProjectAlias
	err := r.Scan(&a.ID, &a.ProjectID, &a.AliasIdentity, &a.AliasKind, &a.RootPath, &a.CreatedAt, &a.LastSeenAt)
	if errors.Is(err, sql.ErrNoRows) {
		return ProjectAlias{}, ErrNotFound
	}
	if err != nil {
		return ProjectAlias{}, fmt.Errorf("scan alias: %w", err)
	}
	return a, nil
}

// ErrNoFields is returned by EditIssue when no field is set.
var ErrNoFields = errors.New("no fields to update")

// InitialLink describes one of the optional links created in the same TX as
// the issue itself. The to_number is resolved within the same project.
type InitialLink struct {
	Type     string // "parent" | "blocks" | "related"
	ToNumber int64
}

// CreateIssueParams carries inputs for CreateIssue.
type CreateIssueParams struct {
	ProjectID int64
	Title     string
	Body      string
	Author    string

	// Optional initial state. Plan 2 fields. CreateIssue inserts label/link
	// rows and applies the owner in the same TX, then folds them into the
	// issue.created event payload (no separate labeled/linked/assigned events).
	Labels []string
	Links  []InitialLink
	Owner  *string

	// Optional. When non-empty, both fields are folded into the issue.created
	// event payload so future LookupIdempotency calls can find the row via
	// idx_events_idempotency.
	IdempotencyKey         string
	IdempotencyFingerprint string
}

// ErrInitialLinkTargetNotFound is returned when an InitialLink's to_number
// does not resolve to an existing, non-deleted issue in the same project.
var ErrInitialLinkTargetNotFound = errors.New("initial link target not found")

// ErrInitialLinkInvalidType is returned when an InitialLink's Type is not one
// of {parent, blocks, related}.
var ErrInitialLinkInvalidType = errors.New("invalid initial link type")

// CreateIssue inserts an issue, applies optional initial labels/links/owner,
// and appends a single issue.created event whose payload describes the initial
// state. All steps run in one TX.
func (d *DB) CreateIssue(ctx context.Context, p CreateIssueParams) (Issue, Event, error) {
	// Normalize: a non-nil pointer to "" is treated as no owner. The payload
	// already drops empty owner via omitempty; making the DB column NULL keeps
	// the two views consistent and matches the unassigned semantic.
	owner := p.Owner
	if owner != nil && *owner == "" {
		owner = nil
	}

	// Dedupe links by (type, to_number) before validation so the validation
	// switch still rejects bad types and downstream insertion + payload both
	// reflect the same deduped slice.
	links := dedupeLinks(p.Links)

	// Link types are validated client-side (small fixed set) so a bad type
	// returns immediately without opening a transaction. Label charset is
	// validated server-side via classifyLabelInsertError because mirroring
	// the schema's GLOB pattern in Go would risk drift; a bad label rolls
	// back the whole TX, which is acceptable for an all-or-nothing create.
	for _, l := range links {
		switch l.Type {
		case "parent", "blocks", "related":
		default:
			return Issue{}, Event{}, ErrInitialLinkInvalidType
		}
	}

	tx, err := d.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return Issue{}, Event{}, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var (
		identity   string
		projectUID string
		nextNum    int64
	)
	if err := tx.QueryRowContext(ctx,
		`UPDATE projects
		 SET next_issue_number = next_issue_number + 1
		 WHERE id = ? AND deleted_at IS NULL
		 RETURNING next_issue_number - 1, identity, uid`, p.ProjectID).
		Scan(&nextNum, &identity, &projectUID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Issue{}, Event{}, ErrNotFound
		}
		return Issue{}, Event{}, fmt.Errorf("allocate issue number: %w", err)
	}

	issueUID, err := katauid.New()
	if err != nil {
		return Issue{}, Event{}, fmt.Errorf("generate issue uid: %w", err)
	}

	// Insert issue + optional owner column in one statement.
	res, err := tx.ExecContext(ctx,
		`INSERT INTO issues(uid, project_id, number, title, body, author, owner)
		 VALUES(?, ?, ?, ?, ?, ?, ?)`,
		issueUID, p.ProjectID, nextNum, p.Title, p.Body, p.Author, owner)
	if err != nil {
		return Issue{}, Event{}, fmt.Errorf("insert issue: %w", err)
	}
	issueID, err := res.LastInsertId()
	if err != nil {
		return Issue{}, Event{}, err
	}

	// Initial labels — dedupe (preserve first occurrence), then alphabetize
	// for stable payload + storage order.
	labels := dedupeStrings(p.Labels)
	sortStrings(labels)
	for _, label := range labels {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO issue_labels(issue_id, label, author) VALUES(?, ?, ?)`,
			issueID, label, p.Author); err != nil {
			return Issue{}, Event{}, classifyLabelInsertError(err)
		}
	}

	// Initial links — resolve to_number → to_issue_id within the same project,
	// excluding soft-deleted targets. The schema's same-project trigger
	// enforces the cross-project check, but we'd rather surface a typed
	// not-found than a generic constraint failure.
	for _, l := range links {
		var toIssueID int64
		err := tx.QueryRowContext(ctx,
			`SELECT id FROM issues
			 WHERE project_id = ? AND number = ? AND deleted_at IS NULL`,
			p.ProjectID, l.ToNumber).Scan(&toIssueID)
		if errors.Is(err, sql.ErrNoRows) {
			return Issue{}, Event{}, ErrInitialLinkTargetNotFound
		}
		if err != nil {
			return Issue{}, Event{}, fmt.Errorf("resolve initial link target: %w", err)
		}
		// Canonical ordering is a storage concern: the payload still reports
		// the caller's to_number unchanged, so the wire shape isn't affected.
		fromID, toID := issueID, toIssueID
		if l.Type == "related" && fromID > toID {
			fromID, toID = toID, fromID
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO links(project_id, from_issue_id, to_issue_id, from_issue_uid, to_issue_uid, type, author)
			 VALUES(?, ?, ?, (SELECT uid FROM issues WHERE id = ?), (SELECT uid FROM issues WHERE id = ?), ?, ?)`,
			p.ProjectID, fromID, toID, fromID, toID, l.Type, p.Author); err != nil {
			return Issue{}, Event{}, classifyLinkInsertError(err)
		}
	}

	payload := buildCreatedPayload(labels, links, owner, p.IdempotencyKey, p.IdempotencyFingerprint)

	evt, err := d.insertEventTx(ctx, tx, eventInsert{
		ProjectID:       p.ProjectID,
		ProjectUID:      projectUID,
		ProjectIdentity: identity,
		IssueID:         &issueID,
		IssueUID:        &issueUID,
		IssueNumber:     &nextNum,
		Type:            "issue.created",
		Actor:           p.Author,
		Payload:         payload,
	})
	if err != nil {
		return Issue{}, Event{}, err
	}

	if err := tx.Commit(); err != nil {
		return Issue{}, Event{}, fmt.Errorf("commit: %w", err)
	}

	issue, err := d.IssueByID(ctx, issueID)
	if err != nil {
		return Issue{}, Event{}, err
	}
	return issue, evt, nil
}

// buildCreatedPayload returns the issue.created event payload as JSON. Empty
// initial state → "{}". Otherwise emits keys for whichever components are set,
// preserving determinism (sorted labels) so events are byte-stable.
func buildCreatedPayload(labels []string, links []InitialLink, owner *string, idempotencyKey, idempotencyFingerprint string) string {
	type linkOut struct {
		Type     string `json:"type"`
		ToNumber int64  `json:"to_number"`
	}
	type out struct {
		Labels                 []string  `json:"labels,omitempty"`
		Links                  []linkOut `json:"links,omitempty"`
		Owner                  string    `json:"owner,omitempty"`
		IdempotencyKey         string    `json:"idempotency_key,omitempty"`
		IdempotencyFingerprint string    `json:"idempotency_fingerprint,omitempty"`
	}
	var o out
	if len(labels) > 0 {
		o.Labels = labels
	}
	if len(links) > 0 {
		// Layout-coupled with InitialLink: identical fields and order. If
		// InitialLink ever gains a field that linkOut shouldn't expose, replace
		// this conversion with explicit field assignment.
		o.Links = make([]linkOut, 0, len(links))
		for _, l := range links {
			o.Links = append(o.Links, linkOut(l))
		}
	}
	if owner != nil {
		o.Owner = *owner
	}
	o.IdempotencyKey = idempotencyKey
	o.IdempotencyFingerprint = idempotencyFingerprint
	bs, err := json.Marshal(o)
	if err != nil {
		return "{}"
	}
	return string(bs)
}

func dedupeStrings(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}

// dedupeLinks removes repeated (type, to_number) entries while preserving
// first-occurrence order. Used by CreateIssue to avoid hitting the schema's
// links UNIQUE on duplicate initial links and to keep the issue.created
// event payload aligned with what was actually inserted.
func dedupeLinks(in []InitialLink) []InitialLink {
	type key struct {
		Type     string
		ToNumber int64
	}
	seen := make(map[key]struct{}, len(in))
	out := make([]InitialLink, 0, len(in))
	for _, l := range in {
		k := key(l)
		if _, ok := seen[k]; ok {
			continue
		}
		seen[k] = struct{}{}
		out = append(out, l)
	}
	return out
}

func sortStrings(in []string) {
	sort.Strings(in)
}

// IssueByID fetches an issue by rowid. Includes soft-deleted rows; callers
// that want only live issues must filter on the returned issue's DeletedAt.
// (The destructive ladder and the idempotency-deleted path both need to see
// soft-deleted rows, which is why the filter isn't pushed into the query.)
func (d *DB) IssueByID(ctx context.Context, id int64) (Issue, error) {
	row := d.QueryRowContext(ctx, issueSelect+` WHERE i.id = ?`, id)
	return scanIssue(row)
}

// IssueByNumber fetches an issue by (project_id, number). Includes
// soft-deleted rows; see IssueByID for the rationale.
func (d *DB) IssueByNumber(ctx context.Context, projectID, number int64) (Issue, error) {
	row := d.QueryRowContext(ctx, issueSelect+` WHERE i.project_id = ? AND i.number = ?`, projectID, number)
	return scanIssue(row)
}

// IssueByUID fetches an issue by stable UID. Includes soft-deleted rows; see
// IssueByID for the rationale.
func (d *DB) IssueByUID(ctx context.Context, issueUID string) (Issue, error) {
	row := d.QueryRowContext(ctx, issueSelect+` WHERE i.uid = ?`, issueUID)
	return scanIssue(row)
}

// IssueUIDPrefixMatch returns issues whose UID starts with prefix, ordered by
// UID for deterministic ambiguity reporting.
func (d *DB) IssueUIDPrefixMatch(ctx context.Context, prefix string, limit int) ([]Issue, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := d.QueryContext(ctx, issueSelect+` WHERE i.uid LIKE ? || '%' ORDER BY i.uid ASC LIMIT ?`, prefix, limit)
	if err != nil {
		return nil, fmt.Errorf("issue uid prefix match: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Issue
	for rows.Next() {
		issue, err := scanIssue(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, issue)
	}
	return out, rows.Err()
}

// ListIssuesParams filters list output. Status="" → all. Empty struct → all.
type ListIssuesParams struct {
	ProjectID int64
	Status    string // "open" | "closed" | "" (any)
	Limit     int    // 0 = no limit
}

// ListIssues returns issues in the given project, excluding soft-deleted rows.
func (d *DB) ListIssues(ctx context.Context, p ListIssuesParams) ([]Issue, error) {
	q := issueSelect + ` WHERE i.project_id = ? AND i.deleted_at IS NULL`
	args := []any{p.ProjectID}
	if p.Status != "" {
		q += ` AND i.status = ?`
		args = append(args, p.Status)
	}
	q += ` ORDER BY i.updated_at DESC, i.id DESC`
	if p.Limit > 0 {
		q += fmt.Sprintf(` LIMIT %d`, p.Limit)
	}
	rows, err := d.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list issues: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Issue
	for rows.Next() {
		i, err := scanIssue(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, i)
	}
	return out, rows.Err()
}

// ListAllIssuesParams filters cross-project list output. ProjectID==0 means
// "every project"; >0 narrows to a single project. Status="" → all statuses.
type ListAllIssuesParams struct {
	ProjectID int64
	Status    string
	Limit     int
}

// ListAllIssues returns issues across one or every project, excluding
// soft-deleted rows. Ordering is (created_at DESC, id DESC) per #22 — a
// stable "newest first" feed across projects, distinct from the per-project
// endpoint's updated_at-DESC ordering which leads with recent activity.
func (d *DB) ListAllIssues(ctx context.Context, p ListAllIssuesParams) ([]Issue, error) {
	q := issueSelect + ` WHERE i.deleted_at IS NULL AND p.deleted_at IS NULL`
	var args []any
	if p.ProjectID > 0 {
		q += ` AND i.project_id = ?`
		args = append(args, p.ProjectID)
	}
	if p.Status != "" {
		q += ` AND i.status = ?`
		args = append(args, p.Status)
	}
	q += ` ORDER BY i.created_at DESC, i.id DESC`
	if p.Limit > 0 {
		q += fmt.Sprintf(` LIMIT %d`, p.Limit)
	}
	rows, err := d.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list all issues: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Issue
	for rows.Next() {
		i, err := scanIssue(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, i)
	}
	return out, rows.Err()
}

// CreateCommentParams carries inputs for CreateComment.
type CreateCommentParams struct {
	IssueID int64
	Author  string
	Body    string
}

// CreateComment appends a comment + issue.commented event in one tx, bumping
// issues.updated_at.
func (d *DB) CreateComment(ctx context.Context, p CreateCommentParams) (Comment, Event, error) {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return Comment{}, Event{}, err
	}
	defer func() { _ = tx.Rollback() }()

	issue, projectIdentity, err := lookupIssueForEvent(ctx, tx, p.IssueID)
	if err != nil {
		return Comment{}, Event{}, err
	}

	res, err := tx.ExecContext(ctx,
		`INSERT INTO comments(issue_id, author, body) VALUES(?, ?, ?)`,
		p.IssueID, p.Author, p.Body)
	if err != nil {
		return Comment{}, Event{}, fmt.Errorf("insert comment: %w", err)
	}
	commentID, err := res.LastInsertId()
	if err != nil {
		return Comment{}, Event{}, err
	}

	if _, err := tx.ExecContext(ctx,
		`UPDATE issues SET updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id = ?`,
		p.IssueID); err != nil {
		return Comment{}, Event{}, fmt.Errorf("touch issue: %w", err)
	}

	payload := fmt.Sprintf(`{"comment_id":%d}`, commentID)
	evt, err := d.insertEventTx(ctx, tx, eventInsert{
		ProjectID:       issue.ProjectID,
		ProjectIdentity: projectIdentity,
		IssueID:         &issue.ID,
		IssueNumber:     &issue.Number,
		Type:            "issue.commented",
		Actor:           p.Author,
		Payload:         payload,
	})
	if err != nil {
		return Comment{}, Event{}, err
	}

	if err := tx.Commit(); err != nil {
		return Comment{}, Event{}, err
	}

	var c Comment
	if err := d.QueryRowContext(ctx,
		`SELECT id, issue_id, author, body, created_at FROM comments WHERE id = ?`,
		commentID).Scan(&c.ID, &c.IssueID, &c.Author, &c.Body, &c.CreatedAt); err != nil {
		return Comment{}, Event{}, fmt.Errorf("read comment: %w", err)
	}
	return c, evt, nil
}

// CloseIssue sets status=closed unless already closed.
func (d *DB) CloseIssue(ctx context.Context, issueID int64, reason, actor string) (Issue, *Event, bool, error) {
	if reason == "" {
		reason = "done"
	}
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return Issue{}, nil, false, err
	}
	defer func() { _ = tx.Rollback() }()

	issue, projectIdentity, err := lookupIssueForEvent(ctx, tx, issueID)
	if err != nil {
		return Issue{}, nil, false, err
	}
	if issue.Status == "closed" {
		return issue, nil, false, tx.Commit()
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE issues
		 SET status        = 'closed',
		     closed_reason = ?,
		     closed_at     = strftime('%Y-%m-%dT%H:%M:%fZ','now'),
		     updated_at    = strftime('%Y-%m-%dT%H:%M:%fZ','now')
		 WHERE id = ?`, reason, issueID); err != nil {
		return Issue{}, nil, false, fmt.Errorf("close: %w", err)
	}
	payload := fmt.Sprintf(`{"reason":%q}`, reason)
	evt, err := d.insertEventTx(ctx, tx, eventInsert{
		ProjectID:       issue.ProjectID,
		ProjectIdentity: projectIdentity,
		IssueID:         &issue.ID,
		IssueNumber:     &issue.Number,
		Type:            "issue.closed",
		Actor:           actor,
		Payload:         payload,
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

// ReopenIssue clears status=closed unless already open.
func (d *DB) ReopenIssue(ctx context.Context, issueID int64, actor string) (Issue, *Event, bool, error) {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return Issue{}, nil, false, err
	}
	defer func() { _ = tx.Rollback() }()

	issue, projectIdentity, err := lookupIssueForEvent(ctx, tx, issueID)
	if err != nil {
		return Issue{}, nil, false, err
	}
	if issue.Status == "open" {
		return issue, nil, false, tx.Commit()
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE issues
		 SET status        = 'open',
		     closed_reason = NULL,
		     closed_at     = NULL,
		     updated_at    = strftime('%Y-%m-%dT%H:%M:%fZ','now')
		 WHERE id = ?`, issueID); err != nil {
		return Issue{}, nil, false, fmt.Errorf("reopen: %w", err)
	}
	evt, err := d.insertEventTx(ctx, tx, eventInsert{
		ProjectID:       issue.ProjectID,
		ProjectIdentity: projectIdentity,
		IssueID:         &issue.ID,
		IssueNumber:     &issue.Number,
		Type:            "issue.reopened",
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

// EditIssueParams carries the optional fields for an edit. Nil = leave alone.
type EditIssueParams struct {
	IssueID int64
	Title   *string
	Body    *string
	Owner   *string
	Actor   string
}

// EditIssue mutates title/body/owner. ErrNoFields if none are set.
func (d *DB) EditIssue(ctx context.Context, p EditIssueParams) (Issue, *Event, bool, error) {
	if p.Title == nil && p.Body == nil && p.Owner == nil {
		return Issue{}, nil, false, ErrNoFields
	}
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return Issue{}, nil, false, err
	}
	defer func() { _ = tx.Rollback() }()

	issue, projectIdentity, err := lookupIssueForEvent(ctx, tx, p.IssueID)
	if err != nil {
		return Issue{}, nil, false, err
	}

	sets := []string{`updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now')`}
	args := []any{}
	if p.Title != nil {
		sets = append(sets, `title = ?`)
		args = append(args, *p.Title)
	}
	if p.Body != nil {
		sets = append(sets, `body = ?`)
		args = append(args, *p.Body)
	}
	if p.Owner != nil {
		sets = append(sets, `owner = ?`)
		args = append(args, *p.Owner)
	}
	args = append(args, p.IssueID)
	// `sets` only contains string literals chosen above; user-provided values
	// are parameterized via `args`. Safe to concatenate.
	q := `UPDATE issues SET ` + joinComma(sets) + ` WHERE id = ?` // #nosec G202
	if _, err := tx.ExecContext(ctx, q, args...); err != nil {
		return Issue{}, nil, false, fmt.Errorf("update issue: %w", err)
	}
	evt, err := d.insertEventTx(ctx, tx, eventInsert{
		ProjectID:       issue.ProjectID,
		ProjectIdentity: projectIdentity,
		IssueID:         &issue.ID,
		IssueNumber:     &issue.Number,
		Type:            "issue.updated",
		Actor:           p.Actor,
		Payload:         "{}",
	})
	if err != nil {
		return Issue{}, nil, false, err
	}
	if err := tx.Commit(); err != nil {
		return Issue{}, nil, false, err
	}
	updated, err := d.IssueByID(ctx, p.IssueID)
	if err != nil {
		return Issue{}, nil, false, err
	}
	return updated, &evt, true, nil
}

func joinComma(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += ", "
		}
		out += p
	}
	return out
}

// lookupIssueForEvent fetches the issue + its project's identity for event
// snapshotting. Used inside transactions. Soft-deleted issues are excluded so
// lifecycle mutations (close/reopen/edit/comment) cannot operate on hidden
// rows; callers see ErrNotFound for both nonexistent and deleted issues.
func lookupIssueForEvent(ctx context.Context, tx *sql.Tx, issueID int64) (Issue, string, error) {
	const q = `
		SELECT i.id, i.uid, i.project_id, p.uid, i.number, i.title, i.body, i.status,
		       i.closed_reason, i.owner, i.author, i.created_at, i.updated_at,
		       i.closed_at, i.deleted_at, p.identity
		FROM issues i
		JOIN projects p ON p.id = i.project_id
		WHERE i.id = ? AND i.deleted_at IS NULL`
	var i Issue
	var identity string
	err := tx.QueryRowContext(ctx, q, issueID).
		Scan(&i.ID, &i.UID, &i.ProjectID, &i.ProjectUID, &i.Number, &i.Title, &i.Body, &i.Status, &i.ClosedReason, &i.Owner, &i.Author, &i.CreatedAt, &i.UpdatedAt, &i.ClosedAt, &i.DeletedAt, &identity)
	if errors.Is(err, sql.ErrNoRows) {
		return Issue{}, "", ErrNotFound
	}
	if err != nil {
		return Issue{}, "", fmt.Errorf("lookup issue: %w", err)
	}
	return i, identity, nil
}

const issueSelect = `SELECT i.id, i.uid, i.project_id, p.uid, i.number, i.title, i.body, i.status, i.closed_reason, i.owner, i.author, i.created_at, i.updated_at, i.closed_at, i.deleted_at FROM issues i JOIN projects p ON p.id = i.project_id`

func scanIssue(r rowScanner) (Issue, error) {
	var i Issue
	err := r.Scan(&i.ID, &i.UID, &i.ProjectID, &i.ProjectUID, &i.Number, &i.Title, &i.Body, &i.Status, &i.ClosedReason, &i.Owner, &i.Author, &i.CreatedAt, &i.UpdatedAt, &i.ClosedAt, &i.DeletedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Issue{}, ErrNotFound
	}
	if err != nil {
		return Issue{}, fmt.Errorf("scan issue: %w", err)
	}
	return i, nil
}

// eventInsert is the tx-internal payload used by insertEventTx.
type eventInsert struct {
	ProjectID       int64
	ProjectUID      string
	ProjectIdentity string
	IssueID         *int64
	IssueUID        *string
	IssueNumber     *int64
	RelatedIssueID  *int64
	RelatedIssueUID *string
	Type            string
	Actor           string
	Payload         string
}

// UpdateOwner sets issues.owner to the new value and emits the matching
// assigned/unassigned event. newOwner == nil means unassign. No-op when the
// new value matches the current value (returns nil event, changed=false).
func (d *DB) UpdateOwner(ctx context.Context, issueID int64, newOwner *string, actor string) (Issue, *Event, bool, error) {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return Issue{}, nil, false, err
	}
	defer func() { _ = tx.Rollback() }()

	issue, projectIdentity, err := lookupIssueForEvent(ctx, tx, issueID)
	if err != nil {
		return Issue{}, nil, false, err
	}
	// No-op: same owner.
	if ownerEqual(issue.Owner, newOwner) {
		return issue, nil, false, tx.Commit()
	}

	if _, err := tx.ExecContext(ctx,
		`UPDATE issues
		 SET owner      = ?,
		     updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now')
		 WHERE id = ?`, newOwner, issueID); err != nil {
		return Issue{}, nil, false, fmt.Errorf("update owner: %w", err)
	}

	eventType := "issue.unassigned"
	payload := "{}"
	if newOwner != nil {
		eventType = "issue.assigned"
		bs, marshalErr := json.Marshal(struct {
			Owner string `json:"owner"`
		}{Owner: *newOwner})
		if marshalErr != nil {
			return Issue{}, nil, false, fmt.Errorf("marshal assigned payload: %w", marshalErr)
		}
		payload = string(bs)
	}
	evt, err := d.insertEventTx(ctx, tx, eventInsert{
		ProjectID:       issue.ProjectID,
		ProjectIdentity: projectIdentity,
		IssueID:         &issue.ID,
		IssueNumber:     &issue.Number,
		Type:            eventType,
		Actor:           actor,
		Payload:         payload,
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

// ownerEqual returns true when two *string owners reference the same value
// (both nil = equal; nil vs non-nil = different; otherwise compare strings).
func ownerEqual(a, b *string) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return *a == *b
}

// ReadyIssues returns open, non-deleted issues with no open `blocks` predecessor,
// ordered by updated_at DESC. limit==0 means no limit.
func (d *DB) ReadyIssues(ctx context.Context, projectID int64, limit int) ([]Issue, error) {
	q := issueSelect + `
		WHERE i.project_id = ? AND i.status = 'open' AND i.deleted_at IS NULL
		  AND NOT EXISTS (
		    SELECT 1 FROM links l
		    JOIN issues blocker ON blocker.id = l.from_issue_id
		    WHERE l.type = 'blocks' AND l.to_issue_id = i.id
		      AND blocker.status = 'open' AND blocker.deleted_at IS NULL
		  )
		ORDER BY i.updated_at DESC, i.id DESC`
	args := []any{projectID}
	if limit > 0 {
		q += fmt.Sprintf(` LIMIT %d`, limit)
	}
	rows, err := d.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("ready issues: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Issue
	for rows.Next() {
		i, err := scanIssue(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, i)
	}
	return out, rows.Err()
}

func (d *DB) insertEventTx(ctx context.Context, tx *sql.Tx, in eventInsert) (Event, error) {
	eventUID, err := katauid.New()
	if err != nil {
		return Event{}, fmt.Errorf("generate event uid: %w", err)
	}
	res, err := tx.ExecContext(ctx,
		`INSERT INTO events(uid, origin_instance_uid, project_id, project_identity, issue_id, issue_uid, issue_number, related_issue_id, related_issue_uid, type, actor, payload)
		 VALUES(
		   ?, ?, ?, ?, ?,
		   COALESCE(?, (SELECT uid FROM issues WHERE id = ?)),
		   ?, ?,
		   COALESCE(?, (SELECT uid FROM issues WHERE id = ?)),
		   ?, ?, ?
		 )`,
		eventUID, d.instanceUID,
		in.ProjectID, in.ProjectIdentity, in.IssueID,
		stringPtrValue(in.IssueUID), in.IssueID,
		in.IssueNumber, in.RelatedIssueID,
		stringPtrValue(in.RelatedIssueUID), in.RelatedIssueID,
		in.Type, in.Actor, in.Payload)
	if err != nil {
		return Event{}, fmt.Errorf("insert event: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Event{}, err
	}
	var e Event
	err = tx.QueryRowContext(ctx, eventSelectByID, id).
		Scan(&e.ID, &e.UID, &e.OriginInstanceUID, &e.ProjectID, &e.ProjectUID, &e.ProjectIdentity, &e.IssueID,
			&e.IssueUID, &e.IssueNumber, &e.RelatedIssueID, &e.RelatedIssueUID,
			&e.Type, &e.Actor, &e.Payload, &e.CreatedAt)
	if err != nil {
		return Event{}, fmt.Errorf("read event: %w", err)
	}
	return e, nil
}

const eventSelectByID = `SELECT e.id, e.uid, e.origin_instance_uid, e.project_id, p.uid, e.project_identity,
       e.issue_id, e.issue_uid, e.issue_number, e.related_issue_id, e.related_issue_uid,
       e.type, e.actor, e.payload, e.created_at
  FROM events e
  JOIN projects p ON p.id = e.project_id
 WHERE e.id = ?`

func stringPtrValue(s *string) any {
	if s == nil {
		return nil
	}
	return *s
}
