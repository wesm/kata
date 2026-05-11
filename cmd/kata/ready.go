package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/spf13/cobra"
	"github.com/wesm/kata/internal/textsafe"
)

func newReadyCmd() *cobra.Command {
	var limit int
	cmd := &cobra.Command{
		Use:   "ready",
		Short: "list open issues with no open blocks predecessor",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if limit < 0 {
				return &cliError{Message: "--limit must be non-negative", Kind: kindValidation, ExitCode: ExitValidation}
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
			pid, err := resolveProjectID(ctx, baseURL, start)
			if err != nil {
				return err
			}
			client, err := httpClientFor(ctx, baseURL)
			if err != nil {
				return err
			}
			getURL := fmt.Sprintf("%s/api/v1/projects/%d/ready", baseURL, pid)
			if limit > 0 {
				getURL += fmt.Sprintf("?limit=%d", limit)
			}
			status, bs, err := httpDoJSON(ctx, client, http.MethodGet, getURL, nil)
			if err != nil {
				return err
			}
			if status >= 400 {
				return apiErrFromBody(status, bs)
			}
			if flags.JSON {
				var buf bytes.Buffer
				if err := emitJSON(&buf, json.RawMessage(bs)); err != nil {
					return err
				}
				_, err := fmt.Fprint(cmd.OutOrStdout(), buf.String())
				return err
			}
			var b struct {
				Issues []struct {
					ShortID string  `json:"short_id"`
					Title   string  `json:"title"`
					Owner   *string `json:"owner,omitempty"`
				} `json:"issues"`
			}
			if err := json.Unmarshal(bs, &b); err != nil {
				return err
			}
			for _, i := range b.Issues {
				owner := "-"
				if i.Owner != nil {
					owner = *i.Owner
				}
				if _, err := fmt.Fprintf(cmd.OutOrStdout(), "%-8s  %s  (%s)\n",
					i.ShortID, textsafe.Line(i.Title), textsafe.Line(owner)); err != nil {
					return err
				}
			}
			return nil
		},
	}
	cmd.Flags().IntVar(&limit, "limit", 0, "max rows (0 = no limit)")
	return cmd
}
