package backup

import (
	"archive/zip"
	"context"
	"errors"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	"github.com/QwikByte/noryx/internal/agent/archive"
	"github.com/QwikByte/noryx/internal/agent/datadir"
	"github.com/QwikByte/noryx/internal/agent/fileset"
	"github.com/QwikByte/noryx/internal/agent/network"
	"github.com/QwikByte/noryx/internal/agent/secrets"

	"github.com/QwikByte/noryx/internal/agent/progress"
)

// chosen returns the files and folders that restoring a backup replaces: those asked for,
// each of which the backup must hold, or all of the backup.
func chosen(zr *zip.Reader, b Archive, requested []string) ([]string, error) {
	if len(requested) == 0 {
		return b.Paths, nil
	}
	if len(requested) > noryxv1.MaxBackupPaths {
		return nil, status.Errorf(codes.InvalidArgument, "Choose at most %d files or folders.", noryxv1.MaxBackupPaths)
	}
	var paths []string
	for _, p := range requested {
		name, ok := datadir.Name(p)
		if !ok {
			return nil, status.Errorf(codes.InvalidArgument, "The path %q is invalid.", p)
		}
		// A path around those of the backup would remove what the backup doesn't hold.
		if !slices.ContainsFunc(b.Paths, within(name)) || !slices.ContainsFunc(zr.File, func(f *zip.File) bool { return within(entry(f))(name) }) {
			return nil, status.Errorf(codes.NotFound, "The backup doesn't hold %s.", p)
		}
		paths = append(paths, name)
	}
	return datadir.Outermost(paths), nil
}

// entry returns the name of a file or folder of an archive in the data directory.
func entry(f *zip.File) string { return filepath.FromSlash(strings.TrimSuffix(f.Name, "/")) }

// list returns the files and folders in a folder of an archive, folders first, and whether
// the archive has the folder.
func list(zr *zip.Reader, folder string, hidden secrets.Files) ([]*noryxv1.FileInfo, bool) {
	prefix := folder + string(filepath.Separator)
	if folder == "." {
		prefix = ""
	}
	byName := map[string]*noryxv1.FileInfo{}
	found := folder == "."
	for _, f := range zr.File {
		name := entry(f)
		rel, ok := strings.CutPrefix(name, prefix)
		if !ok || rel == "" {
			found = found || name == folder
			continue
		}
		found = true
		child, _, deeper := strings.Cut(rel, string(filepath.Separator))
		if !deeper && !f.FileInfo().IsDir() && hidden.Hidden(name) {
			continue
		}
		e := byName[child]
		if e == nil {
			e = &noryxv1.FileInfo{Name: child, ModifiedUnix: f.Modified.Unix()}
			byName[child] = e
		}
		e.Directory = e.Directory || deeper || f.FileInfo().IsDir()
		e.Size += int64(f.UncompressedSize64) //nolint:gosec // a size that fits on a disk
	}
	files := slices.Collect(maps.Values(byName))
	slices.SortFunc(files, func(a, b *noryxv1.FileInfo) int {
		switch {
		case a.GetDirectory() && !b.GetDirectory():
			return -1
		case b.GetDirectory() && !a.GetDirectory():
			return 1
		}
		return strings.Compare(a.GetName(), b.GetName())
	})
	return files, found
}

// stage extracts what a backup holds of paths into a temporary folder of the data directory,
// which the caller removes unless its name is empty. Nothing of the server changes yet, so it
// can keep running meanwhile, and a backup that doesn't fit with the space kept free is
// refused before anything is extracted. Untrusted backups are staged by stageUntrusted.
func stage(ctx context.Context, dir *datadir.Dir, zr *zip.Reader, paths []string) (string, error) {
	files := slices.DeleteFunc(slices.Clone(zr.File), func(f *zip.File) bool { return !slices.ContainsFunc(paths, within(entry(f))) })
	progress.Step(ctx, "restore", datadir.UnpackedSize(files))
	tmp := datadir.TempName(".")
	return tmp, dir.ExtractZip(ctx, &zip.Reader{File: files}, tmp)
}

// stageUntrusted stages what an untrusted backup holds of paths like stage, checked and
// confined like an archive of the file manager: see archive.Extract. It leaves out the files a
// server never takes from an archive.
func stageUntrusted(ctx context.Context, dir *datadir.Dir, b Archive, paths []string) (string, error) {
	f, err := os.Open(b.Path())
	if err != nil {
		return "", err
	}
	defer f.Close()
	a, err := archive.Open(f, b.Size, archive.Backups)
	if err != nil {
		return "", err
	}
	tmp := datadir.TempName(".")
	if err := dir.MkdirAll(tmp); err != nil {
		return tmp, err
	}
	staged, err := dir.Sub(tmp)
	if err != nil {
		return tmp, err
	}
	defer staged.Close()
	_, err = a.Extract(ctx, staged, ".", archive.Options{
		Protected: archive.Foreign(fileset.Read(dir).Secrets()), Skip: true, Step: "restore",
		Only: func(name string) bool { return slices.ContainsFunc(paths, within(filepath.FromSlash(name))) },
	})
	return tmp, err
}

// kept returns the files and folders that restoring a backup leaves as they are: what the
// backup left out, and the files of the server with secrets of its own, of its network and
// of file sets. A backup must not bring back the secret of a network the server left since,
// nor one of a set that no longer targets it, nor the manifest of file sets, which would
// hide less; a backup of another server holds none of them. Restoring an untrusted backup
// also leaves the other files of the agent as they are, which stageUntrusted leaves out, e.g.
// the pardons that wait for the end of temporary bans.
func kept(dir *datadir.Dir, b Archive) []string {
	hidden := fileset.Read(dir).Secrets()
	paths := slices.Concat(b.Exclude, hidden.Paths())
	if b.Untrusted {
		top, _ := names(dir, ".") // what can't be listed can't be swapped either
		paths = append(paths, slices.DeleteFunc(top, func(name string) bool { return !archive.Foreign(hidden)(name) })...)
	}
	return paths
}

// keep gives the staged backup of a server the forwarding settings of its network as they
// are, as it may have left the network or joined another since, and its secrets wherever a
// backup of another server says "<hidden>", or in place of all secrets of an untrusted one. It
// changes only the staged backup, which swap moves into place, so it runs while the server is
// stopped; if it fails, the server stays as it is.
func keep(dir *datadir.Dir, typ noryxv1.ServerType, staged string, untrusted bool) error {
	restored, err := dir.Sub(staged)
	if err != nil {
		return err
	}
	defer restored.Close()
	fill := secrets.Fill
	if untrusted {
		fill = secrets.Take
	}
	if err := fill(dir, restored); err != nil {
		return err
	}
	return network.KeepForwarding(dir, restored, typ)
}

// swap replaces paths of the data directory with their staged state, also removing what the
// staged state lacks, except for the paths kept, which stay as they are.
func swap(dir *datadir.Dir, staged string, paths, kept []string) error {
	for _, k := range kept { // so that no folder brings them back
		if err := dir.RemoveAll(filepath.Join(staged, k)); err != nil {
			return err
		}
	}
	for _, p := range paths {
		if err := replace(dir, staged, p, kept); err != nil {
			return err
		}
	}
	return nil
}

// replace replaces a path with its staged state. A folder with paths kept in it is replaced
// entry by entry.
func replace(dir *datadir.Dir, staged, p string, kept []string) error {
	if slices.Contains(kept, p) {
		return nil
	}
	if p == "." || slices.ContainsFunc(kept, func(k string) bool { return k != p && within(k)(p) && exists(dir, k) }) {
		current, err := names(dir, p)
		if err != nil {
			return err
		}
		restored, _ := names(dir, filepath.Join(staged, p)) // none where the backup has no folder
		all := slices.Concat(current, restored)
		slices.Sort(all)
		for _, name := range slices.Compact(all) { // a second removal would hit the restored entry
			if err := replace(dir, staged, filepath.Join(p, name), kept); err != nil {
				return err
			}
		}
		return nil
	}
	if err := dir.RemoveAll(p); err != nil {
		return err
	}
	from := filepath.Join(staged, p)
	if _, err := dir.Lstat(from); errors.Is(err, fs.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	if err := dir.MkdirAll(filepath.Dir(p)); err != nil {
		return err
	}
	return dir.Rename(from, p)
}

func exists(dir *datadir.Dir, name string) bool {
	_, err := dir.Lstat(name)
	return err == nil
}

// names lists a folder of the data directory, without temporary files.
func names(dir *datadir.Dir, folder string) ([]string, error) {
	entries, err := fs.ReadDir(dir.FS(), filepath.ToSlash(folder))
	var list []string
	for _, e := range entries {
		if !datadir.IsTemp(e.Name()) {
			list = append(list, e.Name())
		}
	}
	return list, err
}
