package backup

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"database/sql"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filippo.io/age"
	"filippo.io/age/armor"

	"github.com/QwikByte/noryx/internal/master/database"
	"github.com/QwikByte/noryx/internal/pki"
)

func init() { scryptWorkFactor = 10 } // fast enough for tests

const passphrase = "correct horse battery staple"

// master returns the database and the data directory of a master with a CA and a user.
func master(t *testing.T) (*sql.DB, string, *pki.CA) {
	t.Helper()
	dir := t.TempDir()
	db, err := database.Open(filepath.Join(dir, "master.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(`INSERT INTO users (username, password_hash, created_at) VALUES ('admin', 'hash', 0)`); err != nil {
		t.Fatal(err)
	}
	ca, err := pki.LoadOrCreateCA(filepath.Join(dir, "pki"))
	if err != nil {
		t.Fatal(err)
	}
	return db, dir, ca
}

func archiveOf(t *testing.T, db *sql.DB, dir, passphrase string) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := WriteMaster(t.Context(), db, dir, &buf, passphrase); err != nil {
		t.Fatal(err)
	}
	if temp, err := filepath.Glob(filepath.Join(dir, ".*")); err != nil || len(temp) > 0 {
		t.Errorf("writing an archive left %v, %v", temp, err)
	}
	return buf.Bytes()
}

func given(p string) func() (string, error) { return func() (string, error) { return p, nil } }

// An archive of the master restores its database and CA, also encrypted with a passphrase, also
// in the ASCII armor of age; age decrypts it too.
func TestMasterArchive(t *testing.T) {
	db, dir, ca := master(t)
	plain := archiveOf(t, db, dir, "")
	encrypted := archiveOf(t, db, dir, passphrase)
	if !bytes.HasPrefix(plain, []byte{0x1f, 0x8b}) || !bytes.HasPrefix(encrypted, []byte("age-encryption.org/v1\n")) {
		t.Fatalf("archives start with %q and %q", plain[:2], encrypted[:22])
	}
	var armored bytes.Buffer
	w := armor.NewWriter(&armored)
	if _, err := w.Write(encrypted); err != nil || w.Close() != nil {
		t.Fatal(err)
	}
	id, err := age.NewScryptIdentity(passphrase)
	if err != nil {
		t.Fatal(err)
	}
	if r, err := age.Decrypt(bytes.NewReader(encrypted), id); err != nil {
		t.Errorf("age can't decrypt it: %v", err)
	} else if data, _ := io.ReadAll(r); !bytes.Equal(data[:2], []byte{0x1f, 0x8b}) {
		t.Errorf("age decrypts it to %q…", data[:2])
	}

	for name, archive := range map[string][]byte{"plain": plain, "encrypted": encrypted, "armored": armored.Bytes()} {
		asked := false
		out := t.TempDir()
		m, err := ReadMaster(bytes.NewReader(archive), func() (string, error) { asked = true; return passphrase, nil }, out)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if asked != (name != "plain") || !m.CA.Equal(ca.Cert) || time.Since(m.Made) > time.Minute {
			t.Errorf("%s: asked for the passphrase %v, %+v", name, asked, m)
		}
		restored, err := database.Open(filepath.Join(out, "master.db"))
		if err != nil {
			t.Fatal(err)
		}
		var user string
		if err := restored.QueryRow(`SELECT username FROM users`).Scan(&user); err != nil || user != "admin" {
			t.Errorf("%s: user %q, %v", name, user, err)
		}
		restored.Close()
	}
}

// Archives with a wrong passphrase, that were changed, cut off or hold anything else are
// refused.
func TestMasterArchiveRefused(t *testing.T) {
	db, dir, _ := master(t)
	plain, encrypted := archiveOf(t, db, dir, ""), archiveOf(t, db, dir, passphrase)
	changed := bytes.Clone(encrypted)
	changed[len(changed)-100] ^= 1

	caPair, err := os.ReadFile(filepath.Join(dir, "pki", "ca.pem"))
	if err != nil {
		t.Fatal(err)
	}
	// tarball returns a .tar.gz of files with their contents; a name ending in / is a folder,
	// one with -> a link.
	tarball := func(files ...string) []byte {
		var buf bytes.Buffer
		gz := gzip.NewWriter(&buf)
		tw := tar.NewWriter(gz)
		for i := 0; i < len(files); i += 2 {
			h := &tar.Header{Name: files[i], Typeflag: tar.TypeReg, Mode: 0o600, Size: int64(len(files[i+1]))}
			if name, target, ok := strings.Cut(files[i], " -> "); ok {
				h.Name, h.Typeflag, h.Linkname, h.Size = name, tar.TypeSymlink, target, 0
			} else if strings.HasSuffix(h.Name, "/") {
				h.Typeflag, h.Size = tar.TypeDir, 0
			}
			if err := tw.WriteHeader(h); err != nil {
				t.Fatal(err)
			}
			_, _ = tw.Write([]byte(files[i+1][:h.Size]))
		}
		if err := errors.Join(tw.Close(), gz.Close()); err != nil {
			t.Fatal(err)
		}
		return buf.Bytes()
	}

	for name, tc := range map[string]struct {
		archive    []byte
		passphrase string
		want       string
	}{
		"a wrong passphrase": {encrypted, "wrong horse battery staple", "passphrase is wrong"},
		"no passphrase":      {encrypted, "", "passphrase is wrong"},
		"changed":            {changed, passphrase, "read the archive"},
		"cut off":            {encrypted[:len(encrypted)-200], passphrase, "read the archive"},
		"cut off, plain":     {plain[:len(plain)-200], "", "read the archive"},
		"something else":     {[]byte("PK\x03\x04 a ZIP archive"), "", "isn't an archive of noryx-master backup"},
		"nothing at all":     {nil, "", "isn't an archive of noryx-master backup"},
		"a link":             {tarball("pki/ca.pem -> /etc/shadow", ""), "", `holds "pki/ca.pem"`},
		"a path out":         {tarball("pki/../../evil", "data"), "", "holds"},
		"an absolute path":   {tarball("/etc/", ""), "", "holds"},
		"another file":       {tarball("https/panel.key", "data"), "", "holds"},
		"a file in a folder": {tarball("pki/sub/ca.pem", "data"), "", "holds"},
		"a hidden file":      {tarball("pki/.ca.pem", "data"), "", "holds"},
		"a file twice":       {tarball("master.db", "data", "master.db", "data"), "", "exists"},
		"no CA":              {tarball("master.db", "data"), "", "no certificate authority"},
		"a CA without key":   {tarball("pki/ca.crt", string(caPair)), "", "no certificate authority"},
		"no database":        {tarball("pki/", "", "pki/ca.pem", string(caPair)), "", "no database"},
		"a database of junk": {tarball("master.db", strings.Repeat("junk", 1000), "pki/ca.pem", string(caPair)), "", "database"},
		"an empty archive":   {tarball(), "", "no certificate authority"},
	} {
		_, err := ReadMaster(bytes.NewReader(tc.archive), given(tc.passphrase), t.TempDir())
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want %q", name, err, tc.want)
		}
	}
	asked := errors.New("no terminal")
	if _, err := ReadMaster(bytes.NewReader(encrypted), func() (string, error) { return "", asked }, t.TempDir()); !errors.Is(err, asked) {
		t.Errorf("without a passphrase: %v", err)
	}
}
