package db_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wesm/kata/internal/db"
	"github.com/wesm/kata/internal/uid"
)

func TestFingerprint_DeterministicOverInputOrder(t *testing.T) {
	owner := "alice"
	a := db.Fingerprint("fix login", "details", &owner,
		[]string{"bug", "ui"},
		[]db.InitialLink{{Type: "blocks", ToNumber: 7}, {Type: "parent", ToNumber: 3}})
	b := db.Fingerprint("fix login", "details", &owner,
		[]string{"ui", "bug"}, // labels reordered
		[]db.InitialLink{{Type: "parent", ToNumber: 3}, {Type: "blocks", ToNumber: 7}}) // links reordered
	assert.Equal(t, a, b, "fingerprint must be order-independent for labels and links")
}

func TestFingerprint_CanonicalizesWhitespace(t *testing.T) {
	a := db.Fingerprint("fix login", "body text", nil, nil, nil)
	b := db.Fingerprint("  fix\t\n  login  ", "body  text", nil, nil, nil)
	assert.Equal(t, a, b, "internal whitespace runs and trimming must collapse")
}

func TestFingerprint_DiffersOnDifferentInputs(t *testing.T) {
	base := db.Fingerprint("a", "b", nil, nil, nil)
	cases := []struct {
		name        string
		fingerprint string
	}{
		{"different_title", db.Fingerprint("aa", "b", nil, nil, nil)},
		{"different_body", db.Fingerprint("a", "bb", nil, nil, nil)},
		{"different_owner", db.Fingerprint("a", "b", strPtr("x"), nil, nil)},
		{"different_labels", db.Fingerprint("a", "b", nil, []string{"bug"}, nil)},
		{"different_links", db.Fingerprint("a", "b", nil, nil,
			[]db.InitialLink{{Type: "blocks", ToNumber: 1}})},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.NotEqual(t, base, tc.fingerprint)
		})
	}
}

func TestFingerprint_CaseSensitive(t *testing.T) {
	// Spec §3.6: canonical() does NOT lowercase. Title casing matters.
	a := db.Fingerprint("Fix Login", "", nil, nil, nil)
	b := db.Fingerprint("fix login", "", nil, nil, nil)
	assert.NotEqual(t, a, b)
}

func TestFingerprint_NilAndEmptyOwnerAreEquivalent(t *testing.T) {
	empty := ""
	a := db.Fingerprint("a", "b", nil, nil, nil)
	b := db.Fingerprint("a", "b", &empty, nil, nil)
	assert.Equal(t, a, b, "nil owner and empty owner produce the same fingerprint")
}

func TestFingerprint_HexLowercaseSHA256(t *testing.T) {
	got := db.Fingerprint("a", "b", nil, nil, nil)
	assert.Len(t, got, 64, "sha256 hex is 64 chars")
	assert.True(t, strings.ToLower(got) == got, "must be lowercase hex")
}

// TestFingerprint_Vector pins exact hex outputs so any change to the canonical
// byte layout, separator order, JSON shape, sort order, or Canonical()
// behavior immediately breaks the test. This is the cross-language contract.
func TestFingerprint_Vector(t *testing.T) {
	// All-empty inputs: title=\nbody=\nowner=\nlabels=\nlinks=[]
	assert.Equal(t,
		"3e3678620b59364a3d56c8608ff431933b042a8619e74892243b0d2bfdb09af2",
		db.Fingerprint("", "", nil, nil, nil),
		"empty-everything fingerprint must not drift")

	// Filled: one label, one parent link.
	assert.Equal(t,
		"2c77531b9b3e7522ccf86eb353fc2aaa8cd8418e1132c8ebb1f2f80ea1dca8db",
		db.Fingerprint("hello", "world", nil, []string{"bug"},
			[]db.InitialLink{{Type: "parent", ToNumber: 3}}),
		"filled fingerprint must not drift")
}

func TestLookupIdempotency_ReturnsMatchWithinWindow(t *testing.T) {
	// Hand-write an issue.created event with idempotency_key + fingerprint
	// in the payload so we test LookupIdempotency in isolation from the
	// CreateIssue extension landing in Task 9.
	d, ctx, p, issue := setupTestIssue(t)
	fp := "abc123"
	injectIdempotencyKey(ctx, t, d, issue.ID, "K1", fp)

	since := time.Now().Add(-1 * time.Hour)
	got, err := d.LookupIdempotency(ctx, p.ID, "K1", since)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, issue.ID, got.IssueID)
	assert.Equal(t, issue.Number, got.IssueNumber)
	assert.Equal(t, fp, got.Fingerprint)
	assert.Equal(t, "issue.created", got.Event.Type)
}

func TestLookupIdempotency_OutsideWindowIsNil(t *testing.T) {
	d, ctx, p, issue := setupTestIssue(t)
	injectIdempotencyKey(ctx, t, d, issue.ID, "K1", "fp")

	// Window starts in the future — every existing event is "outside".
	future := time.Now().Add(1 * time.Hour)
	got, err := d.LookupIdempotency(ctx, p.ID, "K1", future)
	require.NoError(t, err)
	assert.Nil(t, got)
}

func TestLookupIdempotency_DifferentKeyIsNil(t *testing.T) {
	d, ctx, p := setupTestProject(t)
	got, err := d.LookupIdempotency(ctx, p.ID, "no-such-key", time.Now().Add(-1*time.Hour))
	require.NoError(t, err)
	assert.Nil(t, got)
}

func TestLookupIdempotency_DifferentProjectIsNil(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p1 := createProject(ctx, t, d, "p1", "p1")
	p2 := createProject(ctx, t, d, "p2", "p2")
	issue, _ := createTesterIssue(ctx, t, d, p1.ID, "x")
	injectIdempotencyKey(ctx, t, d, issue.ID, "K1", "fp")

	got, err := d.LookupIdempotency(ctx, p2.ID, "K1", time.Now().Add(-1*time.Hour))
	require.NoError(t, err)
	assert.Nil(t, got, "key in p1 must not match a lookup in p2")
}

// TestLookupIdempotency_OnlyIssueCreatedEvents ensures that an idempotency_key
// in the payload of a non-issue.created event (e.g. issue.edited) is never
// returned. The partial index already enforces this; the SQL WHERE clause
// reinforces it. This test locks both layers.
func TestLookupIdempotency_OnlyIssueCreatedEvents(t *testing.T) {
	d, ctx, p, issue := setupTestIssue(t)
	// Stamp the idempotency_key onto a NON-issue.created row by inserting a
	// fake issue.edited event. The partial index excludes this row by type;
	// the WHERE e.type = 'issue.created' clause reinforces.
	eventUID, err := uid.New()
	require.NoError(t, err)
	_, err = d.ExecContext(ctx, `
		INSERT INTO events (uid, origin_instance_uid, project_id, project_identity, issue_id, issue_number, type, actor, payload, created_at)
		VALUES (?, (SELECT value FROM meta WHERE key='instance_uid'), ?, ?, ?, ?, 'issue.edited', 'tester',
		        json_object('idempotency_key', 'K1', 'idempotency_fingerprint', 'fp'),
		        strftime('%Y-%m-%dT%H:%M:%fZ','now'))`,
		eventUID, p.ID, p.Identity, issue.ID, issue.Number)
	require.NoError(t, err)

	got, err := d.LookupIdempotency(ctx, p.ID, "K1", time.Now().Add(-1*time.Hour))
	require.NoError(t, err)
	assert.Nil(t, got, "non-issue.created event with same key must not match")
}
