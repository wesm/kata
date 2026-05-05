package db_test

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wesm/kata/internal/db"
)

func TestCreateIssue_WithInitialLabels(t *testing.T) {
	d, ctx, p := setupTestProject(t)

	issue, evt, err := d.CreateIssue(ctx, db.CreateIssueParams{
		ProjectID: p.ID, Title: "x", Author: "tester",
		Labels: []string{"bug", "priority:high", "bug" /* dupe */},
	})
	require.NoError(t, err)
	assert.Equal(t, "issue.created", evt.Type)

	labels, err := d.LabelsByIssue(ctx, issue.ID)
	require.NoError(t, err)
	got := []string{}
	for _, l := range labels {
		got = append(got, l.Label)
	}
	assert.ElementsMatch(t, []string{"bug", "priority:high"}, got, "duplicates deduplicated")

	// Payload includes initial labels (sorted, deduplicated).
	payload := unmarshalPayload[struct {
		Labels []string `json:"labels"`
	}](t, evt.Payload)
	assert.Equal(t, []string{"bug", "priority:high"}, payload.Labels)
}

func TestCreateIssue_WithInitialOwner(t *testing.T) {
	d, ctx, p := setupTestProject(t)

	owner := "alice"
	issue, evt, err := d.CreateIssue(ctx, db.CreateIssueParams{
		ProjectID: p.ID, Title: "x", Author: "tester",
		Owner: &owner,
	})
	require.NoError(t, err)
	require.NotNil(t, issue.Owner)
	assert.Equal(t, "alice", *issue.Owner)

	payload := unmarshalPayload[struct {
		Owner string `json:"owner"`
	}](t, evt.Payload)
	assert.Equal(t, "alice", payload.Owner)
}

func TestCreateIssue_WithInitialLinks(t *testing.T) {
	d, ctx, p := setupTestProject(t)
	parent := makeIssue(t, ctx, d, p.ID, "parent", "tester")
	blocker := makeIssue(t, ctx, d, p.ID, "blocker", "tester")

	child, evt, err := d.CreateIssue(ctx, db.CreateIssueParams{
		ProjectID: p.ID, Title: "child", Author: "tester",
		Links: []db.InitialLink{
			{Type: "parent", ToNumber: parent.Number},
			{Type: "blocks", ToNumber: blocker.Number},
		},
	})
	require.NoError(t, err)

	// DB state: 2 link rows from child.
	links, err := d.LinksByIssue(ctx, child.ID)
	require.NoError(t, err)
	assert.Len(t, links, 2)

	// Payload references to_number, not to_issue_id.
	payload := unmarshalPayload[struct {
		Links []struct {
			Type     string `json:"type"`
			ToNumber int64  `json:"to_number"`
		} `json:"links"`
	}](t, evt.Payload)
	require.Len(t, payload.Links, 2)
}

func TestCreateIssue_RejectsInitialLinkToMissingTarget(t *testing.T) {
	d, ctx, p := setupTestProject(t)

	_, _, err := d.CreateIssue(ctx, db.CreateIssueParams{
		ProjectID: p.ID, Title: "x", Author: "tester",
		Links: []db.InitialLink{{Type: "parent", ToNumber: 999}},
	})
	assert.True(t, errors.Is(err, db.ErrInitialLinkTargetNotFound),
		"expected ErrInitialLinkTargetNotFound, got %v", err)
}

func TestCreateIssue_RejectsInvalidInitialLinkType(t *testing.T) {
	d, ctx, p := setupTestProject(t)
	target := makeIssue(t, ctx, d, p.ID, "t", "tester")

	_, _, err := d.CreateIssue(ctx, db.CreateIssueParams{
		ProjectID: p.ID, Title: "x", Author: "tester",
		Links: []db.InitialLink{{Type: "child", ToNumber: target.Number}},
	})
	assert.True(t, errors.Is(err, db.ErrInitialLinkInvalidType))
}

func TestCreateIssue_RejectsInvalidLabel(t *testing.T) {
	d, ctx, p := setupTestProject(t)

	_, _, err := d.CreateIssue(ctx, db.CreateIssueParams{
		ProjectID: p.ID, Title: "x", Author: "tester",
		Labels: []string{"BadCase"},
	})
	assert.True(t, errors.Is(err, db.ErrLabelInvalid))
}

func TestCreateIssue_NoInitialStateEmitsEmptyPayload(t *testing.T) {
	d, ctx, p := setupTestProject(t)

	_, evt, err := d.CreateIssue(ctx, db.CreateIssueParams{
		ProjectID: p.ID, Title: "x", Author: "tester",
	})
	require.NoError(t, err)
	assert.Equal(t, "{}", evt.Payload)
}

func TestCreateIssue_DuplicateInitialLinksAreDeduped(t *testing.T) {
	d, ctx, p := setupTestProject(t)
	parent := makeIssue(t, ctx, d, p.ID, "parent", "tester")

	child, evt, err := d.CreateIssue(ctx, db.CreateIssueParams{
		ProjectID: p.ID, Title: "child", Author: "tester",
		Links: []db.InitialLink{
			{Type: "parent", ToNumber: parent.Number},
			{Type: "parent", ToNumber: parent.Number}, // exact dup
		},
	})
	require.NoError(t, err, "duplicate initial links must not roll back")

	// Only one link row inserted.
	links, err := d.LinksByIssue(ctx, child.ID)
	require.NoError(t, err)
	assert.Len(t, links, 1)

	// Payload reflects the deduped list.
	payload := unmarshalPayload[struct {
		Links []struct {
			Type     string `json:"type"`
			ToNumber int64  `json:"to_number"`
		} `json:"links"`
	}](t, evt.Payload)
	assert.Len(t, payload.Links, 1)
}

func TestCreateIssue_EmptyStringOwnerNormalizesToNil(t *testing.T) {
	d, ctx, p := setupTestProject(t)

	empty := ""
	issue, evt, err := d.CreateIssue(ctx, db.CreateIssueParams{
		ProjectID: p.ID, Title: "x", Author: "tester",
		Owner: &empty,
	})
	require.NoError(t, err)
	assert.Nil(t, issue.Owner, "empty-string owner must persist as NULL")
	assert.Equal(t, "{}", evt.Payload, "payload must agree: no owner")
}

func TestCreateIssue_WithAllInitialState(t *testing.T) {
	d, ctx, p := setupTestProject(t)
	parent := makeIssue(t, ctx, d, p.ID, "parent", "tester")

	owner := "alice"
	issue, evt, err := d.CreateIssue(ctx, db.CreateIssueParams{
		ProjectID: p.ID, Title: "child", Author: "tester",
		Labels: []string{"bug", "priority:high"},
		Links:  []db.InitialLink{{Type: "parent", ToNumber: parent.Number}},
		Owner:  &owner,
	})
	require.NoError(t, err)

	// Issue carries the owner.
	require.NotNil(t, issue.Owner)
	assert.Equal(t, "alice", *issue.Owner)

	// DB has 2 labels + 1 link.
	labels, err := d.LabelsByIssue(ctx, issue.ID)
	require.NoError(t, err)
	assert.Len(t, labels, 2)
	links, err := d.LinksByIssue(ctx, issue.ID)
	require.NoError(t, err)
	assert.Len(t, links, 1)

	// Payload union: labels, links, and owner all present.
	payload := unmarshalPayload[struct {
		Labels []string `json:"labels"`
		Links  []struct {
			Type     string `json:"type"`
			ToNumber int64  `json:"to_number"`
		} `json:"links"`
		Owner string `json:"owner"`
	}](t, evt.Payload)
	assert.Equal(t, []string{"bug", "priority:high"}, payload.Labels)
	require.Len(t, payload.Links, 1)
	assert.Equal(t, "parent", payload.Links[0].Type)
	assert.Equal(t, parent.Number, payload.Links[0].ToNumber)
	assert.Equal(t, "alice", payload.Owner)
}
