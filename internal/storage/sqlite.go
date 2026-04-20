package storage

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	storedb "github.com/nicotsx/microhook/internal/storage/sqlc"
	_ "modernc.org/sqlite"
)

//go:embed sql/storage_schemas.sql
var storageSchemaSQL string

type Store struct {
	db      *sql.DB
	queries *storedb.Queries
	path    string
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

	return &Store{db: db, queries: storedb.New(db), path: cleanPath}, nil
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

	if _, err := tx.ExecContext(ctx, storageSchemaSQL); err != nil {
		return fmt.Errorf("apply schema bootstrap: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit schema bootstrap: %w", err)
	}
	tx = nil

	return nil
}
