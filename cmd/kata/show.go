package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
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
			ctx, baseURL, pid, ref, err := resolveIssueRefForCommand(cmd, args[0])
			if err != nil {
				return err
			}
			client, err := httpClientFor(ctx, baseURL)
			if err != nil {
				return err
			}
			httpStatus, bs, err := httpDoJSON(ctx, client, http.MethodGet,
				fmt.Sprintf("%s/api/v1/projects/%d/issues/%s", baseURL, pid, url.PathEscape(ref.RefForAPI)), nil)
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
					ShortID string `json:"short_id"`
					UID     string `json:"uid"`
					Title   string `json:"title"`
					Body    string `json:"body"`
					Status  string `json:"status"`
					Author  string `json:"author"`
				} `json:"issue"`
				Comments []struct {
					Author string `json:"author"`
					Body   string `json:"body"`
				} `json:"comments"`
				Labels []struct {
					Label string `json:"label"`
				} `json:"labels"`
				Links []struct {
					Type string         `json:"type"`
					From linkPeerForCLI `json:"from"`
					To   linkPeerForCLI `json:"to"`
				} `json:"links"`
			}
			if err := json.Unmarshal(bs, &b); err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if _, err := fmt.Fprintf(out, "%s  %s  [%s]  by %s\n",
				b.Issue.ShortID,
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
					label, other := linkLabelFromPOV(l.Type, b.Issue.UID, l.From, l.To)
					if _, err := fmt.Fprintf(out, "%s: %s\n", label, other); err != nil {
						return err
					}
				}
			}
			return nil
		},
	}
}

// linkPeerForCLI mirrors api.LinkPeer for the show command's decode path. UID
// is the stable handle; short_id is the human-readable display.
type linkPeerForCLI struct {
	UID     string `json:"uid"`
	ShortID string `json:"short_id"`
}

// linkLabelFromPOV returns the label and the OTHER endpoint's short_id,
// framed from the viewing issue's point of view. The display matches the
// relationship-flag vocabulary on `kata create` / `kata edit`: "parent" /
// "child" for the parent slot, "blocks" / "blocked-by" for the directed
// blocks edge, and "related" for the symmetric one.
func linkLabelFromPOV(linkType, viewerUID string, from, to linkPeerForCLI) (label, other string) {
	if from.UID == viewerUID {
		switch linkType {
		case "parent":
			return "parent", to.ShortID
		case "blocks":
			return "blocks", to.ShortID
		case "related":
			return "related", to.ShortID
		default:
			return linkType, to.ShortID
		}
	}
	switch linkType {
	case "parent":
		return "child", from.ShortID
	case "blocks":
		return "blocked-by", from.ShortID
	case "related":
		return "related", from.ShortID
	default:
		return linkType, from.ShortID
	}
}
