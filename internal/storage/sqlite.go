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
	if _, err := db.ExecContext(ctx, "PRAGMA user_version = 1"); err != nil {
		return err
	}

	if _, err := db.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS microhook_metadata (
	key TEXT PRIMARY KEY,
	value TEXT NOT NULL
)
`); err != nil {
		return err
	}

	return nil
}
