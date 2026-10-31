package database

import (
	"context"
	"database/sql"
	"strings"
	"unicode/utf8"
)

// InTx runs fn in a transaction, which is committed if fn succeeds and rolled back otherwise.
func InTx(ctx context.Context, db *sql.DB, fn func(*sql.Tx) error) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // a no-op after Commit
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

// RowsAffected returns how many rows a statement changed; SQLite always tells.
func RowsAffected(res sql.Result) int64 {
	n, _ := res.RowsAffected()
	return n
}

// Scanner is a row of a query, or one of its rows.
type Scanner interface{ Scan(dest ...any) error }

// Clip shortens a text to at most n bytes of valid UTF-8 and an ellipsis, e.g. to store it.
func Clip(s string, n int) string {
	s = strings.ToValidUTF8(s, "�")
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "…"
}
