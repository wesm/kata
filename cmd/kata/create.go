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
		labels   []string
		parent   int64
		blocks   []int64
		owner    string
		priority int
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
	cmd.Flags().Int64Var(&parent, "parent", 0, "initial parent link target (issue number)")
	cmd.Flags().Int64SliceVar(&blocks, "blocks", nil, "initial blocks link target (issue number, repeatable)")
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
		var links []map[string]any
		if cmd.Flags().Changed("parent") {
			links = append(links, map[string]any{"type": "parent", "to_number": parent})
		}
		for _, b := range blocks {
			links = append(links, map[string]any{"type": "blocks", "to_number": b})
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
		return printMutation(cmd, bs)
	}
	return cmd
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

// resolveProjectID resolves the project ID for a given workspace start
// path by calling POST /api/v1/projects/resolve on the daemon.
//
// When the workspace has a readable .kata.toml at startPath or any
// ancestor directory, the project identity is sent and the daemon
// skips its filesystem walk. This is what lets a client on host B
// resolve a project registered on host A's daemon — the daemon cannot
// stat the client's startPath, but it can look up the project by its
// committed identity. Walking upward also matches user expectations:
// `kata create` from a subdirectory of an initialized workspace
// behaves the same as from the workspace root.
//
// When no .kata.toml is found (the usual case mid-init), the
// start_path fallback engages and the daemon walks its own filesystem
// as before. Local-mode behavior is unchanged.
func resolveProjectID(ctx context.Context, baseURL, startPath string) (int64, error) {
	client, err := httpClientFor(ctx, baseURL)
	if err != nil {
		return 0, err
	}
	body := map[string]any{}
	cfg, _, err := config.FindProjectConfig(startPath)
	switch {
	case err == nil && cfg.Project.Identity != "":
		body["project_identity"] = cfg.Project.Identity
	case err == nil, errors.Is(err, config.ErrProjectConfigMissing):
		// Truly missing (or present but with empty identity): fall
		// back to start_path so the daemon walks its own filesystem
		// in local mode.
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
func printMutation(cmd *cobra.Command, bs []byte) error {
	var b struct {
		Issue struct {
			Number int64  `json:"number"`
			Title  string `json:"title"`
			Status string `json:"status"`
		} `json:"issue"`
		Changed bool `json:"changed"`
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
	_, err := fmt.Fprintf(cmd.OutOrStdout(), "#%d %s [%s]\n",
		b.Issue.Number, textsafe.Line(b.Issue.Title), b.Issue.Status)
	return err
}
