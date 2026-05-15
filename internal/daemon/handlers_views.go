package daemon

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/wesm/kata/internal/api"
	"github.com/wesm/kata/internal/db"
)

// viewNames enumerates the named views db.ListIssuesByView understands. Any
// value outside this set is rejected with a 400 invalid_view envelope before
// the DB is touched.
var viewNames = []string{"today", "upcoming", "inbox", "someday", "anytime", "logbook"}

// listIssuesByView is the named-view branch of GET /api/v1/issues. It
// validates the view name, resolves the client's local today boundary
// from the supplied X-Kata-Client-TZ header, ensures the inbox sentinel
// project exists, and delegates to db.ListIssuesByView. The returned
// issues are unhydrated db.Issue rows; the caller is responsible for
// projecting them onto whatever response shape the endpoint emits.
func listIssuesByView(
	ctx context.Context, store *db.DB, in *api.ListAllIssuesRequest,
) ([]db.Issue, error) {
	if !isKnownView(in.View) {
		return nil, api.NewError(http.StatusBadRequest, "invalid_view",
			fmt.Sprintf("view must be one of today|upcoming|inbox|someday|anytime|logbook, got %q", in.View),
			"", nil)
	}
	today, err := LocalDateBoundary(in.ClientTZ, time.Now().UTC())
	if err != nil {
		return nil, api.NewError(http.StatusBadRequest, "invalid_tz", err.Error(), "", nil)
	}
	inbox, err := EnsureInbox(ctx, store)
	if err != nil {
		return nil, api.NewError(http.StatusInternalServerError, "internal", err.Error(), "", nil)
	}
	issues, err := store.ListIssuesByView(ctx, db.ListIssuesByViewIn{
		View:           in.View,
		TodayDate:      today,
		InboxProjectID: inbox.ID,
		ProjectID:      in.ProjectID,
		Area:           in.Area,
		Limit:          in.Limit,
		Offset:         in.Offset,
	})
	if err != nil {
		return nil, api.NewError(http.StatusInternalServerError, "internal", err.Error(), "", nil)
	}
	return issues, nil
}

// isKnownView reports whether v matches one of the documented view names.
func isKnownView(v string) bool {
	for _, name := range viewNames {
		if v == name {
			return true
		}
	}
	return false
}

// listIssuesViewResponse serves the named-view branch of GET /api/v1/issues.
// It dispatches to listIssuesByView for the heavy lifting, then hydrates the
// returned rows so the response shape stays compatible with the unfiltered
// path (labels + parent/child + link peer summaries).
func listIssuesViewResponse(
	ctx context.Context, cfg ServerConfig, in *api.ListAllIssuesRequest,
) (*api.ListIssuesResponse, error) {
	issues, err := listIssuesByView(ctx, cfg.DB, in)
	if err != nil {
		return nil, err
	}
	issueOuts, err := hydrateIssueOutsCrossProject(ctx, cfg.DB, issues)
	if err != nil {
		return nil, api.NewError(http.StatusInternalServerError, "internal", err.Error(), "", nil)
	}
	out := &api.ListIssuesResponse{}
	out.Body.Issues = issueOuts
	return out, nil
}
