// Package storage is the SQLite archive (pure Go driver, no CGO).
package storage

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

type Store struct{ db *sql.DB }

// Open opens (creating if needed) the archive at path and applies pending migrations.
func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	dsn := "file:" + path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // one writer: sync and downloader share the store without SQLITE_BUSY
	s := &Store{db: db}
	if err := s.migrate(context.Background()); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

// migrate applies migrations/NNN_*.sql in order, tracking the last one in PRAGMA user_version.
func (s *Store) migrate(ctx context.Context) error {
	files, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		return err
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Name() < files[j].Name() })
	var cur int
	if err := s.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&cur); err != nil {
		return err
	}
	for _, f := range files {
		n, err := strconv.Atoi(strings.SplitN(f.Name(), "_", 2)[0])
		if err != nil {
			return fmt.Errorf("bad migration name %q", f.Name())
		}
		if n <= cur {
			continue
		}
		body, err := migrationsFS.ReadFile("migrations/" + f.Name())
		if err != nil {
			return err
		}
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		// PRAGMA user_version cannot be parameterized; n is an int parsed above.
		if _, err := tx.ExecContext(ctx, string(body)+fmt.Sprintf("\nPRAGMA user_version = %d;", n)); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %s: %w", f.Name(), err)
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}
