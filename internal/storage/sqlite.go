package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite"
)

type Store struct {
	db   *sql.DB
	path string
}

func Open(ctx context.Context, path string) (*Store, error) {
	cleanPath := filepath.Clean(strings.TrimSpace(path))
	if cleanPath == "." || cleanPath == "" {
		return nil, fmt.Errorf("storage path is required")
	}

	absPath, err := filepath.Abs(cleanPath)
	if err != nil {
		return nil, fmt.Errorf("resolve storage path %q: %w", cleanPath, err)
	}
	cleanPath = absPath

	if err := os.MkdirAll(filepath.Dir(cleanPath), 0o750); err != nil {
		return nil, fmt.Errorf("create storage directory for %q: %w", cleanPath, err)
	}

	db, err := sql.Open("sqlite", sqliteDSN(cleanPath))
	if err != nil {
		return nil, fmt.Errorf("open storage %q: %w", cleanPath, err)
	}

	if err := db.PingContext(ctx); err != nil {
		pingErr := fmt.Errorf("ping storage %q: %w", cleanPath, err)
		if closeErr := db.Close(); closeErr != nil {
			return nil, errors.Join(pingErr, fmt.Errorf("close storage %q after ping failure: %w", cleanPath, closeErr))
		}
		return nil, pingErr
	}

	if err := initialize(ctx, db); err != nil {
		initErr := fmt.Errorf("initialize storage %q: %w", cleanPath, err)
		if closeErr := db.Close(); closeErr != nil {
			return nil, errors.Join(initErr, fmt.Errorf("close storage %q after init failure: %w", cleanPath, closeErr))
		}
		return nil, initErr
	}

	return &Store{db: db, path: cleanPath}, nil
}

func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}

	return s.db.Close()
}

func (s *Store) Path() string {
	if s == nil {
		return ""
	}

	return s.path
}

func sqliteDSN(path string) string {
	u := &url.URL{Scheme: "file", Path: path}
	query := u.Query()
	query.Add("_pragma", "busy_timeout(5000)")
	query.Add("_pragma", "journal_mode(WAL)")
	query.Add("_pragma", "foreign_keys(ON)")
	u.RawQuery = query.Encode()

	return u.String()
}

func initialize(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer func() {
		if tx != nil {
			_ = tx.Rollback()
		}
	}()

	for _, statement := range []string{
		`
CREATE TABLE IF NOT EXISTS microhook_metadata (
	key TEXT PRIMARY KEY,
	value TEXT NOT NULL
)
`,
		`
CREATE TABLE IF NOT EXISTS microhook_runs (
	id TEXT PRIMARY KEY,
	action_name TEXT NOT NULL,
	status TEXT NOT NULL,
	exit_code INTEGER,
	created_at_unix_nano INTEGER NOT NULL,
	started_at_unix_nano INTEGER,
	finished_at_unix_nano INTEGER,
	timed_out INTEGER NOT NULL DEFAULT 0,
	request_metadata_json BLOB,
	stdout_tail TEXT NOT NULL DEFAULT '',
	stderr_tail TEXT NOT NULL DEFAULT '',
	error_summary TEXT NOT NULL DEFAULT ''
)
`,
		`
CREATE TABLE IF NOT EXISTS microhook_action_snapshots (
	run_id TEXT PRIMARY KEY REFERENCES microhook_runs(id) ON DELETE CASCADE,
	description TEXT NOT NULL,
	mode TEXT NOT NULL,
	command_json BLOB NOT NULL,
	shell_command TEXT NOT NULL,
	cwd TEXT NOT NULL,
	timeout_nanoseconds INTEGER NOT NULL,
	env_json BLOB NOT NULL,
	concurrency_policy TEXT NOT NULL,
	max_output_bytes INTEGER NOT NULL,
	enabled INTEGER NOT NULL
)
`,
		`CREATE INDEX IF NOT EXISTS idx_microhook_runs_action_created ON microhook_runs(action_name, created_at_unix_nano DESC, id DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_microhook_runs_status_created ON microhook_runs(status, created_at_unix_nano DESC, id DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_microhook_runs_created ON microhook_runs(created_at_unix_nano DESC, id DESC)`,
	} {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return err
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit schema bootstrap: %w", err)
	}
	tx = nil

	return nil
}
