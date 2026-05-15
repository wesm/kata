package db

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// ListIssuesByViewIn holds parameters for ListIssuesByView.
type ListIssuesByViewIn struct {
	// View is one of: today, upcoming, inbox, someday, anytime, logbook.
	View string
	// TodayDate is the client-local date in YYYY-MM-DD format.
	TodayDate string
	// InboxProjectID is the project used as the "inbox" sentinel.
	// Required for the inbox and anytime views.
	InboxProjectID int64
	// ProjectID, when non-zero, restricts results to a single project.
	ProjectID int64
	// Area, when non-empty, restricts results to projects whose metadata.area
	// matches (case-insensitive).
	Area string
	// Limit caps the number of returned issues. 0 defaults to 50; max 500.
	Limit int
	// Offset is the row offset for pagination.
	Offset int
}

// ListIssuesByView returns issues matching one of six named views.
//
// #nosec G202 — the SQL string is assembled from fixed literals only;
// all user-supplied values flow through ? placeholders.
func (d *DB) ListIssuesByView(ctx context.Context, in ListIssuesByViewIn) ([]Issue, error) {
	if in.Limit <= 0 {
		in.Limit = 50
	}
	if in.Limit > 500 {
		in.Limit = 500
	}

	where := []string{"i.deleted_at IS NULL", "p.deleted_at IS NULL"}
	var args []any

	if in.ProjectID > 0 {
		where = append(where, "i.project_id = ?")
		args = append(args, in.ProjectID)
	}
	if in.Area != "" {
		where = append(where,
			"lower(coalesce(json_extract(p.metadata,'$.area'),'')) = ?")
		args = append(args, strings.ToLower(in.Area))
	}

	switch in.View {
	case "today":
		if _, err := time.Parse("2006-01-02", in.TodayDate); err != nil {
			return nil, fmt.Errorf("today view requires TodayDate in YYYY-MM-DD: %w", err)
		}
		where = append(where,
			"i.status = 'open'",
			`(json_extract(i.metadata,'$.scheduled_on') <= ?
              OR (json_extract(i.metadata,'$.scheduled_on') IS NULL
                  AND json_extract(i.metadata,'$.deadline_on') IS NOT NULL
                  AND json_extract(i.metadata,'$.deadline_on') <= ?))`)
		args = append(args, in.TodayDate, in.TodayDate)
	case "upcoming":
		if _, err := time.Parse("2006-01-02", in.TodayDate); err != nil {
			return nil, fmt.Errorf("upcoming view requires TodayDate in YYYY-MM-DD: %w", err)
		}
		where = append(where,
			"i.status = 'open'",
			"json_extract(i.metadata,'$.scheduled_on') > ?")
		args = append(args, in.TodayDate)
	case "inbox":
		if in.InboxProjectID == 0 {
			return nil, fmt.Errorf("inbox view requires InboxProjectID")
		}
		where = append(where, "i.status = 'open'", "i.project_id = ?")
		args = append(args, in.InboxProjectID)
	case "someday":
		where = append(where,
			"i.status = 'open'",
			"json_extract(i.metadata,'$.someday') = 1")
	case "anytime":
		if in.InboxProjectID == 0 {
			return nil, fmt.Errorf("anytime view requires InboxProjectID")
		}
		where = append(where,
			"i.status = 'open'",
			"json_extract(i.metadata,'$.scheduled_on') IS NULL",
			"(json_extract(i.metadata,'$.someday') IS NULL OR json_extract(i.metadata,'$.someday') = 0)",
			"i.project_id != ?")
		args = append(args, in.InboxProjectID)
	case "logbook":
		where = append(where, "i.status = 'closed'")
	default:
		return nil, fmt.Errorf("unknown view %q", in.View)
	}

	q := issueSelect +
		" WHERE " + strings.Join(where, " AND ") +
		" ORDER BY i.priority IS NULL, i.priority, i.updated_at DESC, i.id DESC" +
		" LIMIT ? OFFSET ?"
	args = append(args, in.Limit, in.Offset)

	rows, err := d.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
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
