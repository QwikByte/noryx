//go:build unix

package app

import (
	"errors"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// lockDataDir takes the lock that a running master holds on its data directory, master.lock in
// it, and returns a function that releases it. If another process holds it, it returns
// errRunning. A file system without locks can't tell.
func lockDataDir(dataDir string) (func() error, error) {
	if err := checkDataDir(dataDir); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dataDir, "master.lock"), os.O_RDWR|os.O_CREATE, 0o600) //nolint:gosec // in the data directory
	if err != nil {
		return nil, err
	}
	//nolint:gosec // a file descriptor fits an int
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); errors.Is(err, unix.EWOULDBLOCK) {
		f.Close()
		return nil, errRunning
	}
	return f.Close, nil
}
