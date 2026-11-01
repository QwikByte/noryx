package fileset

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	"github.com/QwikByte/noryx/internal/agent/datadir"
	"github.com/QwikByte/noryx/internal/agent/secrets"
	"github.com/QwikByte/noryx/internal/logging"
)

const maxManifest = 8 << 20

var idPattern = regexp.MustCompile(`^[a-z2-7]{26}$`)

// manifest is noryx-filesets.json in the data of a server, so that it moves with the server.
// It is left out of backups and restores, and the file manager hides it.
type manifest struct {
	Sets map[string]*applied `json:"sets,omitempty"`
	// Marked are the files that held secrets of a set. They stay hidden for the life of the
	// server, also after they left the set or a backup brought back an older copy.
	Marked []string `json:"marked,omitempty"`
}

// applied is a set on a server, with its files by path.
type applied struct {
	Name     string           `json:"name"`
	Version  int64            `json:"version"`
	Revision string           `json:"revision"`
	Files    map[string]*file `json:"files"`
}

type file struct {
	// SHA256 is that of what the set wrote; empty if it wrote nothing, as the server had the
	// file already and the set only writes missing ones.
	SHA256        string `json:"sha256,omitempty"`
	Secret        bool   `json:"secret,omitempty"`
	OnlyIfMissing bool   `json:"onlyIfMissing,omitempty"`
}

// read reads the manifest of a server. The server can change it, so whatever isn't valid
// is left out, and one that isn't JSON is an error.
func read(dir *datadir.Dir) (manifest, error) {
	m := manifest{Sets: map[string]*applied{}}
	f, err := dir.Open(ManifestFile)
	if errors.Is(err, fs.ErrNotExist) {
		return m, nil
	}
	if err != nil {
		return m, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxManifest))
	if err == nil {
		err = json.Unmarshal(data, &m)
	}
	if err != nil {
		return m, fmt.Errorf("%s is damaged: %w", ManifestFile, err)
	}
	valid := func(p string) bool { clean, problem := noryxv1.CleanFileSetPath(p); return problem == "" && clean == p }
	maps.DeleteFunc(m.Sets, func(id string, a *applied) bool {
		if a != nil {
			maps.DeleteFunc(a.Files, func(p string, f *file) bool { return f == nil || !valid(p) })
		}
		return a == nil || !idPattern.MatchString(id) || len(a.Files) == 0
	})
	if m.Sets == nil {
		m.Sets = map[string]*applied{}
	}
	m.Marked = slices.DeleteFunc(m.Marked, func(p string) bool { return !valid(p) })
	return m, nil
}

func (m manifest) write(dir *datadir.Dir) error {
	slices.Sort(m.Marked)
	m.Marked = slices.Compact(m.Marked)
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return dir.WriteFile(ManifestFile, data)
}

// owner returns the set that has a file, other than the set except, or "".
func (m manifest) owner(path, except string) string {
	for id, a := range m.Sets {
		if _, ok := a.Files[path]; ok && id != except {
			return a.Name
		}
	}
	return ""
}

// Files tells about the files of sets in the data of a server.
type Files struct{ m manifest }

// Read reads what sets wrote in the data of a server. A damaged manifest, which only the
// server itself can cause, counts as none.
func Read(dir *datadir.Dir) Files {
	m, err := read(dir)
	if err != nil {
		slog.Warn("Can't read which files of file sets a server has", logging.Files, "err", err)
	}
	return Files{m}
}

// Secrets returns the files with secrets of the server: besides those of every server, the
// files that held secrets of a set, and the manifest, which tells their hashes.
func (f Files) Secrets() secrets.Files {
	marked := []string{ManifestFile}
	for _, p := range f.m.Marked {
		marked = append(marked, filepath.FromSlash(p))
	}
	return secrets.With(marked...)
}

// Set returns the name of the set that wrote a file, given as a clean path, or "".
func (f Files) Set(name string) string { return f.m.owner(filepath.ToSlash(name), "") }

// Watch returns the files with secrets of a server as they are each time it is called, for
// what reads files over a while, e.g. an archive of a folder: it reads the manifest again
// whenever it was replaced. Asked after a file was opened, it knows whether the file holds
// secrets, as a set marks files before it writes them.
func Watch(dir *datadir.Dir) func() secrets.Files {
	var seen fs.FileInfo
	var current secrets.Files
	return func() secrets.Files {
		info, err := dir.Lstat(ManifestFile)
		if err != nil || seen == nil || !os.SameFile(info, seen) || !info.ModTime().Equal(seen.ModTime()) || info.Size() != seen.Size() {
			seen, current = info, Read(dir).Secrets()
		}
		return current
	}
}

// maxDigest is larger than any file a set writes, even a megabyte of placeholders of the
// longest secrets filled in.
const maxDigest = 128 << 20

// digest returns the SHA-256 of a file in hex, or "" if it is no regular file or too large
// to be one a set wrote, e.g. a huge sparse file of the server, and whether anything exists
// at its path.
func digest(dir *datadir.Dir, name string) (sum string, exists bool, err error) {
	info, err := dir.Lstat(filepath.FromSlash(name))
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return "", false, nil
	case err != nil:
		return "", false, err
	case !info.Mode().IsRegular() || info.Size() > maxDigest:
		return "", true, nil
	}
	f, err := dir.Open(filepath.FromSlash(name))
	if err != nil {
		return "", true, err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", true, err
	}
	return hex.EncodeToString(h.Sum(nil)), true, nil
}

func hash(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}
