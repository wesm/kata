package metadata

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// KeyDiff records the before/after JSON values for one metadata key.
// From is nil when the key was absent (or null) in the old blob.
// To is nil when the key was removed (or set to null) in the new blob.
type KeyDiff struct {
	From json.RawMessage // nil means "was absent/null"
	To   json.RawMessage // nil means "now absent/null"
}

// Diff computes the per-key changes between oldBlob and newBlob, both of
// which must be JSON objects (or null/empty, treated as {}).
// Only keys that actually changed are included in the returned map.
// A key that goes from absent/null to absent/null is suppressed (no-op).
func Diff(oldBlob, newBlob json.RawMessage) (map[string]KeyDiff, error) {
	oldMap, err := parseMetaBlob(oldBlob)
	if err != nil {
		return nil, fmt.Errorf("parsing old metadata: %w", err)
	}
	newMap, err := parseMetaBlob(newBlob)
	if err != nil {
		return nil, fmt.Errorf("parsing new metadata: %w", err)
	}

	result := make(map[string]KeyDiff)

	// Keys present in old.
	for k, oldVal := range oldMap {
		newVal, inNew := newMap[k]
		if !inNew || isNull(newVal) {
			if isNull(oldVal) {
				continue // null → null: no-op
			}
			result[k] = KeyDiff{From: oldVal, To: nil}
			continue
		}
		if !bytes.Equal(normalizeJSON(oldVal), normalizeJSON(newVal)) {
			// Normalize null From to nil: contract says From==nil means
			// "was absent or null", so raw `null` bytes must not leak out.
			var from json.RawMessage
			if !isNull(oldVal) {
				from = oldVal
			}
			result[k] = KeyDiff{From: from, To: newVal}
		}
	}

	// Keys present only in new.
	for k, newVal := range newMap {
		if _, inOld := oldMap[k]; inOld {
			continue // already handled above
		}
		if isNull(newVal) {
			continue // absent → null: no-op
		}
		result[k] = KeyDiff{From: nil, To: newVal}
	}

	return result, nil
}

// parseMetaBlob decodes a JSON object blob into a raw-value map.
// Null and empty input are treated as an empty object.
func parseMetaBlob(blob json.RawMessage) (map[string]json.RawMessage, error) {
	if len(blob) == 0 || string(blob) == "null" {
		return map[string]json.RawMessage{}, nil
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(blob, &m); err != nil {
		return nil, err
	}
	if m == nil {
		return map[string]json.RawMessage{}, nil
	}
	return m, nil
}

// isNull reports whether raw is a JSON null literal.
func isNull(raw json.RawMessage) bool {
	return string(raw) == "null"
}

// normalizeJSON round-trips raw through encoding/json to produce a canonical
// form for equality comparison (e.g. removes insignificant whitespace,
// normalises number representations). Falls back to the original bytes on
// error.
func normalizeJSON(raw json.RawMessage) []byte {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return raw
	}
	out, err := json.Marshal(v)
	if err != nil {
		return raw
	}
	return out
}
