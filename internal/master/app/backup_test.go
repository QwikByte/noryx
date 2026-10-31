package app

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"filippo.io/age"

	"github.com/QwikByte/noryx/internal/master/auth"
	"github.com/QwikByte/noryx/internal/master/backup"
	"github.com/QwikByte/noryx/internal/master/settings"
	"github.com/QwikByte/noryx/internal/pki"
)

// A backup of a running master restores its users and certificate authority.
func TestBackupMaster(t *testing.T) {
	dir := t.TempDir()
	cfg := config{dataDir: filepath.Join(dir, "master")}
	db, err := openDB(cfg.dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	conf, err := settings.Load(t.Context(), db, settings.Master{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := auth.NewService(db, conf).CreateUser(t.Context(), "admin", "correct horse battery"); err != nil {
		t.Fatal(err)
	}
	ca, err := pki.LoadOrCreateCA(filepath.Join(cfg.dataDir, "pki"))
	if err != nil {
		t.Fatal(err)
	}

	file := filepath.Join(dir, "backup.tar.gz")
	if err := backupMaster(t.Context(), cfg, file); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(file); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("backup file: %v, %v", info, err)
	}
	if err := backupMaster(t.Context(), cfg, file); !errors.Is(err, fs.ErrExist) {
		t.Errorf("backup over an existing file: %v", err)
	}
	if err := backupMaster(t.Context(), config{dataDir: filepath.Join(dir, "typo")}, filepath.Join(dir, "other.tar.gz")); err == nil {
		t.Error("backup of a directory without a master succeeded")
	}

	restored := config{dataDir: filepath.Join(dir, "restored")}
	extract(t, file, restored.dataDir)
	db2, err := openDB(restored.dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer db2.Close()
	if _, _, err := auth.NewService(db2, conf).Login(t.Context(), "admin", "correct horse battery", "", time.Hour, auth.Client{}); err != nil {
		t.Errorf("sign in after restoring: %v", err)
	}
	ca2, err := pki.LoadOrCreateCA(filepath.Join(restored.dataDir, "pki"))
	if err != nil || !ca2.Cert.Equal(ca.Cert) {
		t.Errorf("restored CA differs: %v", err)
	}
}

func extract(t *testing.T, file, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(file)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	archive := tar.NewReader(gz)
	for {
		h, err := archive.Next()
		if errors.Is(err, io.EOF) {
			return
		}
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, h.Name)
		if h.Typeflag == tar.TypeDir {
			err = os.MkdirAll(path, fs.FileMode(h.Mode))
		} else {
			var data []byte
			if data, err = io.ReadAll(archive); err == nil {
				err = os.WriteFile(path, data, fs.FileMode(h.Mode))
			}
		}
		if err != nil {
			t.Fatal(err)
		}
	}
}

// newMaster creates a master in dir with a user and a CA, as a master that ran once would have.
func newMaster(t *testing.T, dir, username string) *pki.CA {
	t.Helper()
	db, err := openDB(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	conf, err := settings.Load(t.Context(), db, settings.Master{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := auth.NewService(db, conf).CreateUser(t.Context(), username, "correct horse battery"); err != nil {
		t.Fatal(err)
	}
	ca, err := pki.LoadOrCreateCA(filepath.Join(dir, "pki"))
	if err != nil {
		t.Fatal(err)
	}
	return ca
}

// state returns the contents of the files of the database and the CA in dir.
func state(t *testing.T, dir string) map[string]string {
	t.Helper()
	files := map[string]string{}
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(dir, path)
		if rel == "master.db" || strings.HasPrefix(rel, "master.db-") || strings.HasPrefix(rel, "pki") {
			data, err := os.ReadFile(path)
			files[rel] = string(data)
			return err
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

// users returns the users of the master in dir.
func users(t *testing.T, dir string) []string {
	t.Helper()
	db, err := openDB(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Query(`SELECT username FROM users ORDER BY username`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		names = append(names, name)
	}
	return names
}

// noryx-master restore replaces the database and the CA with those of an archive, also an
// encrypted one, and keeps what it replaced. It refuses while the master runs, with a wrong
// passphrase or a damaged archive, and replaces nothing then.
func TestRestoreMaster(t *testing.T) {
	dir := t.TempDir()
	old := config{dataDir: filepath.Join(dir, "old")}
	oldCA := newMaster(t, old.dataDir, "admin")
	plain := filepath.Join(dir, "master.tar.gz")
	if err := backupMaster(t.Context(), old, plain); err != nil {
		t.Fatal(err)
	}
	// Encrypted like the backup job of the master does, with a lower work factor for the test.
	data, err := os.ReadFile(plain)
	if err != nil {
		t.Fatal(err)
	}
	recipient, err := age.NewScryptRecipient("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	recipient.SetWorkFactor(10)
	var buf bytes.Buffer
	w, err := age.Encrypt(&buf, recipient)
	if err == nil {
		_, err = w.Write(data)
	}
	if err = errors.Join(err, w.Close()); err != nil {
		t.Fatal(err)
	}
	encrypted := filepath.Join(dir, "master.tar.gz.age")
	damaged := filepath.Join(dir, "damaged.tar.gz.age")
	changed := bytes.Clone(buf.Bytes())
	changed[len(changed)-10] ^= 1
	if err := errors.Join(os.WriteFile(encrypted, buf.Bytes(), 0o600), os.WriteFile(damaged, changed, 0o600)); err != nil {
		t.Fatal(err)
	}

	// The new master, e.g. on another machine after installing it, has a CA and users of its own.
	cfg := config{dataDir: filepath.Join(dir, "new")}
	newMaster(t, cfg.dataDir, "other")
	before := state(t, cfg.dataDir)
	unlock, err := lockDataDir(cfg.dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := restoreMaster(cfg, plain); err == nil || !strings.Contains(err.Error(), "stop it first") {
		t.Errorf("restore while the master runs: %v", err)
	}
	if err := unlock(); err != nil {
		t.Fatal(err)
	}
	t.Setenv(passphraseEnv, "wrong horse battery staple")
	if err := restoreMaster(cfg, encrypted); !errors.Is(err, backup.ErrPassphrase) {
		t.Errorf("restore with a wrong passphrase: %v", err)
	}
	t.Setenv(passphraseEnv, "correct horse battery staple")
	if err := restoreMaster(cfg, damaged); err == nil {
		t.Error("restored a damaged archive")
	}
	if err := restoreMaster(cfg, filepath.Join(dir, "missing.tar.gz")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("restore of a missing file: %v", err)
	}
	if after := state(t, cfg.dataDir); !maps.Equal(before, after) {
		t.Fatal("a restore that failed changed the database or the CA")
	}
	for _, pattern := range []string{"replaced-*", ".*"} {
		if left, _ := filepath.Glob(filepath.Join(cfg.dataDir, pattern)); len(left) != 0 {
			t.Errorf("failed restores left %v", left)
		}
	}

	if err := restoreMaster(cfg, encrypted); err != nil {
		t.Fatal(err)
	}
	if got := users(t, cfg.dataDir); !slices.Equal(got, []string{"admin"}) {
		t.Errorf("users after restoring = %v", got)
	}
	if ca, err := pki.LoadOrCreateCA(filepath.Join(cfg.dataDir, "pki")); err != nil || !ca.Cert.Equal(oldCA.Cert) {
		t.Errorf("the CA wasn't restored: %v", err)
	}
	// What it replaced is kept.
	replaced, _ := filepath.Glob(filepath.Join(cfg.dataDir, "replaced-*"))
	if len(replaced) != 1 || users(t, replaced[0])[0] != "other" || state(t, replaced[0])["pki/ca.pem"] != before["pki/ca.pem"] {
		t.Errorf("replaced = %v", replaced)
	}
	if temp, _ := filepath.Glob(filepath.Join(cfg.dataDir, ".*")); len(temp) != 0 {
		t.Errorf("the restore left %v", temp)
	}

	// Into an empty data directory, there is nothing to keep.
	empty := config{dataDir: filepath.Join(dir, "empty")}
	if err := restoreMaster(empty, plain); err != nil {
		t.Fatal(err)
	}
	if got := users(t, empty.dataDir); !slices.Equal(got, []string{"admin"}) {
		t.Errorf("users after restoring into an empty directory = %v", got)
	}
	if replaced, _ := filepath.Glob(filepath.Join(empty.dataDir, "replaced-*")); len(replaced) != 0 {
		t.Errorf("replaced = %v", replaced)
	}
}
