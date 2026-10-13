package database

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"sync"
	"testing"
)

// TestConcurrentWriters runs transactions that read before they write at the same time, which
// SQLite refuses with "database is locked" unless they take the write lock as they begin.
func TestConcurrentWriters(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `CREATE TABLE counter (n INTEGER NOT NULL); INSERT INTO counter VALUES (0)`); err != nil {
		t.Fatal(err)
	}
	const writers, rounds = 8, 25
	var wg sync.WaitGroup
	errs := make(chan error, writers*rounds)
	for range writers {
		wg.Go(func() {
			for range rounds {
				tx, err := db.BeginTx(ctx, nil)
				if err != nil {
					errs <- err
					continue
				}
				var n int
				err = tx.QueryRowContext(ctx, `SELECT n FROM counter`).Scan(&n)
				if err == nil {
					_, err = tx.ExecContext(ctx, `UPDATE counter SET n = ?`, n+1)
				}
				if err == nil {
					err = tx.Commit()
				} else {
					tx.Rollback() //nolint:errcheck // the error is reported
				}
				if err != nil {
					errs <- err
				}
			}
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	var n int
	if err := db.QueryRowContext(ctx, `SELECT n FROM counter`).Scan(&n); err != nil || n != writers*rounds {
		t.Fatalf("counter = %d, %v; want %d", n, err, writers*rounds)
	}
}

// InTx keeps what fn wrote if it succeeds, and nothing of it otherwise, and returns the error
// of fn as it is, e.g. one that tells the status of an answer.
func TestInTx(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `CREATE TABLE things (name TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	insert := func(name string, fail error) error {
		return InTx(ctx, db, func(tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx, `INSERT INTO things VALUES (?)`, name); err != nil {
				return err
			}
			return fail
		})
	}
	failed := errors.New("failed")
	if err := insert("kept", nil); err != nil {
		t.Fatal(err)
	}
	if err := insert("dropped", failed); err != failed { //nolint:errorlint // the error itself, not wrapped
		t.Fatalf("err = %v, want %v", err, failed)
	}
	var names []string
	rows, err := db.QueryContext(ctx, `SELECT name FROM things`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		names = append(names, name)
	}
	if len(names) != 1 || names[0] != "kept" {
		t.Fatalf("names = %v", names)
	}
}

// Clip cuts at the start of a character, so that what it keeps is valid UTF-8.
func TestClip(t *testing.T) {
	for _, tc := range []struct {
		s    string
		n    int
		want string
	}{
		{"short", 10, "short"},
		{"exactly", 7, "exactly"},
		{"abcdef", 3, "abc…"},
		{"Grüße", 3, "Gr…"}, // ü takes 2 bytes, from the 3rd
		{"Grüße", 4, "Grü…"},
	} {
		if got := Clip(tc.s, tc.n); got != tc.want {
			t.Errorf("Clip(%q, %d) = %q, want %q", tc.s, tc.n, got, tc.want)
		}
	}
}
