// Package modpack creates servers from Modrinth modpacks and moves them to other versions of
// their pack. The master downloads a pack and the files its servers need from Modrinth's CDN,
// checks each against its SHA-512 hash and writes them into the data of a server, which then
// is a Fabric, Quilt, Forge or NeoForge server like any other, with the Minecraft and loader
// version of the pack. It remembers the files it wrote, to tell later which of them the
// administrator changed.
package modpack

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha512"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"path"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	"github.com/QwikByte/noryx/internal/master/files"
	"github.com/QwikByte/noryx/internal/master/httpapi"
	"github.com/QwikByte/noryx/internal/master/modrinth"
	"github.com/QwikByte/noryx/internal/master/operation"
)

const (
	indexFile     = "modrinth.index.json"
	maxIndexBytes = 16 << 20
	maxFiles      = 5000
	// downloads is how many files are downloaded and sent to the agent at once.
	downloads = 4
	// HealthStart is how many minutes servers created from a pack get to start before their
	// failed health checks count, unless another start period is chosen, as packs load many mods.
	HealthStart = 10
)

// loaders are the server types of the mod loaders packs depend on, by their name in the index.
var loaders = map[string]noryxv1.ServerType{
	"fabric-loader": noryxv1.ServerType_SERVER_TYPE_FABRIC,
	"quilt-loader":  noryxv1.ServerType_SERVER_TYPE_QUILT,
	"forge":         noryxv1.ServerType_SERVER_TYPE_FORGE,
	"neoforge":      noryxv1.ServerType_SERVER_TYPE_NEOFORGE,
}

// skipped are files packs don't replace: server.properties, which the manager and the agent
// keep with its secrets, and the EULA, which the operator accepted.
var skipped = []string{"server.properties", "eula.txt"}

var (
	errNoPack = httpapi.Errorf(http.StatusBadRequest, "This isn't a version of a Modrinth modpack.")
	// Versions end up in variables of the server image, like those the agent accepts.
	versionPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]{0,63}$`)
	sha512Pattern  = regexp.MustCompile(`^[0-9a-f]{128}$`)
)

// Pack is a version of a modpack, downloaded and checked, with what its servers run.
type Pack struct {
	Project, Version string // IDs on Modrinth
	Number           string // the version number
	Type             noryxv1.ServerType
	GameVersion      string
	LoaderVersion    string
	// files are what the pack writes into a server's data, each path once: downloads, and
	// files of the pack itself, those of server-overrides replacing the others.
	files []file
}

// file is a file of a pack: a download, or with zip set one of the pack itself.
type file struct {
	path     string
	sha512   string
	project  string // the Modrinth project of a download, if its address tells
	download modrinth.File
	zip      *zip.File
}

// index is the modrinth.index.json of a pack, see
// https://support.modrinth.com/en/articles/8802351-modrinth-modpack-format-mrpack.
type index struct {
	FormatVersion int    `json:"formatVersion"`
	Game          string `json:"game"`
	Files         []struct {
		Path   string `json:"path"`
		Hashes struct {
			SHA512 string `json:"sha512"`
		} `json:"hashes"`
		// Env tells whether servers need the file: required, optional or unsupported.
		Env *struct {
			Server string `json:"server"`
		} `json:"env"`
		Downloads []string `json:"downloads"`
		FileSize  int64    `json:"fileSize"`
	} `json:"files"`
	Dependencies map[string]string `json:"dependencies"`
}

// Nodes provides connections to the agents.
type Nodes interface {
	Conn(ctx context.Context, nodeID string) (grpc.ClientConnInterface, error)
}

type Service struct {
	db       *sql.DB
	nodes    Nodes
	modrinth *modrinth.Client

	mu       sync.Mutex
	updating map[string]bool // servers whose pack is being updated, by node and server ID
}

func NewService(db *sql.DB, nodes Nodes, client *modrinth.Client) *Service {
	return &Service{db: db, nodes: nodes, modrinth: client, updating: map[string]bool{}}
}

// Resolve downloads a version of a modpack and checks that servers can run it.
func (s *Service) Resolve(ctx context.Context, project, version string) (*Pack, error) {
	v, err := s.modrinth.Version(ctx, version)
	if err != nil {
		return nil, err
	}
	f, ok := v.File()
	if v.ProjectID != project || !ok || !strings.HasSuffix(f.Filename, ".mrpack") {
		return nil, errNoPack
	}
	data, err := s.modrinth.Download(ctx, f)
	if err != nil {
		return nil, err
	}
	archive, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, errNoPack
	}
	p, err := s.parse(archive)
	if err == nil {
		p.Project, p.Version, p.Number = v.ProjectID, v.ID, v.VersionNumber
	}
	return p, err
}

func (s *Service) parse(archive *zip.Reader) (*Pack, error) {
	var idx index
	if err := readJSON(archive, indexFile, &idx); err != nil || idx.FormatVersion != 1 || idx.Game != "minecraft" {
		return nil, errNoPack
	}
	p := &Pack{GameVersion: idx.Dependencies["minecraft"]}
	for name, version := range idx.Dependencies {
		if typ, ok := loaders[name]; ok {
			if p.Type != noryxv1.ServerType_SERVER_TYPE_UNSPECIFIED {
				return nil, httpapi.Errorf(http.StatusBadRequest, "The modpack needs two mod loaders.")
			}
			p.Type, p.LoaderVersion = typ, version
		}
	}
	switch {
	case p.Type == noryxv1.ServerType_SERVER_TYPE_UNSPECIFIED:
		return nil, httpapi.Errorf(http.StatusBadRequest, "The modpack needs a mod loader servers don't run. Choose one for Fabric, Quilt, Forge or NeoForge.")
	case !versionPattern.MatchString(p.GameVersion) || !versionPattern.MatchString(p.LoaderVersion):
		return nil, errNoPack
	case len(idx.Files) > maxFiles:
		return nil, httpapi.Errorf(http.StatusBadRequest, "The modpack has more than %d files.", maxFiles)
	}
	byPath := map[string]int{}
	add := func(f file) {
		if i, ok := byPath[f.path]; ok {
			p.files[i] = f
			return
		}
		byPath[f.path] = len(p.files)
		p.files = append(p.files, f)
	}
	for _, f := range idx.Files {
		if f.Env != nil && f.Env.Server == "unsupported" || slices.Contains(skipped, f.Path) {
			continue
		}
		hash := strings.ToLower(f.Hashes.SHA512)
		switch {
		case !validPath(f.Path):
			return nil, httpapi.Errorf(http.StatusBadRequest, "The modpack has a file at the invalid path %q.", f.Path)
		case len(f.Downloads) == 0 || !s.modrinth.OnCDN(f.Downloads[0]):
			return nil, httpapi.Errorf(http.StatusBadRequest, "%s of the modpack isn't on Modrinth's CDN, the only place the master downloads from.", f.Path)
		case !sha512Pattern.MatchString(hash):
			return nil, httpapi.Errorf(http.StatusBadRequest, "%s of the modpack has no valid SHA-512 hash.", f.Path)
		}
		d := modrinth.File{URL: f.Downloads[0], Filename: f.Path, Size: f.FileSize}
		d.Hashes.SHA512 = hash
		add(file{path: f.Path, sha512: hash, project: s.modrinth.ProjectOf(d.URL), download: d})
	}
	for _, dir := range []string{"overrides/", "server-overrides/"} {
		for _, f := range archive.File {
			name, ok := strings.CutPrefix(f.Name, dir)
			switch {
			case !ok || !f.Mode().IsRegular() || slices.Contains(skipped, name): // folders and links are left out
				continue
			case !validPath(name):
				return nil, httpapi.Errorf(http.StatusBadRequest, "The modpack has a file at the invalid path %q.", f.Name)
			}
			hash, err := hashOf(f)
			if err != nil {
				return nil, err
			}
			add(file{path: name, sha512: hash, zip: f})
		}
	}
	return p, nil
}

// validPath reports whether name is a relative path in a server's data without "..".
func validPath(name string) bool {
	return fs.ValidPath(name) && name != "." && !strings.Contains(name, `\`)
}

func readJSON(archive *zip.Reader, name string, v any) error {
	f, err := archive.Open(name)
	if err != nil {
		return err
	}
	defer f.Close()
	return json.NewDecoder(io.LimitReader(f, maxIndexBytes)).Decode(v)
}

// hashOf returns the SHA-512 hash of a file of the pack itself.
func hashOf(f *zip.File) (string, error) {
	h := sha512.New()
	err := extract(f, func(r io.Reader, _ int64) error {
		if _, err := io.Copy(h, r); err != nil {
			return errNoPack
		}
		return nil
	})
	return hex.EncodeToString(h.Sum(nil)), err
}

// extract reads a file of the pack itself, whose size the archive may tell wrongly.
func extract(f *zip.File, read func(io.Reader, int64) error) error {
	if f.UncompressedSize64 > modrinth.MaxFileSize {
		return httpapi.Errorf(http.StatusBadRequest, "%s of the modpack is larger than %d MB.", f.Name, modrinth.MaxFileSize>>20)
	}
	r, err := f.Open()
	if err != nil {
		return errNoPack
	}
	defer r.Close()
	size := int64(f.UncompressedSize64) //nolint:gosec // limited above
	return read(io.LimitReader(r, size), size)
}

// Install writes the files of a pack into the data of a new server, counting them in the
// operation of ctx, and remembers the pack.
func (s *Service) Install(ctx context.Context, nodeID, serverID string, p *Pack) error {
	conn, err := s.nodes.Conn(ctx, nodeID)
	if err == nil {
		err = s.write(ctx, noryxv1.NewFileServiceClient(conn), serverID, p.files)
	}
	if err == nil {
		err = s.remember(ctx, nodeID, serverID, p, p.record())
	}
	return err
}

// write writes files of a pack into the data of a server, a few downloads at a time, and
// counts them in the operation of ctx.
func (s *Service) write(ctx context.Context, c noryxv1.FileServiceClient, serverID string, list []file) error {
	if err := mkdirs(ctx, c, serverID, list); err != nil {
		return err
	}
	total, done := int64(len(list)), atomic.Int64{}
	operation.Count(ctx, 0, total, "files")
	write := func(ctx context.Context, name string, content io.Reader, size int64) error {
		header := &noryxv1.WriteFileHeader{ServerId: serverID, Path: name, Overwrite: true, Size: size}
		if _, err := files.Write(ctx, c, header, content); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		operation.Count(ctx, done.Add(1), total, "files")
		return nil
	}

	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	queue := make(chan file)
	var wg sync.WaitGroup
	for range downloads {
		wg.Go(func() {
			for f := range queue {
				if ctx.Err() != nil {
					continue // after a failure, the others are left out
				}
				data, err := s.modrinth.Download(ctx, f.download)
				if err == nil {
					err = write(ctx, f.path, bytes.NewReader(data), int64(len(data)))
				}
				if err != nil {
					cancel(err)
				}
			}
		})
	}
	for _, f := range list {
		if f.zip == nil {
			queue <- f
		}
	}
	close(queue)
	wg.Wait()
	if err := context.Cause(ctx); err != nil {
		return err
	}
	for _, f := range list {
		if f.zip == nil {
			continue
		}
		err := extract(f.zip, func(r io.Reader, size int64) error { return write(ctx, f.path, r, size) })
		if err != nil {
			return err
		}
	}
	return nil
}

// mkdirs creates the folders of files, which the agent only writes into folders that exist.
func mkdirs(ctx context.Context, c noryxv1.FileServiceClient, serverID string, list []file) error {
	dirs := map[string]bool{}
	for _, f := range list {
		dirs[path.Dir(f.path)] = true
	}
	delete(dirs, ".")
	for dir := range dirs {
		_, err := c.CreateDirectory(ctx, &noryxv1.CreateDirectoryRequest{ServerId: serverID, Path: dir})
		if status.Code(err) != codes.AlreadyExists && err != nil {
			return fmt.Errorf("%s: %w", dir, err)
		}
	}
	return nil
}
