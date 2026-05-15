// Package metadata defines the whitelisted set of keys allowed inside
// issues.metadata and projects.metadata JSON blobs, plus their value types.
//
// Adding a new key:
//  1. Add an entry below.
//  2. (Optional) Add an SQLite expression index in internal/db/schema.sql.
package metadata

// Type describes the expected value type for a metadata key.
type Type int

const (
	TypeUnknown      Type = iota
	TypeDate              // "YYYY-MM-DD"
	TypeBool              // true / false
	TypeEnum              // string limited to a closed set
	TypeString            // free-form string
	TypeInt               // integer
	TypeChecklist         // array of {id: ULID, text: string, done: bool}
	TypeTimezoneIANA      // IANA timezone string
)

// Entry describes one whitelisted metadata key.
type Entry struct {
	Type Type
	Enum []string // populated only for TypeEnum
}

// IssueRegistry is the whitelisted set of keys for issues.metadata.
var IssueRegistry = map[string]Entry{
	"scheduled_on": {Type: TypeDate},
	"deadline_on":  {Type: TypeDate},
	"someday":      {Type: TypeBool},
	"today_bucket": {Type: TypeEnum, Enum: []string{"day", "evening"}},
	"checklist":    {Type: TypeChecklist},
	"timezone":     {Type: TypeTimezoneIANA},
}

// ProjectRegistry is the whitelisted set of keys for projects.metadata.
var ProjectRegistry = map[string]Entry{
	"area":          {Type: TypeString},
	"sidebar_order": {Type: TypeInt},
	"icon":          {Type: TypeString},
	"timezone":      {Type: TypeTimezoneIANA},
}
