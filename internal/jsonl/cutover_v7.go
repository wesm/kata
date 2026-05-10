package jsonl

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/wesm/kata/internal/shortid"
	katauid "github.com/wesm/kata/internal/uid"
)

// applyCutoverV7toV8 transforms a decoded v7-or-earlier envelope stream into a
// v8-compatible one in place: it validates project names against the new
// `#`-free invariant, backfills issue UIDs for pre-v2 envelopes, and derives
// each issue's short_id by replaying the §5 auto-extend algorithm in
// ULID-ascending order within each project. The transformation runs before any
// SQL inserts so a bad project name fails the import before mutating the
// target.
//
// Envelope Data is rewritten in place (with the legacy `number` field
// stripped and a new `short_id` field) so the downstream importEnvelope loop
// sees current-version-shaped envelopes without any version-aware special
// cases.
func applyCutoverV7toV8(envs []Envelope) error {
	if err := validateNoHashInProjectNames(envs); err != nil {
		return err
	}
	return deriveShortIDsForIssues(envs)
}

// validateNoHashInProjectNames walks every project envelope and rejects names
// containing '#' before any inserts run. The error names the offending
// project so the operator can rename it before retrying.
func validateNoHashInProjectNames(envs []Envelope) error {
	for _, env := range envs {
		if env.Kind != KindProject {
			continue
		}
		var rec struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(env.Data, &rec); err != nil {
			return fmt.Errorf("decode project name for cutover validation: %w", err)
		}
		if strings.Contains(rec.Name, "#") {
			return fmt.Errorf("project name %q must not contain '#'; rename before cutover", rec.Name)
		}
	}
	return nil
}

// deriveShortIDsForIssues replays the §5 auto-extend algorithm per project in
// ULID-ascending order. Each issue envelope's Data is rewritten in place with
// the derived short_id field; the legacy `number` field is stripped.
//
// Issues are grouped by `project_id`, backfilled with a UID via FromStableSeed
// when missing (pre-v2 envelopes), then sorted by UID ascending. For each
// issue in order, the shortest length L (starting at shortid.MinLength) whose
// lowercased UID suffix is unique within the same project's already-assigned
// short_ids is the issue's short_id.
func deriveShortIDsForIssues(envs []Envelope) error {
	// Snapshot every issue envelope's index, UID, and project_id so we can
	// process them in cross-envelope ULID order without losing the slot to
	// write back to.
	type pendingIssue struct {
		envIdx    int                        // index into envs
		uid       string                     // backfilled if missing
		projectID int64                      // for grouping
		raw       map[string]json.RawMessage // decoded data, mutated in place
	}
	var pending []pendingIssue
	for i, env := range envs {
		if env.Kind != KindIssue {
			continue
		}
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(env.Data, &raw); err != nil {
			return fmt.Errorf("decode issue for cutover: %w", err)
		}
		uid, err := issueUIDFromRaw(raw)
		if err != nil {
			return fmt.Errorf("derive issue uid for cutover: %w", err)
		}
		var projectID int64
		if v, ok := raw["project_id"]; ok {
			if err := json.Unmarshal(v, &projectID); err != nil {
				return fmt.Errorf("decode issue project_id for cutover: %w", err)
			}
		}
		pending = append(pending, pendingIssue{
			envIdx:    i,
			uid:       uid,
			projectID: projectID,
			raw:       raw,
		})
	}
	// Group by project_id; sort each group by UID ascending so auto-extend
	// is deterministic. The canonical ordering matches a fresh
	// ULID-ascending replay (spec §4.2).
	byProject := map[int64][]int{}
	for i, p := range pending {
		byProject[p.projectID] = append(byProject[p.projectID], i)
	}
	for _, idxs := range byProject {
		sort.SliceStable(idxs, func(a, b int) bool {
			return pending[idxs[a]].uid < pending[idxs[b]].uid
		})
		assigned := map[string]bool{}
		for _, idx := range idxs {
			p := pending[idx]
			short, err := deriveAutoExtendShortID(p.uid, assigned)
			if err != nil {
				return fmt.Errorf("derive short_id for uid %s: %w", p.uid, err)
			}
			assigned[short] = true
			// Rewrite envelope data in place: drop `number`, set
			// `short_id`, set `uid` (in case it was backfilled for v1).
			delete(p.raw, "number")
			p.raw["short_id"] = json.RawMessage(`"` + short + `"`)
			p.raw["uid"] = json.RawMessage(`"` + p.uid + `"`)
			data, err := json.Marshal(p.raw)
			if err != nil {
				return fmt.Errorf("re-marshal cutover issue: %w", err)
			}
			envs[p.envIdx].Data = data
		}
	}
	return nil
}

// issueUIDFromRaw reads the issue envelope's uid field, backfilling via
// FromStableSeed when absent (pre-v2 envelopes carried `(project_id, number,
// created_at)` and no UID). The backfill rule mirrors fillIssueUID so the
// cutover and the per-envelope import path agree on UIDs.
func issueUIDFromRaw(raw map[string]json.RawMessage) (string, error) {
	if v, ok := raw["uid"]; ok {
		var u string
		if err := json.Unmarshal(v, &u); err != nil {
			return "", fmt.Errorf("decode issue uid: %w", err)
		}
		if u != "" {
			return u, nil
		}
	}
	// Pre-v2 path: derive from (project_id, number, created_at).
	var projectID, number int64
	var createdAt string
	if v, ok := raw["project_id"]; ok {
		if err := json.Unmarshal(v, &projectID); err != nil {
			return "", fmt.Errorf("decode issue project_id: %w", err)
		}
	}
	if v, ok := raw["number"]; ok {
		if err := json.Unmarshal(v, &number); err != nil {
			return "", fmt.Errorf("decode issue number: %w", err)
		}
	}
	if v, ok := raw["created_at"]; ok {
		if err := json.Unmarshal(v, &createdAt); err != nil {
			return "", fmt.Errorf("decode issue created_at: %w", err)
		}
	}
	t, err := parseExportTime(createdAt)
	if err != nil {
		return "", fmt.Errorf("parse issue created_at: %w", err)
	}
	return katauid.FromStableSeed([]byte(fmt.Sprintf("issue:%d:%d", projectID, number)), t)
}

// deriveAutoExtendShortID returns the shortest length-L suffix of uid (lowercased)
// from shortid.MinLength to shortid.MaxLength that is not already in
// assigned. The caller owns the assigned set and adds the returned value
// after each call.
func deriveAutoExtendShortID(uid string, assigned map[string]bool) (string, error) {
	for length := shortid.MinLength; length <= shortid.MaxLength; length++ {
		candidate, err := shortid.Derive(uid, length)
		if err != nil {
			return "", err
		}
		if !assigned[candidate] {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("auto-extend exhausted")
}
