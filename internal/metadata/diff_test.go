package metadata

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDiffAddedKey(t *testing.T) {
	old := json.RawMessage(`{}`)
	newBlob := json.RawMessage(`{"scheduled_on":"2026-06-01"}`)
	d, err := Diff(old, newBlob)
	require.NoError(t, err)
	require.Contains(t, d, "scheduled_on")
	assert.Nil(t, d["scheduled_on"].From)
	assert.JSONEq(t, `"2026-06-01"`, string(d["scheduled_on"].To))
}

func TestDiffRemovedKey(t *testing.T) {
	old := json.RawMessage(`{"scheduled_on":"2026-05-01"}`)
	newBlob := json.RawMessage(`{}`)
	d, err := Diff(old, newBlob)
	require.NoError(t, err)
	require.Contains(t, d, "scheduled_on")
	assert.JSONEq(t, `"2026-05-01"`, string(d["scheduled_on"].From))
	assert.Nil(t, d["scheduled_on"].To)
}

func TestDiffChangedKey(t *testing.T) {
	old := json.RawMessage(`{"scheduled_on":"2026-05-01"}`)
	newBlob := json.RawMessage(`{"scheduled_on":"2026-06-15"}`)
	d, err := Diff(old, newBlob)
	require.NoError(t, err)
	require.Contains(t, d, "scheduled_on")
	assert.JSONEq(t, `"2026-05-01"`, string(d["scheduled_on"].From))
	assert.JSONEq(t, `"2026-06-15"`, string(d["scheduled_on"].To))
}

func TestDiffUnchangedKeySuppressed(t *testing.T) {
	blob := json.RawMessage(`{"scheduled_on":"2026-05-01"}`)
	d, err := Diff(blob, blob)
	require.NoError(t, err)
	assert.Empty(t, d, "identical blobs should produce an empty diff")
}

func TestDiffNullClearsKey(t *testing.T) {
	old := json.RawMessage(`{"scheduled_on":"2026-05-01"}`)
	newBlob := json.RawMessage(`{"scheduled_on":null}`)
	d, err := Diff(old, newBlob)
	require.NoError(t, err)
	require.Contains(t, d, "scheduled_on")
	assert.JSONEq(t, `"2026-05-01"`, string(d["scheduled_on"].From))
	assert.Nil(t, d["scheduled_on"].To)
}

func TestDiffNullToNullNoOp(t *testing.T) {
	old := json.RawMessage(`{"scheduled_on":null}`)
	newBlob := json.RawMessage(`{"scheduled_on":null}`)
	d, err := Diff(old, newBlob)
	require.NoError(t, err)
	assert.Empty(t, d)
}

func TestDiffAbsentToNullNoOp(t *testing.T) {
	old := json.RawMessage(`{}`)
	newBlob := json.RawMessage(`{"scheduled_on":null}`)
	d, err := Diff(old, newBlob)
	require.NoError(t, err)
	assert.Empty(t, d)
}

func TestDiffEmptyBlobsNoOp(t *testing.T) {
	d, err := Diff(json.RawMessage(`null`), json.RawMessage(`{}`))
	require.NoError(t, err)
	assert.Empty(t, d)
}

func TestDiffMultipleKeys(t *testing.T) {
	old := json.RawMessage(`{"scheduled_on":"2026-05-01","someday":true}`)
	newBlob := json.RawMessage(`{"scheduled_on":"2026-06-01","deadline_on":"2026-07-01"}`)
	d, err := Diff(old, newBlob)
	require.NoError(t, err)

	// scheduled_on changed
	require.Contains(t, d, "scheduled_on")
	assert.JSONEq(t, `"2026-05-01"`, string(d["scheduled_on"].From))
	assert.JSONEq(t, `"2026-06-01"`, string(d["scheduled_on"].To))

	// someday removed
	require.Contains(t, d, "someday")
	assert.JSONEq(t, `true`, string(d["someday"].From))
	assert.Nil(t, d["someday"].To)

	// deadline_on added
	require.Contains(t, d, "deadline_on")
	assert.Nil(t, d["deadline_on"].From)
	assert.JSONEq(t, `"2026-07-01"`, string(d["deadline_on"].To))
}
