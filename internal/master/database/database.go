// Package database opens the SQLite database of the master and keeps its schema up to date.
package database

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"

	_ "modernc.org/sqlite" // pure Go driver, keeps the master a static binary
)

//go:embed migrations/*.sql
var migrations embed.FS

// maxConns bounds the connections of the pool: readers share the write-ahead log, while
// writers wait for each other anyway.
const maxConns = 8

// Open opens the database at path and applies all pending migrations. The space of deleted
// rows is reused, and the write-ahead log shrinks back to 16 MiB after a large transaction,
// such as deleting many log entries.
//
// Transactions take the write lock as they begin, so that one that reads before it writes
// waits up to the busy timeout for another writer instead of failing with "database is
// locked", which SQLite answers when a reading transaction can't become a writing one.
// Read-only transactions don't.
func Open(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?_txlock=immediate&_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=journal_size_limit(16777216)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(maxConns)
	db.SetMaxIdleConns(maxConns)
	if err := migrate(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate database: %w", err)
	}
	return db, nil
}

// migrate runs every migration newer than the schema version stored in user_version.
func migrate(db *sql.DB) error {
	files, err := fs.Glob(migrations, "migrations/*.sql")
	if err != nil {
		return err
	}
	var version int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if version > len(files) {
		return fmt.Errorf("schema version %d is newer than this binary", version)
	}
	for ; version < len(files); version++ {
		script, err := migrations.ReadFile(files[version])
		if err != nil {
			return err
		}
		tx, err := db.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(fmt.Sprintf("%s;\nPRAGMA user_version = %d", script, version+1)); err != nil {
			return errors.Join(fmt.Errorf("%s: %w", files[version], err), tx.Rollback())
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// InTx runs fn in a transaction, which is committed if fn succeeds and rolled back otherwise.
func InTx(ctx context.Context, db *sql.DB, fn func(*sql.Tx) error) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		if rollbackErr := tx.Rollback(); rollbackErr != nil {
			return errors.Join(err, rollbackErr)
		}
		return err
	}
	return tx.Commit()
}

// RowsAffected returns how many rows a statement changed, which SQLite always tells.
func RowsAffected(res sql.Result) int64 {
	n, _ := res.RowsAffected()
	return n
}

// Clip shortens a text to at most n bytes and an ellipsis, at the start of a character, e.g. an
// error to what is kept of it.
func Clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && s[n]&0xC0 == 0x80 {
		n--
	}
	return s[:n] + "…"
}

// Scanner is the row of a query, or the current one of several, e.g. for a function that
// scans both.
type Scanner interface{ Scan(dest ...any) error }

// IDs returns the IDs that a query selects, e.g. to find references to what was deleted.
func IDs(ctx context.Context, db *sql.DB, query string) (map[string]bool, error) {
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids[id] = true
	}
	return ids, rows.Err()
}
