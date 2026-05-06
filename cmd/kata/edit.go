package main

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
)

func newEditCmd() *cobra.Command {
	var (
		title    string
		body     string
		owner    string
		priority string
	)
	cmd := &cobra.Command{
		Use:   "edit <issue-ref>",
		Short: "edit issue title/body/owner/priority",
		Args:  cobra.ExactArgs(1),
	}
	cmd.Flags().StringVar(&title, "title", "", "new title")
	cmd.Flags().StringVar(&body, "body", "", "new body")
	cmd.Flags().StringVar(&owner, "owner", "", "new owner")
	cmd.Flags().StringVar(&priority, "priority", "",
		"new priority (0..4; 0 = highest). Pass '-' to clear.")

	// RunE is set after flag registration so we can reference cmd.Flags().Changed.
	// This lets --body "" explicitly clear the body rather than being ignored.
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		payload := map[string]any{}
		if cmd.Flags().Changed("title") {
			// Mirror create's title-non-empty gate so we don't forward
			// a blank/whitespace-only title to the daemon and surface a
			// raw SQLite CHECK-constraint error to the user
			// (hammer-test finding #4).
			if strings.TrimSpace(title) == "" {
				return &cliError{
					Message:  "--title must not be empty (omit the flag to keep the existing title)",
					Kind:     kindValidation,
					ExitCode: ExitValidation,
				}
			}
			payload["title"] = title
		}
		if cmd.Flags().Changed("body") {
			payload["body"] = body
		}
		if cmd.Flags().Changed("owner") {
			payload["owner"] = owner
		}

		var priorityChange *int64
		priorityClear := false
		if cmd.Flags().Changed("priority") {
			v, cleared, err := parseEditPriority(priority)
			if err != nil {
				return err
			}
			priorityChange = v
			priorityClear = cleared
		}

		hasPriority := priorityChange != nil || priorityClear
		if len(payload) == 0 && !hasPriority {
			return &cliError{
				Message:  "pass at least one of --title, --body, --owner, --priority",
				Kind:     kindValidation,
				ExitCode: ExitValidation,
			}
		}
		actor, _ := resolveActor(flags.As, nil)
		payload["actor"] = actor

		ctx, baseURL, pid, issue, err := resolveIssueRefForCommand(cmd, args[0])
		if err != nil {
			return err
		}
		client, err := httpClientFor(ctx, baseURL)
		if err != nil {
			return err
		}
		// PATCH for title/body/owner first (when any are set). PATCH and the
		// priority action are independent endpoints with independent events,
		// so a combined `kata edit --title X --priority 1` runs as two HTTP
		// calls. The priority call's response is what we surface to the user
		// when both are present; on PATCH-only or priority-only invocations
		// the corresponding single response is printed.
		var lastBody []byte
		if len(payload) > 1 { // > 1 because actor is always present
			status, bs, err := httpDoJSON(ctx, client, http.MethodPatch,
				fmt.Sprintf("%s/api/v1/projects/%d/issues/%d", baseURL, pid, issue.Number),
				payload)
			if err != nil {
				return err
			}
			if status >= 400 {
				return apiErrFromBody(status, bs)
			}
			lastBody = bs
		}
		if hasPriority {
			body := map[string]any{"actor": actor}
			if priorityChange != nil {
				body["priority"] = *priorityChange
			}
			status, bs, err := httpDoJSON(ctx, client, http.MethodPost,
				fmt.Sprintf("%s/api/v1/projects/%d/issues/%d/actions/priority",
					baseURL, pid, issue.Number),
				body)
			if err != nil {
				return err
			}
			if status >= 400 {
				return apiErrFromBody(status, bs)
			}
			lastBody = bs
		}
		return printMutation(cmd, lastBody)
	}
	return cmd
}

// parseEditPriority interprets the --priority value: "-" clears, an integer
// 0..4 sets. Returns (value, cleared, err).
func parseEditPriority(raw string) (*int64, bool, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "-" {
		return nil, true, nil
	}
	n, err := strconv.ParseInt(trimmed, 10, 64)
	if err != nil {
		return nil, false, &cliError{
			Message:  "--priority must be an integer 0..4 or '-' to clear",
			Kind:     kindValidation,
			ExitCode: ExitValidation,
		}
	}
	if n < 0 || n > 4 {
		return nil, false, &cliError{
			Message:  "--priority must be between 0 and 4",
			Kind:     kindValidation,
			ExitCode: ExitValidation,
		}
	}
	return &n, false, nil
}
