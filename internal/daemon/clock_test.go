package daemon

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLocalDateBoundary_KnownTZ(t *testing.T) {
	// 2026-05-15 03:00 UTC is still 2026-05-14 in America/New_York (-04:00 DST).
	fixed := time.Date(2026, 5, 15, 3, 0, 0, 0, time.UTC)
	d, err := LocalDateBoundary("America/New_York", fixed)
	require.NoError(t, err)
	assert.Equal(t, "2026-05-14", d)
}

func TestLocalDateBoundary_DefaultUTCOnEmpty(t *testing.T) {
	fixed := time.Date(2026, 5, 15, 3, 0, 0, 0, time.UTC)
	d, err := LocalDateBoundary("", fixed)
	require.NoError(t, err)
	assert.Equal(t, "2026-05-15", d)
}

func TestLocalDateBoundary_InvalidTZReturnsError(t *testing.T) {
	_, err := LocalDateBoundary("Not/Real", time.Now())
	assert.Error(t, err)
}

func TestLocalDateBoundary_DSTBoundary(t *testing.T) {
	// 2026-03-08 06:30 UTC is 2026-03-08 02:30 EST OR 01:30 EDT — the DST
	// boundary. Confirm the date is still "2026-03-08" regardless.
	fixed := time.Date(2026, 3, 8, 6, 30, 0, 0, time.UTC)
	d, err := LocalDateBoundary("America/New_York", fixed)
	require.NoError(t, err)
	assert.Equal(t, "2026-03-08", d)
}
