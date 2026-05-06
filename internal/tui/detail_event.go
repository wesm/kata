package tui

import (
	"fmt"
	"strings"
	"time"
)

// eventDescriber returns the human-readable description for an event of
// a given type. The describer can read payload fields (label name,
// to_number, owner). Functions instead of plain strings keep "labeled
// %s" / "linked %s #N" / closed-with-reason all expressible without a
// branchy switch in eventDescription.
type eventDescriber func(e EventLogEntry) string

// staticDesc returns an eventDescriber that always emits s. Used for
// the simple cases (created, reopened, etc.) that don't need payload.
func staticDesc(s string) eventDescriber {
	return func(EventLogEntry) string { return s }
}

// payloadDesc returns an eventDescriber that emits "<prefix> <field>"
// where field is read out of the payload as a string.
func payloadDesc(prefix, field string) eventDescriber {
	return func(e EventLogEntry) string {
		return prefix + " " + payloadString(e, field)
	}
}

// eventDescribers is the per-type dispatch table for eventDescription.
// Unknown types fall through to a stripped "issue." prefix in
// eventDescription so the column always carries something readable.
var eventDescribers = map[string]eventDescriber{
	"issue.created":          staticDesc("created"),
	"issue.closed":           func(e EventLogEntry) string { return "closed" + reasonSuffix(e) },
	"issue.reopened":         staticDesc("reopened"),
	"issue.commented":        staticDesc("added comment"),
	"issue.labeled":          payloadDesc("labeled", "label"),
	"issue.unlabeled":        payloadDesc("unlabeled", "label"),
	"issue.linked":           func(e EventLogEntry) string { return "linked " + linkPayloadDesc(e) },
	"issue.unlinked":         func(e EventLogEntry) string { return "unlinked " + linkPayloadDesc(e) },
	"issue.assigned":         payloadDesc("assigned", "owner"),
	"issue.unassigned":       staticDesc("unassigned"),
	"issue.priority_set":     prioritySetDesc,
	"issue.priority_cleared": priorityClearedDesc,
	"issue.updated":          staticDesc("updated"),
	"issue.soft_deleted":     staticDesc("deleted"),
	"issue.restored":         staticDesc("restored"),
}

// prioritySetDesc renders "priority set to N" or "priority N → M" when the
// payload carries the prior value, so digest-style scrubbing surfaces both
// old and new priorities in one line.
func prioritySetDesc(e EventLogEntry) string {
	newP, ok := payloadInt(e, "priority")
	if !ok {
		return "priority changed"
	}
	if old, ok := payloadInt(e, "old_priority"); ok {
		return fmt.Sprintf("priority %d → %d", old, newP)
	}
	return fmt.Sprintf("priority set to %d", newP)
}

// priorityClearedDesc renders "priority cleared (was N)" when the payload
// carries the prior value, otherwise "priority cleared" alone.
func priorityClearedDesc(e EventLogEntry) string {
	if old, ok := payloadInt(e, "old_priority"); ok {
		return fmt.Sprintf("priority cleared (was %d)", old)
	}
	return "priority cleared"
}

// payloadInt reads a numeric field out of the event payload. Missing keys,
// non-numeric values, and a nil payload all return ok=false.
func payloadInt(e EventLogEntry, key string) (int64, bool) {
	if e.Payload == nil {
		return 0, false
	}
	v, ok := e.Payload[key]
	if !ok {
		return 0, false
	}
	return numberFromAny(v)
}

// eventDescription returns the type-specific short description used in
// the events tab. Unknown types fall back to a stripped "issue." prefix
// so the column always carries something readable.
func eventDescription(e EventLogEntry) string {
	if d, ok := eventDescribers[e.Type]; ok {
		return d(e)
	}
	return strings.TrimPrefix(e.Type, "issue.")
}

// reasonSuffix renders " (reason)" for closed events that carry one.
func reasonSuffix(e EventLogEntry) string {
	if r := payloadString(e, "reason"); r != "" {
		return " (" + r + ")"
	}
	return ""
}

// linkPayloadDesc formats "type #to_number" from a link.added/removed
// payload. Missing fields degrade gracefully — type alone, or just "?".
func linkPayloadDesc(e EventLogEntry) string {
	t := payloadString(e, "type")
	to, ok := readEventTargetNumber(e)
	if !ok {
		if t == "" {
			return "?"
		}
		return t
	}
	if t == "" {
		return fmt.Sprintf("#%d", to)
	}
	return fmt.Sprintf("%s #%d", t, to)
}

// payloadString reads a string field out of the event payload. Missing
// keys, non-string values, and a nil payload all return "".
func payloadString(e EventLogEntry, key string) string {
	if e.Payload == nil {
		return ""
	}
	if v, ok := e.Payload[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// fmtTime is the compact timestamp used in tab content. The zero value
// renders as a dash so empty fixtures don't show "0001-01-01 00:00".
func fmtTime(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.Format("2006-01-02 15:04")
}

// eventJumpTarget reads the issue number that a jumpable event refers
// to. link.added/link.removed carry to_number; we also accept
// issue_number for forward-compat.
func eventJumpTarget(events []EventLogEntry, idx int) (int64, bool) {
	if idx < 0 || idx >= len(events) {
		return 0, false
	}
	return readEventTargetNumber(events[idx])
}

// readEventTargetNumber pulls an int64 issue number out of e.Payload.
// JSON decodes numbers as float64 by default; int64/int are accepted so
// hand-built test fixtures don't need to round-trip through json.
func readEventTargetNumber(e EventLogEntry) (int64, bool) {
	if e.Payload == nil {
		return 0, false
	}
	for _, k := range []string{"to_number", "issue_number"} {
		if v, ok := e.Payload[k]; ok {
			if n, ok := numberFromAny(v); ok {
				return n, true
			}
		}
	}
	return 0, false
}

// numberFromAny widens a JSON-decoded number to int64.
func numberFromAny(v any) (int64, bool) {
	switch n := v.(type) {
	case float64:
		return int64(n), true
	case int64:
		return n, true
	case int:
		return int64(n), true
	}
	return 0, false
}

// linkJumpTarget returns the issue number to navigate to from the
// link at idx. Outgoing links jump to ToNumber; incoming links (where
// ToNumber == current) jump to FromNumber instead so Enter on an
// "X blocks me" entry takes the user to X rather than re-opening the
// current issue. self-loop links (rare) fall through to ToNumber and
// re-open the current issue, which is harmless.
func linkJumpTarget(links []LinkEntry, idx int, current int64) (int64, bool) {
	if idx < 0 || idx >= len(links) {
		return 0, false
	}
	l := links[idx]
	target := l.ToNumber
	if target == current && l.FromNumber != 0 {
		target = l.FromNumber
	}
	return target, true
}
