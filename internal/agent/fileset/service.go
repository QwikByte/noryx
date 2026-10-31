// Package fileset implements the FileSetService of the agent, which keeps the files of file
// sets on its servers: files the master manages for groups of servers, such as the
// configuration of a plugin or an image. The agent fills in the secrets of a set and the
// passwords of databases, and hides the files that hold them from the file manager and from
// downloads, like the RCON password. Each server records in its data which set wrote which
// file, so that the master learns which servers have the newest files and which changed them.
package fileset

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"maps"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"unicode"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	"github.com/QwikByte/noryx/internal/agent/datadir"
	"github.com/QwikByte/noryx/internal/agent/grpcerr"
	"github.com/QwikByte/noryx/internal/agent/runtime"
	"github.com/QwikByte/noryx/internal/agent/secrets"
	"github.com/QwikByte/noryx/internal/logging"
)

// ManifestFile records in the data of a server which set wrote which file.
const ManifestFile = noryxv1.FileSetManifest

const (
	maxSetName  = 64
	maxRevision = 128
)

// secretKey matches the keys of the secrets of sets, e.g. secret:db-password.
var secretKey = regexp.MustCompile(`^secret:[a-z0-9][a-z0-9_-]{0,63}$`)

type Service struct {
	noryxv1.UnimplementedFileSetServiceServer
	rt    runtime.Runtime
	locks sync.Map // a *sync.Mutex per server, so that its manifest changes one at a time
}

func NewService(rt runtime.Runtime) *Service { return &Service{rt: rt} }

// change is what applying or removing a set does with a file.
type change struct {
	path    string
	content string // written if the file is created or changed
	action  noryxv1.FileSetAction
	secret  bool
	file    *file // what the manifest records, nil if the file is no longer part of the set
}

func (s *Service) ApplyFileSet(ctx context.Context, req *noryxv1.ApplyFileSetRequest) (*noryxv1.ApplyFileSetResponse, error) {
	files, err := render(req)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	dir, m, unlock, err := s.open(ctx, req.GetServerId(), req.GetSetId())
	if err != nil {
		return nil, err
	}
	defer unlock()
	defer dir.Close()
	prev := m.Sets[req.GetSetId()]
	if prev == nil {
		prev = &applied{}
	}
	var changes []change
	for _, f := range files {
		if owner := m.owner(f.path, req.GetSetId()); owner != "" {
			return nil, failed("%s comes from the file set %q on this server.", f.path, owner)
		}
		c, err := plan(dir, f, prev.Files[f.path])
		if err != nil {
			return nil, toStatus(err)
		}
		changes = append(changes, c)
	}
	for _, p := range slices.Sorted(maps.Keys(prev.Files)) {
		if !slices.ContainsFunc(files, func(f change) bool { return f.path == p }) {
			c, err := leave(dir, p, prev.Files[p], noryxv1.FileSetRemoval_FILE_SET_REMOVAL_UNCHANGED)
			if err != nil {
				return nil, toStatus(err)
			}
			changes = append(changes, c)
		}
	}
	res := &noryxv1.ApplyFileSetResponse{Changes: response(changes)}
	if req.GetDryRun() {
		return res, nil
	}
	next := applied{Name: strings.TrimSpace(req.GetSetName()), Version: req.GetVersion(), Revision: req.GetRevision()}
	return res, toStatus(carry(dir, m, req.GetSetId(), next, changes))
}

func (s *Service) RemoveFileSet(ctx context.Context, req *noryxv1.RemoveFileSetRequest) (*noryxv1.RemoveFileSetResponse, error) {
	removal := req.GetRemoval()
	if _, ok := noryxv1.FileSetRemoval_name[int32(removal)]; !ok || removal == noryxv1.FileSetRemoval_FILE_SET_REMOVAL_UNSPECIFIED {
		return nil, status.Error(codes.InvalidArgument, "Choose how to remove the file set.")
	}
	dir, m, unlock, err := s.open(ctx, req.GetServerId(), req.GetSetId())
	if err != nil {
		return nil, err
	}
	defer unlock()
	defer dir.Close()
	set := m.Sets[req.GetSetId()]
	if set == nil {
		return &noryxv1.RemoveFileSetResponse{}, nil
	}
	var changes []change
	for _, p := range slices.Sorted(maps.Keys(set.Files)) {
		c, err := leave(dir, p, set.Files[p], removal)
		if err != nil {
			return nil, toStatus(err)
		}
		changes = append(changes, c)
	}
	res := &noryxv1.RemoveFileSetResponse{Changes: response(changes)}
	if req.GetDryRun() {
		return res, nil
	}
	next := *set
	next.Revision = "" // whatever stays isn't the state the master sent anymore
	return res, toStatus(carry(dir, m, req.GetSetId(), next, changes))
}

func (s *Service) ListFileSets(ctx context.Context, _ *noryxv1.ListFileSetsRequest) (*noryxv1.ListFileSetsResponse, error) {
	servers, err := s.rt.List(ctx)
	if err != nil {
		return nil, toStatus(err)
	}
	res := &noryxv1.ListFileSetsResponse{}
	for _, srv := range servers {
		sets, err := s.list(ctx, srv.ID)
		switch {
		case errors.Is(err, runtime.ErrNotFound): // deleted meanwhile
		case err != nil:
			slog.Warn("Can't tell which files of file sets a server has", logging.Files, logging.KeyServer, srv.ID, "err", err)
		case len(sets) > 0:
			res.Servers = append(res.Servers, &noryxv1.ServerFileSets{ServerId: srv.ID, Sets: sets})
		}
	}
	return res, nil
}

func (s *Service) list(ctx context.Context, id string) ([]*noryxv1.AppliedFileSet, error) {
	dir, err := s.rt.Data(ctx, id)
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	m, err := read(dir)
	if err != nil {
		return nil, err
	}
	var sets []*noryxv1.AppliedFileSet
	for _, setID := range slices.Sorted(maps.Keys(m.Sets)) {
		a := m.Sets[setID]
		set := &noryxv1.AppliedFileSet{SetId: setID, Version: a.Version, Revision: a.Revision}
		for _, p := range slices.Sorted(maps.Keys(a.Files)) {
			f := a.Files[p]
			sum, exists, err := digest(dir, p)
			if err != nil {
				return nil, err
			}
			set.Files = append(set.Files, &noryxv1.AppliedFile{
				Path: p, Secret: f.Secret, OnlyIfMissing: f.OnlyIfMissing, Changed: !exists || f.SHA256 != "" && sum != f.SHA256, Missing: !exists,
			})
		}
		sets = append(sets, set)
	}
	return sets, nil
}

// Standalone removes the files that held secrets of sets from dir, the copy of the data of
// original, as the copy is no target of the sets that gave them. They stay hidden. The marks
// of original are read once the copy is complete, so that they cover every file with secrets
// that a set wrote meanwhile.
func Standalone(dir, original *datadir.Dir) error {
	m, err := read(dir)
	if err != nil {
		return err
	}
	o, err := read(original)
	if err != nil {
		return err
	}
	m.Marked = append(m.Marked, o.Marked...)
	if len(m.Marked) == 0 {
		return nil
	}
	for _, p := range m.Marked {
		if err := dir.Remove(filepath.FromSlash(p)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	for id, set := range m.Sets {
		maps.DeleteFunc(set.Files, func(p string, _ *file) bool { return slices.Contains(m.Marked, p) })
		set.Revision = ""
		if len(set.Files) == 0 {
			delete(m.Sets, id)
		}
	}
	return m.write(dir)
}

// open opens the data of a server and reads its manifest, holding the server's lock until
// unlock is called.
func (s *Service) open(ctx context.Context, serverID, setID string) (_ *datadir.Dir, _ manifest, unlock func(), err error) {
	if !runtime.ValidID(serverID) || !idPattern.MatchString(setID) {
		return nil, manifest{}, nil, status.Error(codes.InvalidArgument, "invalid server or file set ID")
	}
	v, _ := s.locks.LoadOrStore(serverID, new(sync.Mutex))
	mu := v.(*sync.Mutex)
	mu.Lock()
	dir, err := s.rt.Data(ctx, serverID)
	if err != nil {
		mu.Unlock()
		return nil, manifest{}, nil, toStatus(err)
	}
	m, err := read(dir)
	if err != nil {
		mu.Unlock()
		dir.Close()
		return nil, m, nil, failed("%s", err)
	}
	return dir, m, mu.Unlock, nil
}

// render validates the files of a set and fills in their secrets.
func render(req *noryxv1.ApplyFileSetRequest) ([]change, error) {
	name := strings.TrimSpace(req.GetSetName())
	switch {
	case name == "" || len(name) > maxSetName || strings.ContainsFunc(name, unicode.IsControl):
		return nil, errors.New("invalid file set name")
	case req.GetVersion() < 1 || len(req.GetRevision()) > maxRevision:
		return nil, errors.New("invalid file set version")
	case len(req.GetFiles()) > noryxv1.MaxFileSetFiles:
		return nil, fmt.Errorf("a file set has up to %d files", noryxv1.MaxFileSetFiles)
	case len(req.GetSecrets()) > noryxv1.MaxFileSetSecrets:
		return nil, errors.New("too many secrets")
	}
	for key, value := range req.GetSecrets() {
		m := noryxv1.DatastorePlaceholder.FindStringSubmatch(key)
		switch {
		case m != nil && m[3] == "password":
			if !noryxv1.DatabasePassword.MatchString(value) {
				return nil, fmt.Errorf("invalid password of %q", key)
			}
		case !secretKey.MatchString(key):
			return nil, fmt.Errorf("invalid secret %q", key)
		default:
			if problem := noryxv1.SecretValueProblem(value); problem != "" {
				return nil, errors.New(problem)
			}
		}
	}
	var files []change
	size := 0
	for _, f := range req.GetFiles() {
		p, problem := noryxv1.CleanFileSetPath(f.GetPath())
		binary := len(f.GetData()) > 0
		if size += len(f.GetContent()) + len(f.GetData()); problem == "" && size > noryxv1.MaxFileSetSize {
			problem = fmt.Sprintf("A file set has up to %d MiB.", noryxv1.MaxFileSetSize>>20)
		}
		switch {
		case binary && f.GetContent() != "":
			problem = cmp.Or(problem, p+" is text and binary at once.")
		case binary:
			problem = cmp.Or(problem, noryxv1.FileSetDataProblem(p, f.GetData()))
		default:
			problem = cmp.Or(problem, noryxv1.FileSetContentProblem(p, f.GetContent()))
		}
		if name := filepath.FromSlash(p); problem == "" && (secrets.Hidden(name) || secrets.Redacted(name)) {
			problem = p + " holds secrets of the server."
		}
		if problem == "" && slices.ContainsFunc(files, func(c change) bool { return c.path == p }) {
			problem = p + " is there twice."
		}
		if problem != "" {
			return nil, errors.New(problem)
		}
		content, secret := string(f.GetData()), false
		if !binary {
			var err error
			if content, secret, err = fill(p, f.GetContent(), req.GetSecrets()); err != nil {
				return nil, err
			}
		}
		files = append(files, change{path: p, content: content, secret: secret, file: &file{SHA256: hash(content), OnlyIfMissing: f.GetOnlyIfMissing()}})
	}
	return files, nil
}

// fill fills in the secrets and passwords of a file and reports whether it has any. The
// master fills in the variables, so those left are unknown.
func fill(name, content string, values map[string]string) (string, bool, error) {
	var missing string
	filled := noryxv1.Placeholder.ReplaceAllStringFunc(content, func(m string) string {
		value, ok := values[m[2:len(m)-2]]
		if !ok {
			missing = cmp.Or(missing, m)
		}
		return value
	})
	if missing != "" {
		return "", false, fmt.Errorf("%s: %s is unknown or has no value", name, missing)
	}
	return filled, noryxv1.Placeholder.MatchString(content), nil
}

// plan tells what applying a file of a set does, which the set had as prev, if at all.
func plan(dir *datadir.Dir, c change, prev *file) (change, error) {
	if err := folders(dir, c.path); err != nil {
		return c, err
	}
	sum, exists, err := digest(dir, c.path)
	if err != nil {
		return c, err
	}
	if info, err := dir.Lstat(filepath.FromSlash(c.path)); err == nil && info.IsDir() {
		return c, failed("%s is a folder on this server.", c.path)
	}
	written := prev != nil && prev.SHA256 != "" // by the set, earlier
	switch {
	case !exists:
		c.action = noryxv1.FileSetAction_FILE_SET_ACTION_CREATED
	case c.file.OnlyIfMissing:
		// The file stays as it is: the server's own one never counts as written by the set, and
		// it holds secrets only if the set wrote them.
		c.action, c.file.SHA256, c.secret = noryxv1.FileSetAction_FILE_SET_ACTION_UNCHANGED, "", false
		if written {
			c.file.SHA256 = prev.SHA256
		}
	case sum == c.file.SHA256:
		c.action = noryxv1.FileSetAction_FILE_SET_ACTION_UNCHANGED
	default:
		c.action = noryxv1.FileSetAction_FILE_SET_ACTION_CHANGED
	}
	// A file that held secrets is taken off like one, also if the new version has none.
	c.file.Secret = c.secret || written && prev.Secret
	return c, nil
}

// leave tells what taking a file off a server does: files with secrets that the set wrote go,
// the others as the removal says, if they didn't change since they were written.
func leave(dir *datadir.Dir, p string, f *file, removal noryxv1.FileSetRemoval) (change, error) {
	c := change{path: p, action: noryxv1.FileSetAction_FILE_SET_ACTION_REMOVED, secret: f.Secret}
	if !f.Secret && removal == noryxv1.FileSetRemoval_FILE_SET_REMOVAL_SECRETS {
		c.action, c.file = noryxv1.FileSetAction_FILE_SET_ACTION_UNCHANGED, f // stays part of the set
		return c, nil
	}
	sum, exists, err := digest(dir, p)
	switch {
	case err != nil:
		return c, err
	case !exists:
		c.action = noryxv1.FileSetAction_FILE_SET_ACTION_UNCHANGED // gone already
	case f.Secret && f.SHA256 != "":
	case removal != noryxv1.FileSetRemoval_FILE_SET_REMOVAL_UNCHANGED || f.SHA256 == "" || sum != f.SHA256:
		c.action = noryxv1.FileSetAction_FILE_SET_ACTION_KEPT
	}
	return c, nil
}

// carry carries out the changes of a set, which then has the files they keep. Files with
// secrets are marked before they are written, so that they are never shown, and the set's
// new state is recorded once its files are written.
func carry(dir *datadir.Dir, m manifest, setID string, next applied, changes []change) error {
	marked := len(m.Marked)
	for _, c := range changes {
		if c.file != nil && c.file.Secret && !slices.Contains(m.Marked, c.path) {
			m.Marked = append(m.Marked, c.path)
		}
	}
	if len(m.Marked) > marked {
		if err := m.write(dir); err != nil {
			return err
		}
	}
	next.Files = map[string]*file{}
	for _, c := range changes {
		name := filepath.FromSlash(c.path)
		var err error
		switch c.action {
		case noryxv1.FileSetAction_FILE_SET_ACTION_REMOVED:
			if err = dir.Remove(name); errors.Is(err, fs.ErrNotExist) {
				err = nil
			}
		case noryxv1.FileSetAction_FILE_SET_ACTION_CREATED, noryxv1.FileSetAction_FILE_SET_ACTION_CHANGED:
			if err = dir.MkdirAll(filepath.Dir(name)); err == nil {
				err = dir.WriteFile(name, []byte(c.content))
			}
		}
		if err != nil {
			return fmt.Errorf("%s: %w", c.path, err)
		}
		if c.file != nil {
			next.Files[c.path] = c.file
		}
	}
	delete(m.Sets, setID)
	if len(next.Files) > 0 {
		m.Sets[setID] = &next
	}
	return m.write(dir)
}

// folders fails if a folder on the path of a file is something else, such as a link, which
// could lead the file elsewhere in the server's data.
func folders(dir *datadir.Dir, p string) error {
	for folder := path.Dir(p); folder != "."; folder = path.Dir(folder) {
		info, err := dir.Lstat(filepath.FromSlash(folder))
		if err == nil && !info.IsDir() {
			return failed("%s isn't a folder on this server.", folder)
		}
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	return nil
}

func response(changes []change) []*noryxv1.FileSetChange {
	res := make([]*noryxv1.FileSetChange, len(changes))
	for i, c := range changes {
		res[i] = &noryxv1.FileSetChange{Path: c.path, Action: c.action, Secret: c.secret || c.file != nil && c.file.Secret}
	}
	return res
}

// failed is the error for a server whose files keep a set from being applied.
func failed(format string, args ...any) error {
	return status.Errorf(codes.FailedPrecondition, format, args...)
}

func toStatus(err error) error {
	if datadir.Escapes(err) {
		return failed("A path of the file set leads outside of the server folder.")
	}
	return grpcerr.Status(err)
}
