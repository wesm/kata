package daemon

import (
	"context"

	"github.com/danielgtaylor/huma/v2"

	"github.com/wesm/kata/internal/api"
)

// registerActionsHandlers installs POST /actions/close and /actions/reopen.
// CloseIssue and ReopenIssue return changed=false with a nil event when the
// issue is already in the target state; both fields propagate verbatim into
// the MutationResponse envelope.
func registerActionsHandlers(humaAPI huma.API, cfg ServerConfig) {
	huma.Register(humaAPI, huma.Operation{
		OperationID: "closeIssue",
		Method:      "POST",
		Path:        "/api/v1/projects/{project_id}/issues/{number}/actions/close",
	}, func(ctx context.Context, in *api.ActionRequest) (*api.MutationResponse, error) {
		if err := validateActor(in.Body.Actor); err != nil {
			return nil, err
		}
		issue, err := activeIssueByNumber(ctx, cfg.DB, in.ProjectID, in.Number)
		if err != nil {
			return nil, err
		}
		updated, evt, changed, err := cfg.DB.CloseIssue(ctx, issue.ID, in.Body.Reason, in.Body.Actor)
		if err != nil {
			return nil, api.NewError(500, "internal", err.Error(), "", nil)
		}
		if changed && evt != nil {
			cfg.Broadcaster.Broadcast(StreamMsg{Kind: "event", Event: evt, ProjectID: in.ProjectID})
			cfg.Hooks.Enqueue(*evt)
		}
		out := &api.MutationResponse{}
		out.Body.Issue = updated
		out.Body.Event = evt
		out.Body.Changed = changed
		return out, nil
	})

	huma.Register(humaAPI, huma.Operation{
		OperationID: "reopenIssue",
		Method:      "POST",
		Path:        "/api/v1/projects/{project_id}/issues/{number}/actions/reopen",
	}, func(ctx context.Context, in *api.ActionRequest) (*api.MutationResponse, error) {
		if err := validateActor(in.Body.Actor); err != nil {
			return nil, err
		}
		issue, err := activeIssueByNumber(ctx, cfg.DB, in.ProjectID, in.Number)
		if err != nil {
			return nil, err
		}
		updated, evt, changed, err := cfg.DB.ReopenIssue(ctx, issue.ID, in.Body.Actor)
		if err != nil {
			return nil, api.NewError(500, "internal", err.Error(), "", nil)
		}
		if changed && evt != nil {
			cfg.Broadcaster.Broadcast(StreamMsg{Kind: "event", Event: evt, ProjectID: in.ProjectID})
			cfg.Hooks.Enqueue(*evt)
		}
		out := &api.MutationResponse{}
		out.Body.Issue = updated
		out.Body.Event = evt
		out.Body.Changed = changed
		return out, nil
	})
}
