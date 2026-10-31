package backup

import (
	"archive/tar"
	"bufio"
	"bytes"
	"cmp"
	"compress/gzip"
	"context"
	"crypto/x509"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"filippo.io/age"
	"filippo.io/age/armor"
	"golang.org/x/crypto/ssh"

	"github.com/QwikByte/noryx/internal/master/database"
	"github.com/QwikByte/noryx/internal/pki"
)

// An archive of the master is a .tar.gz with its database, master.db, its certificate authority,
// the folder pki, and the host key of SFTP, sftp/host_key, if it has one, so that clients still
// trust a restored master. noryx-master backup writes it, and the backup job of the
// master too. With a passphrase, the job encrypts it with age, so that `age -d` decrypts it as
// well. It holds every secret of the master, and the CA's private key lets anyone control the
// agents.

const (
	masterDB = "master.db"
	pkiDir   = "pki"
	// SFTPDir is the folder of the data directory with the host key of SFTP.
	SFTPDir     = "sftp"
	sftpHostKey = SFTPDir + "/host_key"
)

var (
	// ErrPassphrase is the error of an encrypted archive of the master whose passphrase is wrong.
	ErrPassphrase = errors.New("the passphrase is wrong, or the archive isn't encrypted with one")
	// pkiFile matches the names of the files of the CA in an archive.
	pkiFile = regexp.MustCompile(`^pki/[A-Za-z0-9_-][A-Za-z0-9._-]{0,63}$`)
	// scryptWorkFactor is that of the passphrases of encrypted archives, age's default; tests
	// lower it.
	scryptWorkFactor = 18
)

// WriteMaster writes an archive of the master's database db and of the certificate authority
// in dataDir to w, encrypted with the passphrase unless it is empty. It copies the database
// with VACUUM INTO into a temporary folder in dataDir, so the master may keep running.
func WriteMaster(ctx context.Context, db *sql.DB, dataDir string, w io.Writer, passphrase string) error {
	tmp, err := os.MkdirTemp(dataDir, ".backup-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	snapshot := filepath.Join(tmp, masterDB)
	if _, err := db.ExecContext(ctx, "VACUUM INTO ?", snapshot); err != nil {
		return fmt.Errorf("copy the database: %w", err)
	}
	out := io.WriteCloser(nopCloser{w})
	if passphrase != "" {
		r, err := age.NewScryptRecipient(passphrase)
		if err != nil {
			return err
		}
		r.SetWorkFactor(scryptWorkFactor)
		if out, err = age.Encrypt(w, r); err != nil {
			return err
		}
	}
	gz := gzip.NewWriter(out)
	archive := tar.NewWriter(gz)
	err = addFile(archive, masterDB, snapshot)
	if err == nil {
		err = addFolder(archive, pkiDir, filepath.Join(dataDir, pkiDir))
	}
	if key := filepath.Join(dataDir, filepath.FromSlash(sftpHostKey)); err == nil && exists(key) {
		err = archive.WriteHeader(&tar.Header{Typeflag: tar.TypeDir, Name: SFTPDir + "/", Mode: 0o700, ModTime: time.Now()})
		if err == nil {
			err = addFile(archive, sftpHostKey, key)
		}
	}
	return errors.Join(err, archive.Close(), gz.Close(), out.Close())
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

type nopCloser struct{ io.Writer }

func (nopCloser) Close() error { return nil }

// addFolder adds the regular files of a folder to an archive, under name.
func addFolder(archive *tar.Writer, name, dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	if err := archive.WriteHeader(&tar.Header{Typeflag: tar.TypeDir, Name: name + "/", Mode: 0o700, ModTime: time.Now()}); err != nil {
		return err
	}
	for _, e := range entries {
		if e.Type().IsRegular() {
			if err := addFile(archive, name+"/"+e.Name(), filepath.Join(dir, e.Name())); err != nil {
				return err
			}
		}
	}
	return nil
}

func addFile(archive *tar.Writer, name, path string) error {
	f, err := os.Open(path) //nolint:gosec // files of the data directory
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if err := archive.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: info.Size(), ModTime: info.ModTime()}); err != nil {
		return err
	}
	_, err = io.Copy(archive, f)
	return err
}

// Master is what an archive of the master holds.
type Master struct {
	// Made is when its database was copied.
	Made time.Time
	// CA is the certificate of its certificate authority.
	CA *x509.Certificate
}

// ReadMaster extracts an archive of the master that r reads into the empty folder dir, and
// checks it: that it holds nothing but a database that this master can open, which it migrates
// to the current schema, and a certificate authority with its key. An encrypted archive is
// decrypted with what passphrase returns, which is only called for one.
func ReadMaster(r io.Reader, passphrase func() (string, error), dir string) (Master, error) {
	var m Master
	src, err := decrypt(r, passphrase)
	if err != nil {
		return m, err
	}
	gz, err := gzip.NewReader(src)
	if errors.Is(err, gzip.ErrHeader) || errors.Is(err, io.EOF) {
		return m, errors.New("this isn't an archive of noryx-master backup")
	}
	if err != nil {
		return m, fmt.Errorf("read the archive: %w", err)
	}
	archive := tar.NewReader(gz)
	for {
		h, err := archive.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return m, fmt.Errorf("read the archive: %w", err)
		}
		switch {
		case h.Typeflag == tar.TypeDir && (h.Name == pkiDir+"/" || h.Name == SFTPDir+"/"):
			err = os.MkdirAll(filepath.Join(dir, strings.TrimSuffix(h.Name, "/")), 0o700)
		case h.Typeflag == tar.TypeReg && (h.Name == masterDB || pkiFile.MatchString(h.Name) || h.Name == sftpHostKey):
			if h.Name == masterDB {
				m.Made = h.ModTime
			}
			err = extract(archive, filepath.Join(dir, filepath.FromSlash(h.Name)))
		default:
			return m, fmt.Errorf("the archive holds %q, which an archive of noryx-master backup doesn't", h.Name)
		}
		if err != nil {
			return m, fmt.Errorf("read the archive: %w", err)
		}
	}
	// Reading to the end checks the end of the compressed and of the encrypted data too. Only
	// the padding of the tar archive is left.
	if _, err := io.CopyN(io.Discard, gz, 1<<20); !errors.Is(err, io.EOF) {
		return m, fmt.Errorf("read the archive: %w", cmp.Or(err, errors.New("it goes on after its end")))
	}
	if m.CA, err = checkMaster(dir); err != nil {
		return m, err
	}
	return m, nil
}

// decrypt returns what r reads, decrypted with the passphrase if it is encrypted with age, also
// in its ASCII armor.
func decrypt(r io.Reader, passphrase func() (string, error)) (io.Reader, error) {
	const armored = "-----BEGIN AGE ENCRYPTED FILE-----"
	br := bufio.NewReader(r)
	head, _ := br.Peek(len(armored))
	src := io.Reader(br)
	switch {
	case bytes.HasPrefix(head, []byte(armored)):
		src = armor.NewReader(br)
	case bytes.HasPrefix(head, []byte("age-encryption.org/")):
	default:
		return br, nil
	}
	p, err := passphrase()
	if err != nil {
		return nil, err
	}
	id, err := age.NewScryptIdentity(p)
	if err != nil {
		return nil, ErrPassphrase
	}
	dec, err := age.Decrypt(src, id)
	if errors.Is(err, age.ErrIncorrectIdentity) {
		return nil, ErrPassphrase
	}
	if err != nil {
		return nil, fmt.Errorf("decrypt the archive: %w", err)
	}
	return dec, nil
}

func extract(r io.Reader, path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // a checked name in a new folder
	if err != nil {
		return err
	}
	_, err = io.Copy(f, r)
	return errors.Join(err, f.Close())
}

// checkMaster checks the database and the certificate authority extracted into dir, and returns
// the CA's certificate.
func checkMaster(dir string) (*x509.Certificate, error) {
	ca, err := pki.LoadKeyPair(filepath.Join(dir, pkiDir), "ca")
	if err != nil {
		return nil, fmt.Errorf("the archive holds no certificate authority with its key: %w", err)
	}
	if key := filepath.Join(dir, filepath.FromSlash(sftpHostKey)); exists(key) {
		data, err := os.ReadFile(key) //nolint:gosec // extracted into a new folder
		if err == nil {
			_, err = ssh.ParsePrivateKey(data)
		}
		if err != nil {
			return nil, fmt.Errorf("the host key of SFTP in the archive is damaged: %w", err)
		}
	}
	path := filepath.Join(dir, masterDB)
	if _, err := os.Stat(path); err != nil {
		return nil, errors.New("the archive holds no database")
	}
	db, err := database.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open the database of the archive: %w", err)
	}
	defer db.Close()
	var check string
	if err := db.QueryRow("PRAGMA quick_check").Scan(&check); err != nil {
		return nil, fmt.Errorf("check the database of the archive: %w", err)
	}
	if check != "ok" {
		return nil, fmt.Errorf("the database of the archive is damaged: %s", strings.TrimSpace(check))
	}
	return ca.Leaf, nil
}
