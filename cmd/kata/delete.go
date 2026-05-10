package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

// newDeleteCmd returns the cobra.Command for `kata delete`.
//
// Spec §3.5 / §4.4: deletion is gated by --force and an X-Kata-Confirm header
// whose value is the exact string "DELETE <qualified>". The CLI accepts the
// header value via --confirm (noninteractive) or builds it from a TTY prompt
// where the user types the short_id.
func newDeleteCmd() *cobra.Command {
	var force bool
	var confirm string
	cmd := &cobra.Command{
		Use:   "delete <issue-ref>",
		Short: "soft-delete an issue (reversible via kata restore)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !force {
				return &cliError{
					Message: "deletion requires --force; use `kata restore` to undo if you change your mind",
					Code:    "validation",
					Kind:    kindValidation, ExitCode: ExitValidation,
				}
			}
			ctx, baseURL, pid, issue, err := resolveIssueRefForCommand(cmd, args[0])
			if err != nil {
				return err
			}
			// Resolve the issue's display short_id so the X-Kata-Confirm
			// header carries the daemon-expected "<project>#<short_id>"
			// form even when the user passed a ULID.
			issue, err = hydrateRefWithQualified(ctx, baseURL, pid, issue, false)
			if err != nil {
				return err
			}
			expected := fmt.Sprintf("DELETE %s", issue.QualifiedID)
			confirm, err = resolveConfirm(cmd, confirm, expected,
				fmt.Sprintf("Type %q to confirm: ", expected), confirmPromptFull)
			if err != nil {
				return err
			}
			return runDestructive(cmd, issue.RefForAPI, issue.ShortID, "delete", confirm, nil)
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "required to perform the soft delete")
	cmd.Flags().StringVar(&confirm, "confirm", "", `exact confirmation string ("DELETE <short_id>")`)
	return cmd
}

// confirmMatcher decides whether the user's TTY input satisfies the prompt.
// Both delete and purge now use confirmPromptFull (the full
// "VERB <project>#<short_id>" string) so the X-Kata-Confirm header — which
// must match that exact form — works whether the user typed in interactively
// or passed it noninteractively via --confirm.
type confirmMatcher func(line, expected string) bool

// confirmPromptFull accepts only the exact expected string.
func confirmPromptFull(line, expected string) bool {
	return line == expected
}

// resolveConfirm returns the X-Kata-Confirm value the daemon expects:
//   - if --confirm was passed, use it as-is (the daemon validates exact match);
//   - otherwise, if stdin is a TTY, prompt with `prompt` and accept input that
//     `match` says satisfies the verb's friction rule;
//   - otherwise, exit 6 confirm_required.
func resolveConfirm(cmd *cobra.Command, flagVal, expected, prompt string,
	match confirmMatcher) (string, error) {
	if flagVal != "" {
		return flagVal, nil
	}
	if !isTTY(os.Stdin) {
		return "", &cliError{
			Message: "no TTY: pass --confirm \"" + expected + "\" to proceed noninteractively",
			Code:    "confirm_required",
			Kind:    kindConfirm, ExitCode: ExitConfirm,
		}
	}
	if _, err := fmt.Fprint(cmd.ErrOrStderr(), prompt); err != nil {
		return "", err
	}
	r := bufio.NewReader(cmd.InOrStdin())
	//nolint:errcheck // ReadString returns the data read up to EOF; an EOF here
	// just means the user closed stdin, which we treat as an empty mismatch.
	line, _ := r.ReadString('\n')
	line = strings.TrimSpace(line)
	if !match(line, expected) {
		return "", &cliError{
			Message: "confirmation input did not match",
			Code:    "confirm_mismatch",
			Kind:    kindConfirm, ExitCode: ExitConfirm,
		}
	}
	return expected, nil
}

// runDestructive POSTs to /actions/{verb} with the X-Kata-Confirm header.
// pathRef is the literal {ref} URL path component; displayRef is the
// short_id rendered in human-mode success lines (may differ from pathRef
// when the user passed a ULID).
func runDestructive(cmd *cobra.Command, pathRef, displayRef, verb, confirm string,
	extraBody map[string]any) error {
	ctx := cmd.Context()
	start, err := resolveStartPath(flags.Workspace)
	if err != nil {
		return err
	}
	baseURL, err := ensureDaemon(ctx)
	if err != nil {
		return err
	}
	pid, err := resolveProjectID(ctx, baseURL, start)
	if err != nil {
		return err
	}
	actor, _ := resolveActor(flags.As, nil)
	// Build body from extraBody first so a future caller can't overwrite the
	// resolved actor with a stray map key.
	body := map[string]any{}
	for k, v := range extraBody {
		body[k] = v
	}
	body["actor"] = actor
	client, err := httpClientFor(ctx, baseURL)
	if err != nil {
		return err
	}
	postURL := fmt.Sprintf("%s/api/v1/projects/%d/issues/%s/actions/%s", baseURL, pid, url.PathEscape(pathRef), verb)
	status, bs, err := httpDoJSONWithHeader(ctx, client, http.MethodPost, postURL,
		map[string]string{"X-Kata-Confirm": confirm}, body)
	if err != nil {
		return err
	}
	if status >= 400 {
		return apiErrFromBody(status, bs)
	}
	return printDestructive(cmd, displayRef, verb, bs)
}

// printDestructive renders the destructive-action response in the active
// output mode (JSON envelope, quiet, or one-line human).
func printDestructive(cmd *cobra.Command, ref, verb string, bs []byte) error {
	if flags.JSON {
		var buf bytes.Buffer
		if err := emitJSON(&buf, json.RawMessage(bs)); err != nil {
			return err
		}
		_, err := fmt.Fprint(cmd.OutOrStdout(), buf.String())
		return err
	}
	if flags.Quiet {
		return nil
	}
	switch verb {
	case "delete":
		_, err := fmt.Fprintf(cmd.OutOrStdout(),
			"%s deleted (use `kata restore %s` to undo)\n", ref, ref)
		return err
	case "purge":
		_, err := fmt.Fprintf(cmd.OutOrStdout(), "%s purged (irreversible)\n", ref)
		return err
	}
	return nil
}

// httpDoJSONWithHeader mirrors httpDoJSON but lets callers attach extra
// request headers (notably X-Kata-Confirm). Defined here so delete and the
// upcoming purge command don't have to extend the helpers.go signature.
func httpDoJSONWithHeader(ctx context.Context, client *http.Client,
	method, url string, headers map[string]string, body any) (int, []byte, error) {
	var rdr io.Reader
	if body != nil {
		bs, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		rdr = bytes.NewReader(bs)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, rdr)
	if err != nil {
		return 0, nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req) //nolint:gosec // G107: daemon-local URL controlled by ensureDaemon.
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	out, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, nil, err
	}
	return resp.StatusCode, out, nil
}
