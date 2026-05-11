package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
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
// ParentUID is the parent's immutable UID at close time. ParentShortID
// is the close-time display value, retained as a fallback when the
// parent has since been purged and the UID no longer resolves through
// the live issues table. Resolving UID → current short_id at audit
// time keeps the row stable across project-merge collision reshuffles
// (which can rewrite source-side short_ids) without losing the audit
// signal. Nil pointers signal "legacy event that predates these
// fields" — the audit projection falls back to a live links lookup
// only for that case; non-nil empty signals "no parent at close".
type closeEventPayload struct {
	Reason        string         `json:"reason"`
	Message       string         `json:"message,omitempty"`
	Evidence      []api.Evidence `json:"evidence,omitempty"`
	ParentUID     *string        `json:"parent_uid,omitempty"`
	ParentShortID *string        `json:"parent_short_id,omitempty"`
}

// buildAuditRows projects close events into AuditCloseRow shape and
// applies the reason / parent / no-evidence filters. Parent context
// comes from the close event's frozen parent_uid (resolved to the
// CURRENT short_id at read time so a project-merge collision rewrite
// is reflected) with parent_short_id as a fallback when the UID has
// since been purged. Legacy events that predate both fields fall
// back to a live links lookup — the best we can do for that subset.
//
// The throttled flag is decorated via a single chronological pass: a
// close.throttled event marks the (issue, actor) pair pending, and
// the next matching issue.closed consumes the marker and is flagged.
// Subsequent reopen → close cycles start fresh, so the flag never
// bleeds into closes that happened later in unrelated history.
func buildAuditRows(
	ctx context.Context, cfg ServerConfig,
	in *api.AuditClosesRequest, events []db.Event,
) ([]api.AuditCloseRow, error) {
	parentFilterShortID, ok, err := resolveAuditParentFilter(ctx, cfg, in.ProjectID, in.Parent)
	if err != nil {
		return nil, err
	}
	if in.Parent != "" && !ok {
		// Filter ref didn't resolve to any issue in this project. The
		// pre-fix code returned empty rows for a literal string miss; we
		// preserve that observable behavior rather than surface a 404
		// from a read endpoint.
		return nil, nil
	}
	parsed := make([]closeEventPayload, len(events))
	for i, ev := range events {
		if ev.Type != "issue.closed" {
			continue
		}
		_ = json.Unmarshal([]byte(ev.Payload), &parsed[i])
	}
	parentUIDToSID, err := resolveParentUIDs(ctx, cfg, in.ProjectID, parsed)
	if err != nil {
		return nil, err
	}
	legacyParents, err := loadLegacyParentsForCloseEvents(ctx, cfg, in.ProjectID, events, parsed)
	if err != nil {
		return nil, err
	}
	rows := make([]api.AuditCloseRow, 0, len(events))
	pendingThrottle := map[string]struct{}{}
	for i, ev := range events {
		// close.throttled markers arm a pending flag for the next
		// matching issue.closed; consume happens below. Skip the rest
		// of the loop body for non-close events so the throttle path
		// doesn't accidentally produce a row.
		if ev.Type == "close.throttled" && ev.IssueShortID != nil {
			pendingThrottle[throttleKey(*ev.IssueShortID, ev.Actor)] = struct{}{}
			continue
		}
		if ev.Type != "issue.closed" {
			continue
		}
		// Consume any pending throttle for this (issue, actor) BEFORE
		// applying output filters (--reason, --parent). The marker
		// belongs to whichever close came next chronologically, even
		// if that close is filtered out of the response — otherwise a
		// reason-filtered query could leave a stale marker that
		// wrongly decorates a later close in a separate cycle.
		p := parsed[i]
		var throttledNow bool
		if ev.IssueShortID != nil {
			k := throttleKey(*ev.IssueShortID, ev.Actor)
			if _, ok := pendingThrottle[k]; ok {
				throttledNow = true
				delete(pendingThrottle, k)
			}
		}
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
		case p.ParentUID != nil && *p.ParentUID != "":
			if sid, ok := parentUIDToSID[*p.ParentUID]; ok {
				row.Parent = sid
			} else if p.ParentShortID != nil {
				// UID no longer resolves (parent purged). Fall back to
				// the stored short_id as best-effort display; it may be
				// stale after a merge, but it is the only label we kept
				// for an issue that no longer exists.
				row.Parent = *p.ParentShortID
			}
		case p.ParentUID != nil:
			// Modern event with no parent at close time. Leave empty.
		case p.ParentShortID != nil:
			// Transition-only: a payload with parent_short_id but no
			// parent_uid would mean a brief intermediate format; trust
			// the stored short_id.
			row.Parent = *p.ParentShortID
		case ev.IssueID != nil:
			if pn, ok := legacyParents[*ev.IssueID]; ok {
				row.Parent = pn
			}
		}
		if in.Parent != "" && row.Parent != parentFilterShortID {
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
		if throttledNow {
			row.Flags = append(row.Flags, "throttled")
		}
		if in.NoEvidence && !auditFlagsContain(row.Flags, "no-evidence") {
			continue
		}
		rows = append(rows, row)
	}
	return rows, nil
}

func throttleKey(issue, actor string) string {
	return fmt.Sprintf("%s|%s", issue, actor)
}

// resolveAuditParentFilter turns the --parent query value into the
// current short_id of the referenced parent, accepting any ref form
// the issue resolver accepts (bare short_id, full UID, qualified
// `project#short_id`). Comparing row.Parent (also a current short_id)
// against a normalized filter value makes the audit projection
// stable across project-merge collision rewrites: both sides reflect
// the same post-merge short_id. ok=false signals "the filter ref did
// not resolve to any issue in this project" and the caller short-
// circuits to an empty response.
func resolveAuditParentFilter(
	ctx context.Context, cfg ServerConfig, projectID int64, parentRef string,
) (shortID string, ok bool, err error) {
	if parentRef == "" {
		return "", false, nil
	}
	issue, rerr := activeIssueByRef(ctx, cfg.DB, projectID, parentRef, db.IncludeDeletedNo)
	if rerr != nil {
		// Treat a not-found ref as an empty filter match rather than
		// surface a 404 from a read endpoint. Resolver internal errors
		// (500-class) bubble up unchanged.
		var apiErr *api.APIError
		if errors.As(rerr, &apiErr) && apiErr.Status == http.StatusNotFound {
			return "", false, nil
		}
		return "", false, rerr
	}
	return issue.ShortID, true, nil
}

// loadLegacyParentsForCloseEvents gathers the issue ids of close events
// whose payload carries neither parent_uid nor parent_short_id (legacy
// events from before close-time parent freezing) and resolves each
// child -> parent via the live links table. New events skip the lookup
// entirely because their payload already carries the close-time parent.
//
// The live lookup remains best-effort for legacy data — if the issue
// was reparented since closing, the audit row reflects the current
// link, not the close-time link. There is no record of the original
// parent for legacy events.
func loadLegacyParentsForCloseEvents(
	ctx context.Context, cfg ServerConfig,
	projectID int64, events []db.Event, parsed []closeEventPayload,
) (map[int64]string, error) {
	ids := make([]int64, 0, len(events))
	seen := map[int64]struct{}{}
	for i, ev := range events {
		if ev.Type != "issue.closed" || ev.IssueID == nil {
			continue
		}
		if parsed[i].ParentUID != nil || parsed[i].ParentShortID != nil {
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

// resolveParentUIDs gathers every distinct, non-empty parent_uid across
// the parsed close payloads and asks the db to resolve them to current
// short_ids. The result map omits UIDs that no longer resolve (parent
// purged); the audit projection falls back to the close-time
// parent_short_id for those.
func resolveParentUIDs(
	ctx context.Context, cfg ServerConfig,
	projectID int64, parsed []closeEventPayload,
) (map[string]string, error) {
	seen := map[string]struct{}{}
	uids := make([]string, 0, len(parsed))
	for _, p := range parsed {
		if p.ParentUID == nil || *p.ParentUID == "" {
			continue
		}
		if _, ok := seen[*p.ParentUID]; ok {
			continue
		}
		seen[*p.ParentUID] = struct{}{}
		uids = append(uids, *p.ParentUID)
	}
	if len(uids) == 0 {
		return map[string]string{}, nil
	}
	out, err := cfg.DB.ShortIDsByUIDs(ctx, projectID, uids)
	if err != nil {
		return nil, api.NewError(500, "internal", err.Error(), "", nil)
	}
	return out, nil
}

func auditFlagsContain(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}
