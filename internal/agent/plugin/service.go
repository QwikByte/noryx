// Package plugin implements the PluginService of the agent, which manages the plugins
// and mods of servers. Files are confined to the plugin folder of the server's type and the
// folder of turned-off plugins in it.
package plugin

import (
	"context"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"errors"
	"hash"
	"io"
	"io/fs"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	"github.com/QwikByte/noryx/internal/agent/datadir"
	"github.com/QwikByte/noryx/internal/agent/grpcerr"
	"github.com/QwikByte/noryx/internal/agent/runtime"
)

const (
	maxPlugins = 1000
	// MaxSize limits plugin files; even the largest mods stay well below it.
	MaxSize = 256 << 20
	// Disabled is the folder of turned-off plugins in the plugin folder. Servers only load the
	// files in the plugin folder itself, and none loads a folder whose name starts with a dot.
	// Backups of the plugins include it.
	Disabled = ".disabled"
)

// folders are where the server types load plugins or mods from.
var folders = map[noryxv1.ServerType]string{
	noryxv1.ServerType_SERVER_TYPE_PAPER:      "plugins",
	noryxv1.ServerType_SERVER_TYPE_PURPUR:     "plugins",
	noryxv1.ServerType_SERVER_TYPE_FOLIA:      "plugins",
	noryxv1.ServerType_SERVER_TYPE_LEAF:       "plugins",
	noryxv1.ServerType_SERVER_TYPE_VELOCITY:   "plugins",
	noryxv1.ServerType_SERVER_TYPE_BUNGEECORD: "plugins",
	noryxv1.ServerType_SERVER_TYPE_FABRIC:     "mods",
	noryxv1.ServerType_SERVER_TYPE_QUILT:      "mods",
	noryxv1.ServerType_SERVER_TYPE_FORGE:      "mods",
	noryxv1.ServerType_SERVER_TYPE_NEOFORGE:   "mods",
}

// Folder returns the folder that servers of a type load plugins or mods from.
func Folder(t noryxv1.ServerType) (string, bool) {
	folder, ok := folders[t]
	return folder, ok
}

// fileName matches plugin files: a plain .jar file name without paths.
var fileName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 ._+()\[\]-]{0,127}\.jar$`)

type Service struct {
	noryxv1.UnimplementedPluginServiceServer
	rt runtime.Runtime

	mu sync.Mutex
	// sums caches the hashes of plugin files per server, as modpacks can have hundreds.
	sums map[string]map[string]sum
}

type sum struct {
	size           int64
	modified       time.Time
	sha512, sha256 string
	name           string // of the plugin, see pluginName
}

func NewService(rt runtime.Runtime) *Service {
	return &Service{rt: rt, sums: map[string]map[string]sum{}}
}

// ListPlugins lists the plugin files of a server, and the turned-off ones if asked.
func (s *Service) ListPlugins(ctx context.Context, req *noryxv1.ListPluginsRequest) (*noryxv1.ListPluginsResponse, error) {
	dir, folder, typ, err := s.open(ctx, req.GetServerId())
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	res := &noryxv1.ListPluginsResponse{Folder: folder}
	s.mu.Lock()
	known := s.sums[req.GetServerId()]
	s.mu.Unlock()
	current := map[string]sum{}
	places := []bool{false}
	if req.GetIncludeDisabled() {
		places = append(places, true)
	}
	for _, disabled := range places {
		at, err := place(dir, folder, disabled)
		var entries []fs.DirEntry
		if err == nil {
			entries, err = fs.ReadDir(dir.FS(), at)
		}
		if errors.Is(err, fs.ErrNotExist) || status.Code(err) == codes.FailedPrecondition {
			continue
		}
		if err != nil {
			return nil, toStatus(err)
		}
		for _, e := range entries {
			info, err := e.Info()
			if err != nil || !e.Type().IsRegular() || !fileName.MatchString(e.Name()) || len(res.Plugins) == maxPlugins {
				continue
			}
			name := filepath.Join(at, e.Name())
			cached, ok := known[name]
			if !ok || cached.size != info.Size() || !cached.modified.Equal(info.ModTime()) {
				// A file larger than any plugin, e.g. a huge sparse file of the server, isn't read.
				cached = sum{}
				if info.Size() <= MaxSize {
					if cached, err = inspect(dir, name, descriptors(typ)); err != nil {
						return nil, toStatus(err)
					}
				}
				cached.size, cached.modified = info.Size(), info.ModTime()
			}
			current[name] = cached
			res.Plugins = append(res.Plugins, &noryxv1.PluginFile{
				FileName: e.Name(), Size: info.Size(), Sha512: cached.sha512, Sha256: cached.sha256, Disabled: disabled, Settings: settings(dir, folder, cached.name),
			})
		}
	}
	s.mu.Lock()
	s.sums[req.GetServerId()] = current
	s.mu.Unlock()
	return res, nil
}

// inspect returns the hashes of a plugin file, with which plugin catalogues identify it, and
// the name its plugin gives itself in one of descriptors, see pluginName.
func inspect(dir *datadir.Dir, name string, descriptors []string) (sum, error) {
	f, err := dir.Open(name)
	if err != nil {
		return sum{}, err
	}
	defer f.Close()
	h := newHashes()
	size, err := io.Copy(h, f)
	if err != nil {
		return sum{}, err
	}
	s := h.sum()
	s.name = pluginName(f, size, descriptors)
	return s, nil
}

// hashes computes the SHA-512 and SHA-256 of what is written to it.
type hashes struct {
	io.Writer
	sha512, sha256 hash.Hash
}

func newHashes() hashes {
	h := hashes{sha512: sha512.New(), sha256: sha256.New()}
	h.Writer = io.MultiWriter(h.sha512, h.sha256)
	return h
}

func (h hashes) sum() sum {
	return sum{sha512: hex.EncodeToString(h.sha512.Sum(nil)), sha256: hex.EncodeToString(h.sha256.Sum(nil))}
}

func (s *Service) InstallPlugin(stream noryxv1.PluginService_InstallPluginServer) error {
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	header := first.GetHeader()
	switch {
	case header == nil:
		return status.Error(codes.InvalidArgument, "the first message must name the file")
	case !fileName.MatchString(header.GetFileName()), header.GetReplaces() != "" && !fileName.MatchString(header.GetReplaces()):
		return status.Error(codes.InvalidArgument, "Plugins are .jar files with a name of letters, digits, spaces and . _ - + ( ) [ ].")
	}
	dir, folder, _, err := s.open(stream.Context(), header.GetServerId())
	if err != nil {
		return err
	}
	defer dir.Close()
	if folder, err = place(dir, folder, header.GetDisabled()); err != nil {
		return err
	}
	if err := dir.MkdirAll(folder); err != nil {
		return toStatus(err)
	}
	h := newHashes()
	var size int64
	err = dir.Replace(filepath.Join(folder, header.GetFileName()), true, func(w io.Writer) error {
		for {
			msg, err := stream.Recv()
			switch {
			case errors.Is(err, io.EOF) && size == 0:
				return status.Error(codes.InvalidArgument, "The plugin file is empty.")
			case errors.Is(err, io.EOF):
				return nil
			case err != nil:
				return err
			}
			if size += int64(len(msg.GetData())); size > MaxSize {
				return status.Errorf(codes.ResourceExhausted, "Plugins can have up to %d MB.", MaxSize>>20)
			}
			if _, err := io.MultiWriter(w, h).Write(msg.GetData()); err != nil {
				return err
			}
		}
	})
	if err != nil {
		return toStatus(err)
	}
	if old := header.GetReplaces(); old != "" && old != header.GetFileName() {
		if err := dir.Remove(filepath.Join(folder, old)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return toStatus(err)
		}
	}
	sums := h.sum()
	return stream.SendAndClose(&noryxv1.InstallPluginResponse{Plugin: &noryxv1.PluginFile{
		FileName: header.GetFileName(), Size: size, Sha512: sums.sha512, Sha256: sums.sha256, Disabled: header.GetDisabled(),
	}})
}

func (s *Service) RemovePlugin(ctx context.Context, req *noryxv1.RemovePluginRequest) (*noryxv1.RemovePluginResponse, error) {
	if !fileName.MatchString(req.GetFileName()) {
		return nil, status.Error(codes.InvalidArgument, "invalid plugin file name")
	}
	dir, folder, _, err := s.open(ctx, req.GetServerId())
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	if folder, err = place(dir, folder, req.GetDisabled()); err != nil {
		return nil, err
	}
	return &noryxv1.RemovePluginResponse{}, toStatus(dir.Remove(filepath.Join(folder, req.GetFileName())))
}

// EnablePlugin moves a plugin file into the folder of turned-off plugins, or back.
func (s *Service) EnablePlugin(ctx context.Context, req *noryxv1.EnablePluginRequest) (*noryxv1.EnablePluginResponse, error) {
	if !fileName.MatchString(req.GetFileName()) {
		return nil, status.Error(codes.InvalidArgument, "invalid plugin file name")
	}
	dir, folder, _, err := s.open(ctx, req.GetServerId())
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	off, err := place(dir, folder, true)
	if err != nil {
		return nil, err
	}
	from, to := filepath.Join(off, req.GetFileName()), filepath.Join(folder, req.GetFileName())
	if !req.GetEnabled() {
		from, to = to, from
	}
	if info, err := dir.Lstat(from); err != nil || !info.Mode().IsRegular() {
		return nil, status.Error(codes.NotFound, "Plugin not found.")
	}
	if _, err := dir.Lstat(to); err == nil {
		return nil, status.Errorf(codes.AlreadyExists, "%s exists already.", filepath.ToSlash(to))
	}
	if err := dir.MkdirAll(off); err != nil {
		return nil, toStatus(err)
	}
	return &noryxv1.EnablePluginResponse{}, toStatus(dir.Rename(from, to))
}

// place returns the folder of a server's plugin files that are turned on, or off with
// disabled. The server owns the folder of turned-off ones, which must be a folder, not a link.
func place(dir *datadir.Dir, folder string, disabled bool) (string, error) {
	if !disabled {
		return folder, nil
	}
	off := filepath.Join(folder, Disabled)
	if info, err := dir.Lstat(off); err == nil && !info.IsDir() {
		return "", status.Errorf(codes.FailedPrecondition, "%s isn't a folder.", filepath.ToSlash(off))
	}
	return off, nil
}

// open opens the data directory of a server and returns its plugin folder and its type.
func (s *Service) open(ctx context.Context, id string) (*datadir.Dir, string, noryxv1.ServerType, error) {
	if !runtime.ValidID(id) {
		return nil, "", 0, status.Error(codes.InvalidArgument, "invalid server ID")
	}
	srv, err := runtime.Find(ctx, s.rt, id)
	if err != nil {
		return nil, "", 0, toStatus(err)
	}
	folder, ok := Folder(srv.Type)
	if !ok {
		return nil, "", 0, status.Error(codes.FailedPrecondition, "Vanilla and Bedrock servers can't load plugins or mods.")
	}
	dir, err := s.rt.Data(ctx, id)
	if err != nil {
		return nil, "", 0, toStatus(err)
	}
	return dir, folder, srv.Type, nil
}

func toStatus(err error) error {
	return grpcerr.Status(err, grpcerr.Known{Err: fs.ErrNotExist, Code: codes.NotFound, Text: "Plugin not found."})
}
