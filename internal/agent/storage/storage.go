// Package storage manages the locations on this node where servers keep their data.
// Only the node's administrator adds locations, with the local CLI. The master can
// only choose among them, so it can't mount other host directories into containers.
package storage

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync/atomic"

	"golang.org/x/sys/unix"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
)

// Default is the location inside the agent's data directory, which always exists.
const Default = "default"

var (
	ErrUnknown  = errors.New("unknown storage location")
	namePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)
)

type Location struct {
	Name       string
	Path       string
	FreeBytes  uint64
	TotalBytes uint64
}

// Locations are kept in a file in the agent's data directory. It is read on every use,
// so locations added with the CLI apply without restarting the agent.
type Locations struct {
	dataDir string
	file    string
}

func New(dataDir string) *Locations {
	return &Locations{dataDir: dataDir, file: filepath.Join(dataDir, "storage.json")}
}

// List returns all locations with their disk usage, the default location first.
func (l *Locations) List() ([]Location, error) {
	paths, err := l.load()
	if err != nil {
		return nil, err
	}
	locations := []Location{{Name: Default, Path: l.defaultPath()}}
	for _, name := range slices.Sorted(maps.Keys(paths)) {
		locations = append(locations, Location{Name: name, Path: paths[name]})
	}
	for i, loc := range locations {
		var st unix.Statfs_t
		if unix.Statfs(loc.Path, &st) == nil {
			locations[i].FreeBytes, locations[i].TotalBytes = free(st), st.Blocks*uint64(st.Bsize) //nolint:gosec // never negative
		}
	}
	return locations, nil
}

// MinFree is the least space that uploads and backups leave free on a file system, so that
// the servers can keep saving their worlds. The settings of the node can keep more free.
const MinFree = noryxv1.MinKeepFreeGB << 30

// keepFree is the space that uploads and backups leave free, which KeepFree sets; less than
// MinFree, e.g. before it is set, keeps MinFree.
var keepFree atomic.Uint64

// KeepFree sets the space that uploads and backups leave free on every file system of the
// node, at least MinFree.
func KeepFree(bytes uint64) { keepFree.Store(bytes) }

func kept() uint64 { return max(keepFree.Load(), MinFree) }

// FullError refuses what would leave less than the space kept free.
type FullError struct{ Free, Need, Kept uint64 }

func (e FullError) Error() string {
	return fmt.Sprintf("The node has %.1f GB free, but this needs %.1f GB, as %d GB stays free for the servers.",
		float64(e.Free)/(1<<30), float64(e.Need)/(1<<30), e.Kept>>30)
}

// Fits returns a FullError unless size more bytes leave the space kept free on the file system
// of the open file or folder f. A file system whose free space is unknown passes.
func Fits(f *os.File, size int64) error {
	var st unix.Statfs_t
	kept := kept()
	need := uint64(max(size, 0)) + kept
	if unix.Fstatfs(int(f.Fd()), &st) == nil && need > free(st) { //nolint:gosec // a file descriptor fits an int
		return FullError{free(st), need, kept}
	}
	return nil
}

//nolint:gosec // block counts and sizes are never negative
func free(st unix.Statfs_t) uint64 { return st.Bavail * uint64(st.Bsize) }

// checkEvery is how many bytes a Guard writes between two checks of the free space.
const checkEvery = 64 << 20

// Guard returns a writer to w that stops writing before the free space of the file system of
// the open file or folder f falls below the space kept free, which it checks every 64 MiB,
// e.g. while an upload of unknown size is written.
func Guard(w io.Writer, f *os.File) io.Writer { return &guard{w: w, f: f} }

type guard struct {
	w         io.Writer
	f         *os.File
	unchecked int
}

func (g *guard) Write(p []byte) (int, error) {
	if g.unchecked += len(p); g.unchecked >= checkEvery {
		g.unchecked = 0
		if err := Fits(g.f, int64(len(p))); err != nil {
			return 0, err
		}
	}
	return g.w.Write(p)
}

// Path returns the directory of a location; an empty name means the default location.
func (l *Locations) Path(name string) (string, error) {
	if name == "" || name == Default {
		return l.defaultPath(), os.MkdirAll(l.defaultPath(), 0o700)
	}
	paths, err := l.load()
	if err != nil {
		return "", err
	}
	path, ok := paths[name]
	if !ok {
		return "", fmt.Errorf("%w %q", ErrUnknown, name)
	}
	return path, nil
}

// BackupPath returns the directory with the backups kept in a location; an empty name
// means the default location. Only the agent can access it, containers can't.
func (l *Locations) BackupPath(name string) (string, error) {
	if name == "" || name == Default {
		return filepath.Join(l.dataDir, "backups"), nil
	}
	path, err := l.Path(name)
	return filepath.Join(path, "backups"), err
}

// Add registers a directory as a location, creating it if needed.
func (l *Locations) Add(name, path string) error {
	if !namePattern.MatchString(name) || name == Default {
		return fmt.Errorf("invalid name %q: use up to 32 lowercase letters, digits or '-'", name)
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	if path == "/" || path == l.dataDir || strings.HasPrefix(path, l.dataDir+string(filepath.Separator)) {
		return fmt.Errorf("choose a directory outside of / and the agent's data directory")
	}
	paths, err := l.load()
	if err != nil {
		return err
	}
	if _, ok := paths[name]; ok {
		return fmt.Errorf("location %q already exists", name)
	}
	// Only the runtime, which runs as root, needs access; containers see their own directory only.
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	paths[name] = path
	return l.save(paths)
}

// Remove unregisters a location. Its directory, data and backups are kept.
func (l *Locations) Remove(name string) error {
	paths, err := l.load()
	if err != nil {
		return err
	}
	if _, ok := paths[name]; !ok {
		return fmt.Errorf("%w %q", ErrUnknown, name)
	}
	delete(paths, name)
	return l.save(paths)
}

func (l *Locations) defaultPath() string { return filepath.Join(l.dataDir, "servers") }

func (l *Locations) load() (map[string]string, error) {
	paths := map[string]string{}
	data, err := os.ReadFile(l.file)
	if errors.Is(err, fs.ErrNotExist) {
		return paths, nil
	}
	if err != nil {
		return nil, err
	}
	return paths, json.Unmarshal(data, &paths)
}

func (l *Locations) save(paths map[string]string) error {
	data, err := json.MarshalIndent(paths, "", "  ")
	if err != nil {
		return err
	}
	tmp := l.file + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, l.file)
}
