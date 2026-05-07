package db

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	backoff "github.com/cenkalti/backoff/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	sqlite3 "modernc.org/sqlite/lib"
)

type codedSQLiteErr int

func (e codedSQLiteErr) Error() string { return "sqlite error" }
func (e codedSQLiteErr) Code() int     { return int(e) }

func TestIsLockContentionRecognizesSQLiteBusyAndLocked(t *testing.T) {
	assert.True(t, IsLockContention(codedSQLiteErr(sqlite3.SQLITE_BUSY)))
	assert.True(t, IsLockContention(codedSQLiteErr(sqlite3.SQLITE_LOCKED)))
	assert.True(t, IsLockContention(fmt.Errorf("wrapped: %w",
		codedSQLiteErr(sqlite3.SQLITE_LOCKED|(1<<8)))))
	assert.False(t, IsLockContention(codedSQLiteErr(sqlite3.SQLITE_CONSTRAINT)))
	assert.False(t, IsLockContention(errors.New("database is locked")))
}

func TestRetryLockContentionRetriesLockContentionUntilSuccess(t *testing.T) {
	attempts := 0

	err := retryLockContention(context.Background(), lockRetryConfig{
		maxElapsed: time.Second,
		newBackOff: func() backoff.BackOff {
			return &backoff.ZeroBackOff{}
		},
	}, func() error {
		attempts++
		if attempts < 4 {
			return fmt.Errorf("reopen: %w", codedSQLiteErr(sqlite3.SQLITE_LOCKED))
		}
		return nil
	})

	require.NoError(t, err)
	assert.Equal(t, 4, attempts)
}

func TestRetryLockContentionDoesNotRetryOtherErrors(t *testing.T) {
	wantErr := errors.New("constraint failed")
	attempts := 0

	err := retryLockContention(context.Background(), lockRetryConfig{
		maxElapsed: time.Second,
		newBackOff: func() backoff.BackOff {
			return &backoff.ZeroBackOff{}
		},
	}, func() error {
		attempts++
		return wantErr
	})

	assert.ErrorIs(t, err, wantErr)
	assert.Equal(t, 1, attempts)
}

func TestNewLockBackOffUsesSmallJitteredPolicy(t *testing.T) {
	got, ok := newLockBackOff().(*lockBackOff)
	require.True(t, ok)
	inner, ok := got.inner.(*backoff.ExponentialBackOff)
	require.True(t, ok)

	assert.Equal(t, time.Millisecond, inner.InitialInterval)
	assert.Equal(t, time.Second, inner.MaxInterval)
	assert.Equal(t, 2.0, inner.Multiplier)
	assert.Equal(t, 0.5, inner.RandomizationFactor)
	assert.Equal(t, time.Second, got.max)
}

func TestLockBackOffCapsRandomizedDelayAtOneSecond(t *testing.T) {
	bo := &lockBackOff{
		inner: &sequenceBackOff{next: []time.Duration{
			2 * time.Second,
		}},
		max: time.Second,
	}

	assert.Equal(t, time.Second, bo.NextBackOff())
}

type sequenceBackOff struct {
	next []time.Duration
}

func (b *sequenceBackOff) NextBackOff() time.Duration {
	if len(b.next) == 0 {
		return backoff.Stop
	}
	d := b.next[0]
	b.next = b.next[1:]
	return d
}

func (b *sequenceBackOff) Reset() {}
