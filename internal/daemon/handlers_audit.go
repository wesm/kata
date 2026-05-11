package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/wesm/kata/internal/api"
	"github.com/wesm/kata/internal/db"
)

// registerAuditHandlers installs /api/v1/audit/closes, a read-only
// projection of issue.closed events that surfaces per-close audit rows
// with derived flags. The endpoint is project-scoped via ?project_id;
// callers identify the project the same way `kata list` does. The
// handler reuses EventsInWindow for the time / actor filter, then
// applies in-process filters for reason, parent, and no-evidence.
// Close.throttled events in the same window decorate the matching
// close rows with the "throttled" flag so reviewers can see which
// closes the agent retried past a refusal.
func registerAuditHandlers(humaAPI huma.API, cfg ServerConfig) {
	huma.Register(humaAPI, huma.Operation{
		OperationID: "auditCloses",
		Method:      "GET",
		Path:        "/api/v1/audit/closes",
	}, func(ctx context.Context, in *api.AuditClosesRequest) (*api.AuditClosesResponse, error) {
		return doAuditCloses(ctx, cfg, in)
	})
}

func doAuditCloses(
	ctx context.Context, cfg ServerConfig, in *api.AuditClosesRequest,
) (*api.AuditClosesResponse, error) {
	if in.ProjectID <= 0 {
		return nil, api.NewError(400, "validation",
			"project_id must be a positive integer", "", nil)
	}
	if _, err := activeProjectByID(ctx, cfg.DB, in.ProjectID); err != nil {
		return nil, err
	}
	since, until, err := parseAuditWindow(in.Since, in.Until)
	if err != nil {
		return nil, err
	}
	params := db.EventsInWindowParams{
		ProjectID: in.ProjectID,
		Since:     since,
		Until:     until,
	}
	if in.Actor != "" {
		params.Actors = []string{in.Actor}
	}
	events, err := cfg.DB.EventsInWindow(ctx, params)
	if err != nil {
		return nil, api.NewError(500, "internal", err.Error(), "", nil)
	}
	rows, err := buildAuditRows(ctx, cfg, in, events)
	if err != nil {
		return nil, err
	}
	out := &api.AuditClosesResponse{}
	out.Body.Rows = rows
	return out, nil
}

// parseAuditWindow parses the request's --since / --until pair. Empty
// since defaults to the zero time; empty until defaults to now. The
// daemon's events table stores created_at at millisecond precision via
// a sqlite strftime format, so we format here in the same way.
const auditTimeFmt = "2006-01-02T15:04:05.000Z"

func parseAuditWindow(sinceIn, untilIn string) (since, until string, err error) {
	since = time.Time{}.UTC().Format(auditTimeFmt)
	until = time.Now().UTC().Format(auditTimeFmt)
	if sinceIn != "" {
		t, perr := time.Parse(time.RFC3339, sinceIn)
		if perr != nil {
			return "", "", api.NewError(400, "validation",
				"since must be an RFC3339 timestamp", "", nil)
		}
		since = t.UTC().Format(auditTimeFmt)
	}
	if untilIn != "" {
		t, perr := time.Parse(time.RFC3339, untilIn)
		if perr != nil {
			return "", "", api.NewError(400, "validation",
				"until must be an RFC3339 timestamp", "", nil)
		}
		until = t.UTC().Format(auditTimeFmt)
	}
	return since, until, nil
}

// closeEventPayload is the wire shape the daemon writes into the
// payload column for issue.closed events. Defined locally because the
// db package does not export it.
//
// ParentShortID is the parent's short_id at the moment of close,
// captured inside the close transaction so audit history survives a
// later reparent / remove-parent on the closed child. Nil = legacy
// event that predates this field (the audit projection falls back to
// a live links lookup); non-nil empty = the issue had no parent at
// close time.
type closeEventPayload struct {
	Reason        string         `json:"reason"`
	Message       string         `json:"message,omitempty"`
	Evidence      []api.Evidence `json:"evidence,omitempty"`
	ParentShortID *string        `json:"parent_short_id,omitempty"`
}

// buildAuditRows projects close events into AuditCloseRow shape and
// applies the reason / parent / no-evidence filters. Parent context
// comes from the close event's stored parent_short_id (frozen at
// close-time inside the close tx) so a later reparent / remove-parent
// cannot mutate historical audit rows or shift them out of a --parent
// filter. Legacy events that predate the stored field fall back to a
// live links lookup — the best we can do for that subset.
func buildAuditRows(
	ctx context.Context, cfg ServerConfig,
	in *api.AuditClosesRequest, events []db.Event,
) ([]api.AuditCloseRow, error) {
	throttled := throttledIssuesByActor(events)
	legacyParents, err := loadLegacyParentsForCloseEvents(ctx, cfg, in.ProjectID, events)
	if err != nil {
		return nil, err
	}
	rows := make([]api.AuditCloseRow, 0, len(events))
	for _, ev := range events {
		if ev.Type != "issue.closed" {
			continue
		}
		closeEventID := ev.ID
		var p closeEventPayload
		// Lenient: a malformed payload surfaces as a row with empty
		// reason/evidence so a reviewer sees the gap rather than the
		// audit silently dropping it.
		_ = json.Unmarshal([]byte(ev.Payload), &p)
		if in.Reason != "" && p.Reason != in.Reason {
			continue
		}
		row := api.AuditCloseRow{
			Time:    ev.CreatedAt.UTC().Format(time.RFC3339),
			Actor:   ev.Actor,
			Reason:  p.Reason,
			Message: p.Message,
		}
		if ev.IssueShortID != nil {
			row.Issue = *ev.IssueShortID
		}
		switch {
		case p.ParentShortID != nil:
			row.Parent = *p.ParentShortID
		case ev.IssueID != nil:
			if pn, ok := legacyParents[*ev.IssueID]; ok {
				row.Parent = pn
			}
		}
		if in.Parent != "" && row.Parent != in.Parent {
			continue
		}
		for _, e := range p.Evidence {
			row.EvidenceTypes = append(row.EvidenceTypes, string(e.Type))
		}
		// Spec §5: legacy / pre-v1 closes have no evidence and surface
		// with flags=no-evidence. wontfix is the one reason where a
		// no-evidence close is admissible by design.
		if len(p.Evidence) == 0 && p.Reason != "wontfix" {
			row.Flags = append(row.Flags, "no-evidence")
		}
		if row.Issue != "" {
			// Only flag closes that have a prior throttle event for the
			// same (issue, actor). A later throttle event is a separate
			// retry attempt against this same close and would mislead
			// the audit reader into thinking the close itself tripped
			// the guard. Event IDs are monotonic so the comparison is
			// sound across the window.
			if firstID, ok := throttled[throttleKey(row.Issue, row.Actor)]; ok && firstID < closeEventID {
				row.Flags = append(row.Flags, "throttled")
			}
		}
		if in.NoEvidence && !auditFlagsContain(row.Flags, "no-evidence") {
			continue
		}
		rows = append(rows, row)
	}
	return rows, nil
}

// throttledIssuesByActor builds the (issue-number, actor) -> earliest
// throttle event id map for close.throttled events in the window.
// Storing the earliest id (events come in ASC order) lets buildAuditRows
// flag only closes that occurred AFTER a throttle — later throttles for
// the same key are retry attempts against an already-recorded close and
// must not propagate the flag back to that close.
func throttledIssuesByActor(events []db.Event) map[string]int64 {
	out := map[string]int64{}
	for _, ev := range events {
		if ev.Type != "close.throttled" || ev.IssueShortID == nil {
			continue
		}
		key := throttleKey(*ev.IssueShortID, ev.Actor)
		if _, ok := out[key]; !ok {
			out[key] = ev.ID
		}
	}
	return out
}

func throttleKey(issue, actor string) string {
	return fmt.Sprintf("%s|%s", issue, actor)
}

// loadLegacyParentsForCloseEvents gathers the issue ids of close events
// whose payload lacks the parent_short_id field (legacy events from
// before close-time parent freezing) and resolves each child -> parent
// via the live links table. New events skip the lookup entirely because
// their payload already carries the close-time parent.
//
// The live lookup remains best-effort for legacy data — if the issue
// was reparented since closing, the audit row reflects the current
// link, not the close-time link. There is no record of the original
// parent for legacy events.
func loadLegacyParentsForCloseEvents(
	ctx context.Context, cfg ServerConfig,
	projectID int64, events []db.Event,
) (map[int64]string, error) {
	ids := make([]int64, 0, len(events))
	seen := map[int64]struct{}{}
	for _, ev := range events {
		if ev.Type != "issue.closed" || ev.IssueID == nil {
			continue
		}
		var p closeEventPayload
		_ = json.Unmarshal([]byte(ev.Payload), &p)
		if p.ParentShortID != nil {
			continue
		}
		if _, ok := seen[*ev.IssueID]; ok {
			continue
		}
		seen[*ev.IssueID] = struct{}{}
		ids = append(ids, *ev.IssueID)
	}
	if len(ids) == 0 {
		return map[int64]string{}, nil
	}
	parents, err := cfg.DB.ParentShortIDsByIssues(ctx, projectID, ids)
	if err != nil {
		return nil, api.NewError(500, "internal", err.Error(), "", nil)
	}
	return parents, nil
}

func auditFlagsContain(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}
