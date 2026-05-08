package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/spf13/cobra"
	"github.com/wesm/kata/internal/textsafe"
)

func newShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show <issue-ref>",
		Short: "show issue + comments",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
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
			ref, err := resolveIssueRef(ctx, baseURL, pid, args[0])
			if err != nil {
				return err
			}
			client, err := httpClientFor(ctx, baseURL)
			if err != nil {
				return err
			}
			httpStatus, bs, err := httpDoJSON(ctx, client, http.MethodGet,
				fmt.Sprintf("%s/api/v1/projects/%d/issues/%d", baseURL, pid, ref.Number), nil)
			if err != nil {
				return err
			}
			if httpStatus >= 400 {
				return apiErrFromBody(httpStatus, bs)
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
				Issue struct {
					Number int64  `json:"number"`
					Title  string `json:"title"`
					Body   string `json:"body"`
					Status string `json:"status"`
					Author string `json:"author"`
				} `json:"issue"`
				Comments []struct {
					Author string `json:"author"`
					Body   string `json:"body"`
				} `json:"comments"`
				Labels []struct {
					Label string `json:"label"`
				} `json:"labels"`
				Links []struct {
					Type       string `json:"type"`
					FromNumber int64  `json:"from_number"`
					ToNumber   int64  `json:"to_number"`
				} `json:"links"`
			}
			if err := json.Unmarshal(bs, &b); err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if _, err := fmt.Fprintf(out, "#%d  %s  [%s]  by %s\n",
				b.Issue.Number,
				textsafe.Line(b.Issue.Title),
				b.Issue.Status,
				textsafe.Line(b.Issue.Author)); err != nil {
				return err
			}
			if b.Issue.Body != "" {
				if _, err := fmt.Fprintln(out); err != nil {
					return err
				}
				if _, err := fmt.Fprintln(out, textsafe.Block(b.Issue.Body)); err != nil {
					return err
				}
			}
			if len(b.Comments) > 0 {
				if _, err := fmt.Fprintln(out, "\n--- comments ---"); err != nil {
					return err
				}
				for _, c := range b.Comments {
					if _, err := fmt.Fprintf(out, "%s: %s\n",
						textsafe.Line(c.Author), textsafe.Block(c.Body)); err != nil {
						return err
					}
				}
			}
			if len(b.Labels) > 0 {
				if _, err := fmt.Fprintln(out, "\n--- labels ---"); err != nil {
					return err
				}
				parts := make([]string, 0, len(b.Labels))
				for _, l := range b.Labels {
					parts = append(parts, textsafe.Line(l.Label))
				}
				if _, err := fmt.Fprintln(out, strings.Join(parts, ", ")); err != nil {
					return err
				}
			}
			if len(b.Links) > 0 {
				if _, err := fmt.Fprintln(out, "\n--- links ---"); err != nil {
					return err
				}
				for _, l := range b.Links {
					label, other := linkLabelFromPOV(l.Type, b.Issue.Number, l.FromNumber, l.ToNumber)
					if _, err := fmt.Fprintf(out, "%s: #%d\n", label, other); err != nil {
						return err
					}
				}
			}
			return nil
		},
	}
}

// linkLabelFromPOV returns the label and the OTHER endpoint number,
// framed from the viewing issue's point of view. The display matches
// the relationship-flag vocabulary on `kata create` / `kata edit`:
// "parent" / "child" for the parent slot (parent points up, child
// points down), "blocks" / "blocked-by" for the directed blocks
// edge, and "related" for the symmetric one. This reads unambiguously
// without arrows: `child: #5` says "this issue's child is #5", which
// is what the previous `parent ← #5` rendering tried to convey via
// arrow direction.
func linkLabelFromPOV(linkType string, viewerNumber, fromNumber, toNumber int64) (label string, other int64) {
	if fromNumber == viewerNumber {
		// Viewer is the link's source.
		switch linkType {
		case "parent":
			return "parent", toNumber
		case "blocks":
			return "blocks", toNumber
		case "related":
			return "related", toNumber
		default:
			return linkType, toNumber
		}
	}
	// Viewer is the link's target — relabel to reflect the inverse.
	switch linkType {
	case "parent":
		return "child", fromNumber
	case "blocks":
		return "blocked-by", fromNumber
	case "related":
		return "related", fromNumber
	default:
		return linkType, fromNumber
	}
}
