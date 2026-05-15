package metadata

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
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
