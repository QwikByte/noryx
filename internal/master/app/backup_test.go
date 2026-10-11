package app

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/QwikByte/noryx/internal/master/auth"
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
