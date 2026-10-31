package app

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/term"

	"github.com/QwikByte/noryx/internal/master/backup"
	"github.com/QwikByte/noryx/internal/pki"
)

// passphraseEnv is the environment variable that noryx-master restore reads the passphrase of
// an encrypted archive from, e.g. in scripts.
const passphraseEnv = "NORYX_MASTER_PASSPHRASE" //nolint:gosec // the name of a variable, not its value

// errRunning is the error of a lock of the data directory that a running master holds.
var errRunning = errors.New("the master runs")

// restored are the files of the data directory that a restore replaces: the database with its
// write-ahead log, and the certificate authority.
var restored = []string{"master.db", "master.db-wal", "master.db-shm", "pki"}

// backupMaster writes the master's database and certificate authority to a .tar.gz file, or to
// stdout for "-", see backup.WriteMaster. The master may keep running. The archive holds the
// CA's private key, so the file is only readable by its owner.
func backupMaster(ctx context.Context, cfg config, target string) (err error) {
	if target == "-" && term.IsTerminal(int(os.Stdout.Fd())) {
		return errors.New("redirect the backup to a file, e.g. noryx-master backup - > master-backup.tar.gz")
	}
	if _, err := os.Stat(filepath.Join(cfg.dataDir, "master.db")); err != nil {
		return fmt.Errorf("no master in %s, set its --data-dir: %w", cfg.dataDir, err)
	}
	db, err := openDB(cfg.dataDir)
	if err != nil {
		return err
	}
	defer db.Close()
	out := os.Stdout
	if target != "-" {
		//nolint:gosec // the administrator's choice on the command line
		if out, err = os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600); err != nil {
			return err
		}
		defer func() {
			if err = errors.Join(err, out.Close()); err != nil {
				os.Remove(target)
				return
			}
			fmt.Fprintf(os.Stderr, "Saved the database and the certificate authority to %s. Keep it as safe as the master, as it holds the CA's private key.\n", target)
		}()
	}
	return backup.WriteMaster(ctx, db, cfg.dataDir, out, "")
}

// restoreMaster replaces the database and the certificate authority in the data directory with
// those of an archive of noryx-master backup or of the backup job of the master, while the
// master is stopped. It checks all of the archive before it replaces anything, and keeps what
// it replaced in a folder of the data directory.
func restoreMaster(cfg config, file string) error {
	f, err := os.Open(file) //nolint:gosec // the administrator's choice on the command line
	if err != nil {
		return err
	}
	defer f.Close()
	unlock, err := lockDataDir(cfg.dataDir)
	if errors.Is(err, errRunning) {
		err = fmt.Errorf("the master runs with %s; stop it first, e.g. with: sudo systemctl stop noryx-master", cfg.dataDir)
	}
	if err != nil {
		return err
	}
	defer func() { _ = unlock() }()
	staged, err := os.MkdirTemp(cfg.dataDir, ".restore-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(staged)
	m, err := backup.ReadMaster(f, readPassphrase, staged)
	if err != nil {
		return fmt.Errorf("%s: %w. Nothing was replaced", file, err)
	}

	kept := filepath.Join(cfg.dataDir, "replaced-"+time.Now().Format("20060102-150405"))
	if err := os.Mkdir(kept, 0o700); err != nil {
		return err
	}
	moved, err := move(cfg.dataDir, kept)
	var placed []string
	if err == nil {
		placed, err = move(staged, cfg.dataDir)
	}
	if err != nil {
		// Put back what was there.
		for _, name := range placed {
			_ = os.RemoveAll(filepath.Join(cfg.dataDir, name))
		}
		for _, name := range moved {
			_ = os.Rename(filepath.Join(kept, name), filepath.Join(cfg.dataDir, name))
		}
		_ = os.Remove(kept)
		return fmt.Errorf("replace the database and the certificate authority: %w", err)
	}
	fmt.Fprintf(os.Stderr, "Restored the database of %s and the certificate authority with the fingerprint %s from %s.\n",
		m.Made.Local().Format(time.DateTime), pki.Fingerprint(m.CA), file)
	if len(moved) == 0 {
		_ = os.Remove(kept)
	} else {
		fmt.Fprintf(os.Stderr, "What they replaced is in %s; delete it once the master works, as it holds the private key of the CA it had.\n", kept)
	}
	fmt.Fprintln(os.Stderr, "Start the master, e.g. with: sudo systemctl start noryx-master")
	return nil
}

// move moves those of the restored files that are in from to the folder to, and returns which.
func move(from, to string) ([]string, error) {
	var moved []string
	for _, name := range restored {
		err := os.Rename(filepath.Join(from, name), filepath.Join(to, name))
		switch {
		case errors.Is(err, fs.ErrNotExist):
		case err != nil:
			return moved, err
		default:
			moved = append(moved, name)
		}
	}
	return moved, nil
}

// readPassphrase reads the passphrase of an encrypted archive from passphraseEnv or else from
// the terminal.
func readPassphrase() (string, error) {
	if p, ok := os.LookupEnv(passphraseEnv); ok {
		return p, nil
	}
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return "", fmt.Errorf("the archive is encrypted: enter its passphrase on a terminal, or set %s", passphraseEnv)
	}
	fmt.Fprint(os.Stderr, "Passphrase of the archive: ")
	p, err := term.ReadPassword(fd)
	fmt.Fprintln(os.Stderr)
	return string(p), err
}
