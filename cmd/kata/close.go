package main

import (
	"fmt"
	"net/http"
	"net/url"

	"github.com/spf13/cobra"
)

func newCloseCmd() *cobra.Command {
	var reason string
	cmd := &cobra.Command{
		Use:   "close <issue-ref>",
		Short: "close an issue",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAction(cmd, args[0], "close", map[string]any{"reason": reason})
		},
	}
	cmd.Flags().StringVar(&reason, "reason", "done", "done|wontfix|duplicate")
	return cmd
}

// runAction is shared by close and reopen. It resolves the issue reference,
// resolves the project, merges extra fields into the request body, and calls
// the daemon action endpoint.
func runAction(cmd *cobra.Command, raw, action string, extra map[string]any) error {
	ctx, baseURL, pid, issue, err := resolveIssueRefForCommand(cmd, raw)
	if err != nil {
		return err
	}
	actor, _ := resolveActor(flags.As, nil)
	body := map[string]any{"actor": actor}
	for k, v := range extra {
		body[k] = v
	}
	client, err := httpClientFor(ctx, baseURL)
	if err != nil {
		return err
	}
	status, bs, err := httpDoJSON(ctx, client, http.MethodPost,
		fmt.Sprintf("%s/api/v1/projects/%d/issues/%s/actions/%s", baseURL, pid, url.PathEscape(issue.RefForAPI), action),
		body)
	if err != nil {
		return err
	}
	if status >= 400 {
		return apiErrFromBody(status, bs)
	}
	return printMutation(cmd, bs)
}
