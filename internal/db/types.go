package db

import "time"

// Project mirrors a row in projects. DeletedAt is set when the project has
// been archived via kata projects remove (#24); the row stays in the table so
// events/issues keep referring to a valid FK target, but read paths filter it
// out. Identity stays UNIQUE so re-creating the same identity hits a clean
// "project was archived" error rather than silently resurrecting it.
type Project struct {
	ID              int64      `json:"id"`
	UID             string     `json:"uid"`
	Identity        string     `json:"identity"`
	Name            string     `json:"name"`
	CreatedAt       time.Time  `json:"created_at"`
	NextIssueNumber int64      `json:"next_issue_number"`
	DeletedAt       *time.Time `json:"deleted_at,omitempty"`
}

// ProjectStats is the per-project aggregate returned by BatchProjectStats.
// Used by GET /api/v1/projects?include=stats. LastEventAt is nil for a
// project with zero events; tests pin this so the TUI's "—" rendering
// is exercised.
type ProjectStats struct {
	Open        int
	Closed      int
	LastEventAt *time.Time
}

// ProjectAlias mirrors a row in project_aliases.
type ProjectAlias struct {
	ID            int64     `json:"id"`
	ProjectID     int64     `json:"project_id"`
	AliasIdentity string    `json:"alias_identity"`
	AliasKind     string    `json:"alias_kind"`
	RootPath      string    `json:"root_path"`
	CreatedAt     time.Time `json:"created_at"`
	LastSeenAt    time.Time `json:"last_seen_at"`
}

// Issue mirrors a row in issues.
type Issue struct {
	ID           int64      `json:"id"`
	UID          string     `json:"uid"`
	ProjectID    int64      `json:"project_id"`
	ProjectUID   string     `json:"project_uid,omitempty"`
	Number       int64      `json:"number"`
	Title        string     `json:"title"`
	Body         string     `json:"body"`
	Status       string     `json:"status"`
	ClosedReason *string    `json:"closed_reason,omitempty"`
	Owner        *string    `json:"owner,omitempty"`
	Author       string     `json:"author"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
	ClosedAt     *time.Time `json:"closed_at,omitempty"`
	DeletedAt    *time.Time `json:"deleted_at,omitempty"`
}

// Comment mirrors a row in comments.
type Comment struct {
	ID        int64     `json:"id"`
	IssueID   int64     `json:"issue_id"`
	Author    string    `json:"author"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"created_at"`
}

// Event mirrors a row in events.
type Event struct {
	ID                int64     `json:"id"`
	UID               string    `json:"uid"`
	OriginInstanceUID string    `json:"origin_instance_uid"`
	ProjectID         int64     `json:"project_id"`
	ProjectUID        string    `json:"project_uid"`
	ProjectIdentity   string    `json:"project_identity"`
	IssueID           *int64    `json:"issue_id,omitempty"`
	IssueUID          *string   `json:"issue_uid,omitempty"`
	IssueNumber       *int64    `json:"issue_number,omitempty"`
	RelatedIssueID    *int64    `json:"related_issue_id,omitempty"`
	RelatedIssueUID   *string   `json:"related_issue_uid,omitempty"`
	Type              string    `json:"type"`
	Actor             string    `json:"actor"`
	Payload           string    `json:"payload"`
	CreatedAt         time.Time `json:"created_at"`
}

// Link mirrors a row in links.
type Link struct {
	ID           int64     `json:"id"`
	ProjectID    int64     `json:"project_id"`
	FromIssueID  int64     `json:"from_issue_id"`
	FromIssueUID string    `json:"from_issue_uid"`
	ToIssueID    int64     `json:"to_issue_id"`
	ToIssueUID   string    `json:"to_issue_uid"`
	Type         string    `json:"type"`
	Author       string    `json:"author"`
	CreatedAt    time.Time `json:"created_at"`
}

// IssueLabel mirrors a row in issue_labels.
type IssueLabel struct {
	IssueID   int64     `json:"issue_id"`
	Label     string    `json:"label"`
	Author    string    `json:"author"`
	CreatedAt time.Time `json:"created_at"`
}

// LabelCount is the per-label aggregate returned by LabelCounts.
type LabelCount struct {
	Label string `json:"label"`
	Count int64  `json:"count"`
}

// ChildCounts is the direct-child aggregate for one parent issue.
type ChildCounts struct {
	Open  int `json:"open"`
	Total int `json:"total"`
}

// SearchCandidate is one row from SearchFTS: an issue, a BM25 score (lower is
// better in raw form; we negate so higher = better), and the columns where
// the query matched. MatchedIn is the basis for the wire response's matched_in.
type SearchCandidate struct {
	Issue     Issue    `json:"issue"`
	Score     float64  `json:"score"` // BM25, negated; higher = better match
	MatchedIn []string `json:"matched_in"`
}

// IdempotencyMatch is the payload returned by LookupIdempotency. The Event row
// is included so the handler can populate `original_event` in the reuse-case
// MutationResponse without a second query.
type IdempotencyMatch struct {
	IssueID     int64
	IssueNumber int64
	Fingerprint string
	Event       Event
}

// PurgeLog mirrors a row in purge_log. Snapshots the issue identity at purge
// time so audits survive any future project rename. EventsDeletedMinID/MaxID
// and PurgeResetAfterEventID are nullable: NULL when no events were attached
// to the purged issue.
type PurgeLog struct {
	ID                     int64     `json:"id"`
	UID                    string    `json:"uid"`
	OriginInstanceUID      string    `json:"origin_instance_uid"`
	ProjectID              int64     `json:"project_id"`
	PurgedIssueID          int64     `json:"purged_issue_id"`
	IssueUID               *string   `json:"issue_uid,omitempty"`
	ProjectUID             *string   `json:"project_uid,omitempty"`
	ProjectIdentity        string    `json:"project_identity"`
	IssueNumber            int64     `json:"issue_number"`
	IssueTitle             string    `json:"issue_title"`
	IssueAuthor            string    `json:"issue_author"`
	CommentCount           int64     `json:"comment_count"`
	LinkCount              int64     `json:"link_count"`
	LabelCount             int64     `json:"label_count"`
	EventCount             int64     `json:"event_count"`
	EventsDeletedMinID     *int64    `json:"events_deleted_min_id,omitempty"`
	EventsDeletedMaxID     *int64    `json:"events_deleted_max_id,omitempty"`
	PurgeResetAfterEventID *int64    `json:"purge_reset_after_event_id,omitempty"`
	Actor                  string    `json:"actor"`
	Reason                 *string   `json:"reason,omitempty"`
	PurgedAt               time.Time `json:"purged_at"`
}
