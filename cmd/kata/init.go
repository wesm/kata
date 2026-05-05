package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/wesm/kata/internal/config"
)

// initOptions holds the flags specific to `kata init`.
type initOptions struct {
	Project  string
	Name     string
	Replace  bool
	Reassign bool
}

// callInitOpts is the parameter bag passed to callInit.
type callInitOpts struct {
	Project  string
	Name     string
	Replace  bool
	Reassign bool
}

// cliError is a structured error that carries an exit code for main().
//
// Kind is the coarse classification used by the --json error envelope so
// scripts can branch on a stable taxonomy instead of grepping the
// human-readable message. Code is the daemon-supplied per-error tag
// (e.g. "issue_not_found"); empty when the error originated client-side.
// Message is the human-readable text. ExitCode is what main() exits with.
type cliError struct {
	Message  string
	Kind     errKind
	Code     string
	ExitCode int
}

func (e *cliError) Error() string { return e.Message }

// errKind is the coarse classification surfaced in the --json error
// envelope. Maps roughly onto the spec §4.7 exit codes but is named
// for the kind of failure rather than the numeric exit, so JSON
// consumers can branch on a stable identifier.
type errKind string

const (
	kindUsage         errKind = "usage"
	kindValidation    errKind = "validation"
	kindNotFound      errKind = "not_found"
	kindConflict      errKind = "conflict"
	kindConfirm       errKind = "confirm"
	kindDaemonUnavail errKind = "daemon_unavailable"
	kindInternal      errKind = "internal"
)

// kindForExit maps an exit code to the conventional errKind. Used when
// a non-cliError reaches main and we still want to emit a JSON
// envelope under --json.
func kindForExit(exit int) errKind {
	switch exit {
	case ExitUsage:
		return kindUsage
	case ExitValidation:
		return kindValidation
	case ExitNotFound:
		return kindNotFound
	case ExitConflict:
		return kindConflict
	case ExitConfirm:
		return kindConfirm
	case ExitDaemonUnavail:
		return kindDaemonUnavail
	}
	return kindInternal
}

// kindForStatus maps an HTTP status to the conventional errKind. The
// daemon-supplied error code is reserved for future per-code overrides.
func kindForStatus(status int) errKind {
	switch status {
	case http.StatusBadRequest:
		return kindValidation
	case http.StatusNotFound:
		return kindNotFound
	case http.StatusConflict:
		return kindConflict
	case http.StatusPreconditionFailed:
		return kindConfirm
	}
	return kindInternal
}

// newInitCmd returns the cobra.Command for `kata init`.
func newInitCmd() *cobra.Command {
	var opts initOptions

	cmd := &cobra.Command{
		Use:   "init",
		Short: "bind workspace to a project",
		Long: `Initialize kata in this workspace.

Writes a committed .kata.toml that binds the workspace to a project
identity. The daemon derives the identity from a git remote when one
is present; pass --project to override, or --name to set the
human-readable name.

Also adds .kata.local.toml to .gitignore so a developer's per-machine
overrides (e.g., a remote daemon URL via [server] url = "...") never
get committed.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			baseURL, err := ensureDaemon(cmd.Context())
			if err != nil {
				return fmt.Errorf("daemon: %w", err)
			}
			startPath, err := resolveStartPath(flags.Workspace)
			if err != nil {
				return fmt.Errorf("resolve workspace: %w", err)
			}
			out, err := callInit(cmd.Context(), baseURL, startPath, callInitOpts(opts))
			if err != nil {
				return err
			}
			_, err = fmt.Fprint(cmd.OutOrStdout(), out)
			return err
		},
	}

	cmd.Flags().StringVar(&opts.Project, "project", "", "project identity (default: derived from git remote)")
	cmd.Flags().StringVar(&opts.Name, "name", "", "human name for the project (default: last path segment)")
	cmd.Flags().BoolVar(&opts.Replace, "replace", false, "overwrite .kata.toml binding when it conflicts")
	cmd.Flags().BoolVar(&opts.Reassign, "reassign", false, "move an existing alias to this project")

	return cmd
}

// callInit dispatches `kata init` between the path-free flow (client
// derives identity locally, daemon registers project, client writes
// files) and the legacy path-based flow (daemon does everything).
//
// Path-free runs whenever the client can resolve identity locally —
// from .kata.toml, --project, or a discoverable git workspace. That's
// the contract that lets a daemon on another host serve init without
// filesystem access to the client workspace. The client falls back to
// the path-based request only when local derivation can't produce an
// identity, so the daemon (or its absence) emits today's validation
// error.
func callInit(ctx context.Context, baseURL, startPath string, opts callInitOpts) (string, error) {
	derived, err := localDerive(startPath, opts)
	switch {
	case err == nil:
		return runIdentityInit(ctx, baseURL, derived, opts)
	case errors.Is(err, config.ErrIdentityConflict):
		return "", &cliError{
			Message:  err.Error(),
			Kind:     kindConflict,
			Code:     "project_binding_conflict",
			ExitCode: ExitConflict,
		}
	case errors.Is(err, config.ErrNoIdentitySource):
		return runStartPathInit(ctx, baseURL, startPath, opts)
	default:
		return "", &cliError{
			Message:  err.Error(),
			Kind:     kindValidation,
			ExitCode: ExitValidation,
		}
	}
}

// localInit captures everything callInit needs to run the path-free
// flow: the chosen identity, the discovered roots (so .kata.toml lands
// at the workspace/git root rather than the cwd), the existing
// .kata.toml binding (so we can skip a redundant write), the absolute
// start path used as a final fallback for the write location, and
// optional alias metadata so the daemon can attach an alias without
// stat'ing the client's filesystem.
type localInit struct {
	Choice       config.IdentityChoice
	Disc         config.DiscoveredPaths
	ExistingToml *config.ProjectConfig
	StartPath    string
	Alias        *config.AliasInfo
}

// localDerive runs the same identity-selection logic the daemon uses
// in path-based init, but on the client's filesystem. Errors from
// PickInitIdentity (conflict, no-source) are returned unwrapped so
// callInit can dispatch on them. Alias metadata is computed
// best-effort: when the workspace can't yield an alias, the daemon
// still gets project_identity but no alias attach happens.
func localDerive(startPath string, opts callInitOpts) (localInit, error) {
	disc, err := config.DiscoverPaths(startPath)
	if err != nil {
		return localInit{}, err
	}
	var tomlCfg *config.ProjectConfig
	if disc.WorkspaceRoot != "" {
		cfg, err := config.ReadProjectConfig(disc.WorkspaceRoot)
		switch {
		case err == nil:
			tomlCfg = cfg
		case errors.Is(err, config.ErrProjectConfigMissing):
			// Discovered workspace root, but file vanished between the
			// walk and the read; treat as no-toml so we fall through
			// to the next identity source.
		default:
			return localInit{}, err
		}
	}
	choice, err := config.PickInitIdentity(disc, tomlCfg, opts.Project, opts.Name, opts.Replace)
	if err != nil {
		return localInit{}, err
	}
	alias, err := computeAliasInfo(disc, startPath)
	if err != nil {
		return localInit{}, err
	}
	return localInit{
		Choice:       choice,
		Disc:         disc,
		ExistingToml: tomlCfg,
		StartPath:    startPath,
		Alias:        alias,
	}, nil
}

// computeAliasInfo derives the alias metadata the daemon needs to
// attach an alias on the client's behalf. Mirrors the daemon-side
// path-based init: when the workspace has neither a git ancestor
// nor a .kata.toml ancestor, we synthesize a workspace root at the
// start path so ComputeAliasIdentity has something to anchor on
// (matching the path-based local:// fallback).
func computeAliasInfo(disc config.DiscoveredPaths, startPath string) (*config.AliasInfo, error) {
	if disc.GitRoot == "" && disc.WorkspaceRoot == "" {
		disc.WorkspaceRoot = startPath
	}
	info, err := config.ComputeAliasIdentity(disc)
	if err != nil {
		return nil, err
	}
	return &info, nil
}

// runIdentityInit POSTs the derived identity to the daemon, then
// writes .kata.toml and .gitignore on the client's filesystem. The
// daemon never sees the client's workspace path.
func runIdentityInit(ctx context.Context, baseURL string, in localInit, opts callInitOpts) (string, error) {
	if err := config.ValidateIdentity(in.Choice.Identity); err != nil {
		return "", &cliError{
			Message:  err.Error(),
			Kind:     kindValidation,
			ExitCode: ExitValidation,
		}
	}
	reqBody := map[string]any{
		"project_identity": in.Choice.Identity,
		"name":             in.Choice.Name,
	}
	if opts.Replace {
		reqBody["replace"] = true
	}
	if opts.Reassign {
		reqBody["reassign"] = true
	}
	if in.Alias != nil {
		reqBody["alias"] = map[string]any{
			"identity":  in.Alias.Identity,
			"kind":      in.Alias.Kind,
			"root_path": in.Alias.RootPath,
		}
	}
	bs, err := postProjects(ctx, baseURL, reqBody)
	if err != nil {
		return "", err
	}

	var resp struct {
		Project struct {
			Identity string `json:"identity"`
			Name     string `json:"name"`
		} `json:"project"`
		Created bool `json:"created"`
	}
	if err := json.Unmarshal(bs, &resp); err != nil {
		return "", fmt.Errorf("decode response: %w", err)
	}

	dest := config.WriteDestination(in.Disc, in.StartPath)
	if needsTomlWrite(in.ExistingToml, resp.Project.Identity, resp.Project.Name) {
		if err := config.WriteProjectConfig(dest, resp.Project.Identity, resp.Project.Name); err != nil {
			return "", fmt.Errorf("write .kata.toml: %w", err)
		}
	}
	if err := ensureGitignoreEntry(dest, ".kata.local.toml"); err != nil {
		fmt.Fprintf(os.Stderr, "kata: warning: could not update .gitignore: %v\n", err)
	}

	return formatInitOutput(bs, resp.Project.Identity, resp.Project.Name, resp.Created)
}

// runStartPathInit is the legacy fallback used when the client cannot
// derive identity locally (no .kata.toml, no --project, no git). It
// preserves today's behavior: the daemon walks its own filesystem,
// writes .kata.toml, and reports back the workspace root so the client
// places .gitignore beside it.
func runStartPathInit(ctx context.Context, baseURL, startPath string, opts callInitOpts) (string, error) {
	reqBody := map[string]any{"start_path": startPath}
	if opts.Project != "" {
		reqBody["project_identity"] = opts.Project
	}
	if opts.Name != "" {
		reqBody["name"] = opts.Name
	}
	if opts.Replace {
		reqBody["replace"] = true
	}
	if opts.Reassign {
		reqBody["reassign"] = true
	}
	bs, err := postProjects(ctx, baseURL, reqBody)
	if err != nil {
		return "", err
	}

	var resp struct {
		Project struct {
			Identity string `json:"identity"`
			Name     string `json:"name"`
		} `json:"project"`
		WorkspaceRoot string `json:"workspace_root,omitempty"`
		Created       bool   `json:"created"`
	}
	if err := json.Unmarshal(bs, &resp); err != nil {
		return "", fmt.Errorf("decode response: %w", err)
	}

	gitignoreDir := resp.WorkspaceRoot
	if gitignoreDir == "" {
		gitignoreDir = startPath
	}
	if err := ensureGitignoreEntry(gitignoreDir, ".kata.local.toml"); err != nil {
		fmt.Fprintf(os.Stderr, "kata: warning: could not update .gitignore: %v\n", err)
	}

	return formatInitOutput(bs, resp.Project.Identity, resp.Project.Name, resp.Created)
}

// postProjects POSTs the request and returns the raw response body on
// success. Non-2xx responses are decoded into a *cliError so callers
// can return them directly.
func postProjects(ctx context.Context, baseURL string, reqBody any) ([]byte, error) {
	client, err := httpClientFor(ctx, baseURL)
	if err != nil {
		return nil, fmt.Errorf("client: %w", err)
	}
	status, bs, err := httpDoJSON(ctx, client, http.MethodPost, baseURL+"/api/v1/projects", reqBody)
	if err != nil {
		return nil, fmt.Errorf("POST /api/v1/projects: %w", err)
	}
	if status >= 300 {
		return nil, apiErrFromBody(status, bs)
	}
	return bs, nil
}

// needsTomlWrite reports whether .kata.toml needs to be written: true
// when no toml exists yet or its identity/name don't match the chosen
// values. Mirrors the daemon-side guard so re-running init in the
// same workspace is a no-op rather than a redundant rewrite.
func needsTomlWrite(existing *config.ProjectConfig, identity, name string) bool {
	if existing == nil {
		return true
	}
	return existing.Project.Identity != identity || existing.Project.Name != name
}

// formatInitOutput renders the human-readable or JSON form of the init
// result, shared between the path-free and path-based flows.
func formatInitOutput(bs []byte, identity, name string, created bool) (string, error) {
	if flags.JSON {
		var buf bytes.Buffer
		if err := emitJSON(&buf, json.RawMessage(bs)); err != nil {
			return "", fmt.Errorf("emit json: %w", err)
		}
		return buf.String(), nil
	}
	action := "bound"
	if created {
		action = "created and bound"
	}
	return fmt.Sprintf("%s project %s (%s)\n", action, identity, name), nil
}

// resolveStartPath returns the absolute path to use as the daemon's
// start_path. Relative paths are resolved against the CLI's current working
// directory so the daemon (which may have a different cwd) doesn't end up
// binding or writing .kata.toml in the wrong place.
func resolveStartPath(workspace string) (string, error) {
	if workspace == "" {
		return os.Getwd()
	}
	return filepath.Abs(workspace)
}

// apiErrFromBody decodes a daemon ErrorEnvelope and returns a *cliError with
// the appropriate exit code. Falls back to a raw-body error when the envelope
// can't be decoded so the caller still has debugging context.
func apiErrFromBody(status int, bs []byte) *cliError {
	var env struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(bs, &env); err != nil {
		return &cliError{
			Message:  errors.New(string(bs)).Error(),
			Code:     "",
			Kind:     kindForStatus(status),
			ExitCode: mapStatusToExit(status, ""),
		}
	}
	return &cliError{
		Message:  env.Error.Message,
		Code:     env.Error.Code,
		Kind:     kindForStatus(status),
		ExitCode: mapStatusToExit(status, env.Error.Code),
	}
}

// mapStatusToExit maps an HTTP status to a CLI exit code. The code parameter
// is reserved for future per-code overrides (e.g. distinguishing
// project_not_found from project_not_initialized within 404s).
func mapStatusToExit(status int, _ string) int {
	switch status {
	case http.StatusBadRequest:
		return ExitValidation
	case http.StatusNotFound:
		return ExitNotFound
	case http.StatusConflict:
		return ExitConflict
	case http.StatusPreconditionFailed:
		return ExitConfirm
	default:
		return ExitInternal
	}
}

// ensureGitignoreEntry appends a single line to <dir>/.gitignore if
// the entry is not already present. Creates the file if absent.
// Idempotent: re-running on a file that already lists the entry is a
// no-op.
func ensureGitignoreEntry(dir, entry string) error {
	path := filepath.Join(dir, ".gitignore")
	existing, err := os.ReadFile(path) //nolint:gosec
	switch {
	case err == nil:
		// Walk lines so we don't false-match a substring inside a longer
		// pattern (e.g. ".kata.local.toml.bak").
		for _, line := range strings.Split(string(existing), "\n") {
			if strings.TrimSpace(line) == entry {
				return nil
			}
		}
		// Preserve trailing-newline convention: if the file ends without
		// a newline, add one before appending so we don't merge our line
		// into theirs.
		var prefix string
		if len(existing) > 0 && existing[len(existing)-1] != '\n' {
			prefix = "\n"
		}
		f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644) //nolint:gosec // .gitignore is world-readable by convention; mode is unused by O_APPEND on existing files but golangci-lint flags it
		if err != nil {
			return err
		}
		defer func() { _ = f.Close() }()
		if _, err := f.WriteString(prefix + entry + "\n"); err != nil {
			return err
		}
		return nil
	case errors.Is(err, os.ErrNotExist):
		return os.WriteFile(path, []byte(entry+"\n"), 0o644) //nolint:gosec
	default:
		return err
	}
}
