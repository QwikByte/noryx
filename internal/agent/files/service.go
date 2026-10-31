// Package files implements the FileService of the agent, which backs the file manager
// of the panel. All paths are confined to the data directory of the server.
package files

import (
	"bytes"
	"cmp"
	"context"
	"crypto/sha512"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	"github.com/QwikByte/noryx/internal/agent/archive"
	"github.com/QwikByte/noryx/internal/agent/datadir"
	"github.com/QwikByte/noryx/internal/agent/fileset"
	"github.com/QwikByte/noryx/internal/agent/grpcerr"
	"github.com/QwikByte/noryx/internal/agent/runtime"
	"github.com/QwikByte/noryx/internal/agent/secrets"
	"github.com/QwikByte/noryx/internal/agent/storage"
)

const (
	chunkSize  = 256 << 10
	maxEntries = 5000
	// MaxFileSize limits uploads; worlds and modpacks stay well below it.
	MaxFileSize = 16 << 30
	// maxRedacted limits files with secrets, which are read at once to hide them.
	maxRedacted = 1 << 20
)

var (
	// errSecret refuses access to the files that only hold secrets, and moves of those with secrets.
	errSecret = status.Error(codes.PermissionDenied, "This file holds secrets of the server, such as the RCON password, which the panel doesn't show.")
	// errChanged refuses to replace a file that changed since it was read.
	errChanged = status.Error(codes.FailedPrecondition, "The file changed on the server since it was opened.")
)

type Service struct {
	noryxv1.UnimplementedFileServiceServer
	rt runtime.Runtime
}

func NewService(rt runtime.Runtime) *Service { return &Service{rt: rt} }

func (s *Service) ListFiles(ctx context.Context, req *noryxv1.ListFilesRequest) (*noryxv1.ListFilesResponse, error) {
	dir, name, err := s.open(ctx, req.GetServerId(), req.GetPath())
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	f, err := dir.Open(name)
	if err != nil {
		return nil, toStatus(err)
	}
	defer f.Close()
	entries, err := f.ReadDir(maxEntries + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, toStatus(err)
	}
	res := &noryxv1.ListFilesResponse{Truncated: len(entries) > maxEntries}
	sets := fileset.Read(dir)
	hidden := sets.Secrets()
	for _, e := range entries[:min(len(entries), maxEntries)] {
		p := path.Join(name, e.Name())
		// Info also skips entries deleted in the meantime.
		if info, err := e.Info(); err == nil && !hidden.Hidden(p) && !datadir.IsTemp(e.Name()) {
			f := fileInfo(info)
			f.FileSet = sets.Set(p)
			res.Files = append(res.Files, f)
		}
	}
	slices.SortFunc(res.Files, func(a, b *noryxv1.FileInfo) int {
		if a.GetDirectory() != b.GetDirectory() {
			return map[bool]int{true: -1, false: 1}[a.GetDirectory()]
		}
		return cmp.Compare(strings.ToLower(a.GetName()), strings.ToLower(b.GetName()))
	})
	return res, nil
}

func (s *Service) ReadFile(req *noryxv1.ReadFileRequest, stream noryxv1.FileService_ReadFileServer) error {
	dir, name, err := s.open(stream.Context(), req.GetServerId(), req.GetPath())
	if err != nil {
		return err
	}
	defer dir.Close()
	f, err := dir.Open(name)
	if err != nil {
		return toStatus(err)
	}
	defer f.Close()
	// Checked once the file is open, as file sets mark files before they write them.
	if fileset.Read(dir).Secrets().Hidden(name) {
		return errSecret
	}
	info, err := f.Stat()
	if err != nil {
		return toStatus(err)
	}
	switch {
	case info.IsDir():
		return status.Error(codes.InvalidArgument, "This is a folder. Download it as a ZIP archive instead.")
	case req.GetLimit() < 0:
		return status.Error(codes.InvalidArgument, "The limit can't be negative.")
	}
	r, size := io.ReaderAt(f), info.Size()
	if secrets.Redacted(name) {
		data, err := readRedacted(f)
		if err != nil {
			return err
		}
		data = secrets.Redact(name, data)
		r, size = bytes.NewReader(data), int64(len(data))
	}
	res := &noryxv1.ReadFileResponse{Size: size, Version: version(info)}
	start, n := int64(0), size
	if req.GetOffset() != 0 || req.GetLimit() != 0 {
		start, n = part(req.GetOffset(), req.GetLimit(), size)
		res.Size, res.Offset, res.FileSize = n, start, size
	}
	// Sends what the size tells, also of a log that grows meanwhile.
	return sendChunks(io.NewSectionReader(r, start, n), func(data []byte) error {
		res.Data = data
		err := stream.Send(res)
		res = &noryxv1.ReadFileResponse{}
		return err
	})
}

func (s *Service) WriteFile(stream noryxv1.FileService_WriteFileServer) error {
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	header := first.GetHeader()
	if header == nil {
		return status.Error(codes.InvalidArgument, "the first message must name the file")
	}
	dir, name, err := s.open(stream.Context(), header.GetServerId(), header.GetPath())
	if err != nil {
		return err
	}
	defer dir.Close()
	switch {
	case name == ".":
		return status.Error(codes.InvalidArgument, "Choose a file name.")
	case fileset.Read(dir).Secrets().Hidden(name):
		return errSecret
	}
	top, err := dir.Open(".")
	if err != nil {
		return toStatus(err)
	}
	defer top.Close()
	if err := storage.Fits(top, header.GetSize()); err != nil {
		return toStatus(err)
	}
	// The file is replaced right after the content is written, and checked just before.
	err = dir.Replace(name, header.GetOverwrite(), func(w io.Writer) error {
		if !secrets.Redacted(name) {
			if err := receive(stream, storage.Guard(w, top), MaxFileSize); err != nil {
				return err
			}
			return unchanged(dir, name, header.GetExpected())
		}
		// The secrets the panel showed as placeholders stay as they are.
		var data bytes.Buffer
		if err := receive(stream, &data, maxRedacted); err != nil {
			return err
		}
		if err := unchanged(dir, name, header.GetExpected()); err != nil {
			return err
		}
		current, err := dir.ReadOptional(name)
		if err == nil {
			_, err = w.Write(secrets.Restore(name, data.Bytes(), current))
		}
		return err
	})
	if err != nil {
		return toStatus(err)
	}
	info, err := dir.Stat(name)
	if err != nil {
		return toStatus(err)
	}
	return stream.SendAndClose(&noryxv1.WriteFileResponse{File: fileInfo(info), Version: version(info)})
}

// part returns where the part of a file of the given size that ReadFileRequest names with
// offset and limit starts, and its length.
func part(offset, limit, size int64) (start, n int64) {
	if offset < 0 {
		start = max(size+offset, 0)
	} else {
		start = min(offset, size)
	}
	n = size - start
	if limit > 0 {
		n = min(n, limit)
	}
	return start, n
}

// unchanged refuses to replace a file that no longer has the expected version, if any.
func unchanged(dir *datadir.Dir, name string, expected *noryxv1.FileVersion) error {
	if expected == nil {
		return nil
	}
	info, err := dir.Stat(name)
	if errors.Is(err, fs.ErrNotExist) || err == nil && !proto.Equal(version(info), expected) {
		return errChanged
	}
	return err
}

func (s *Service) ArchiveDirectory(req *noryxv1.ArchiveDirectoryRequest, stream noryxv1.FileService_ArchiveDirectoryServer) error {
	dir, name, err := s.open(stream.Context(), req.GetServerId(), req.GetPath())
	if err != nil {
		return err
	}
	defer dir.Close()
	sub, err := dir.OpenRoot(name)
	if err != nil {
		return toStatus(err)
	}
	defer sub.Close()
	paths, err := archived(sub, req.GetPaths())
	if err != nil {
		return err
	}
	var censor datadir.Censor
	if req.GetHideSecrets() {
		// A file set may write files with secrets while the archive is read.
		hidden := fileset.Watch(dir)
		censor = func(n string) (bool, func([]byte) []byte) { return hidden().Censor(name)(n) }
	}
	r, w := io.Pipe()
	defer r.Close() // stops WriteZip if the client goes away
	go func() { w.CloseWithError(datadir.WriteZip(stream.Context(), w, sub, censor, paths...)) }()
	return sendChunks(r, func(data []byte) error { return stream.Send(&noryxv1.ArchiveDirectoryResponse{Data: data}) })
}

// archived returns what an archive of the folder root holds: all of it, or the files and
// folders of paths in it, which must exist. Symbolic links are left out, as in folders.
func archived(root *os.Root, paths []string) ([]string, error) {
	if len(paths) == 0 {
		return []string{"."}, nil
	}
	if len(paths) > maxEntries {
		return nil, status.Errorf(codes.InvalidArgument, "An archive can hold up to %d files and folders.", maxEntries)
	}
	names := make([]string, 0, len(paths))
	for _, p := range paths {
		name, err := clean(p)
		if err != nil {
			return nil, err
		}
		info, err := root.Lstat(name)
		if err != nil {
			return nil, toStatus(err)
		}
		if info.IsDir() || info.Mode().IsRegular() {
			names = append(names, name)
		}
	}
	return datadir.Outermost(names), nil
}

func (s *Service) CreateDirectory(ctx context.Context, req *noryxv1.CreateDirectoryRequest) (*noryxv1.CreateDirectoryResponse, error) {
	dir, name, err := s.open(ctx, req.GetServerId(), req.GetPath())
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	if _, err := dir.Lstat(name); err == nil {
		return nil, toStatus(fs.ErrExist)
	}
	if fileset.Read(dir).Secrets().Hidden(name) {
		return nil, errSecret // e.g. the manifest of file sets, which a folder would block
	}
	return &noryxv1.CreateDirectoryResponse{}, toStatus(dir.MkdirAll(name))
}

func (s *Service) MoveFile(ctx context.Context, req *noryxv1.MoveFileRequest) (*noryxv1.MoveFileResponse, error) {
	dir, from, err := s.open(ctx, req.GetServerId(), req.GetFrom())
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	to, err := clean(req.GetTo())
	if err != nil {
		return nil, err
	}
	hidden := fileset.Read(dir).Secrets()
	switch {
	case from == "." || to == ".":
		return nil, status.Error(codes.InvalidArgument, "The server folder itself can't be moved.")
	case strings.HasPrefix(to, from+string(filepath.Separator)):
		return nil, status.Error(codes.InvalidArgument, "A folder can't be moved into itself.")
	case slices.ContainsFunc(hidden.Under(from), func(p string) bool { _, err := dir.Lstat(p); return err == nil }):
		return nil, errSecret // the secrets would show at the new place
	case hidden.Hidden(to):
		return nil, errSecret
	}
	if _, err := dir.Lstat(to); err == nil {
		return nil, toStatus(fs.ErrExist)
	}
	return &noryxv1.MoveFileResponse{}, toStatus(dir.Rename(from, to))
}

func (s *Service) DeleteFile(ctx context.Context, req *noryxv1.DeleteFileRequest) (*noryxv1.DeleteFileResponse, error) {
	dir, name, err := s.open(ctx, req.GetServerId(), req.GetPath())
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	switch name {
	case ".":
		return nil, status.Error(codes.InvalidArgument, "The server folder itself can't be deleted.")
	case fileset.ManifestFile: // it tells which files hold secrets
		return nil, errSecret
	}
	if _, err := dir.Lstat(name); err != nil {
		return nil, toStatus(err)
	}
	return &noryxv1.DeleteFileResponse{}, toStatus(dir.RemoveAll(name))
}

// ExtractArchive extracts an archive of the server, which is untrusted: it extracts nothing
// into the files with secrets of the server, nor those that Noryx writes itself, and refuses
// the archive before it writes anything if any entry would go there; see archive.Extract.
func (s *Service) ExtractArchive(ctx context.Context, req *noryxv1.ExtractArchiveRequest) (*noryxv1.ExtractArchiveResponse, error) {
	dir, name, err := s.open(ctx, req.GetServerId(), req.GetPath())
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	dest, err := clean(req.GetDestination())
	if err != nil {
		return nil, err
	}
	if err := errors.Join(dir.Direct(name), dir.Direct(dest)); err != nil {
		return nil, toStatus(err)
	}
	f, err := dir.Open(name)
	if err != nil {
		return nil, toStatus(err)
	}
	defer f.Close()
	hidden := fileset.Read(dir).Secrets() // once the archive is open, see ReadFile
	info, err := f.Stat()
	switch {
	case err != nil:
		return nil, toStatus(err)
	case hidden.Hidden(name):
		return nil, errSecret
	case info.IsDir():
		return nil, status.Error(codes.InvalidArgument, "This is a folder, not an archive.")
	}
	a, err := archive.Open(f, info.Size(), archive.Uploads)
	if err != nil {
		return nil, toStatus(err)
	}
	res, err := a.Extract(ctx, dir, dest, archive.Options{Protected: archive.Guarded(hidden), Overwrite: req.GetOverwrite()})
	if err != nil {
		return nil, toStatus(err)
	}
	return &noryxv1.ExtractArchiveResponse{Files: res.Files, Size: res.Size}, nil
}

// CopyFile copies a file or folder within the data of a server, following the rules of
// MoveFile: what holds secrets can't be copied, and the copy goes nowhere where they would
// show.
func (s *Service) CopyFile(ctx context.Context, req *noryxv1.CopyFileRequest) (*noryxv1.CopyFileResponse, error) {
	dir, from, err := s.open(ctx, req.GetServerId(), req.GetFrom())
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	to, err := clean(req.GetTo())
	if err != nil {
		return nil, err
	}
	exists := func(p string) bool { _, err := dir.Lstat(p); return err == nil }
	// Whether the copy holds a file with secrets, or would make one at a place of such files.
	secret := func(hidden secrets.Files) bool {
		return slices.ContainsFunc(hidden.Under(from), exists) ||
			slices.ContainsFunc(hidden.Under(to), func(p string) bool { return exists(filepath.Join(from, strings.TrimPrefix(p, to))) })
	}
	switch {
	case from == "." || to == ".":
		return nil, status.Error(codes.InvalidArgument, "The server folder itself can't be copied.")
	case from == to || strings.HasPrefix(to, from+string(filepath.Separator)):
		return nil, status.Error(codes.InvalidArgument, "A folder can't be copied into itself.")
	case secret(fileset.Read(dir).Secrets()):
		return nil, errSecret
	}
	if err := errors.Join(dir.Direct(from), dir.Direct(to)); err != nil {
		return nil, toStatus(err)
	}
	if _, err := dir.Lstat(from); err != nil {
		return nil, toStatus(err)
	}
	if exists(to) {
		return nil, toStatus(fs.ErrExist)
	}
	top, err := dir.Open(".")
	if err != nil {
		return nil, toStatus(err)
	}
	defer top.Close()
	if err := storage.Fits(top, datadir.Size(dir.FS(), filepath.ToSlash(from))); err != nil {
		return nil, toStatus(err)
	}
	if err := dir.CopyTree(ctx, from, to); err != nil {
		return nil, toStatus(err)
	}
	// A file set marks a file before it writes it: one that it wrote meanwhile is marked now.
	if secret(fileset.Read(dir).Secrets()) {
		if err := dir.RemoveAll(to); err != nil {
			return nil, toStatus(err)
		}
		return nil, errSecret
	}
	return &noryxv1.CopyFileResponse{}, nil
}

// HashFiles hashes regular files, e.g. those of a modpack. Files with secrets get no hash,
// so that a hash can't tell anything about a secret, and neither do links, as the master
// writes no file through one.
func (s *Service) HashFiles(ctx context.Context, req *noryxv1.HashFilesRequest) (*noryxv1.HashFilesResponse, error) {
	if len(req.GetPaths()) > noryxv1.MaxHashPaths {
		return nil, status.Errorf(codes.InvalidArgument, "Up to %d files can be hashed at once.", noryxv1.MaxHashPaths)
	}
	dir, _, err := s.open(ctx, req.GetServerId(), "")
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	hidden := fileset.Read(dir).Secrets()
	res := &noryxv1.HashFilesResponse{Sha512: map[string]string{}}
	for _, p := range req.GetPaths() {
		name, err := clean(p)
		if err != nil {
			return nil, err
		}
		info, err := dir.Lstat(name)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			continue
		case err != nil:
			return nil, toStatus(err)
		case !info.Mode().IsRegular() || info.Size() > noryxv1.MaxHashedSize || hidden.Hidden(name) || secrets.Redacted(name):
			res.Sha512[p] = ""
		default:
			if res.Sha512[p], err = hashFile(dir, name); err != nil {
				return nil, toStatus(err)
			}
		}
	}
	return res, nil
}

func hashFile(dir *datadir.Dir, name string) (string, error) {
	f, err := dir.Open(name)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha512.New()
	if _, err := io.Copy(h, io.LimitReader(f, noryxv1.MaxHashedSize+1)); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// open opens the data directory of a server and returns the path as a name in it.
func (s *Service) open(ctx context.Context, id, p string) (*datadir.Dir, string, error) {
	if !runtime.ValidID(id) {
		return nil, "", status.Error(codes.InvalidArgument, "invalid server ID")
	}
	name, err := clean(p)
	if err != nil {
		return nil, "", err
	}
	dir, err := s.rt.Data(ctx, id)
	return dir, name, grpcerr.Status(err)
}

// clean returns a path of the file manager as a name in the data directory. No path leads into
// a temporary file or folder of the agent, which can hold secrets, e.g. a backup that is being
// restored with the forwarding secret of the server.
func clean(p string) (string, error) {
	name, ok := datadir.Name(p)
	if !ok || slices.ContainsFunc(strings.Split(name, string(filepath.Separator)), datadir.IsTemp) {
		return "", status.Error(codes.InvalidArgument, "invalid path")
	}
	return name, nil
}

func version(info fs.FileInfo) *noryxv1.FileVersion {
	return &noryxv1.FileVersion{ModifiedUnixNano: info.ModTime().UnixNano(), Size: info.Size()}
}

func fileInfo(info fs.FileInfo) *noryxv1.FileInfo {
	return &noryxv1.FileInfo{Name: info.Name(), Directory: info.IsDir(), Size: info.Size(), ModifiedUnix: info.ModTime().Unix()}
}

// receive writes the data of a WriteFile stream to w, up to limit bytes.
func receive(stream noryxv1.FileService_WriteFileServer, w io.Writer, limit int64) error {
	var written int64
	for {
		msg, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if written += int64(len(msg.GetData())); written > limit {
			return tooLarge(limit)
		}
		if _, err := w.Write(msg.GetData()); err != nil {
			return err
		}
	}
}

// readRedacted reads a file with secrets, up to maxRedacted bytes.
func readRedacted(r io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, maxRedacted+1))
	if err == nil && len(data) > maxRedacted {
		return nil, tooLarge(maxRedacted)
	}
	return data, toStatus(err)
}

func tooLarge(limit int64) error {
	if limit < MaxFileSize {
		return status.Errorf(codes.ResourceExhausted, "Files with secrets can have up to %d MB.", limit>>20)
	}
	return status.Errorf(codes.ResourceExhausted, "Files can have up to %d GB.", limit>>30)
}

// sendChunks reads r to the end and passes it on in chunks. It always sends at least
// one chunk, so that the receiver gets the first message of an empty file.
func sendChunks(r io.Reader, send func([]byte) error) error {
	buf := make([]byte, chunkSize)
	for first := true; ; first = false {
		n, err := io.ReadFull(r, buf)
		if n > 0 || first {
			if err := send(buf[:n]); err != nil {
				return err
			}
		}
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return nil
		}
		if err != nil {
			return toStatus(err)
		}
	}
}

// known are the errors of files that the service answers with a code and a text.
var known = []grpcerr.Known{
	{Err: fs.ErrNotExist, Code: codes.NotFound, Text: "File or folder not found."},
	{Err: fs.ErrExist, Code: codes.AlreadyExists, Text: "A file or folder with this name exists already."},
	{Err: fs.ErrPermission, Code: codes.PermissionDenied, Text: "The agent isn't allowed to access this file."},
	{Err: datadir.ErrLink, Code: codes.InvalidArgument, Text: "This path leads through a link of the server, which the file manager doesn't follow."},
}

func toStatus(err error) error {
	if datadir.Escapes(err) {
		return status.Error(codes.InvalidArgument, "This path leads outside of the server folder.")
	}
	return grpcerr.Status(err, known...)
}
