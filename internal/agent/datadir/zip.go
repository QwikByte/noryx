package datadir

import (
	"archive/zip"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/QwikByte/noryx/internal/agent/progress"
	"github.com/QwikByte/noryx/internal/agent/storage"
)

// stored lists extensions of files that are compressed already; deflating them again
// costs time for nothing, which matters for worlds of several gigabytes.
var stored = map[string]bool{".mca": true, ".jar": true, ".zip": true, ".gz": true, ".png": true, ".jpg": true, ".ogg": true}

// maxEdited limits the files a Censor changes, which are read at once.
const maxEdited = 1 << 20

// A Censor decides about each file of an archive, by its name, whether to omit it and how
// to edit its content, if at all. WriteZip asks it once the file is open, so that what marks
// a file as secret before it is written always counts, and about each folder before it
// walks it, to omit the folder with all in it.
type Censor func(name string) (omit bool, edit func([]byte) []byte)

// WriteZip writes files and folders of root as a ZIP archive; "." writes all of it.
// Symbolic links, other special files and temporary files of the agent are left out, and
// censor, if not nil, omits files and folders or edits files.
func WriteZip(ctx context.Context, w io.Writer, root *os.Root, censor Censor, paths ...string) error {
	zw := zip.NewWriter(w)
	var err error
	for _, p := range paths {
		if err = fs.WalkDir(root.FS(), p, func(name string, d fs.DirEntry, err error) error {
			switch {
			case err != nil || ctx.Err() != nil:
				return cmp.Or(err, ctx.Err())
			case d.IsDir() && (IsTemp(d.Name()) || name != "." && censor != nil && omitted(censor, name)):
				return fs.SkipDir
			case name == "." || IsTemp(d.Name()) || (!d.IsDir() && !d.Type().IsRegular()):
				return nil
			}
			return addFile(ctx, zw, root, name, d, censor)
		}); err != nil {
			break
		}
	}
	return errors.Join(err, zw.Close())
}

func omitted(censor Censor, name string) bool {
	omit, _ := censor(name)
	return omit
}

// CopyZip copies the archive zr to w, without compressing it again, as censor says.
func CopyZip(w io.Writer, zr *zip.Reader, censor Censor) error {
	zw := zip.NewWriter(w)
	for _, f := range zr.File {
		omit, edit := censor(f.Name)
		var err error
		switch {
		case omit:
			continue
		case edit == nil:
			err = zw.Copy(f)
		default:
			err = copyEdited(zw, f, edit)
		}
		if err != nil {
			return err
		}
	}
	return zw.Close()
}

func copyEdited(zw *zip.Writer, f *zip.File, edit func([]byte) []byte) error {
	r, err := f.Open()
	if err != nil {
		return err
	}
	defer r.Close()
	data, err := readEdited(r)
	if err != nil {
		return err
	}
	header := f.FileHeader
	fw, err := zw.CreateHeader(&header)
	if err == nil {
		_, err = fw.Write(edit(data))
	}
	return err
}

func readEdited(r io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, maxEdited+1))
	if err == nil && len(data) > maxEdited {
		err = fmt.Errorf("a file with secrets has more than %d bytes", maxEdited)
	}
	return data, err
}

func addFile(ctx context.Context, zw *zip.Writer, root *os.Root, name string, d fs.DirEntry, censor Censor) error {
	info, err := d.Info()
	if err != nil {
		return err
	}
	var f *os.File
	var edit func([]byte) []byte
	if !d.IsDir() {
		if f, err = openPlain(root, name); err != nil { // the server may have put a named pipe there meanwhile
			return err
		}
		defer f.Close()
		if censor != nil {
			var omit bool
			if omit, edit = censor(name); omit {
				return nil
			}
		}
	}
	header, err := zip.FileInfoHeader(info)
	if err != nil {
		return err
	}
	header.Name = name
	if d.IsDir() {
		header.Name += "/"
	} else if !stored[strings.ToLower(path.Ext(name))] {
		header.Method = zip.Deflate
	}
	fw, err := zw.CreateHeader(header)
	if err != nil || d.IsDir() {
		return err
	}
	if edit == nil {
		_, err = io.Copy(fw, progress.Reader(ctx, f))
		return err
	}
	data, err := readEdited(f)
	if err == nil {
		_, err = fw.Write(edit(data))
	}
	return err
}

// UnpackedSize returns the size of the files of an archive as their headers tell, at most
// math.MaxInt64.
func UnpackedSize(files []*zip.File) int64 {
	var size uint64 // never more than twice math.MaxInt64, so it can't overflow
	for _, f := range files {
		size = min(size+min(f.UncompressedSize64, math.MaxInt64), math.MaxInt64)
	}
	return int64(size) //nolint:gosec // at most math.MaxInt64
}

// ExtractZip extracts an archive written by WriteZip into the directory dest. Entries
// belong to the owner of the data directory and keep their permissions, without special
// bits, and their modification times. It returns a storage.FullError unless the files fit
// with the space kept free, and stops before it leaves less, as the sizes may be wrong.
func (d *Dir) ExtractZip(ctx context.Context, zr *zip.Reader, dest string) error {
	top, err := d.Open(".")
	if err != nil {
		return err
	}
	defer top.Close()
	if err := storage.Fits(top, UnpackedSize(zr.File)); err != nil {
		return err
	}
	if err := d.MkdirAll(dest); err != nil {
		return err
	}
	for _, f := range zr.File {
		name := filepath.FromSlash(strings.TrimSuffix(f.Name, "/"))
		if !filepath.IsLocal(name) {
			return fmt.Errorf("invalid name in archive: %q", f.Name)
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		target := filepath.Join(dest, name)
		if f.FileInfo().IsDir() {
			if err := d.MkdirAll(target); err != nil {
				return err
			}
			continue
		}
		if err := d.MkdirAll(filepath.Dir(target)); err != nil {
			return err
		}
		if err := d.extract(ctx, f, target, top); err != nil {
			return err
		}
	}
	return nil
}

func (d *Dir) extract(ctx context.Context, f *zip.File, name string, top *os.File) error {
	r, err := f.Open()
	if err != nil {
		return err
	}
	defer r.Close()
	out, err := d.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, filePerm)
	if err != nil {
		return err
	}
	_, err = io.Copy(storage.Guard(out, top), progress.Reader(ctx, r)) //nolint:gosec // archives are written by the agent itself
	if err := errors.Join(err, out.Close(), d.Chmod(name, f.Mode().Perm()), d.own(name)); err != nil {
		return err
	}
	return d.Chtimes(name, f.Modified, f.Modified)
}
