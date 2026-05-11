// Package shortid derives, validates, and parses kata's short_id display
// references. A short_id is the lowercased final L characters (4 ≤ L ≤ 26)
// of an issue's ULID; a qualified reference is "<project>#<short_id>".
package shortid

import (
	"errors"
	"strings"

	"github.com/wesm/kata/internal/uid"
)

// MinLength is the smallest short_id length the auto-extend algorithm
// will assign to a new issue. MaxLength is the full ULID length.
const (
	MinLength = 4
	MaxLength = uid.EncodedLen
)

var (
	// ErrLengthOutOfRange is returned when a caller asks for a short_id
	// length below MinLength or above MaxLength.
	ErrLengthOutOfRange = errors.New("shortid: length out of range")
	// ErrInvalidULID is returned when Derive is given a non-ULID input.
	ErrInvalidULID = errors.New("shortid: invalid ULID")
	// ErrInvalidRef is returned by Parse when the input cannot be
	// interpreted as a bare short_id, qualified short_id, or ULID.
	ErrInvalidRef = errors.New("shortid: invalid ref")
)

// Derive returns the lowercased length-L suffix of ulid as a short_id.
// L must be in [MinLength, MaxLength]; ulid must be a strict 26-char ULID.
func Derive(ulidStr string, length int) (string, error) {
	if length < MinLength || length > MaxLength {
		return "", ErrLengthOutOfRange
	}
	if !uid.Valid(ulidStr) {
		return "", ErrInvalidULID
	}
	return strings.ToLower(ulidStr[uid.Length()-length:]), nil
}

// Valid reports whether s is a syntactically valid short_id (length in
// range, lowercased Crockford base32 alphabet). Valid does not check
// existence in any project.
func Valid(s string) bool {
	if len(s) < MinLength || len(s) > MaxLength {
		return false
	}
	for i := 0; i < len(s); i++ {
		if !isCrockfordLower(s[i]) {
			return false
		}
	}
	return true
}

// Ref is a parsed user-supplied issue reference. Exactly one of ShortID
// or ULID is populated. Project is set only when the input was qualified
// (e.g. "kata#abc4"); a bare short_id leaves Project empty for the
// caller to fill from workspace context.
type Ref struct {
	Project string
	ShortID string
	ULID    string
}

// Parse interprets s as one of:
//   - a 26-char ULID (Ref.ULID set)
//   - a bare short_id (Ref.ShortID set; Ref.Project empty)
//   - a qualified short_id "<project>#<short_id>" (Ref.Project, Ref.ShortID)
//
// Legacy numeric refs that fall below MinLength ("12", "kata#42") are
// rejected with ErrInvalidRef because no valid short_id is that short.
// Numeric refs at or above MinLength ("1234", "kata#1234") parse as
// short_ids and resolve at the database layer — they return NotFound
// unless a real issue's ULID suffix happens to be all-digit (≈1% of
// short_ids at length 4). Legacy per-project issue numbers are not
// kept under the v8 schema, so there is no risk of misroute.
func Parse(s string) (Ref, error) {
	if s == "" {
		return Ref{}, ErrInvalidRef
	}
	// ULIDs are canonically uppercase Crockford. Users often paste them
	// in lowercase (e.g. from a comment body or wherever the display has
	// already been lowercased); normalize before the length check so a
	// lowercase 26-char input is recognized as a ULID rather than mis-
	// parsed as an unusually long short_id.
	if len(s) == uid.EncodedLen {
		upper := strings.ToUpper(s)
		if uid.Valid(upper) {
			return Ref{ULID: upper}, nil
		}
	}
	if uid.Valid(s) {
		return Ref{ULID: s}, nil
	}
	// Split on the LAST '#' so project names containing '#' parse
	// unambiguously. (Project names with '#' are forbidden by schema
	// after cutover, but Parse must work consistently regardless.)
	if i := strings.LastIndex(s, "#"); i >= 0 {
		project := s[:i]
		short := s[i+1:]
		if project == "" || !Valid(short) {
			return Ref{}, ErrInvalidRef
		}
		return Ref{Project: project, ShortID: short}, nil
	}
	if !Valid(s) {
		return Ref{}, ErrInvalidRef
	}
	return Ref{ShortID: s}, nil
}

func isCrockfordLower(c byte) bool {
	switch {
	case c >= '0' && c <= '9':
		return true
	case c >= 'a' && c <= 'z':
		switch c {
		case 'i', 'l', 'o', 'u':
			return false
		default:
			return true
		}
	default:
		return false
	}
}
