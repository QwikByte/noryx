package database

import (
	"cmp"
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

// InTx commits what fn wrote if it succeeds, and nothing of it if it fails.
func TestInTx(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := t.Context()
	if _, err := db.ExecContext(ctx, `CREATE TABLE t (n INTEGER NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	insert := func(n int, fail error) error {
		return InTx(ctx, db, func(tx *sql.Tx) error {
			res, err := tx.ExecContext(ctx, `INSERT INTO t VALUES (?)`, n)
			if err == nil && RowsAffected(res) != 1 {
				t.Errorf("rows affected: %d", RowsAffected(res))
			}
			return cmp.Or(err, fail)
		})
	}
	failed := errors.New("failed")
	if err := insert(1, nil); err != nil {
		t.Fatal(err)
	}
	if err := insert(2, failed); !errors.Is(err, failed) {
		t.Fatalf("InTx = %v, want the error of fn", err)
	}
	var sum int
	if err := db.QueryRowContext(ctx, `SELECT SUM(n) FROM t`).Scan(&sum); err != nil || sum != 1 {
		t.Errorf("sum %d, %v: want only the first row", sum, err)
	}
}

func TestClip(t *testing.T) {
	for _, tc := range []struct {
		in   string
		n    int
		want string
	}{
		{"short", 10, "short"},
		{"exactly", 7, "exactly"},
		{"longer text", 6, "longer…"},
		{"äöü", 3, "ä…"}, // not within a character
		{"a\xffb", 10, "a�b"},
	} {
		if got := Clip(tc.in, tc.n); got != tc.want {
			t.Errorf("Clip(%q, %d) = %q, want %q", tc.in, tc.n, got, tc.want)
		}
	}
}
