package logging

import (
	"fmt"
	"os"
	"sync"
)

const (
	maxFileSize = 10 << 20
	keepFiles   = 5
)

// rotatingFile is a log file that is renamed to <name>.1 once it reaches maxFileSize.
// Older files move up to <name>.<keepFiles>; the oldest is deleted. Logs may name users
// and addresses, so only the owner can read them.
type rotatingFile struct {
	mu   sync.Mutex
	name string
	f    *os.File
	size int64
}

func openRotating(name string) (*rotatingFile, error) {
	r := &rotatingFile{name: name}
	return r, r.open()
}

func (r *rotatingFile) open() error {
	f, err := os.OpenFile(r.name, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}
	r.f, r.size = f, info.Size()
	return nil
}

func (r *rotatingFile) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.size > 0 && r.size+int64(len(p)) > maxFileSize {
		if err := r.rotate(); err != nil {
			return 0, err
		}
	}
	n, err := r.f.Write(p)
	r.size += int64(n)
	return n, err
}

// rotate moves the files up and opens a new one. Logging goes on if they can't be moved, e.g.
// as another program moved or deleted the file: in the file as it is, which rotates again once
// it grew by another maxFileSize.
func (r *rotatingFile) rotate() error {
	_ = r.f.Close() // written already, nothing to lose
	for i := keepFiles - 1; i > 0; i-- {
		_ = os.Rename(fmt.Sprintf("%s.%d", r.name, i), fmt.Sprintf("%s.%d", r.name, i+1)) // missing files are fine
	}
	moved := os.Rename(r.name, r.name+".1")
	if err := r.open(); err != nil {
		return err
	}
	if moved != nil {
		r.size = 0
	}
	return nil
}

func (r *rotatingFile) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.f.Close()
}
