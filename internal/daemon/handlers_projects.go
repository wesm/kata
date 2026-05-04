package daemon

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/danielgtaylor/huma/v2"

	"github.com/wesm/kata/internal/api"
	"github.com/wesm/kata/internal/config"
	"github.com/wesm/kata/internal/db"
)

// activeIssueByNumber resolves an issue by (project_id, number) for surface
// API handlers, gating on the parent project's archive state first. Returns
// project_not_found 404 when the project is archived (mirroring every other
// project-scoped handler) and issue_not_found 404 when the issue does not
// exist. Errors are pre-wrapped api.NewError envelopes so call sites can
// `return nil, err`.
//
// Internal callers that need to operate on issues whose parent project is
// archived (merge / restore plumbing) must use store.IssueByNumber directly.
func activeIssueByNumber(ctx context.Context, store *db.DB, projectID, number int64) (db.Issue, error) {
	if _, err := activeProjectByID(ctx, store, projectID); err != nil {
		return db.Issue{}, err
	}
	issue, err := store.IssueByNumber(ctx, projectID, number)
	if errors.Is(err, db.ErrNotFound) {
		return db.Issue{}, api.NewError(404, "issue_not_found", "issue not found", "", nil)
	}
	if err != nil {
		return db.Issue{}, api.NewError(500, "internal", err.Error(), "", nil)
	}
	return issue, nil
}

// activeProjectByID resolves a project by rowid for surface API handlers
// that should treat archived projects as not-found. Returns the api.NewError
// envelope directly so call sites can `return nil, err`.
//
// Internal helpers (merge, restore, alias resolve) that need to operate on
// archived rows must use store.ProjectByID directly.
func activeProjectByID(ctx context.Context, store *db.DB, id int64) (db.Project, error) {
	p, err := store.ProjectByID(ctx, id)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			return db.Project{}, api.NewError(404, "project_not_found", "project not found", "", nil)
		}
		return db.Project{}, api.NewError(500, "internal", err.Error(), "", nil)
	}
	if p.DeletedAt != nil {
		return db.Project{}, api.NewError(404, "project_not_found", "project not found", "", nil)
	}
	return p, nil
}

// dbProjectToOut maps a db.Project (internal row) to the API-shape
// ProjectOut. Stats stays nil — that field is populated only by the
// list-projects handler when ?include=stats is set (Task 3).
func dbProjectToOut(p db.Project) api.ProjectOut {
	return api.ProjectOut{
		ID:              p.ID,
		UID:             p.UID,
		Identity:        p.Identity,
		Name:            p.Name,
		CreatedAt:       p.CreatedAt,
		NextIssueNumber: p.NextIssueNumber,
		DeletedAt:       p.DeletedAt,
	}
}

// includeContains reports whether the comma-separated ?include= value
// names the given token. Whitespace is trimmed; matching is case-
// insensitive on the token side. Spec §7.1.
func includeContains(includeParam, token string) bool {
	for _, part := range strings.Split(includeParam, ",") {
		if strings.EqualFold(strings.TrimSpace(part), token) {
			return true
		}
	}
	return false
}

// registerProjectsHandlers installs project-scoped routes (resolve, init, list,
// show) on humaAPI. Resolution and init semantics live entirely on the daemon
// per spec §2.4 so all clients (CLI, TUI, future) see identical behavior.
func registerProjectsHandlers(humaAPI huma.API, cfg ServerConfig) {
	huma.Register(humaAPI, huma.Operation{
		OperationID: "resolveProject",
		Method:      "POST",
		Path:        "/api/v1/projects/resolve",
	}, func(ctx context.Context, in *api.ResolveProjectRequest) (*api.ResolveProjectResponse, error) {
		out, err := resolveProject(ctx, cfg.DB, in.Body.StartPath)
		if err != nil {
			return nil, err
		}
		return &api.ResolveProjectResponse{Body: *out}, nil
	})

	huma.Register(humaAPI, huma.Operation{
		OperationID: "initProject",
		Method:      "POST",
		Path:        "/api/v1/projects",
	}, func(ctx context.Context, in *api.InitProjectRequest) (*api.InitProjectResponse, error) {
		out, created, err := initProject(ctx, cfg.DB, in)
		if err != nil {
			return nil, err
		}
		resp := &api.InitProjectResponse{}
		resp.Body.ProjectResolveBody = *out
		resp.Body.Created = created
		return resp, nil
	})

	huma.Register(humaAPI, huma.Operation{
		OperationID: "listProjects",
		Method:      "GET",
		Path:        "/api/v1/projects",
	}, func(ctx context.Context, in *struct {
		Include string `query:"include"`
	}) (*api.ListProjectsResponse, error) {
		ps, err := cfg.DB.ListProjects(ctx)
		if err != nil {
			return nil, api.NewError(500, "internal", err.Error(), "", nil)
		}
		outs := make([]api.ProjectOut, len(ps))
		for i, p := range ps {
			outs[i] = dbProjectToOut(p)
		}
		if includeContains(in.Include, "stats") {
			stats, err := cfg.DB.BatchProjectStats(ctx)
			if err != nil {
				return nil, api.NewError(500, "internal", err.Error(), "", nil)
			}
			for i, p := range ps {
				if s, ok := stats[p.ID]; ok {
					outs[i].Stats = &api.ProjectStatsOut{
						Open:        s.Open,
						Closed:      s.Closed,
						LastEventAt: s.LastEventAt,
					}
				}
			}
		}
		out := &api.ListProjectsResponse{}
		out.Body.Projects = outs
		return out, nil
	})

	huma.Register(humaAPI, huma.Operation{
		OperationID: "resetIssueCounter",
		Method:      "POST",
		Path:        "/api/v1/projects/{project_id}/reset-counter",
	}, func(ctx context.Context, in *api.ResetCounterRequest) (*api.ResetCounterResponse, error) {
		if in.Body.To < 1 {
			return nil, api.NewError(400, "validation", "to must be >= 1", "", nil)
		}
		if _, err := activeProjectByID(ctx, cfg.DB, in.ProjectID); err != nil {
			return nil, err
		}
		err := cfg.DB.ResetIssueCounter(ctx, in.ProjectID, in.Body.To)
		if errors.Is(err, db.ErrInvalidCounterValue) {
			return nil, api.NewError(400, "validation", "to must be >= 1", "", nil)
		}
		if errors.Is(err, db.ErrNotFound) {
			return nil, api.NewError(404, "project_not_found", "project not found", "", nil)
		}
		var hasIssues *db.ProjectHasIssuesError
		if errors.As(err, &hasIssues) {
			return nil, api.NewError(http.StatusConflict, "project_has_issues",
				fmt.Sprintf("cannot reset: project has %d issues; only counter resets on an empty project are allowed", hasIssues.Count),
				"purge all issues before resetting the counter", map[string]any{
					"issue_count": hasIssues.Count,
				})
		}
		if err != nil {
			return nil, api.NewError(500, "internal", err.Error(), "", nil)
		}
		p, err := cfg.DB.ProjectByID(ctx, in.ProjectID)
		if err != nil {
			return nil, api.NewError(500, "internal", err.Error(), "", nil)
		}
		out := &api.ResetCounterResponse{}
		out.Body.Project = dbProjectToOut(p)
		return out, nil
	})

	huma.Register(humaAPI, huma.Operation{
		OperationID: "showProject",
		Method:      "GET",
		Path:        "/api/v1/projects/{project_id}",
	}, func(ctx context.Context, in *struct {
		ProjectID int64 `path:"project_id"`
	}) (*api.ShowProjectResponse, error) {
		p, err := activeProjectByID(ctx, cfg.DB, in.ProjectID)
		if err != nil {
			return nil, err
		}
		aliases, err := cfg.DB.ProjectAliases(ctx, p.ID)
		if err != nil {
			return nil, api.NewError(500, "internal", err.Error(), "", nil)
		}
		out := &api.ShowProjectResponse{}
		out.Body.Project = dbProjectToOut(p)
		out.Body.Aliases = aliases
		return out, nil
	})

	huma.Register(humaAPI, huma.Operation{
		OperationID: "mergeProject",
		Method:      "POST",
		Path:        "/api/v1/projects/{project_id}/merge",
	}, func(ctx context.Context, in *api.MergeProjectRequest) (*api.MergeProjectResponse, error) {
		if in.Body.SourceProjectID == 0 {
			return nil, api.NewError(400, "validation", "source_project_id required", "", nil)
		}
		targetName := strings.TrimSpace(in.Body.TargetName)
		var namePtr *string
		if targetName != "" {
			namePtr = &targetName
		}
		merged, err := cfg.DB.MergeProjects(ctx, db.MergeProjectsParams{
			SourceProjectID: in.Body.SourceProjectID,
			TargetProjectID: in.ProjectID,
			TargetName:      namePtr,
		})
		if errors.Is(err, db.ErrProjectMergeSameProject) {
			return nil, api.NewError(400, "validation", "cannot merge a project into itself", "", nil)
		}
		var collision *db.ProjectMergeCollisionError
		if errors.As(err, &collision) {
			return nil, api.NewError(409, "project_merge_issue_number_collision",
				"source and target have overlapping issue numbers",
				"resolve collisions before merging", map[string]any{"numbers": collision.Numbers})
		}
		if errors.Is(err, db.ErrProjectMergeArchivedSource) {
			return nil, api.NewError(409, "project_merge_archived_source",
				"source project is archived", "", nil)
		}
		if errors.Is(err, db.ErrProjectMergeArchivedTarget) {
			return nil, api.NewError(409, "project_merge_archived_target",
				"target project is archived", "", nil)
		}
		if errors.Is(err, db.ErrNotFound) {
			return nil, api.NewError(404, "project_not_found", "project not found", "", nil)
		}
		if err != nil {
			return nil, api.NewError(500, "internal", err.Error(), "", nil)
		}
		return &api.MergeProjectResponse{Body: api.MergeProjectResultOut{
			Source:         dbProjectToOut(merged.Source),
			Target:         dbProjectToOut(merged.Target),
			IssuesMoved:    merged.IssuesMoved,
			AliasesMoved:   merged.AliasesMoved,
			EventsMoved:    merged.EventsMoved,
			PurgeLogsMoved: merged.PurgeLogsMoved,
		}}, nil
	})

	huma.Register(humaAPI, huma.Operation{
		OperationID: "removeProject",
		Method:      "DELETE",
		Path:        "/api/v1/projects/{project_id}",
	}, func(ctx context.Context, in *api.RemoveProjectRequest) (*api.RemoveProjectResponse, error) {
		if err := validateActor(in.Actor); err != nil {
			return nil, err
		}
		project, evt, err := cfg.DB.RemoveProject(ctx, db.RemoveProjectParams{
			ProjectID: in.ProjectID, Actor: in.Actor, Force: in.Force,
		})
		switch {
		case errors.Is(err, db.ErrNotFound):
			return nil, api.NewError(404, "project_not_found", "project not found", "", nil)
		case errors.Is(err, db.ErrProjectAlreadyArchived):
			return nil, api.NewError(409, "project_already_archived",
				"project is already archived", "", nil)
		}
		var openErr *db.ProjectHasOpenIssuesError
		if errors.As(err, &openErr) {
			return nil, api.NewError(409, "project_has_open_issues",
				"project has open issues",
				"close or purge the open issues first, or pass force=true",
				map[string]any{"open_issues": openErr.OpenIssues})
		}
		if err != nil {
			return nil, api.NewError(500, "internal", err.Error(), "", nil)
		}
		cfg.Broadcaster.Broadcast(StreamMsg{Kind: "event", Event: evt, ProjectID: project.ID})
		cfg.Hooks.Enqueue(*evt)
		out := &api.RemoveProjectResponse{}
		out.Body.Project = dbProjectToOut(project)
		out.Body.Event = evt
		return out, nil
	})

	huma.Register(humaAPI, huma.Operation{
		OperationID: "detachProjectAlias",
		Method:      "DELETE",
		Path:        "/api/v1/projects/{project_id}/aliases/{alias_id}",
	}, func(ctx context.Context, in *api.DetachProjectAliasRequest) (*api.DetachProjectAliasResponse, error) {
		if err := validateActor(in.Actor); err != nil {
			return nil, err
		}
		// (project_id, alias_id) is validated atomically inside the delete
		// transaction so a reassignment between any preflight and the delete
		// cannot drop an alias from a different project than the request named.
		alias, evt, err := cfg.DB.DetachProjectAlias(ctx, db.DetachAliasParams{
			ProjectID: in.ProjectID, AliasID: in.AliasID, Actor: in.Actor, Force: in.Force,
		})
		switch {
		case errors.Is(err, db.ErrNotFound):
			return nil, api.NewError(404, "alias_not_found",
				"alias not found for the requested project", "", nil)
		case errors.Is(err, db.ErrAliasIsLast):
			return nil, api.NewError(409, "alias_is_last",
				"alias is the only one for its project",
				"detach with force=true to drop it anyway, or attach a replacement first", nil)
		}
		if err != nil {
			return nil, api.NewError(500, "internal", err.Error(), "", nil)
		}
		cfg.Broadcaster.Broadcast(StreamMsg{Kind: "event", Event: evt, ProjectID: alias.ProjectID})
		cfg.Hooks.Enqueue(*evt)
		out := &api.DetachProjectAliasResponse{}
		out.Body.Alias = alias
		out.Body.Event = evt
		return out, nil
	})

	huma.Register(humaAPI, huma.Operation{
		OperationID: "renameProject",
		Method:      "PATCH",
		Path:        "/api/v1/projects/{project_id}",
	}, func(ctx context.Context, in *api.RenameProjectRequest) (*api.ShowProjectResponse, error) {
		name := strings.TrimSpace(in.Body.Name)
		if name == "" {
			return nil, api.NewError(400, "validation", "name must be non-empty", "", nil)
		}
		if _, err := activeProjectByID(ctx, cfg.DB, in.ProjectID); err != nil {
			return nil, err
		}
		p, err := cfg.DB.RenameProject(ctx, in.ProjectID, name)
		if errors.Is(err, db.ErrNotFound) {
			return nil, api.NewError(404, "project_not_found", "project not found", "", nil)
		}
		if err != nil {
			return nil, api.NewError(500, "internal", err.Error(), "", nil)
		}
		aliases, err := cfg.DB.ProjectAliases(ctx, p.ID)
		if err != nil {
			return nil, api.NewError(500, "internal", err.Error(), "", nil)
		}
		out := &api.ShowProjectResponse{}
		out.Body.Project = dbProjectToOut(p)
		out.Body.Aliases = aliases
		return out, nil
	})
}

// resolveProject implements the strict resolution flow per spec §2.4. Order:
// .kata.toml binding wins; then alias lookup from the git root; else fail.
func resolveProject(ctx context.Context, store *db.DB, startPath string) (*api.ProjectResolveBody, error) {
	if startPath == "" {
		return nil, api.NewError(400, "validation", "start_path required", "", nil)
	}
	abs, err := filepath.Abs(startPath)
	if err != nil {
		return nil, api.NewError(400, "validation", err.Error(), "", nil)
	}
	disc, err := config.DiscoverPaths(abs)
	if err != nil {
		return nil, api.NewError(400, "validation", err.Error(), "", nil)
	}

	if body, ok, err := resolveByKataToml(ctx, store, disc); err != nil {
		return nil, err
	} else if ok {
		return body, nil
	}

	if disc.GitRoot != "" {
		return resolveByAlias(ctx, store, disc)
	}

	return nil, api.NewError(404, "project_not_initialized",
		"no .kata.toml ancestor and no git ancestor",
		`run "kata init" inside a workspace`, nil)
}

// resolveByKataToml returns (body, true, nil) when a .kata.toml binding
// exists at the workspace root and resolves to a project. Returns
// (nil, false, nil) when there is no .kata.toml. Surfaces parse errors.
func resolveByKataToml(ctx context.Context, store *db.DB, disc config.DiscoveredPaths) (*api.ProjectResolveBody, bool, error) {
	if disc.WorkspaceRoot == "" {
		return nil, false, nil
	}
	cfgFile, err := config.ReadProjectConfig(disc.WorkspaceRoot)
	if err != nil {
		if errors.Is(err, config.ErrProjectConfigMissing) {
			return nil, false, nil
		}
		return nil, false, api.NewError(400, "validation", err.Error(), "", nil)
	}
	project, err := projectByIdentityOrAlias(ctx, store, cfgFile.Project.Identity)
	if errors.Is(err, db.ErrNotFound) {
		return nil, false, api.NewError(404, "project_not_initialized",
			"project "+cfgFile.Project.Identity+" is bound by .kata.toml but not registered",
			`run "kata init" in this workspace`, nil)
	}
	if err != nil {
		return nil, false, api.NewError(500, "internal", err.Error(), "", nil)
	}
	alias, err := upsertAliasFor(ctx, store, project.ID, disc, false)
	if err != nil {
		return nil, false, err
	}
	return &api.ProjectResolveBody{
		Project:       dbProjectToOut(project),
		Alias:         alias,
		WorkspaceRoot: disc.WorkspaceRoot,
	}, true, nil
}

// resolveByAlias looks up the alias derived from the git root and returns
// the bound project. Caller guarantees disc.GitRoot != "".
func resolveByAlias(ctx context.Context, store *db.DB, disc config.DiscoveredPaths) (*api.ProjectResolveBody, error) {
	info, err := config.ComputeAliasIdentity(disc)
	if err != nil {
		return nil, api.NewError(400, "validation", err.Error(), "", nil)
	}
	alias, err := store.AliasByIdentity(ctx, info.Identity)
	if errors.Is(err, db.ErrNotFound) {
		return nil, api.NewError(404, "project_not_initialized",
			"no kata project is attached to this workspace",
			`run "kata init" in this workspace`, nil)
	}
	if err != nil {
		return nil, api.NewError(500, "internal", err.Error(), "", nil)
	}
	if err := store.TouchAlias(ctx, alias.ID, info.RootPath); err != nil && !errors.Is(err, db.ErrNotFound) {
		return nil, api.NewError(500, "internal", err.Error(), "", nil)
	}
	// Refetch the alias so the response carries the updated last_seen_at and
	// root_path, not the pre-touch snapshot.
	if refreshed, err := store.AliasByIdentity(ctx, info.Identity); err == nil {
		alias = refreshed
	}
	project, err := store.ProjectByID(ctx, alias.ProjectID)
	if err != nil {
		return nil, api.NewError(500, "internal", err.Error(), "", nil)
	}
	return &api.ProjectResolveBody{
		Project:       dbProjectToOut(project),
		Alias:         alias,
		WorkspaceRoot: info.RootPath,
	}, nil
}

// initProject implements `kata init` on the daemon side per spec §2.4.
func initProject(ctx context.Context, store *db.DB, req *api.InitProjectRequest) (*api.ProjectResolveBody, bool, error) {
	if req.Body.StartPath == "" {
		return nil, false, api.NewError(400, "validation", "start_path required", "", nil)
	}
	abs, err := filepath.Abs(req.Body.StartPath)
	if err != nil {
		return nil, false, api.NewError(400, "validation", err.Error(), "", nil)
	}
	disc, err := config.DiscoverPaths(abs)
	if err != nil {
		return nil, false, api.NewError(400, "validation", err.Error(), "", nil)
	}

	tomlCfg, err := readWorkspaceConfig(disc)
	if err != nil {
		return nil, false, err
	}

	identity, name, err := pickInitIdentity(req, disc, tomlCfg)
	if err != nil {
		return nil, false, err
	}
	if err := config.ValidateIdentity(identity); err != nil {
		return nil, false, api.NewError(400, "validation", err.Error(), "", nil)
	}

	// When --project was supplied outside any git/workspace ancestor, synthesize
	// a local alias rooted at the start path so upsertAliasFor has something to
	// attach. This is the explicit escape hatch documented in spec §2.4.
	if disc.GitRoot == "" && disc.WorkspaceRoot == "" {
		disc.WorkspaceRoot = abs
	}

	aliasInfo, err := config.ComputeAliasIdentity(disc)
	if err != nil {
		return nil, false, api.NewError(400, "validation", err.Error(), "", nil)
	}
	// Preflight alias conflict before mutating anything: without this, a fresh
	// project row would be created and then orphaned when alias attach fails.
	if err := preflightAliasConflict(ctx, store, aliasInfo, identity, req.Body.Reassign); err != nil {
		return nil, false, err
	}

	project, created, err := upsertProject(ctx, store, identity, name)
	if err != nil {
		return nil, false, err
	}

	alias, err := attachAlias(ctx, store, project.ID, aliasInfo, req.Body.Reassign)
	if err != nil {
		// Concurrent init can race past the preflight: a parallel request can
		// insert the alias between our preflight check and our attach. The
		// preflight catches the no-race case; here we clean up the orphan
		// project row so retries observe consistent state regardless of which
		// failure we hit.
		if created {
			_, _ = store.ExecContext(ctx, `DELETE FROM projects WHERE id = ?`, project.ID)
		}
		return nil, false, err
	}

	dest := writeDestination(disc, abs)
	if tomlCfg == nil || tomlCfg.Project.Identity != project.Identity || tomlCfg.Project.Name != project.Name {
		if err := config.WriteProjectConfig(dest, project.Identity, project.Name); err != nil {
			return nil, false, api.NewError(500, "internal", err.Error(), "", nil)
		}
	}

	return &api.ProjectResolveBody{
		Project:       dbProjectToOut(project),
		Alias:         alias,
		WorkspaceRoot: dest,
	}, created, nil
}

// readWorkspaceConfig reads .kata.toml only when a workspace root was actually
// discovered; passing "" to ReadProjectConfig would resolve to the daemon's
// cwd. Parse errors surface as 400; "missing" returns nil.
func readWorkspaceConfig(disc config.DiscoveredPaths) (*config.ProjectConfig, error) {
	if disc.WorkspaceRoot == "" {
		return nil, nil
	}
	cfgFile, err := config.ReadProjectConfig(disc.WorkspaceRoot)
	if err != nil {
		if errors.Is(err, config.ErrProjectConfigMissing) {
			return nil, nil
		}
		return nil, api.NewError(400, "validation", err.Error(), "", nil)
	}
	return cfgFile, nil
}

// pickInitIdentity decides the (identity, name) pair for kata init based on
// flags, .kata.toml content, and the discovered git workspace.
func pickInitIdentity(req *api.InitProjectRequest, disc config.DiscoveredPaths, tomlCfg *config.ProjectConfig) (string, string, error) {
	switch {
	case tomlCfg != nil && req.Body.ProjectIdentity != "" && tomlCfg.Project.Identity != req.Body.ProjectIdentity:
		if !req.Body.Replace {
			return "", "", api.NewError(http.StatusConflict, "project_binding_conflict",
				".kata.toml declares a different identity",
				"pass replace=true to overwrite", nil)
		}
		identity := req.Body.ProjectIdentity
		return identity, pickName(req.Body.Name, identity), nil
	case tomlCfg != nil:
		identity := tomlCfg.Project.Identity
		name := pickName(req.Body.Name, tomlCfg.Project.Name)
		if name == "" {
			name = pickName("", identity)
		}
		return identity, name, nil
	case req.Body.ProjectIdentity != "":
		identity := req.Body.ProjectIdentity
		return identity, pickName(req.Body.Name, identity), nil
	default:
		if disc.GitRoot == "" {
			return "", "", api.NewError(400, "validation",
				"cannot derive project identity outside a git workspace",
				`pass project_identity or run inside a git repo`, nil)
		}
		info, err := config.ComputeAliasIdentity(disc)
		if err != nil {
			return "", "", api.NewError(400, "validation", err.Error(), "", nil)
		}
		identity := info.Identity
		return identity, pickName(req.Body.Name, identity), nil
	}
}

// writeDestination chooses where to write .kata.toml: workspace root if any,
// else git root, else the absolute start path.
func writeDestination(disc config.DiscoveredPaths, abs string) string {
	if disc.WorkspaceRoot != "" {
		return disc.WorkspaceRoot
	}
	if disc.GitRoot != "" {
		return disc.GitRoot
	}
	return abs
}

// upsertProject returns the existing project (created=false) when one matches
// the identity, otherwise creates a new project (created=true). Archived
// (deleted_at != NULL) projects are NOT auto-resurrected — re-init against
// an archived identity returns project_archived (409) so the operator
// either picks a different identity or runs an explicit restore (when that
// flow ships). The identity stays UNIQUE in projects, so a silent
// re-create would otherwise hit a raw UNIQUE constraint error.
func upsertProject(ctx context.Context, store *db.DB, identity, name string) (db.Project, bool, error) {
	got, err := projectByIdentityOrAlias(ctx, store, identity)
	if err == nil {
		return got, false, nil
	}
	if !errors.Is(err, db.ErrNotFound) {
		return db.Project{}, false, api.NewError(500, "internal", err.Error(), "", nil)
	}
	if archived, archErr := store.ProjectByIdentityIncludingArchived(ctx, identity); archErr == nil && archived.DeletedAt != nil {
		return db.Project{}, false, api.NewError(409, "project_archived",
			"project with this identity was archived via `kata projects remove`",
			"restore the project (not yet supported) or pick a different identity",
			map[string]any{"identity": identity, "deleted_at": archived.DeletedAt})
	}
	created, err := store.CreateProject(ctx, identity, name)
	if err != nil {
		return db.Project{}, false, api.NewError(500, "internal", err.Error(), "", nil)
	}
	return created, true, nil
}

func projectByIdentityOrAlias(ctx context.Context, store *db.DB, identity string) (db.Project, error) {
	project, err := store.ProjectByIdentity(ctx, identity)
	if err == nil {
		return project, nil
	}
	if !errors.Is(err, db.ErrNotFound) {
		return db.Project{}, err
	}
	alias, err := store.AliasByIdentity(ctx, identity)
	if err != nil {
		return db.Project{}, err
	}
	return store.ProjectByID(ctx, alias.ProjectID)
}

// upsertAliasFor is the disc-flavored entry point used during resolve, where
// no preflight has happened. It computes the alias identity then delegates.
func upsertAliasFor(ctx context.Context, store *db.DB, projectID int64, disc config.DiscoveredPaths, reassign bool) (db.ProjectAlias, error) {
	info, err := config.ComputeAliasIdentity(disc)
	if err != nil {
		return db.ProjectAlias{}, api.NewError(400, "validation", err.Error(), "", nil)
	}
	return attachAlias(ctx, store, projectID, info, reassign)
}

// attachAlias attaches a pre-computed alias identity to projectID. If the
// alias is already attached to a *different* project, returns
// project_alias_conflict (409) unless reassign is true (in which case we move
// it). When called after preflightAliasConflict, the conflict branch is
// unreachable but kept for callers that haven't preflit.
func attachAlias(ctx context.Context, store *db.DB, projectID int64, info config.AliasInfo, reassign bool) (db.ProjectAlias, error) {
	existing, err := store.AliasByIdentity(ctx, info.Identity)
	if err == nil {
		return applyExistingAlias(ctx, store, projectID, info, existing, reassign)
	}
	if !errors.Is(err, db.ErrNotFound) {
		return db.ProjectAlias{}, api.NewError(500, "internal", err.Error(), "", nil)
	}
	a, err := store.AttachAlias(ctx, projectID, info.Identity, info.Kind, info.RootPath)
	if err != nil {
		// A UNIQUE constraint failure on alias_identity means a concurrent init
		// beat us to the insert. Refetch the now-existing alias and apply the
		// same existing-alias logic: idempotent if it points to this project,
		// conflict or reassign otherwise.
		if strings.Contains(err.Error(), "UNIQUE constraint failed: project_aliases.alias_identity") {
			raced, refetchErr := store.AliasByIdentity(ctx, info.Identity)
			if refetchErr != nil {
				return db.ProjectAlias{}, api.NewError(500, "internal",
					"alias UNIQUE conflict but refetch failed: "+refetchErr.Error(), "", nil)
			}
			return applyExistingAlias(ctx, store, projectID, info, raced, reassign)
		}
		return db.ProjectAlias{}, api.NewError(500, "internal", err.Error(), "", nil)
	}
	return a, nil
}

// applyExistingAlias handles the case where an alias row already exists.
// If the alias belongs to projectID it is touched (last_seen updated) and
// returned — enabling idempotent concurrent inits. Otherwise the alias is
// either moved (reassign=true) or a 409 is returned.
func applyExistingAlias(ctx context.Context, store *db.DB, projectID int64, info config.AliasInfo, existing db.ProjectAlias, reassign bool) (db.ProjectAlias, error) {
	if existing.ProjectID == projectID {
		if err := store.TouchAlias(ctx, existing.ID, info.RootPath); err != nil && !errors.Is(err, db.ErrNotFound) {
			return db.ProjectAlias{}, api.NewError(500, "internal", err.Error(), "", nil)
		}
		refreshed, _ := store.AliasByIdentity(ctx, info.Identity)
		return refreshed, nil
	}
	if !reassign {
		return db.ProjectAlias{}, api.NewError(http.StatusConflict, "project_alias_conflict",
			"alias already attached to a different project",
			"pass reassign=true to move it", map[string]any{
				"alias_identity":      info.Identity,
				"existing_project_id": existing.ProjectID,
			})
	}
	if _, execErr := store.ExecContext(ctx,
		`UPDATE project_aliases
		 SET project_id = ?, root_path = ?, last_seen_at = strftime('%Y-%m-%dT%H:%M:%fZ','now')
		 WHERE id = ?`,
		projectID, info.RootPath, existing.ID); execErr != nil {
		return db.ProjectAlias{}, api.NewError(500, "internal", execErr.Error(), "", nil)
	}
	refreshed, _ := store.AliasByIdentity(ctx, info.Identity)
	return refreshed, nil
}

// preflightAliasConflict returns 409 project_alias_conflict when an existing
// alias is bound to a different project than targetIdentity and reassign is
// false. Run before any project mutation so a doomed init does not leave an
// orphan project row.
func preflightAliasConflict(ctx context.Context, store *db.DB, info config.AliasInfo, targetIdentity string, reassign bool) error {
	if reassign {
		return nil
	}
	existing, err := store.AliasByIdentity(ctx, info.Identity)
	if errors.Is(err, db.ErrNotFound) {
		return nil
	}
	if err != nil {
		return api.NewError(500, "internal", err.Error(), "", nil)
	}
	existingProject, err := store.ProjectByID(ctx, existing.ProjectID)
	if err != nil {
		return api.NewError(500, "internal", err.Error(), "", nil)
	}
	if existingProject.Identity == targetIdentity {
		return nil
	}
	targetProject, err := projectByIdentityOrAlias(ctx, store, targetIdentity)
	if err == nil && targetProject.ID == existing.ProjectID {
		return nil
	}
	if err != nil && !errors.Is(err, db.ErrNotFound) {
		return api.NewError(500, "internal", err.Error(), "", nil)
	}
	return api.NewError(http.StatusConflict, "project_alias_conflict",
		"alias already attached to a different project",
		"pass reassign=true to move it", map[string]any{
			"alias_identity":      info.Identity,
			"existing_project_id": existing.ProjectID,
		})
}

// pickName returns explicit if non-empty, otherwise the last `/` or `:`-separated
// segment of identity (so "github.com/wesm/kata" → "kata").
func pickName(explicit, identity string) string {
	if explicit != "" {
		return explicit
	}
	for i := len(identity) - 1; i >= 0; i-- {
		if identity[i] == '/' || identity[i] == ':' {
			return identity[i+1:]
		}
	}
	return identity
}
