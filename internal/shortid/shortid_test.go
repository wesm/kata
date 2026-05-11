package shortid_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wesm/kata/internal/shortid"
)

func TestDeriveTakesLowercaseSuffix(t *testing.T) {
	got, err := shortid.Derive("01HZNQ7VFPK1XGD8R5MABCD4EX", 4)
	require.NoError(t, err)
	assert.Equal(t, "d4ex", got)
}

func TestDeriveLength5(t *testing.T) {
	got, err := shortid.Derive("01HZNQ7VFPK1XGD8R5MABCD4EX", 5)
	require.NoError(t, err)
	assert.Equal(t, "cd4ex", got)
}

func TestDeriveRejectsBadULID(t *testing.T) {
	_, err := shortid.Derive("not-a-ulid", 4)
	assert.ErrorIs(t, err, shortid.ErrInvalidULID)
}

func TestDeriveRejectsLengthOutOfRange(t *testing.T) {
	_, err := shortid.Derive("01HZNQ7VFPK1XGD8R5MABCD4EX", 3)
	assert.ErrorIs(t, err, shortid.ErrLengthOutOfRange)
	_, err = shortid.Derive("01HZNQ7VFPK1XGD8R5MABCD4EX", 27)
	assert.ErrorIs(t, err, shortid.ErrLengthOutOfRange)
}

func TestValidShortIDAcceptsCrockfordLowercase(t *testing.T) {
	assert.True(t, shortid.Valid("abc4"))
	assert.True(t, shortid.Valid("d4ex"))
	assert.True(t, shortid.Valid("xabc4"))
}

func TestValidShortIDRejectsBadAlphabet(t *testing.T) {
	assert.False(t, shortid.Valid("ABC4")) // uppercase
	assert.False(t, shortid.Valid("ilou")) // disallowed Crockford letters
	assert.False(t, shortid.Valid("ab-4")) // non-alphabet char
	assert.False(t, shortid.Valid(""))
	assert.False(t, shortid.Valid("abc"))                           // too short
	assert.False(t, shortid.Valid("01234567890123456789012345678")) // too long
}

func TestParseQualified(t *testing.T) {
	r, err := shortid.Parse("kata#abc4")
	require.NoError(t, err)
	assert.Equal(t, "kata", r.Project)
	assert.Equal(t, "abc4", r.ShortID)
	assert.Empty(t, r.ULID)
}

func TestParseBare(t *testing.T) {
	r, err := shortid.Parse("abc4")
	require.NoError(t, err)
	assert.Empty(t, r.Project)
	assert.Equal(t, "abc4", r.ShortID)
	assert.Empty(t, r.ULID)
}

func TestParseULID(t *testing.T) {
	r, err := shortid.Parse("01HZNQ7VFPK1XGD8R5MABCD4EX")
	require.NoError(t, err)
	assert.Empty(t, r.Project)
	assert.Empty(t, r.ShortID)
	assert.Equal(t, "01HZNQ7VFPK1XGD8R5MABCD4EX", r.ULID)
}

func TestParseULID_NormalizesLowercaseToUppercase(t *testing.T) {
	// ULIDs are canonically uppercase Crockford. A user pasting a
	// lowercase 26-char ULID (e.g. from a comment body that's already
	// been lowercased) must still resolve as a ULID ref, not a 26-char
	// short_id — otherwise project-scoped lookup would fail because no
	// stored short_id matches the uppercase ULID's casing.
	r, err := shortid.Parse("01hznq7vfpk1xgd8r5mabcd4ex")
	require.NoError(t, err)
	assert.Empty(t, r.Project)
	assert.Empty(t, r.ShortID)
	assert.Equal(t, "01HZNQ7VFPK1XGD8R5MABCD4EX", r.ULID,
		"lowercase ULID must normalize to uppercase")
}

func TestParseQualifiedWithMultipleHashes(t *testing.T) {
	r, err := shortid.Parse("my#proj#abc4")
	require.NoError(t, err)
	assert.Equal(t, "my#proj", r.Project)
	assert.Equal(t, "abc4", r.ShortID)
}

func TestParseRejectsShortLegacyNumber(t *testing.T) {
	_, err := shortid.Parse("12")
	assert.ErrorIs(t, err, shortid.ErrInvalidRef)
	_, err = shortid.Parse("kata#12")
	assert.ErrorIs(t, err, shortid.ErrInvalidRef)
}

// At or above MinLength, all-digit refs are accepted as candidate short_ids.
// They resolve at the DB layer — usually NotFound unless an issue's ULID
// happens to end in 4+ digits. This documents the boundary so reviewers
// don't mistake "Parse accepts 1234" for legacy-number routing.
func TestParseAcceptsLongAllDigitAsShortID(t *testing.T) {
	r, err := shortid.Parse("1234")
	require.NoError(t, err)
	assert.Equal(t, "1234", r.ShortID)
	assert.Empty(t, r.Project)

	r, err = shortid.Parse("kata#1234")
	require.NoError(t, err)
	assert.Equal(t, "kata", r.Project)
	assert.Equal(t, "1234", r.ShortID)
}

func TestParseRejectsEmpty(t *testing.T) {
	_, err := shortid.Parse("")
	assert.ErrorIs(t, err, shortid.ErrInvalidRef)
}

func TestParseRejectsEmptyProjectBeforeHash(t *testing.T) {
	_, err := shortid.Parse("#abc4")
	assert.ErrorIs(t, err, shortid.ErrInvalidRef)
}
