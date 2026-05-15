package metadata

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 26-char ULIDs from the Crockford alphabet.
const goodULID1 = "01ARZ3NDEKTSV4RRFFQ69G5FAV"
const goodULID2 = "01ARZ3NDEKTSV4RRFFQ69G5FAW"

func TestValidateDate(t *testing.T) {
	assert.NoError(t, Validate(IssueRegistry, "scheduled_on", json.RawMessage(`"2026-05-20"`)))
	assert.Error(t, Validate(IssueRegistry, "scheduled_on", json.RawMessage(`"not-a-date"`)))
	assert.Error(t, Validate(IssueRegistry, "scheduled_on", json.RawMessage(`"2026-13-01"`)))
	assert.Error(t, Validate(IssueRegistry, "scheduled_on", json.RawMessage(`123`)))
}

func TestValidateBool(t *testing.T) {
	assert.NoError(t, Validate(IssueRegistry, "someday", json.RawMessage(`true`)))
	assert.NoError(t, Validate(IssueRegistry, "someday", json.RawMessage(`false`)))
	assert.Error(t, Validate(IssueRegistry, "someday", json.RawMessage(`"true"`)))
}

func TestValidateEnum(t *testing.T) {
	assert.NoError(t, Validate(IssueRegistry, "today_bucket", json.RawMessage(`"day"`)))
	assert.NoError(t, Validate(IssueRegistry, "today_bucket", json.RawMessage(`"evening"`)))
	assert.Error(t, Validate(IssueRegistry, "today_bucket", json.RawMessage(`"midnight"`)))
}

func TestValidateChecklist(t *testing.T) {
	good := json.RawMessage(fmt.Sprintf(`[
		{"id":%q,"text":"draft","done":false},
		{"id":%q,"text":"ship","done":true}
	]`, goodULID1, goodULID2))
	assert.NoError(t, Validate(IssueRegistry, "checklist", good))

	badULID := json.RawMessage(`[{"id":"oops","text":"t","done":false}]`)
	assert.Error(t, Validate(IssueRegistry, "checklist", badULID))

	missingText := json.RawMessage(fmt.Sprintf(`[{"id":%q,"done":false}]`, goodULID1))
	assert.Error(t, Validate(IssueRegistry, "checklist", missingText))
}

func TestValidateChecklist_MissingDoneRejected(t *testing.T) {
	// Item has id and text but no "done" field — must be rejected.
	noDone := json.RawMessage(fmt.Sprintf(`[{"id":%q,"text":"x"}]`, goodULID1))
	err := Validate(IssueRegistry, "checklist", noDone)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidValue)
	assert.Contains(t, err.Error(), "done required")
}

func TestValidateTimezone(t *testing.T) {
	assert.NoError(t, Validate(IssueRegistry, "timezone", json.RawMessage(`"America/New_York"`)))
	assert.Error(t, Validate(IssueRegistry, "timezone", json.RawMessage(`"NotAReal/Zone"`)))
}

func TestValidateUnknownKey(t *testing.T) {
	err := Validate(IssueRegistry, "definitely_not_a_key", json.RawMessage(`null`))
	assert.ErrorIs(t, err, ErrUnknownKey)
}

func TestValidateNullClears(t *testing.T) {
	assert.NoError(t, Validate(IssueRegistry, "scheduled_on", json.RawMessage(`null`)))
	assert.NoError(t, Validate(IssueRegistry, "checklist", json.RawMessage(`null`)))
}

// TestValidate_AllErrorsWrapErrInvalidValue locks in the invariant that every
// validator failure is detectable via errors.Is(err, ErrInvalidValue). Handlers
// translate this to a 400 response; if a validator returns a plain error,
// invalid client input falls through to a 500.
func TestValidate_AllErrorsWrapErrInvalidValue(t *testing.T) {
	cases := []struct {
		name     string
		registry map[string]Entry
		key      string
		raw      json.RawMessage
	}{
		{"date_wrong_type", IssueRegistry, "scheduled_on", json.RawMessage(`123`)},
		{"date_malformed", IssueRegistry, "scheduled_on", json.RawMessage(`"not-a-date"`)},
		{"bool_wrong_type", IssueRegistry, "someday", json.RawMessage(`"yes"`)},
		{"enum_wrong_type", IssueRegistry, "today_bucket", json.RawMessage(`123`)},
		{"enum_not_in_set", IssueRegistry, "today_bucket", json.RawMessage(`"midnight"`)},
		{"timezone_wrong_type", IssueRegistry, "timezone", json.RawMessage(`123`)},
		{"timezone_bogus", IssueRegistry, "timezone", json.RawMessage(`"Not/Real"`)},
		{"project_string_wrong_type", ProjectRegistry, "area", json.RawMessage(`123`)},
		{"project_int_wrong_type", ProjectRegistry, "sidebar_order", json.RawMessage(`"first"`)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := Validate(c.registry, c.key, c.raw)
			assert.ErrorIs(t, err, ErrInvalidValue,
				"validator must wrap ErrInvalidValue so handlers map to 400")
		})
	}
}
