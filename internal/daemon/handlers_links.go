package daemon

import (
	"context"
	"errors"
	"fmt"

	"github.com/danielgtaylor/huma/v2"

	"github.com/wesm/kata/internal/api"
	"github.com/wesm/kata/internal/db"
)

// registerLinksHandlers installs POST/DELETE /links. CreateLinkAndEvent and
// DeleteLinkAndEvent wrap the link mutation, the matching issue.linked /
// issue.unlinked event, and the issues.updated_at touch in one TX so there's
// no window where the row mutation lands without its event.
//
// For type=parent --replace, the handler emits an issue.unlinked event for
// the old parent (in its own TX) before inserting the new parent link with
// its issue.linked event. The response shape carries only the linked event;
// the unlinked event still lands in the events table for SSE/poll clients.
func registerLinksHandlers(humaAPI huma.API, cfg ServerConfig) {
	huma.Register(humaAPI, huma.Operation{
		OperationID: "createLink",
		Method:      "POST",
		Path:        "/api/v1/projects/{project_id}/issues/{number}/links",
	}, createLinkHandler(cfg))

	huma.Register(humaAPI, huma.Operation{
		OperationID: "deleteLink",
		Method:      "DELETE",
		Path:        "/api/v1/projects/{project_id}/issues/{number}/links/{link_id}",
	}, deleteLinkHandler(cfg))
}

func createLinkHandler(cfg ServerConfig) func(context.Context, *api.CreateLinkRequest) (*api.CreateLinkResponse, error) {
	return func(ctx context.Context, in *api.CreateLinkRequest) (*api.CreateLinkResponse, error) {
		if err := validateActor(in.Body.Actor); err != nil {
			return nil, err
		}
		from, err := activeIssueByNumber(ctx, cfg.DB, in.ProjectID, in.Number)
		if err != nil {
			return nil, err
		}
		to, err := cfg.DB.IssueByNumber(ctx, in.ProjectID, in.Body.ToNumber)
		if errors.Is(err, db.ErrNotFound) {
			return nil, api.NewError(404, "issue_not_found",
				fmt.Sprintf("target issue #%d not found", in.Body.ToNumber), "", nil)
		}
		if err != nil {
			return nil, api.NewError(500, "internal", err.Error(), "", nil)
		}

		// Reject self-link before mutating state. The DB will catch this anyway,
		// but in the --replace path we delete the existing parent before we'd
		// see that error from CreateLinkAndEvent — leaving us with an
		// unlinked-but-unreplaced parent and a fired issue.unlinked event.
		// Cross-project links are already prevented by routing (both source and
		// target are looked up via the same in.ProjectID).
		if from.ID == to.ID {
			return nil, api.NewError(400, "validation", "cannot link an issue to itself", "", nil)
		}

		// Storage endpoints: canonical (from < to) for related; otherwise as-is.
		// canonicalFromNum/canonicalToNum match the Link row's actual columns
		// and feed the LinkOut wire projection (so the response shows the
		// canonical link, e.g. (3, 5) regardless of which side the user posted
		// from). Event attribution always uses the URL issue (from), so the
		// payload's from_number is the URL issue's number and to_number is
		// the OTHER endpoint — even when canonicalization swapped storage.
		storageFromID, storageToID := from.ID, to.ID
		canonicalFromNum, canonicalToNum := from.Number, to.Number
		if in.Body.Type == "related" && storageFromID > storageToID {
			storageFromID, storageToID = storageToID, storageFromID
			canonicalFromNum, canonicalToNum = canonicalToNum, canonicalFromNum
		}

		// Parent --replace path: delete the existing parent link in its own TX
		// (emitting issue.unlinked) before inserting the new parent link. Parent
		// links are never canonicalized, so storageFromID == from.ID here.
		if in.Body.Type == "parent" && in.Body.Replace {
			if existing, perr := cfg.DB.ParentOf(ctx, from.ID); perr == nil {
				if existing.ToIssueID == to.ID {
					// Replacing with the same parent is a no-op.
					return mutationLinkResponse(from, existing, canonicalFromNum, canonicalToNum, nil, false), nil
				}
				// Resolve the OLD parent's number so the issue.unlinked event
				// payload's to_number records the parent we're actually
				// removing — not the new parent we're about to insert.
				oldParentIssue, err := cfg.DB.IssueByID(ctx, existing.ToIssueID)
				if err != nil {
					return nil, api.NewError(500, "internal", err.Error(), "", nil)
				}
				unlinkEv := db.LinkEventParams{
					EventType:        "issue.unlinked",
					EventIssueID:     from.ID,
					EventIssueNumber: from.Number,
					FromNumber:       from.Number,
					ToNumber:         oldParentIssue.Number,
					Actor:            in.Body.Actor,
				}
				unlinkEvt, err := cfg.DB.DeleteLinkAndEvent(ctx, existing, unlinkEv)
				if err != nil {
					return nil, api.NewError(500, "internal", err.Error(), "", nil)
				}
				cfg.Broadcaster.Broadcast(StreamMsg{Kind: "event", Event: &unlinkEvt, ProjectID: in.ProjectID})
				cfg.Hooks.Enqueue(unlinkEvt)
			} else if !errors.Is(perr, db.ErrNotFound) {
				return nil, api.NewError(500, "internal", perr.Error(), "", nil)
			}
		}

		// Default path: insert link + emit issue.linked + touch updated_at, all
		// in one TX. Distinct error types map to specific responses.
		linkEv := db.LinkEventParams{
			EventType:        "issue.linked",
			EventIssueID:     from.ID,
			EventIssueNumber: from.Number,
			FromNumber:       from.Number,
			ToNumber:         to.Number,
			Actor:            in.Body.Actor,
		}
		link, evt, err := cfg.DB.CreateLinkAndEvent(ctx, db.CreateLinkParams{
			ProjectID:   in.ProjectID,
			FromIssueID: storageFromID,
			ToIssueID:   storageToID,
			Type:        in.Body.Type,
			Author:      in.Body.Actor,
		}, linkEv)
		switch {
		case errors.Is(err, db.ErrLinkExists):
			// Duplicate (from, to, type) → no-op. Re-fetch and return existing.
			existing, lookupErr := cfg.DB.LinkByEndpoints(ctx, storageFromID, storageToID, in.Body.Type)
			if lookupErr != nil {
				return nil, api.NewError(500, "internal", lookupErr.Error(), "", nil)
			}
			return mutationLinkResponse(from, existing, canonicalFromNum, canonicalToNum, nil, false), nil
		case errors.Is(err, db.ErrParentAlreadySet):
			return nil, api.NewError(409, "parent_already_set",
				"this issue already has a parent", "pass replace=true to swap", nil)
		case errors.Is(err, db.ErrSelfLink):
			return nil, api.NewError(400, "validation", "cannot link an issue to itself", "", nil)
		case errors.Is(err, db.ErrCrossProjectLink):
			return nil, api.NewError(400, "validation", "cross-project links are not allowed", "", nil)
		case err != nil:
			return nil, api.NewError(500, "internal", err.Error(), "", nil)
		}

		updatedIssue, err := cfg.DB.IssueByID(ctx, from.ID)
		if err != nil {
			return nil, api.NewError(500, "internal", err.Error(), "", nil)
		}
		cfg.Broadcaster.Broadcast(StreamMsg{Kind: "event", Event: &evt, ProjectID: in.ProjectID})
		cfg.Hooks.Enqueue(evt)
		return mutationLinkResponse(updatedIssue, link, canonicalFromNum, canonicalToNum, &evt, true), nil
	}
}

func deleteLinkHandler(cfg ServerConfig) func(context.Context, *api.DeleteLinkRequest) (*api.MutationResponse, error) {
	return func(ctx context.Context, in *api.DeleteLinkRequest) (*api.MutationResponse, error) {
		if err := validateActor(in.Actor); err != nil {
			return nil, err
		}
		from, err := activeIssueByNumber(ctx, cfg.DB, in.ProjectID, in.Number)
		if err != nil {
			return nil, err
		}

		link, err := cfg.DB.LinkByID(ctx, in.LinkID)
		if errors.Is(err, db.ErrNotFound) {
			// Idempotent: no row → no-op envelope.
			out := &api.MutationResponse{}
			out.Body.Issue = from
			out.Body.Event = nil
			out.Body.Changed = false
			return out, nil
		}
		if err != nil {
			return nil, api.NewError(500, "internal", err.Error(), "", nil)
		}
		if link.ProjectID != in.ProjectID {
			return nil, api.NewError(404, "link_not_found", "link not in this project", "", nil)
		}
		// The URL says we're operating on issue {number}'s links. Reject if
		// the link's two endpoints don't include this issue — defends against
		// URL manipulation that would otherwise emit an event attributed to
		// the wrong issue.
		if link.FromIssueID != from.ID && link.ToIssueID != from.ID {
			return nil, api.NewError(404, "link_not_found", "link not attached to this issue", "", nil)
		}

		// Resolve numbers for the event payload before deleting.
		fromIssue, err := cfg.DB.IssueByID(ctx, link.FromIssueID)
		if err != nil {
			return nil, api.NewError(500, "internal", err.Error(), "", nil)
		}
		toIssue, err := cfg.DB.IssueByID(ctx, link.ToIssueID)
		if err != nil {
			return nil, api.NewError(500, "internal", err.Error(), "", nil)
		}

		ev := db.LinkEventParams{
			EventType:        "issue.unlinked",
			EventIssueID:     from.ID,
			EventIssueNumber: from.Number,
			FromNumber:       fromIssue.Number,
			ToNumber:         toIssue.Number,
			Actor:            in.Actor,
		}
		evt, err := cfg.DB.DeleteLinkAndEvent(ctx, link, ev)
		if errors.Is(err, db.ErrNotFound) {
			// Lost the race against another DELETE; surface as no-op.
			out := &api.MutationResponse{}
			out.Body.Issue = from
			out.Body.Event = nil
			out.Body.Changed = false
			return out, nil
		}
		if err != nil {
			return nil, api.NewError(500, "internal", err.Error(), "", nil)
		}
		updatedIssue, err := cfg.DB.IssueByID(ctx, from.ID)
		if err != nil {
			return nil, api.NewError(500, "internal", err.Error(), "", nil)
		}
		cfg.Broadcaster.Broadcast(StreamMsg{Kind: "event", Event: &evt, ProjectID: in.ProjectID})
		cfg.Hooks.Enqueue(evt)
		out := &api.MutationResponse{}
		out.Body.Issue = updatedIssue
		out.Body.Event = &evt
		out.Body.Changed = true
		return out, nil
	}
}

// mutationLinkResponse assembles a CreateLinkResponse from the source issue,
// the link row, the canonical endpoint numbers, an optional event, and the
// changed flag. Used for both fresh inserts (event != nil, changed=true) and
// no-op envelopes (event == nil, changed=false).
func mutationLinkResponse(issue db.Issue, link db.Link, fromNum, toNum int64, evt *db.Event, changed bool) *api.CreateLinkResponse {
	out := &api.CreateLinkResponse{}
	out.Body.Issue = issue
	out.Body.Link = api.LinkOut{
		ID:           link.ID,
		ProjectID:    link.ProjectID,
		FromNumber:   fromNum,
		FromIssueUID: link.FromIssueUID,
		ToNumber:     toNum,
		ToIssueUID:   link.ToIssueUID,
		Type:         link.Type,
		Author:       link.Author,
		CreatedAt:    link.CreatedAt,
	}
	out.Body.Event = evt
	out.Body.Changed = changed
	return out
}
