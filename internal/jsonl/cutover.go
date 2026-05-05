package jsonl

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/wesm/kata/internal/db"
)

// ErrCutoverInProgress means a previous JSONL cutover left temp files behind.
// Operators should inspect/remove those files before retrying so we do not
// overwrite evidence from an interrupted swap.
var ErrCutoverInProgress = errors.New("jsonl cutover in progress")

// AutoCutover upgrades an older SQLite database by exporting it to JSONL,
// importing into a fresh current-schema temp DB, then swapping the temp DB into
// place. Databases already at the current schema are left untouched.
func AutoCutover(ctx context.Context, path string) error {
	tmpJSONL := path + ".import.tmp.jsonl"
	tmpDB := path + ".import.tmp.db"
	if err := rejectCutoverTemps(tmpJSONL, tmpDB); err != nil {
		return err
	}
	version, err := db.PeekSchemaVersion(ctx, path)
	if err != nil {
		return err
	}
	if version >= db.CurrentSchemaVersion() {
		return nil
	}

	cleanupTemps := true
	defer func() {
		if cleanupTemps {
			removeSQLiteFileSet(tmpJSONL)
			removeSQLiteFileSet(tmpDB)
		}
	}()
	if err := exportCutoverSource(ctx, path, tmpJSONL); err != nil {
		return err
	}
	if err := importCutoverTarget(ctx, tmpJSONL, tmpDB); err != nil {
		return err
	}

	backup := fmt.Sprintf("%s.bak.%s", path, time.Now().UTC().Format("20060102T150405Z"))
	if err := os.Rename(path, backup); err != nil {
		return fmt.Errorf("backup source db: %w", err)
	}
	if err := os.Rename(tmpDB, path); err != nil {
		_ = os.Rename(backup, path)
		return fmt.Errorf("install cutover db: %w", err)
	}
	cleanupTemps = false
	removeSQLiteFileSet(tmpJSONL)
	return nil
}

func rejectCutoverTemps(paths ...string) error {
	for _, path := range paths {
		if _, err := os.Stat(path); err == nil {
			return fmt.Errorf("%w: %s exists", ErrCutoverInProgress, path)
		} else if err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("stat cutover temp: %w", err)
		}
	}
	return nil
}

func exportCutoverSource(ctx context.Context, sourcePath, tmpJSONL string) error {
	source, err := db.OpenReadOnly(ctx, sourcePath)
	if err != nil {
		return err
	}
	defer func() { _ = source.Close() }()
	f, err := os.OpenFile(tmpJSONL, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600) //nolint:gosec // tmpJSONL is daemon-controlled state-dir filename
	if err != nil {
		return fmt.Errorf("create cutover jsonl: %w", err)
	}
	if err := Export(ctx, source, f, ExportOptions{IncludeDeleted: true}); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return fmt.Errorf("sync cutover jsonl: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close cutover jsonl: %w", err)
	}
	return nil
}

func importCutoverTarget(ctx context.Context, tmpJSONL, tmpDB string) error {
	target, err := db.Open(ctx, tmpDB)
	if err != nil {
		return err
	}
	defer func() { _ = target.Close() }()
	in, err := os.Open(tmpJSONL) //nolint:gosec // temp path is generated from trusted DB path
	if err != nil {
		return fmt.Errorf("open cutover jsonl: %w", err)
	}
	defer func() { _ = in.Close() }()
	if err := Import(ctx, in, target); err != nil {
		return err
	}
	if _, err := target.ExecContext(ctx,
		`INSERT INTO meta(key, value) VALUES('schema_version', ?)
		 ON CONFLICT(key) DO UPDATE SET value=excluded.value`,
		strconv.Itoa(db.CurrentSchemaVersion())); err != nil {
		return fmt.Errorf("record cutover schema version: %w", err)
	}
	return nil
}

func removeSQLiteFileSet(path string) {
	_ = os.Remove(path)
	_ = os.Remove(path + "-wal")
	_ = os.Remove(path + "-shm")
}
