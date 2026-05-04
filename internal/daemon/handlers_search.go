package daemon

import (
	"context"
	"strings"

	"github.com/danielgtaylor/huma/v2"

	"github.com/wesm/kata/internal/api"
)

// registerSearchHandlers installs GET /api/v1/projects/{id}/search. Returns
// the spec §4.10 envelope: query echo + ranked results with score + matched_in.
func registerSearchHandlers(humaAPI huma.API, cfg ServerConfig) {
	huma.Register(humaAPI, huma.Operation{
		OperationID: "searchIssues",
		Method:      "GET",
		Path:        "/api/v1/projects/{project_id}/search",
	}, func(ctx context.Context, in *api.SearchRequest) (*api.SearchResponse, error) {
		if strings.TrimSpace(in.Query) == "" {
			return nil, api.NewError(400, "validation",
				"query parameter q must be non-empty", "", nil)
		}
		if _, err := activeProjectByID(ctx, cfg.DB, in.ProjectID); err != nil {
			return nil, err
		}
		limit := in.Limit
		if limit <= 0 {
			limit = 20
		}
		candidates, err := cfg.DB.SearchFTS(ctx, in.ProjectID, in.Query, limit, in.IncludeDeleted)
		if err != nil {
			return nil, api.NewError(500, "internal", err.Error(), "", nil)
		}
		hits := make([]api.SearchHit, 0, len(candidates))
		for _, c := range candidates {
			hits = append(hits, api.SearchHit{
				Issue:     c.Issue,
				Score:     c.Score,
				MatchedIn: c.MatchedIn,
			})
		}
		out := &api.SearchResponse{}
		out.Body.Query = in.Query
		out.Body.Results = hits
		return out, nil
	})
}
