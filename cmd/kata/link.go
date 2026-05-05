package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"

	"github.com/spf13/cobra"
)

func newLinkCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "link <from-ref> <type> <to-ref>",
		Short: "create a link between two issues (type: parent|blocks|related)",
		Args:  cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runLinkCreate(cmd, args[0], args[1], args[2], false)
		},
	}
}

func newParentCmd() *cobra.Command {
	var replace bool
	cmd := &cobra.Command{
		Use:   "parent <child-ref> <parent-ref>",
		Short: "set the parent link of <child> to <parent>",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runLinkCreate(cmd, args[0], "parent", args[1], replace)
		},
	}
	cmd.Flags().BoolVar(&replace, "replace", false, "swap the existing parent if any")
	return cmd
}

func newUnlinkCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "unlink <from-ref> <type> <to-ref>",
		Short: "remove a link between two issues",
		Args:  cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runUnlinkByEndpoints(cmd, args[0], args[1], args[2])
		},
	}
}

func newUnparentCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "unparent <child-ref>",
		Short: "remove the parent link of <child>",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runUnlinkByType(cmd, args[0], "parent")
		},
	}
}

func newBlockCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "block <blocker-ref> <blocked-ref>",
		Short: "mark <blocker> as blocking <blocked>",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runLinkCreate(cmd, args[0], "blocks", args[1], false)
		},
	}
}

func newUnblockCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "unblock <blocker-ref> <blocked-ref>",
		Short: "remove the blocks link from <blocker> to <blocked>",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runUnlinkByEndpoints(cmd, args[0], "blocks", args[1])
		},
	}
}

func newRelateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "relate <a-ref> <b-ref>",
		Short: "mark two issues as related (canonical-ordered)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runLinkCreate(cmd, args[0], "related", args[1], false)
		},
	}
}

func newUnrelateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "unrelate <a-ref> <b-ref>",
		Short: "remove a related link between two issues",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runUnlinkByEndpoints(cmd, args[0], "related", args[1])
		},
	}
}

func runLinkCreate(cmd *cobra.Command, fromRef string, linkType string, toRef string, replace bool) error {
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
	from, err := resolveIssueRef(ctx, baseURL, pid, fromRef)
	if err != nil {
		return err
	}
	to, err := resolveIssueRef(ctx, baseURL, pid, toRef)
	if err != nil {
		return err
	}
	actor, _ := resolveActor(flags.As, nil)
	client, err := httpClientFor(ctx, baseURL)
	if err != nil {
		return err
	}
	payload := map[string]any{
		"actor":     actor,
		"type":      linkType,
		"to_number": to.Number,
	}
	if replace {
		payload["replace"] = true
	}
	url := fmt.Sprintf("%s/api/v1/projects/%d/issues/%d/links", baseURL, pid, from.Number)
	status, bs, err := httpDoJSON(ctx, client, http.MethodPost, url, payload)
	if err != nil {
		return err
	}
	if status >= 400 {
		return apiErrFromBody(status, bs)
	}
	return printLinkMutation(cmd, bs)
}

func runUnlinkByEndpoints(cmd *cobra.Command, fromRef string, linkType string, toRef string) error {
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
	from, err := resolveIssueRef(ctx, baseURL, pid, fromRef)
	if err != nil {
		return err
	}
	to, err := resolveIssueRef(ctx, baseURL, pid, toRef)
	if err != nil {
		return err
	}
	link, err := lookupLink(ctx, baseURL, pid, from.Number, linkType, &to.Number)
	if err != nil {
		return err
	}
	return runDeleteLink(cmd, baseURL, pid, link)
}

func runUnlinkByType(cmd *cobra.Command, fromRef string, linkType string) error {
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
	from, err := resolveIssueRef(ctx, baseURL, pid, fromRef)
	if err != nil {
		return err
	}
	link, err := lookupLink(ctx, baseURL, pid, from.Number, linkType, nil)
	if err != nil {
		return err
	}
	return runDeleteLink(cmd, baseURL, pid, link)
}

// linkRow is the wire shape of a single link entry inside ShowIssueResponse.
// Mirrors api.LinkOut for the fields the CLI needs.
type linkRow struct {
	ID         int64  `json:"id"`
	Type       string `json:"type"`
	FromNumber int64  `json:"from_number"`
	ToNumber   int64  `json:"to_number"`
}

// lookupLink resolves a (from, type [, to]) tuple to the matching link by
// reading the issue's links via GET /issues/{from}. Returns 404 link_not_found
// when no link matches. The returned linkRow carries enough context for the
// post-DELETE print line.
//
// For type=related the daemon canonicalizes storage to (min,max), so the
// matcher accepts either endpoint order — `kata link 5 related 3` and
// `kata unlink 5 related 3` must agree even though the stored row is (3,5).
func lookupLink(ctx context.Context, baseURL string, pid, fromNumber int64, linkType string, toNumber *int64) (linkRow, error) {
	client, err := httpClientFor(ctx, baseURL)
	if err != nil {
		return linkRow{}, err
	}
	showURL := fmt.Sprintf("%s/api/v1/projects/%d/issues/%d", baseURL, pid, fromNumber)
	status, bs, err := httpDoJSON(ctx, client, http.MethodGet, showURL, nil)
	if err != nil {
		return linkRow{}, err
	}
	if status >= 400 {
		return linkRow{}, apiErrFromBody(status, bs)
	}
	var b struct {
		Links []linkRow `json:"links"`
	}
	if err := json.Unmarshal(bs, &b); err != nil {
		return linkRow{}, err
	}
	for _, l := range b.Links {
		if l.Type != linkType {
			continue
		}
		if !linkEndpointsMatch(l, linkType, fromNumber, toNumber) {
			continue
		}
		return l, nil
	}
	return linkRow{}, &cliError{Message: "link not found", Code: "link_not_found", Kind: kindNotFound, ExitCode: ExitNotFound}
}

// linkEndpointsMatch reports whether l matches the (from, to) request. For
// directional types (parent, blocks) the order is fixed: l.FromNumber == from
// and (if specified) l.ToNumber == to. For type=related the link is undirected
// so either endpoint order is acceptable.
func linkEndpointsMatch(l linkRow, linkType string, fromNumber int64, toNumber *int64) bool {
	if linkType == "related" {
		if l.FromNumber == fromNumber && (toNumber == nil || l.ToNumber == *toNumber) {
			return true
		}
		if l.ToNumber == fromNumber && (toNumber == nil || l.FromNumber == *toNumber) {
			return true
		}
		return false
	}
	if l.FromNumber != fromNumber {
		return false
	}
	if toNumber != nil && l.ToNumber != *toNumber {
		return false
	}
	return true
}

func runDeleteLink(cmd *cobra.Command, baseURL string, pid int64, link linkRow) error {
	ctx := cmd.Context()
	actor, _ := resolveActor(flags.As, nil)
	client, err := httpClientFor(ctx, baseURL)
	if err != nil {
		return err
	}
	deleteURL := fmt.Sprintf("%s/api/v1/projects/%d/issues/%d/links/%d?actor=%s",
		baseURL, pid, link.FromNumber, link.ID, url.QueryEscape(actor))
	status, bs, err := httpDoJSON(ctx, client, http.MethodDelete, deleteURL, nil)
	if err != nil {
		return err
	}
	if status >= 400 {
		return apiErrFromBody(status, bs)
	}
	return printUnlinkMutation(cmd, bs, link)
}

// printUnlinkMutation formats the DELETE-link response. The MutationResponse
// body carries only {issue, event, changed} so the unlink line is built from
// the link the CLI fetched up-front (its from/to/type).
func printUnlinkMutation(cmd *cobra.Command, bs []byte, link linkRow) error {
	if flags.JSON {
		var buf bytes.Buffer
		if err := emitJSON(&buf, json.RawMessage(bs)); err != nil {
			return err
		}
		_, err := fmt.Fprint(cmd.OutOrStdout(), buf.String())
		return err
	}
	var b struct {
		Changed bool `json:"changed"`
	}
	if err := json.Unmarshal(bs, &b); err != nil {
		return err
	}
	if flags.Quiet {
		return nil
	}
	if !b.Changed {
		_, err := fmt.Fprintf(cmd.OutOrStdout(), "#%d already unlinked: %s → #%d (no-op)\n",
			link.FromNumber, link.Type, link.ToNumber)
		return err
	}
	_, err := fmt.Fprintf(cmd.OutOrStdout(), "#%d unlinked: %s → #%d\n",
		link.FromNumber, link.Type, link.ToNumber)
	return err
}

// printLinkMutation formats a CreateLinkResponse for the three output modes.
// Reuses emitJSON for the JSON branch (the daemon body already includes the
// shape we want under the kata_api_version envelope).
func printLinkMutation(cmd *cobra.Command, bs []byte) error {
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
			Number int64 `json:"number"`
		} `json:"issue"`
		Link struct {
			Type       string `json:"type"`
			FromNumber int64  `json:"from_number"`
			ToNumber   int64  `json:"to_number"`
		} `json:"link"`
		Changed bool `json:"changed"`
	}
	if err := json.Unmarshal(bs, &b); err != nil {
		return err
	}
	if flags.Quiet {
		return nil
	}
	if !b.Changed {
		_, err := fmt.Fprintf(cmd.OutOrStdout(), "#%d already linked: %s → #%d (no-op)\n",
			b.Link.FromNumber, b.Link.Type, b.Link.ToNumber)
		return err
	}
	_, err := fmt.Fprintf(cmd.OutOrStdout(), "#%d linked: %s → #%d\n",
		b.Link.FromNumber, b.Link.Type, b.Link.ToNumber)
	return err
}
