// Package jsonl exports and imports kata database state as ordered NDJSON.
package jsonl

import (
	"encoding/json"
	"errors"
)

// Kind is the fixed record kind tag in a JSONL envelope.
type Kind string

// JSONL record kinds. Order matches the export sequence enforced by kindOrder.
const (
	KindMeta           Kind = "meta"
	KindProject        Kind = "project"
	KindProjectAlias   Kind = "project_alias"
	KindIssue          Kind = "issue"
	KindComment        Kind = "comment"
	KindIssueLabel     Kind = "issue_label"
	KindLink           Kind = "link"
	KindImportMapping  Kind = "import_mapping"
	KindEvent          Kind = "event"
	KindPurgeLog       Kind = "purge_log"
	KindSQLiteSequence Kind = "sqlite_sequence"
)

// Sentinel errors returned by the decoder for malformed or out-of-order envelopes.
var (
	ErrMissingExportVersion = errors.New("missing export_version")
	ErrUnknownKind          = errors.New("unknown kind")
	ErrKindOrderViolation   = errors.New("kind order violation")
)

var kindOrder = map[Kind]int{
	KindMeta:           0,
	KindProject:        1,
	KindProjectAlias:   2,
	KindIssue:          3,
	KindComment:        4,
	KindIssueLabel:     5,
	KindLink:           6,
	KindImportMapping:  7,
	KindEvent:          8,
	KindPurgeLog:       9,
	KindSQLiteSequence: 10,
}

// Envelope is one NDJSON record.
type Envelope struct {
	Kind Kind            `json:"kind"`
	Data json.RawMessage `json:"data"`
}

func kindRank(k Kind) (int, bool) {
	rank, ok := kindOrder[k]
	return rank, ok
}
