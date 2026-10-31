// Package datadir gives the agent safe access to the data directory of a server. Paths
// can't leave the directory (os.Root), and whatever the agent creates belongs to the
// directory's owner: the agent runs as root, the server as the user its image chose,
// which can't read files owned by root.
package datadir

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
)

const (
	filePerm = 0o640
	dirPerm  = 0o750
	// tempPrefix starts the names of temporary files and folders of the agent.
	tempPrefix = ".noryx-"
	maxPath    = 1024
	// maxRead limits what ReadFile reads, configuration files of a few kilobytes.
	maxRead = 16 << 20
)

var errSpecial = errors.New("neither a regular file nor a folder")

// ErrLink refuses paths that lead through a symbolic link; see Direct.
var ErrLink = errors.New("the path leads through a link")

// Escapes reports whether err is the refusal of os.Root to leave the directory, e.g. through
// a symbolic link, which os doesn't export.
func Escapes(err error) bool {
	return err != nil && strings.Contains(err.Error(), "path escapes from parent")
}

// Name turns a slash-separated path relative to a data directory into a name in it, "."
// for the directory itself. os.Root rejects escaping paths too, but ".." has no use here,
// so such paths are refused upfront, like backslashes.
func Name(p string) (string, bool) {
	if len(p) > maxPath || strings.ContainsAny(p, "\x00\\") || slices.Contains(strings.Split(p, "/"), "..") {
		return "", false
	}
	name := strings.TrimPrefix(path.Clean("/"+p), "/")
	if name == "" {
		return ".", true
	}
	return filepath.FromSlash(name), true
}

// Outermost removes duplicates and names inside others from a list of names.
func Outermost(names []string) []string {
	if slices.Contains(names, ".") {
		return []string{"."}
	}
	slices.Sort(names)
	all := slices.Compact(slices.Clone(names))
	return slices.DeleteFunc(slices.Compact(names), func(p string) bool {
		return slices.ContainsFunc(all, func(parent string) bool { return strings.HasPrefix(p, parent+string(filepath.Separator)) })
	})
}

// IsTemp reports whether a file or folder name is one of the agent's temporary ones.
func IsTemp(name string) bool { return strings.HasPrefix(name, tempPrefix) }

// TempName returns a new name for a temporary file or folder in the folder dir.
func TempName(dir string) string { return filepath.Join(dir, tempPrefix+strings.ToLower(rand.Text())) }

// Dir is the data directory of a server. Its methods that create files and
// directories set their owner; files created otherwise belong to the agent.
type Dir struct {
	*os.Root
	uid, gid int // owner of new entries, -1 if the agent can't change owners
}

// Open opens an existing data directory.
func Open(path string) (*Dir, error) {
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, err
	}
	d := &Dir{Root: root, uid: -1, gid: -1}
	info, err := root.Stat(".")
	if err != nil {
		return nil, errors.Join(err, root.Close())
	}
	if st, ok := info.Sys().(*syscall.Stat_t); ok && os.Geteuid() == 0 {
		d.uid, d.gid = int(st.Uid), int(st.Gid)
	}
	return d, nil
}

// Sub opens a folder of the directory as a directory of its own, whose new entries belong to
// the same owner, e.g. a backup extracted next to the data.
func (d *Dir) Sub(name string) (*Dir, error) {
	root, err := d.OpenRoot(name)
	if err != nil {
		return nil, err
	}
	return &Dir{Root: root, uid: d.uid, gid: d.gid}, nil
}

// WriteFile replaces a file atomically, so the server never reads a partial file.
func (d *Dir) WriteFile(name string, data []byte) error {
	return d.Replace(name, true, func(w io.Writer) error {
		_, err := w.Write(data)
		return err
	})
}

// Replace writes a file through a temporary file next to it, which takes its place once
// write succeeded. An existing file keeps its permissions. Without overwrite, an
// existing file is left alone and fs.ErrExist is returned.
func (d *Dir) Replace(name string, overwrite bool, write func(io.Writer) error) (err error) {
	tmp := TempName(filepath.Dir(name))
	f, err := d.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, filePerm)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = d.Remove(tmp) // best effort; the write error matters
		}
	}()
	if err := errors.Join(write(f), f.Close(), d.own(tmp)); err != nil {
		return err
	}
	if !overwrite {
		// A hard link never replaces an existing file, unlike a rename.
		if err := d.Link(tmp, name); err != nil {
			return err
		}
		return d.Remove(tmp)
	}
	if info, err := d.Stat(name); err == nil {
		if err := d.Chmod(tmp, info.Mode().Perm()); err != nil {
			return err
		}
	}
	return d.Rename(tmp, name)
}

// MkdirAll creates a directory and its missing parents.
func (d *Dir) MkdirAll(name string) error {
	parts := strings.Split(filepath.Clean(name), string(filepath.Separator))
	for i := range parts {
		dir := filepath.Join(parts[:i+1]...)
		if err := d.Mkdir(dir, dirPerm); errors.Is(err, fs.ErrExist) {
			continue
		} else if err != nil {
			return err
		}
		if err := d.own(dir); err != nil {
			return err
		}
	}
	return nil
}

// Open opens a regular file or a folder for reading. The server owns its data, so it could
// put a named pipe where the agent expects a file, whose opening would wait for a writer.
func (d *Dir) Open(name string) (*os.File, error) { return openPlain(d.Root, name) }

// ReadFile reads a regular file of up to maxRead bytes, as the server could make one huge.
func (d *Dir) ReadFile(name string) ([]byte, error) {
	f, err := d.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxRead+1))
	if err == nil && len(data) > maxRead {
		err = fmt.Errorf("%s is larger than %d MiB", name, maxRead>>20)
	}
	return data, err
}

// openPlain opens a regular file or a folder of root for reading, and refuses anything else
// without waiting for it.
func openPlain(root *os.Root, name string) (*os.File, error) {
	f, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err == nil && !info.Mode().IsRegular() && !info.IsDir() {
		err = &fs.PathError{Op: "open", Path: name, Err: errSpecial}
	}
	if err != nil {
		return nil, errors.Join(err, f.Close())
	}
	return f, nil
}

// Direct returns ErrLink unless name leads to its file or folder without a link: name, if it
// exists, and the folders on its path are no links. Paths stay in the directory anyway, but a
// link of the server could lead to a file that holds secrets under another name.
func (d *Dir) Direct(name string) error {
	for p := name; p != "."; p = filepath.Dir(p) {
		info, err := d.Lstat(p)
		switch {
		case errors.Is(err, fs.ErrNotExist):
		case err != nil:
			return err
		case info.Mode()&fs.ModeSymlink != 0:
			return &fs.PathError{Op: "open", Path: name, Err: ErrLink}
		}
	}
	return nil
}

// ReadOptional reads a file; a missing file reads as empty.
func (d *Dir) ReadOptional(name string) ([]byte, error) {
	data, err := d.ReadFile(name)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	return data, err
}

// HandOver makes uid and gid the owner of the directory, of everything in it and of
// whatever the agent creates in it from now on, for a server that runs as this user from
// its start. Links are changed, not followed. Without root, the agent leaves the owners.
func (d *Dir) HandOver(uid, gid int) error {
	if d.uid < 0 {
		return nil
	}
	d.uid, d.gid = uid, gid
	return fs.WalkDir(d.FS(), ".", func(name string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := e.Info()
		if err != nil {
			return err
		}
		if st, ok := info.Sys().(*syscall.Stat_t); ok && int(st.Uid) == uid && int(st.Gid) == gid {
			return nil
		}
		return d.Lchown(name, uid, gid)
	})
}

func (d *Dir) own(name string) error {
	if d.uid < 0 {
		return nil
	}
	return d.Lchown(name, d.uid, d.gid)
}
