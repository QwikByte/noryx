package database

import (
	"context"
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
