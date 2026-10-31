package database

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"io/fs"
	"path"
	"path/filepath"
	"strings"
	"testing"
)

// released is the number of migrations of the latest release, whose schema the masters that
// update from it have.
const released = 1

// releasedMigrations are the SHA-256 hashes of the migrations of released versions. They never
// change: masters ran them already, and run each only once.
var releasedMigrations = map[string]string{
	"0001_init.sql": "7e27ea963c646c62553ddb69319fd48cf07f37402c6c11f4f59a288da0ff3add",
}

// migrationFiles returns the names of the migrations in the order they run.
func migrationFiles(t *testing.T) []string {
	t.Helper()
	files, err := fs.Glob(migrations, "migrations/*.sql")
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func TestReleasedMigrationsUnchanged(t *testing.T) {
	files := migrationFiles(t)
	if len(files) < released || len(releasedMigrations) != released {
		t.Fatalf("%d migrations, %d of them released", len(files), len(releasedMigrations))
	}
	for i, file := range files {
		data, err := migrations.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		name, sum := path.Base(file), sha256.Sum256(data)
		want, ok := releasedMigrations[name]
		switch {
		case i < released && hex.EncodeToString(sum[:]) != want:
			t.Errorf("%s was released and must not change; add a new migration instead", name)
		case i >= released && ok, i < released && !ok:
			t.Errorf("%s runs as migration %d", name, i+1)
		}
		if !strings.HasPrefix(name, fmt.Sprintf("%04d_", i+1)) {
			t.Errorf("migration %d is named %s", i+1, name)
		}
	}
}

func userVersion(t *testing.T, db *sql.DB) int {
	t.Helper()
	var version int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	return version
}

// A new database gets every migration once, with foreign keys enforced, and a master refuses
// a database of a newer version.
func TestMigrateEmpty(t *testing.T) {
	file := filepath.Join(t.TempDir(), "noryx.db")
	db, err := Open(file)
	if err != nil {
		t.Fatal(err)
	}
	var foreignKeys int
	var journal, admins string
	err = db.QueryRow(`PRAGMA foreign_keys`).Scan(&foreignKeys)
	if err == nil {
		err = db.QueryRow(`PRAGMA journal_mode`).Scan(&journal)
	}
	if err == nil {
		err = db.QueryRow(`SELECT name FROM user_groups WHERE id = 'administrators'`).Scan(&admins)
	}
	if err != nil || userVersion(t, db) != len(migrationFiles(t)) || foreignKeys != 1 || journal != "wal" || admins != "Administrators" {
		t.Fatalf("version %d, foreign keys %d, journal %q, %q: %v", userVersion(t, db), foreignKeys, journal, admins, err)
	}
	if _, err := db.Exec(`INSERT INTO group_members (group_id, user_id) VALUES ('administrators', 42)`); err == nil {
		t.Error("a member that isn't a user was added")
	}
	db.Close()

	// Opening it again runs nothing again, which would fail as the tables exist.
	if db, err = Open(file); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, len(migrationFiles(t))+1)); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if _, err := Open(file); err == nil || !strings.Contains(err.Error(), "newer than this binary") {
		t.Fatalf("a database of a newer version: %v", err)
	}
}
