package daemon

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/wesm/kata/internal/api"
	"github.com/wesm/kata/internal/db"
	"github.com/wesm/kata/internal/similarity"
	"github.com/wesm/kata/internal/uid"
)

const minIssueUIDPrefixLen = 8

// registerIssuesHandlers installs the four issue routes (create/list/show/edit)
// on humaAPI. CreateIssue writes both the issue row and the matching
// issue.created event in one tx (see db.CreateIssue) so the response always
// carries an event for the CLI to render.
func registerIssuesHandlers(humaAPI huma.API, cfg ServerConfig) {
	huma.Register(humaAPI, huma.Operation{
		OperationID: "createIssue",
		Method:      "POST",
		Path:        "/api/v1/projects/{project_id}/issues",
	}, func(ctx context.Context, in *api.CreateIssueRequest) (*api.MutationResponse, error) {
		if err := validateActor(in.Body.Actor); err != nil {
			return nil, err
		}
		if _, err := activeProjectByID(ctx, cfg.DB, in.ProjectID); err != nil {
			return nil, err
		}

		links := make([]db.InitialLink, 0, len(in.Body.Links))
		for _, l := range in.Body.Links {
			links = append(links, db.InitialLink{Type: l.Type, ToNumber: l.ToNumber})
		}

		// Idempotency runs before look-alike so it wins over force_new (§3.7).
		idempotencyFingerprint, reuse, err := tryIdempotencyMatch(ctx, cfg, in, links)
		if err != nil {
			return nil, err
		}
		if reuse != nil {
			return reuse, nil
		}
		if !in.Body.ForceNew {
			if err := runLookalikeCheck(ctx, cfg, in); err != nil {
				return nil, err
			}
		}

		issue, evt, err := cfg.DB.CreateIssue(ctx, db.CreateIssueParams{
			ProjectID:              in.ProjectID,
			Title:                  in.Body.Title,
			Body:                   in.Body.Body,
			Author:                 in.Body.Actor,
			Owner:                  in.Body.Owner,
			Labels:                 in.Body.Labels,
			Links:                  links,
			IdempotencyKey:         in.IdempotencyKey,
			IdempotencyFingerprint: idempotencyFingerprint,
		})
		switch {
		case errors.Is(err, db.ErrInitialLinkInvalidType):
			return nil, api.NewError(400, "validation",
				"link.type must be parent|blocks|related", "", nil)
		case errors.Is(err, db.ErrInitialLinkTargetNotFound):
			return nil, api.NewError(404, "issue_not_found",
				"initial link target not found in this project", "", nil)
		case errors.Is(err, db.ErrSelfLink):
			return nil, api.NewError(400, "validation",
				"cannot link an issue to itself", "", nil)
		case errors.Is(err, db.ErrLabelInvalid):
			return nil, api.NewError(400, "validation",
				"label must match charset [a-z0-9._:-] and length 1..64", "", nil)
		case errors.Is(err, db.ErrParentAlreadySet):
			return nil, api.NewError(409, "parent_already_set",
				"duplicate parent in initial links", "pass at most one parent link", nil)
		case errors.Is(err, db.ErrNotFound):
			return nil, api.NewError(404, "project_not_found", "project not found", "", nil)
		case err != nil:
			return nil, api.NewError(500, "internal", err.Error(), "", nil)
		}
		cfg.Broadcaster.Broadcast(StreamMsg{Kind: "event", Event: &evt, ProjectID: in.ProjectID})
		cfg.Hooks.Enqueue(evt)
		out := &api.MutationResponse{}
		out.Body.Issue = issue
		out.Body.Event = &evt
		out.Body.Changed = true
		return out, nil
	})

	huma.Register(humaAPI, huma.Operation{
		OperationID: "listIssues",
		Method:      "GET",
		Path:        "/api/v1/projects/{project_id}/issues",
	}, func(ctx context.Context, in *api.ListIssuesRequest) (*api.ListIssuesResponse, error) {
		if _, err := activeProjectByID(ctx, cfg.DB, in.ProjectID); err != nil {
			return nil, err
		}
		issues, err := cfg.DB.ListIssues(ctx, db.ListIssuesParams{
			ProjectID: in.ProjectID,
			Status:    in.Status,
			Limit:     in.Limit,
		})
		if err != nil {
			return nil, api.NewError(500, "internal", err.Error(), "", nil)
		}
		issueOuts, err := hydrateIssueOuts(ctx, cfg.DB, in.ProjectID, issues)
		out := &api.ListIssuesResponse{}
		if err != nil {
			return nil, api.NewError(500, "internal", err.Error(), "", nil)
		}
		out.Body.Issues = issueOuts
		return out, nil
	})

	huma.Register(humaAPI, huma.Operation{
		OperationID: "listAllIssues",
		Method:      "GET",
		Path:        "/api/v1/issues",
	}, func(ctx context.Context, in *api.ListAllIssuesRequest) (*api.ListIssuesResponse, error) {
		if in.ProjectID < 0 {
			return nil, api.NewError(400, "validation",
				"project_id must be a positive integer", "", nil)
		}
		if in.ProjectID > 0 {
			if _, err := activeProjectByID(ctx, cfg.DB, in.ProjectID); err != nil {
				return nil, err
			}
		}
		issues, err := cfg.DB.ListAllIssues(ctx, db.ListAllIssuesParams{
			ProjectID: in.ProjectID,
			Status:    in.Status,
			Limit:     in.Limit,
		})
		if err != nil {
			return nil, api.NewError(500, "internal", err.Error(), "", nil)
		}
		issueOuts, err := hydrateIssueOutsCrossProject(ctx, cfg.DB, issues)
		out := &api.ListIssuesResponse{}
		if err != nil {
			return nil, api.NewError(500, "internal", err.Error(), "", nil)
		}
		out.Body.Issues = issueOuts
		return out, nil
	})

	huma.Register(humaAPI, huma.Operation{
		OperationID: "showIssue",
		Method:      "GET",
		Path:        "/api/v1/projects/{project_id}/issues/{number}",
	}, func(ctx context.Context, in *api.ShowIssueRequest) (*api.ShowIssueResponse, error) {
		issue, err := activeIssueByNumber(ctx, cfg.DB, in.ProjectID, in.Number)
		if err != nil {
			return nil, err
		}
		return buildShowIssueResponse(ctx, cfg, issue, in.IncludeDeleted)
	})

	huma.Register(humaAPI, huma.Operation{
		OperationID: "showIssueByUID",
		Method:      "GET",
		Path:        "/api/v1/issues/{uid}",
	}, func(ctx context.Context, in *api.ShowIssueByUIDRequest) (*api.ShowIssueResponse, error) {
		issue, err := resolveIssueByUIDOrPrefix(ctx, cfg.DB, in.UID)
		if err != nil {
			return nil, err
		}
		// Hide issues whose parent project is archived, mirroring every
		// other project-scoped handler. The UID lookup itself returns the
		// row regardless of project archive state.
		if _, perr := activeProjectByID(ctx, cfg.DB, issue.ProjectID); perr != nil {
			return nil, perr
		}
		return buildShowIssueResponse(ctx, cfg, issue, in.IncludeDeleted)
	})

	huma.Register(humaAPI, huma.Operation{
		OperationID: "editIssue",
		Method:      "PATCH",
		Path:        "/api/v1/projects/{project_id}/issues/{number}",
	}, func(ctx context.Context, in *api.EditIssueRequest) (*api.MutationResponse, error) {
		if err := validateActor(in.Body.Actor); err != nil {
			return nil, err
		}
		issue, err := activeIssueByNumber(ctx, cfg.DB, in.ProjectID, in.Number)
		if err != nil {
			return nil, err
		}
		updated, evt, changed, err := cfg.DB.EditIssue(ctx, db.EditIssueParams{
			IssueID: issue.ID,
			Title:   in.Body.Title,
			Body:    in.Body.Body,
			Owner:   in.Body.Owner,
			Actor:   in.Body.Actor,
		})
		if errors.Is(err, db.ErrNoFields) {
			return nil, api.NewError(400, "validation", "no fields to update", "pass at least one of title, body, owner", nil)
		}
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

func resolveIssueByUIDOrPrefix(ctx context.Context, store *db.DB, ref string) (db.Issue, error) {
	if uid.Valid(ref) {
		issue, err := store.IssueByUID(ctx, ref)
		if errors.Is(err, db.ErrNotFound) {
			return db.Issue{}, api.NewError(404, "issue_not_found", "issue not found", "", nil)
		}
		if err != nil {
			return db.Issue{}, api.NewError(500, "internal", err.Error(), "", nil)
		}
		return issue, nil
	}
	if len(ref) < minIssueUIDPrefixLen {
		return db.Issue{}, api.NewError(400, "prefix_too_short",
			"uid prefix must be at least 8 characters", "", nil)
	}
	if !uid.ValidPrefix(ref) {
		return db.Issue{}, api.NewError(400, "validation", "uid must be a valid ULID or prefix", "", nil)
	}
	matches, err := store.IssueUIDPrefixMatch(ctx, ref, 20)
	if err != nil {
		return db.Issue{}, api.NewError(500, "internal", err.Error(), "", nil)
	}
	switch len(matches) {
	case 0:
		return db.Issue{}, api.NewError(404, "issue_not_found", "issue not found", "", nil)
	case 1:
		return matches[0], nil
	default:
		candidates := make([]string, 0, len(matches))
		for _, issue := range matches {
			candidates = append(candidates,
				fmt.Sprintf("%s (#%d project %d)", issue.UID, issue.Number, issue.ProjectID))
		}
		return db.Issue{}, api.NewError(409, "prefix_ambiguous",
			"uid prefix is ambiguous: "+strings.Join(candidates, ", "), "",
			map[string]any{"candidates": candidates})
	}
}

func buildShowIssueResponse(ctx context.Context, cfg ServerConfig, issue db.Issue, includeDeleted bool) (*api.ShowIssueResponse, error) {
	if issue.DeletedAt != nil && !includeDeleted {
		return nil, api.NewError(404, "issue_not_found",
			"issue not found",
			"pass include_deleted=true to view soft-deleted issues",
			nil)
	}
	comments, err := listComments(ctx, cfg.DB, issue.ID)
	if err != nil {
		return nil, api.NewError(500, "internal", err.Error(), "", nil)
	}
	links, err := loadLinkOuts(ctx, cfg.DB, issue.ID)
	if err != nil {
		return nil, api.NewError(500, "internal", err.Error(), "", nil)
	}
	labels, err := cfg.DB.LabelsByIssue(ctx, issue.ID)
	if err != nil {
		return nil, api.NewError(500, "internal", err.Error(), "", nil)
	}
	parent, err := loadParentRef(ctx, cfg.DB, issue)
	if err != nil {
		return nil, api.NewError(500, "internal", err.Error(), "", nil)
	}
	children, err := cfg.DB.ChildrenOfIssue(ctx, issue.ProjectID, issue.ID)
	if err != nil {
		return nil, api.NewError(500, "internal", err.Error(), "", nil)
	}
	childOuts, err := hydrateIssueOuts(ctx, cfg.DB, issue.ProjectID, children)
	if err != nil {
		return nil, api.NewError(500, "internal", err.Error(), "", nil)
	}
	out := &api.ShowIssueResponse{}
	out.Body.Issue = issue
	out.Body.Comments = comments
	out.Body.Links = links
	out.Body.Labels = labels
	out.Body.Parent = parent
	out.Body.Children = childOuts
	return out, nil
}

func issueRefFromDB(iss db.Issue) api.IssueRef {
	return api.IssueRef{Number: iss.Number, Title: iss.Title, Status: iss.Status}
}

func loadParentRef(ctx context.Context, store *db.DB, issue db.Issue) (*api.IssueRef, error) {
	link, err := store.ParentOf(ctx, issue.ID)
	if errors.Is(err, db.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	parent, err := store.IssueByID(ctx, link.ToIssueID)
	if err != nil {
		return nil, err
	}
	ref := issueRefFromDB(parent)
	return &ref, nil
}

// hydrateIssueOutsCrossProject hydrates labels/parent/child-counts for issues
// that may span multiple projects. Per-project hydration helpers
// (LabelsByIssues, ParentNumbersByIssues, ChildCountsByParents) all scope by
// project_id, so we group by ProjectID and run them per group, then assemble
// the IssueOut slice in the input order. Realistic project counts are tiny
// (≤10) so the per-group cost is bounded.
func hydrateIssueOutsCrossProject(ctx context.Context, store *db.DB, issues []db.Issue) ([]api.IssueOut, error) {
	if len(issues) == 0 {
		return []api.IssueOut{}, nil
	}
	byProject := map[int64][]db.Issue{}
	for _, iss := range issues {
		byProject[iss.ProjectID] = append(byProject[iss.ProjectID], iss)
	}
	rowsByID := make(map[int64]api.IssueOut, len(issues))
	for projectID, group := range byProject {
		hydrated, err := hydrateIssueOuts(ctx, store, projectID, group)
		if err != nil {
			return nil, err
		}
		for _, row := range hydrated {
			rowsByID[row.ID] = row
		}
	}
	out := make([]api.IssueOut, len(issues))
	for i, iss := range issues {
		out[i] = rowsByID[iss.ID]
	}
	return out, nil
}

func hydrateIssueOuts(ctx context.Context, store *db.DB, projectID int64, issues []db.Issue) ([]api.IssueOut, error) {
	ids := make([]int64, len(issues))
	for i, iss := range issues {
		ids[i] = iss.ID
	}
	labelsByID, err := store.LabelsByIssues(ctx, projectID, ids)
	if err != nil {
		return nil, err
	}
	parentNumbers, err := store.ParentNumbersByIssues(ctx, projectID, ids)
	if err != nil {
		return nil, err
	}
	childCounts, err := store.ChildCountsByParents(ctx, projectID, ids)
	if err != nil {
		return nil, err
	}
	blocks, err := store.BlockNumbersByIssues(ctx, projectID, ids)
	if err != nil {
		return nil, err
	}
	out := make([]api.IssueOut, len(issues))
	for i, iss := range issues {
		row := api.IssueOut{Issue: iss, Labels: labelsByID[iss.ID], Blocks: blocks[iss.ID]}
		if parentNumber, ok := parentNumbers[iss.ID]; ok {
			row.ParentNumber = &parentNumber
		}
		if counts := childCounts[iss.ID]; counts.Total > 0 {
			row.ChildCounts = &counts
		}
		out[i] = row
	}
	return out, nil
}

// loadLinkOuts fetches every link involving issueID, resolving both endpoint
// numbers so the wire response speaks the agent-facing surface (numbers, not
// internal ids). One IssueByID call per endpoint is fine for show; pagination
// is a Plan 4 concern.
func loadLinkOuts(ctx context.Context, store *db.DB, issueID int64) ([]api.LinkOut, error) {
	rows, err := store.LinksByIssue(ctx, issueID)
	if err != nil {
		return nil, err
	}
	out := make([]api.LinkOut, 0, len(rows))
	for _, l := range rows {
		from, err := store.IssueByID(ctx, l.FromIssueID)
		if err != nil {
			return nil, err
		}
		to, err := store.IssueByID(ctx, l.ToIssueID)
		if err != nil {
			return nil, err
		}
		out = append(out, api.LinkOut{
			ID:           l.ID,
			ProjectID:    l.ProjectID,
			FromNumber:   from.Number,
			FromIssueUID: l.FromIssueUID,
			ToNumber:     to.Number,
			ToIssueUID:   l.ToIssueUID,
			Type:         l.Type,
			Author:       l.Author,
			CreatedAt:    l.CreatedAt,
		})
	}
	return out, nil
}

// listComments fetches every comment attached to issueID in chronological
// order. Plan 1 ships no pagination; the show handler embeds the full slice.
func listComments(ctx context.Context, store *db.DB, issueID int64) ([]db.Comment, error) {
	rows, err := store.QueryContext(ctx,
		`SELECT id, issue_id, author, body, created_at FROM comments WHERE issue_id = ? ORDER BY created_at ASC, id ASC`, issueID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []db.Comment
	for rows.Next() {
		var c db.Comment
		if err := rows.Scan(&c.ID, &c.IssueID, &c.Author, &c.Body, &c.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

const (
	// idempotencyWindow is the 7-day lookback per spec §3.6.
	idempotencyWindow = 7 * 24 * time.Hour
	// similarityThreshold is the soft-block trigger per spec §3.7.
	similarityThreshold = 0.7
)

// tryIdempotencyMatch runs the §3.6 idempotency lookup. Returns the fingerprint
// (so the caller can fold it into the issue.created event payload) and, when a
// prior issue exists for the key, a complete reuse-envelope MutationResponse
// (the caller should return it directly). Returns the relevant 409 wire error
// for mismatch / soft-deleted cases. When IdempotencyKey is empty, returns
// ("", nil, nil) so the caller falls through to the look-alike check.
//
// Known limitation: the lookup → CreateIssue is not atomic. Two concurrent
// requests with the same Idempotency-Key can both miss the lookup and both
// insert a fresh issue. Closing the race requires either a daemon-level
// per-key mutex with bounded GC, or restructuring CreateIssue around
// BEGIN IMMEDIATE with an in-TX re-lookup. Deferred from Plan 3 — small
// in single-user CLI usage. Tracked under roborev Job 16791-1.
func tryIdempotencyMatch(ctx context.Context, cfg ServerConfig, in *api.CreateIssueRequest,
	links []db.InitialLink) (string, *api.MutationResponse, error) {
	if in.IdempotencyKey == "" {
		return "", nil, nil
	}
	fp := db.Fingerprint(in.Body.Title, in.Body.Body, in.Body.Owner, in.Body.Labels, links)
	since := time.Now().Add(-idempotencyWindow)
	match, err := cfg.DB.LookupIdempotency(ctx, in.ProjectID, in.IdempotencyKey, since)
	if err != nil {
		return "", nil, api.NewError(500, "internal", err.Error(), "", nil)
	}
	if match == nil {
		return fp, nil, nil
	}
	if match.Fingerprint != fp {
		return "", nil, api.NewError(409, "idempotency_mismatch",
			"idempotency key matched a prior issue with a different fingerprint",
			"either use a fresh key, or send the exact same fields as the original",
			map[string]any{"original_issue_number": match.IssueNumber})
	}
	existing, err := cfg.DB.IssueByID(ctx, match.IssueID)
	if err != nil {
		return "", nil, api.NewError(500, "internal", err.Error(), "", nil)
	}
	if existing.DeletedAt != nil {
		return "", nil, api.NewError(409, "idempotency_deleted",
			"idempotency key matched a soft-deleted issue",
			"run `kata restore "+strconv.FormatInt(existing.Number, 10)+"` or use a fresh key",
			map[string]any{"original_issue_number": existing.Number})
	}
	// Copy the Event off the *IdempotencyMatch struct so OriginalEvent has a
	// stable address that doesn't alias the lookup result.
	origCopy := match.Event
	out := &api.MutationResponse{}
	out.Body.Issue = existing
	out.Body.Event = nil
	out.Body.OriginalEvent = &origCopy
	out.Body.Changed = false
	out.Body.Reused = true
	return fp, out, nil
}

// runLookalikeCheck runs the §3.7 soft-block: SearchFTSAny over title+body
// (OR-of-tokens for high recall), scores each candidate via similarity.Score,
// and returns a 409 duplicate_candidates error if any candidate is at or
// above the 0.7 threshold. nil means proceed. The OR variant is required
// because near-duplicates that differ by even one token would be filtered
// out by SearchFTS's implicit-AND before similarity scoring runs.
func runLookalikeCheck(ctx context.Context, cfg ServerConfig, in *api.CreateIssueRequest) error {
	q := strings.TrimSpace(in.Body.Title + " " + in.Body.Body)
	candidates, err := cfg.DB.SearchFTSAny(ctx, in.ProjectID, q, 20, false)
	if err != nil {
		return api.NewError(500, "internal", err.Error(), "", nil)
	}
	matched := []map[string]any{}
	for _, c := range candidates {
		score := similarity.Score(in.Body.Title, in.Body.Body, c.Issue.Title, c.Issue.Body)
		if score >= similarityThreshold {
			matched = append(matched, map[string]any{
				"number": c.Issue.Number,
				"title":  c.Issue.Title,
				"score":  score,
			})
		}
	}
	if len(matched) == 0 {
		return nil
	}
	return api.NewError(409, "duplicate_candidates",
		formatDuplicateMessage(matched),
		"comment on an existing issue, or pass force_new=true to create anyway",
		map[string]any{"candidates": matched})
}

// formatDuplicateMessage produces a singular/plural-aware human message for
// the duplicate_candidates 409 response.
func formatDuplicateMessage(matched []map[string]any) string {
	n := len(matched)
	if n == 1 {
		return "1 open issue matches this title"
	}
	return strconv.Itoa(n) + " open issues match this title"
}
