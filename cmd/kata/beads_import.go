package main

import (
	"bufio"
	"bytes"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"
)

const (
	beadsSource               = "beads"
	maxBeadsCommentsJSONBytes = 16 * 1024 * 1024
)

type beadsIssue struct {
	ID           string            `json:"id"`
	Title        string            `json:"title"`
	Description  string            `json:"description"`
	Status       string            `json:"status"`
	Priority     int               `json:"priority"`
	IssueType    string            `json:"issue_type"`
	Owner        string            `json:"owner"`
	CreatedAt    time.Time         `json:"created_at"`
	CreatedBy    string            `json:"created_by"`
	UpdatedAt    time.Time         `json:"updated_at"`
	ClosedAt     *time.Time        `json:"closed_at"`
	CloseReason  string            `json:"close_reason"`
	Labels       []string          `json:"labels"`
	Dependencies []beadsDependency `json:"dependencies"`
	CommentCount int               `json:"comment_count"`
}

type beadsDependency struct {
	IssueID     string `json:"issue_id"`
	DependsOnID string `json:"depends_on_id"`
	Type        string `json:"type"`
	CreatedBy   string `json:"created_by"`
	Metadata    string `json:"metadata"`
	CreatedAt   string `json:"created_at"`
}

type beadsComment struct {
	ID        string    `json:"id"`
	IssueID   string    `json:"issue_id"`
	Author    string    `json:"author"`
	Text      string    `json:"text"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"created_at"`
}

type beadsImportRequest struct {
	Actor  string                  `json:"actor"`
	Source string                  `json:"source"`
	Items  []beadsImportIssueInput `json:"items"`
}

type beadsImportIssueInput struct {
	ExternalID   string                    `json:"external_id"`
	Title        string                    `json:"title"`
	Body         string                    `json:"body"`
	Author       string                    `json:"author"`
	Owner        *string                   `json:"owner,omitempty"`
	Status       string                    `json:"status"`
	ClosedReason *string                   `json:"closed_reason,omitempty"`
	CreatedAt    time.Time                 `json:"created_at"`
	UpdatedAt    time.Time                 `json:"updated_at"`
	ClosedAt     *time.Time                `json:"closed_at,omitempty"`
	Labels       []string                  `json:"labels,omitempty"`
	Comments     []beadsImportCommentInput `json:"comments,omitempty"`
	Links        []beadsImportLinkInput    `json:"links,omitempty"`
}

type beadsImportCommentInput struct {
	ExternalID string    `json:"external_id"`
	Author     string    `json:"author"`
	Body       string    `json:"body"`
	CreatedAt  time.Time `json:"created_at"`
}

type beadsImportLinkInput struct {
	Type             string `json:"type"`
	TargetExternalID string `json:"target_external_id"`
}

var (
	invalidLabelChar = regexp.MustCompile(`[^a-z0-9._:-]+`)
	repeatedDash     = regexp.MustCompile(`-+`)
)

func parseBeadsExport(r io.Reader) ([]beadsIssue, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	var out []beadsIssue
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var issue beadsIssue
		if err := json.Unmarshal([]byte(line), &issue); err != nil {
			return nil, fmt.Errorf("decode beads export: %w", err)
		}
		out = append(out, issue)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan beads export: %w", err)
	}
	return out, nil
}

func parseBeadsCommentsJSON(r io.Reader) ([]beadsComment, error) {
	data, err := io.ReadAll(io.LimitReader(r, maxBeadsCommentsJSONBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read beads comments: %w", err)
	}
	if len(data) > maxBeadsCommentsJSONBytes {
		return nil, &cliError{
			Message:  fmt.Sprintf("beads comments JSON exceeds %d byte limit", maxBeadsCommentsJSONBytes),
			Kind:     kindValidation,
			ExitCode: ExitValidation,
		}
	}
	data = bytes.TrimSpace(data)
	if len(data) == 0 {
		return nil, nil
	}
	var comments []beadsComment
	if err := json.Unmarshal(data, &comments); err == nil {
		return comments, nil
	}
	var wrapped struct {
		Comments []beadsComment `json:"comments"`
	}
	if err := json.Unmarshal(data, &wrapped); err != nil {
		return nil, fmt.Errorf("decode beads comments: %w", err)
	}
	return wrapped.Comments, nil
}

func buildBeadsImportRequest(r io.Reader, comments map[string][]beadsComment, actor string) (beadsImportRequest, error) {
	issues, err := parseBeadsExport(r)
	if err != nil {
		return beadsImportRequest{}, err
	}

	req := beadsImportRequest{Actor: actor, Source: beadsSource, Items: make([]beadsImportIssueInput, 0, len(issues))}
	indexByID := make(map[string]int, len(issues))
	for _, b := range issues {
		status := strings.TrimSpace(b.Status)
		if status == "" {
			status = "open"
		}
		if status != "open" && status != "closed" {
			return beadsImportRequest{}, fmt.Errorf("unsupported beads status %q for %s", status, b.ID)
		}

		labels := []string{"source:beads", beadsIDLabel(b.ID)}
		seenLabels := map[string]struct{}{}
		labels = appendNormalizedLabels(nil, seenLabels, labels...)
		for _, label := range b.Labels {
			labels = appendNormalizedLabels(labels, seenLabels, label)
		}

		var owner *string
		if trimmed := strings.TrimSpace(b.Owner); trimmed != "" {
			owner = &trimmed
		}
		author := strings.TrimSpace(b.CreatedBy)
		if author == "" {
			author = actor
		}
		if strings.TrimSpace(b.Title) == "" {
			return beadsImportRequest{}, fmt.Errorf("beads issue %q missing title", b.ID)
		}

		closedAt := b.ClosedAt
		var closedReason *string
		if status == "closed" {
			mapped := mapBeadsCloseReason(b.CloseReason)
			closedReason = &mapped
			if closedAt == nil {
				updatedAt := b.UpdatedAt
				closedAt = &updatedAt
			}
		}

		item := beadsImportIssueInput{
			ExternalID:   b.ID,
			Title:        b.Title,
			Body:         strings.TrimRight(b.Description, "\n") + beadsFooter(b),
			Author:       author,
			Owner:        owner,
			Status:       status,
			ClosedReason: closedReason,
			CreatedAt:    b.CreatedAt,
			UpdatedAt:    b.UpdatedAt,
			ClosedAt:     closedAt,
			Labels:       labels,
		}
		for _, c := range comments[b.ID] {
			commentAuthor := strings.TrimSpace(c.Author)
			if commentAuthor == "" {
				commentAuthor = actor
			}
			commentBody := c.Text
			if commentBody == "" {
				commentBody = c.Body
			}
			item.Comments = append(item.Comments, beadsImportCommentInput{
				ExternalID: c.ID,
				Author:     commentAuthor,
				Body:       commentBody,
				CreatedAt:  c.CreatedAt,
			})
		}
		req.Items = append(req.Items, item)
		indexByID[b.ID] = len(req.Items) - 1
	}

	for _, b := range issues {
		for _, dep := range b.Dependencies {
			if strings.TrimSpace(dep.DependsOnID) == "" {
				continue
			}
			idx, ok := indexByID[dep.DependsOnID]
			if !ok {
				return beadsImportRequest{}, fmt.Errorf("beads dependency target %q for %s not found in export", dep.DependsOnID, b.ID)
			}
			req.Items[idx].Links = append(req.Items[idx].Links, beadsImportLinkInput{Type: "blocks", TargetExternalID: b.ID})
		}
	}

	return req, nil
}

func appendNormalizedLabels(out []string, seen map[string]struct{}, labels ...string) []string {
	for _, label := range labels {
		normalized := normalizeKataLabel(label)
		if _, ok := seen[normalized]; ok {
			continue
		}
		seen[normalized] = struct{}{}
		out = append(out, normalized)
	}
	return out
}

func normalizeKataLabel(s string) string {
	return normalizeKataLabelMax(s, 64)
}

func normalizeKataLabelMax(s string, maxLen int) string {
	normalized := strings.ToLower(strings.TrimSpace(s))
	normalized = strings.Join(strings.Fields(normalized), "-")
	normalized = invalidLabelChar.ReplaceAllString(normalized, "-")
	normalized = repeatedDash.ReplaceAllString(normalized, "-")
	normalized = strings.Trim(normalized, "-._:")
	if normalized == "" {
		normalized = "imported"
	}
	if len(normalized) <= maxLen {
		return normalized
	}
	sum := sha1.Sum([]byte(normalized))
	suffix := hex.EncodeToString(sum[:])[:8]
	prefixLen := maxLen - len(suffix) - 1
	if prefixLen < 1 {
		return suffix[:maxLen]
	}
	prefix := strings.TrimRight(normalized[:prefixLen], "-._:")
	if prefix == "" {
		prefix = "imported"
	}
	return prefix + "-" + suffix
}

func beadsIDLabel(id string) string {
	const prefix = "beads-id:"
	return prefix + normalizeKataLabelMax(id, 64-len(prefix))
}

func mapBeadsCloseReason(reason string) string {
	switch strings.TrimSpace(reason) {
	case "done", "wontfix", "duplicate":
		return strings.TrimSpace(reason)
	default:
		return "done"
	}
}

func beadsFooter(b beadsIssue) string {
	labels, err := json.Marshal(b.Labels)
	if err != nil {
		labels = []byte("[]")
	}
	closedAt := ""
	if b.ClosedAt != nil {
		closedAt = b.ClosedAt.Format(time.RFC3339Nano)
	}
	return fmt.Sprintf("\n---\nImported from Beads\nbeads_id: %s\nbeads_type: %s\nbeads_priority: %d\nbeads_original_labels: %s\nbeads_created_at: %s\nbeads_updated_at: %s\nbeads_closed_at: %s\nbeads_close_reason: %s\nbeads_comment_count: %d\n",
		b.ID,
		b.IssueType,
		b.Priority,
		string(labels),
		b.CreatedAt.Format(time.RFC3339Nano),
		b.UpdatedAt.Format(time.RFC3339Nano),
		closedAt,
		b.CloseReason,
		b.CommentCount,
	)
}
