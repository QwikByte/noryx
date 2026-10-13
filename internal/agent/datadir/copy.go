package datadir

import (
	"cmp"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/QwikByte/noryx/internal/agent/progress"
	"github.com/QwikByte/noryx/internal/agent/storage"
)

// Copy copies the data directory src into dst, which must not exist yet. Files keep their
// permissions and, if the agent runs as root, their owners. Symbolic links and other
// special files are left out, so the copy can't reach anything outside of src. It returns a
// storage.FullError unless the files fit with the space kept free, and stops before it leaves
// less, as they may grow meanwhile.
func Copy(ctx context.Context, src, dst string) error {
	from, err := os.OpenRoot(src)
	if err != nil {
		return err
	}
	defer from.Close()
	if err := os.Mkdir(dst, dirPerm); err != nil {
		return err
	}
	to, err := os.OpenRoot(dst)
	if err != nil {
		return err
	}
	defer to.Close()
	top, err := to.Open(".")
	if err != nil {
		return err
	}
	defer top.Close()
	size := Size(from.FS(), ".")
	if err := storage.Fits(top, size); err != nil {
		return err
	}
	progress.Step(ctx, "copy", size)
	return fs.WalkDir(from.FS(), ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil || ctx.Err() != nil {
			return cmp.Or(err, ctx.Err())
		}
		info, err := d.Info()
		switch {
		case err != nil:
			return err
		case d.IsDir() && name != ".":
			err = to.Mkdir(name, dirPerm)
		case d.Type().IsRegular():
			err = copyFile(ctx, from, to, name, top)
		case !d.IsDir():
			return nil
		}
		if err != nil {
			return err
		}
		return keepMode(to, name, info)
	})
}

// CopyTree copies the file or folder from to to, which must not exist, within the directory,
// e.g. for the file manager. The copies are new files of the directory's owner. Symbolic
// links, other special files and temporary files of the agent are left out. If copying
// fails, the copy goes away.
func (d *Dir) CopyTree(ctx context.Context, from, to string) (err error) {
	if progress.Active(ctx) {
		progress.Step(ctx, "copy", Size(d.FS(), filepath.ToSlash(from)))
	}
	created := false // a folder at to, which goes away if copying fails
	defer func() {
		if err != nil && created {
			err = errors.Join(err, d.RemoveAll(to))
		}
	}()
	return fs.WalkDir(d.FS(), filepath.ToSlash(from), func(name string, e fs.DirEntry, err error) error {
		if err != nil || ctx.Err() != nil {
			return cmp.Or(err, ctx.Err())
		}
		target := filepath.Join(to, strings.TrimPrefix(filepath.FromSlash(name), from))
		switch {
		case IsTemp(e.Name()) && e.IsDir():
			return fs.SkipDir
		case e.IsDir(): // the first is to, which mustn't exist
			if err := d.Mkdir(target, dirPerm); err != nil {
				return err
			}
			created = true
			return d.own(target)
		case IsTemp(e.Name()) || !e.Type().IsRegular():
			return nil
		}
		return d.Replace(target, false, func(w io.Writer) error {
			in, err := d.Open(filepath.FromSlash(name)) // the server may have put a named pipe there meanwhile
			if err != nil {
				return err
			}
			defer in.Close()
			_, err = io.Copy(w, progress.Reader(ctx, in))
			return err
		})
	})
}

func copyFile(ctx context.Context, from, to *os.Root, name string, top *os.File) error {
	in, err := openPlain(from, name) // the server may have put a named pipe there meanwhile
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := to.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, filePerm)
	if err != nil {
		return err
	}
	_, err = io.Copy(storage.Guard(out, top), progress.Reader(ctx, in))
	return errors.Join(err, out.Close())
}

// Size returns the size of the regular files at paths of fsys and below them, without the
// temporary folders of the agent, e.g. a backup extracted to be restored. Missing paths
// have none.
func Size(fsys fs.FS, paths ...string) int64 {
	var size int64
	for _, p := range paths {
		_ = fs.WalkDir(fsys, p, func(_ string, d fs.DirEntry, err error) error {
			switch {
			case err != nil:
				return nil //nolint:nilerr // what can't be read, e.g. a missing path, counts as nothing
			case d.IsDir() && IsTemp(d.Name()):
				return fs.SkipDir
			}
			if info, err := d.Info(); err == nil && info.Mode().IsRegular() {
				size += info.Size()
			}
			return nil
		})
	}
	return size
}

// keepMode gives a copied entry the permissions and owner of the original.
func keepMode(root *os.Root, name string, info fs.FileInfo) error {
	if err := root.Chmod(name, info.Mode().Perm()); err != nil {
		return err
	}
	if st, ok := info.Sys().(*syscall.Stat_t); ok && os.Geteuid() == 0 {
		return root.Lchown(name, int(st.Uid), int(st.Gid))
	}
	return nil
}
