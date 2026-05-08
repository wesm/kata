package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/spf13/cobra"
	"github.com/wesm/kata/internal/config"
	"github.com/wesm/kata/internal/textsafe"
)

func newCreateCmd() *cobra.Command {
	var src BodySources
	var (
		labels         []string
		parentRefSlice []string
		blocks         []string
		blockedBy      []string
		related        []string
		owner          string
		priority       int
	)
	cmd := &cobra.Command{
		Use:   "create <title>",
		Short: "create a new issue",
		Args:  cobra.ExactArgs(1),
	}
	var idempotencyKey string
	var forceNew bool
	cmd.Flags().StringVar(&src.Body, "body", "", "issue body")
	cmd.Flags().StringVar(&src.File, "body-file", "", "read body from file")
	cmd.Flags().BoolVar(&src.Stdin, "body-stdin", false, "read body from stdin")
	cmd.Flags().StringSliceVar(&labels, "label", nil, "initial label (repeatable)")
	// --parent is at-most-one. We accept it as a slice so duplicate flags
	// produce a parseable error (singletonRefValue) instead of cobra's
	// silent last-wins on StringVar; collapseSingletonRef rejects multiple
	// distinct values explicitly.
	cmd.Flags().Var(newRefSliceValue(&parentRefSlice), "parent",
		"initial parent (must finish before this issue starts; ≤1; ref: #N, N, UID, or 8+ char prefix)")
	cmd.Flags().Var(newRefSliceValue(&blocks), "blocks",
		"this issue blocks <ref> (this must finish before <ref> can; repeatable)")
	cmd.Flags().Var(newRefSliceValue(&blockedBy), "blocked-by",
		"this issue is blocked by <ref> (<ref> must finish before this; repeatable)")
	cmd.Flags().Var(newRefSliceValue(&related), "related",
		"this issue is related to <ref> (symmetric, no ordering; repeatable)")
	cmd.Flags().StringVar(&owner, "owner", "", "initial owner")
	cmd.Flags().IntVar(&priority, "priority", 0, "initial priority (0..4; 0 = highest)")
	cmd.Flags().StringVar(&idempotencyKey, "idempotency-key", "", "send Idempotency-Key header for safe retry")
	cmd.Flags().BoolVar(&forceNew, "force-new", false, "bypass look-alike soft-block (idempotency still wins)")

	// RunE is set after flag registration so we can reference cmd.Flags().Changed.
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		src.BodySet = cmd.Flags().Changed("body")
		src.FileSet = cmd.Flags().Changed("body-file")

		title := args[0]
		if strings.TrimSpace(title) == "" {
			return &cliError{Message: "title must not be empty", Kind: kindValidation, ExitCode: ExitValidation}
		}
		if err := validateCreateLabels(labels); err != nil {
			return err
		}
		if cmd.Flags().Changed("priority") && (priority < 0 || priority > 4) {
			return &cliError{
				Message:  "--priority must be between 0 and 4",
				Kind:     kindValidation,
				ExitCode: ExitValidation,
			}
		}

		ctx := cmd.Context()
		start, err := resolveStartPath(flags.Workspace)
		if err != nil {
			return err
		}
		baseURL, err := ensureDaemon(ctx)
		if err != nil {
			return err
		}
		projectID, err := resolveProjectID(ctx, baseURL, start)
		if err != nil {
			return err
		}
		body, err := resolveBody(src, cmd.InOrStdin())
		if err != nil {
			code := ExitValidation
			if strings.HasPrefix(err.Error(), "must pass exactly one of") {
				code = ExitUsage
			}
			return &cliError{Message: err.Error(), Kind: kindForExit(code), ExitCode: code}
		}
		actor, _ := resolveActor(flags.As, nil)
		client, err := httpClientFor(ctx, baseURL)
		if err != nil {
			return err
		}

		req := map[string]any{"actor": actor, "title": title, "body": body}
		if cmd.Flags().Changed("owner") {
			req["owner"] = owner
		}
		if cmd.Flags().Changed("priority") {
			req["priority"] = priority
		}
		if len(labels) > 0 {
			req["labels"] = labels
		}
		// Resolve every link-target ref to its issue number before building
		// the wire payload. Refs accept the same forms as `kata show`:
		// numeric (#N or N), full UID, or 8+ char UID prefix. Numeric refs
		// resolve client-side; UID refs roundtrip to the daemon.
		var links []map[string]any
		var parentNum int64
		if cmd.Flags().Changed("parent") {
			n, err := resolveSingletonRefToNumber(ctx, baseURL, projectID, parentRefSlice, "--parent", false)
			if err != nil {
				return err
			}
			parentNum = n
			links = append(links, map[string]any{"type": "parent", "to_number": n})
		}
		blocksNums, err := resolveRefSliceToNumbers(ctx, baseURL, projectID, blocks, "--blocks")
		if err != nil {
			return err
		}
		for _, n := range blocksNums {
			links = append(links, map[string]any{"type": "blocks", "to_number": n})
		}
		blockedByNums, err := resolveRefSliceToNumbers(ctx, baseURL, projectID, blockedBy, "--blocked-by")
		if err != nil {
			return err
		}
		for _, n := range blockedByNums {
			links = append(links, map[string]any{"type": "blocks", "to_number": n, "incoming": true})
		}
		relatedNums, err := resolveRefSliceToNumbers(ctx, baseURL, projectID, related, "--related")
		if err != nil {
			return err
		}
		for _, n := range relatedNums {
			links = append(links, map[string]any{"type": "related", "to_number": n})
		}
		if len(links) > 0 {
			req["links"] = links
		}
		if forceNew {
			req["force_new"] = true
		}
		headers := map[string]string{}
		if idempotencyKey != "" {
			headers["Idempotency-Key"] = idempotencyKey
		}

		status, bs, err := httpDoJSONWithHeader(ctx, client, http.MethodPost,
			fmt.Sprintf("%s/api/v1/projects/%d/issues", baseURL, projectID),
			headers, req)
		if err != nil {
			return err
		}
		if status >= 400 {
			return apiErrFromBody(status, bs)
		}
		// The /issues create response doesn't carry a `changes` block (it
		// lives on the PATCH path). Synthesize one from the resolved
		// initial-link numbers so human-mode `kata create` echoes a
		// `links: +parent #N, +blocks #M, ...` summary, mirroring the
		// per-link diff a `kata edit` PATCH produces. Reverse direction
		// (`--blocked-by` adding to `blocked_by_added` rather than
		// `blocks_added`) preserves the user's POV.
		applied := initialLinksAsChanges(parentNum, blocksNums, blockedByNums, relatedNums)
		return printMutationWithApplied(cmd, bs, applied)
	}
	return cmd
}

// initialLinksAsChanges builds a synthetic mutationChanges from the
// resolved initial-link numbers so create's human output can mirror
// edit's `links: +parent #N` diff format. parentNum is 0 when no
// `--parent` was passed; the other slices may be nil/empty. Returns
// nil when no link flags were used.
//
// Dedupes each slice before storing so a request like
// `--blocks 2 --blocks 2` summarizes as a single `+blocks #2`,
// matching the daemon's CreateIssue path which dedupes initial links
// before persisting. Without this, the human-mode summary would
// over-report what actually landed.
func initialLinksAsChanges(parentNum int64, blocks, blockedBy, related []int64) *mutationChanges {
	if parentNum == 0 && len(blocks) == 0 && len(blockedBy) == 0 && len(related) == 0 {
		return nil
	}
	c := &mutationChanges{}
	if parentNum != 0 {
		n := parentNum
		c.ParentSet = &n
	}
	c.BlocksAdded = dedupeInt64s(blocks)
	c.BlockedByAdded = dedupeInt64s(blockedBy)
	c.RelatedAdded = dedupeInt64s(related)
	return c
}

// dedupeInt64s returns a copy of in with duplicate entries dropped,
// preserving first-occurrence order. Returns nil for empty input so
// the caller's omitempty JSON tags do the right thing.
func dedupeInt64s(in []int64) []int64 {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[int64]struct{}, len(in))
	out := make([]int64, 0, len(in))
	for _, n := range in {
		if _, ok := seen[n]; ok {
			continue
		}
		seen[n] = struct{}{}
		out = append(out, n)
	}
	return out
}

func validateCreateLabels(labels []string) error {
	// Reject blank labels client-side so a typo or accidental "--label ''"
	// doesn't silently succeed-with-no-label (hammer-test finding #8). This
	// must run before daemon resolution so validation failures are local.
	for _, l := range labels {
		if strings.TrimSpace(l) == "" {
			return &cliError{
				Message:  "--label must not be empty",
				Kind:     kindValidation,
				ExitCode: ExitValidation,
			}
		}
	}
	return nil
}

// resolveProjectID resolves the project ID by calling
// POST /api/v1/projects/resolve on the daemon.
//
// A global --project value wins and is sent as the project name, allowing
// project-scoped commands to target any project from any directory. Otherwise,
// start_path lets the daemon resolve by alias first and repair stale .kata.toml
// bindings after project rename/merge. A readable local .kata.toml is still
// parsed before the daemon call so malformed config fails with a direct fix-it
// error instead of being hidden behind daemon-side path resolution.
func resolveProjectID(ctx context.Context, baseURL, startPath string) (int64, error) {
	client, err := httpClientFor(ctx, baseURL)
	if err != nil {
		return 0, err
	}
	body := map[string]any{}
	if project := strings.TrimSpace(flags.Project); project != "" {
		body["name"] = project
	} else {
		_, _, err := config.FindProjectConfig(startPath)
		switch {
		case err == nil, errors.Is(err, config.ErrProjectConfigMissing):
			body["start_path"] = startPath
		default:
			// Found a .kata.toml but couldn't read or parse it. Surface
			// that loud and clear instead of silently asking the daemon
			// to resolve a path it may not even be able to stat (remote
			// mode), which would mask the actual fix-it error.
			return 0, &cliError{
				Message:  "read .kata.toml: " + err.Error(),
				Kind:     kindValidation,
				ExitCode: ExitValidation,
			}
		}
	}
	status, bs, err := httpDoJSON(ctx, client, http.MethodPost,
		baseURL+"/api/v1/projects/resolve", body)
	if err != nil {
		return 0, err
	}
	if status >= 400 {
		return 0, apiErrFromBody(status, bs)
	}
	var b struct {
		Project struct{ ID int64 } `json:"project"`
	}
	if err := json.Unmarshal(bs, &b); err != nil {
		return 0, err
	}
	return b.Project.ID, nil
}

// printMutation formats a mutation response (issue create/edit/close/reopen)
// according to the active output mode: JSON envelope, quiet (issue number
// only), or human-readable one-liner.
//
// In human mode, when the response carries a `changes` block (kata edit
// with link/priority mutations), append a per-line summary so the caller
// can see what landed without parsing JSON. A request whose `changed`
// flag is false renders an explicit "(no changes applied)" tail so a
// no-op edit doesn't look identical to a successful one.
func printMutation(cmd *cobra.Command, bs []byte) error {
	return printMutationWithApplied(cmd, bs, nil)
}

// printMutationWithApplied is printMutation with an optional fallback
// `changes` block. When the response itself doesn't carry one (the
// /issues create path doesn't), pass `applied` describing the
// requested mutations so human-mode output still echoes a `links: ...`
// summary. The wire payload (JSON-mode output) is left unchanged —
// the `applied` argument only seeds the human-mode renderer.
func printMutationWithApplied(cmd *cobra.Command, bs []byte, applied *mutationChanges) error {
	var b struct {
		Issue struct {
			Number int64  `json:"number"`
			Title  string `json:"title"`
			Status string `json:"status"`
		} `json:"issue"`
		Changed bool             `json:"changed"`
		Changes *mutationChanges `json:"changes,omitempty"`
	}
	if err := json.Unmarshal(bs, &b); err != nil {
		return err
	}
	if flags.JSON {
		var buf bytes.Buffer
		if err := emitJSON(&buf, json.RawMessage(bs)); err != nil {
			return err
		}
		_, err := fmt.Fprint(cmd.OutOrStdout(), buf.String())
		return err
	}
	if flags.Quiet {
		_, err := fmt.Fprintln(cmd.OutOrStdout(), b.Issue.Number)
		return err
	}
	if _, err := fmt.Fprintf(cmd.OutOrStdout(), "#%d %s [%s]",
		b.Issue.Number, textsafe.Line(b.Issue.Title), b.Issue.Status); err != nil {
		return err
	}
	changes := b.Changes
	changed := b.Changed
	if changes == nil && applied != nil && b.Changed {
		// Create-path fallback: the wire response lacks a changes block,
		// but the caller passed the resolved initial-link state. Only
		// fall back when the daemon also flagged the response as
		// changed=true — an idempotent-reuse response (changed=false,
		// returned when an Idempotency-Key matched a prior create)
		// must not synthesize a "links: ..." summary because nothing
		// was applied THIS call. The original create's links surfaced
		// in its OWN response.
		changes = applied
	}
	if summary := summarizeChanges(changes, changed); summary != "" {
		if _, err := fmt.Fprint(cmd.OutOrStdout(), " "+summary); err != nil {
			return err
		}
	}
	_, err := fmt.Fprintln(cmd.OutOrStdout())
	return err
}

// mutationChanges mirrors the wire `changes` block produced by `kata edit`
// for link mutations. Decoded so the human-mode renderer can summarize
// what happened without round-tripping the full payload.
type mutationChanges struct {
	ParentSet        *int64  `json:"parent_set,omitempty"`
	ParentRemoved    *int64  `json:"parent_removed,omitempty"`
	BlocksAdded      []int64 `json:"blocks_added,omitempty"`
	BlocksRemoved    []int64 `json:"blocks_removed,omitempty"`
	BlockedByAdded   []int64 `json:"blocked_by_added,omitempty"`
	BlockedByRemoved []int64 `json:"blocked_by_removed,omitempty"`
	RelatedAdded     []int64 `json:"related_added,omitempty"`
	RelatedRemoved   []int64 `json:"related_removed,omitempty"`
}

// summarizeChanges renders a human-mode tail for the printMutation
// one-liner. Returns "" when this response has no link information to
// surface (e.g. plain title-edit, plain create, comment, close).
func summarizeChanges(c *mutationChanges, changed bool) string {
	if c == nil {
		return ""
	}
	parts := make([]string, 0, 8)
	if c.ParentSet != nil && c.ParentRemoved != nil {
		parts = append(parts, fmt.Sprintf("parent #%d→#%d", *c.ParentRemoved, *c.ParentSet))
	} else if c.ParentSet != nil {
		parts = append(parts, fmt.Sprintf("+parent #%d", *c.ParentSet))
	} else if c.ParentRemoved != nil {
		parts = append(parts, fmt.Sprintf("-parent #%d", *c.ParentRemoved))
	}
	for _, n := range c.BlocksAdded {
		parts = append(parts, fmt.Sprintf("+blocks #%d", n))
	}
	for _, n := range c.BlocksRemoved {
		parts = append(parts, fmt.Sprintf("-blocks #%d", n))
	}
	for _, n := range c.BlockedByAdded {
		parts = append(parts, fmt.Sprintf("+blocked_by #%d", n))
	}
	for _, n := range c.BlockedByRemoved {
		parts = append(parts, fmt.Sprintf("-blocked_by #%d", n))
	}
	for _, n := range c.RelatedAdded {
		parts = append(parts, fmt.Sprintf("+related #%d", n))
	}
	for _, n := range c.RelatedRemoved {
		parts = append(parts, fmt.Sprintf("-related #%d", n))
	}
	if len(parts) == 0 {
		// `changes` was present but every entry was an idempotent no-op.
		// Make the no-op explicit so callers don't confuse it with a
		// successful mutation.
		if !changed {
			return "(no changes applied)"
		}
		return ""
	}
	return "links: " + strings.Join(parts, ", ")
}
