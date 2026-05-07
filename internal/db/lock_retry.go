package db

import (
	"context"
	"errors"
	"time"

	backoff "github.com/cenkalti/backoff/v5"
	sqlite3 "modernc.org/sqlite/lib"
)

const (
	defaultInitialLockBackoff = time.Millisecond
	defaultMaxLockBackoff     = time.Second
	defaultMaxLockRetryTime   = 30 * time.Second
	sqlitePrimaryCodeMask     = 0xff
)

type sqliteCodeError interface {
	Code() int
}

type lockRetryConfig struct {
	maxElapsed time.Duration
	newBackOff func() backoff.BackOff
}

// IsLockContention reports whether err is a SQLite busy/locked condition that
// may clear if the whole mutation is retried after a short delay.
func IsLockContention(err error) bool {
	var coded sqliteCodeError
	if !errors.As(err, &coded) {
		return false
	}
	switch coded.Code() & sqlitePrimaryCodeMask {
	case sqlite3.SQLITE_BUSY, sqlite3.SQLITE_LOCKED:
		return true
	default:
		return false
	}
}

// RetryLockContention retries operation when SQLite reports lock contention.
// The operation must be safe to re-run as a whole; callers should not wrap only
// a single statement inside an already-open transaction.
func RetryLockContention(ctx context.Context, operation func() error) error {
	return retryLockContention(ctx, lockRetryConfig{
		maxElapsed: defaultMaxLockRetryTime,
		newBackOff: newLockBackOff,
	}, operation)
}

func retryLockContention(ctx context.Context, cfg lockRetryConfig, operation func() error) error {
	cfg = normalizedLockRetryConfig(cfg)
	_, err := backoff.Retry(ctx, func() (struct{}, error) {
		err := operation()
		if err == nil {
			return struct{}{}, nil
		}
		if !IsLockContention(err) {
			return struct{}{}, backoff.Permanent(err)
		}
		return struct{}{}, err
	}, backoff.WithBackOff(cfg.newBackOff()), backoff.WithMaxElapsedTime(cfg.maxElapsed))
	return err
}

func normalizedLockRetryConfig(cfg lockRetryConfig) lockRetryConfig {
	if cfg.maxElapsed <= 0 {
		cfg.maxElapsed = defaultMaxLockRetryTime
	}
	if cfg.newBackOff == nil {
		cfg.newBackOff = newLockBackOff
	}
	return cfg
}

type lockBackOff struct {
	inner backoff.BackOff
	max   time.Duration
}

func newLockBackOff() backoff.BackOff {
	bo := backoff.NewExponentialBackOff()
	bo.InitialInterval = defaultInitialLockBackoff
	bo.MaxInterval = defaultMaxLockBackoff
	bo.Multiplier = 2
	bo.RandomizationFactor = 0.5
	bo.Reset()
	return &lockBackOff{inner: bo, max: defaultMaxLockBackoff}
}

func (b *lockBackOff) NextBackOff() time.Duration {
	d := b.inner.NextBackOff()
	if d > b.max {
		return b.max
	}
	return d
}

func (b *lockBackOff) Reset() {
	b.inner.Reset()
}
