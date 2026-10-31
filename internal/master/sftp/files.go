package sftp

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path"
	"strings"
	"time"

	"github.com/pkg/sftp"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	"github.com/QwikByte/noryx/internal/logging"
	"github.com/QwikByte/noryx/internal/master/access"
	"github.com/QwikByte/noryx/internal/master/auth"
	"github.com/QwikByte/noryx/internal/master/httpapi"
)

// opTimeout limits listing and changing files, which the requests of a connection wait for.
const opTimeout = 30 * time.Second

// statusError is an error for the client: a code of SFTP and a message.
type statusError struct {
	code error // one of sftp.ErrSSHFx…
	msg  string
}

func (e statusError) Error() string { return e.msg }
func (e statusError) Unwrap() error { return e.code }

var (
	errNotFound  = statusError{sftp.ErrSSHFxNoSuchFile, "File or folder not found."}
	errFixed     = statusError{sftp.ErrSSHFxPermissionDenied, "Only the files and folders in the folders of servers can be changed."}
	errFolder    = statusError{sftp.ErrSSHFxFailure, "This is a folder."}
	errNotEmpty  = statusError{sftp.ErrSSHFxFailure, "The folder isn't empty."}
	errElsewhere = statusError{sftp.ErrSSHFxOpUnsupported, "Files only move within the folder of their server."}
	errLinks     = statusError{sftp.ErrSSHFxOpUnsupported, "Links can't be made or read over SFTP."}
	errTruncate  = statusError{sftp.ErrSSHFxOpUnsupported, "Files can't be truncated over SFTP. Upload them as a whole instead."}
	errTooMany   = statusError{sftp.ErrSSHFxFailure, "Too many files are open. Close some first."}
)

// toStatus turns an error of the master or of an agent into one for the client, with what
// users may learn about it.
func toStatus(err error) error {
	var se statusError
	if err == nil || errors.Is(err, io.EOF) || errors.As(err, &se) {
		return err
	}
	var apiErr *httpapi.Error
	code := status.Code(err)
	if errors.As(err, &apiErr) {
		code = map[int]codes.Code{
			http.StatusNotFound: codes.NotFound, http.StatusUnauthorized: codes.PermissionDenied, http.StatusForbidden: codes.PermissionDenied,
		}[apiErr.Status]
	}
	switch code {
	case codes.NotFound:
		return statusError{sftp.ErrSSHFxNoSuchFile, httpapi.Message(err)}
	case codes.PermissionDenied:
		return statusError{sftp.ErrSSHFxPermissionDenied, httpapi.Message(err)}
	case codes.Unimplemented:
		return statusError{sftp.ErrSSHFxOpUnsupported, "Update the agent of the node for this."}
	}
	return statusError{sftp.ErrSSHFxFailure, httpapi.Message(err)}
}

// place is where a path leads: to the root, a node, or the folder of a server or into it.
type place struct {
	sftp         string // the path
	node, server folder
	path         string // in the server's folder
}

// shown is the path that the log shows: the path in the folder of a server like the file
// manager's, otherwise the path of SFTP.
func (p place) shown() string {
	if p.path != "" {
		return p.path
	}
	return p.sftp
}

// resolve finds where a path leads among the servers whose files the user may read.
func (s *session) resolve(ctx context.Context, g access.Grants, p string) (place, error) {
	pl := place{sftp: path.Clean("/" + p)}
	parts := strings.SplitN(pl.sftp[1:], "/", 3)
	if parts[0] == "" {
		return pl, nil
	}
	nodes, err := s.srv.tree.nodeFolders(ctx)
	if err != nil {
		return pl, err
	}
	n, ok := find(nodes, parts[0])
	if !ok || !g.Somewhere(access.FilesRead, n.nodeID) {
		return pl, errNotFound
	}
	if pl.node = n; len(parts) == 1 {
		return pl, nil
	}
	servers, err := s.srv.tree.serverFolders(ctx, n.nodeID)
	if err != nil {
		return pl, err
	}
	srv, ok := find(servers, parts[1])
	if !ok || !g.On(access.FilesRead, n.nodeID, srv.serverID) {
		return pl, errNotFound
	}
	if pl.server = srv; len(parts) == 3 {
		pl.path = parts[2]
	}
	return pl, nil
}

// writable refuses changes outside the folders of servers, of a user without the permission to
// change the server's files, and while the server moves.
func (s *session) writable(g access.Grants, pl place) error {
	switch {
	case pl.path == "":
		return errFixed
	case !g.On(access.FilesWrite, pl.node.nodeID, pl.server.serverID):
		return access.Denied(access.FilesWrite)
	}
	return s.srv.Moving(pl.server.serverID)
}

// Filelist lists folders and tells about files and folders.
func (s *session) Filelist(r *sftp.Request) (sftp.ListerAt, error) {
	ctx, cancel := context.WithTimeout(r.Context(), opTimeout)
	defer cancel()
	_, g, err := s.authorize(ctx)
	if err != nil {
		return nil, err
	}
	pl, err := s.resolve(ctx, g, r.Filepath)
	if err != nil {
		return nil, toStatus(err)
	}
	switch r.Method {
	case "List":
		list, err := s.list(ctx, g, pl)
		return list, toStatus(err)
	case "Stat":
		info, err := s.stat(ctx, pl)
		return listerAt{info}, toStatus(err)
	}
	return nil, errLinks
}

// list lists the nodes with servers whose files the user may read, their servers, or a folder
// of a server.
func (s *session) list(ctx context.Context, g access.Grants, pl place) (listerAt, error) {
	var list listerAt
	switch {
	case pl.node.nodeID == "":
		nodes, err := s.srv.tree.nodeFolders(ctx)
		for _, n := range nodes {
			if g.Somewhere(access.FilesRead, n.nodeID) {
				list = append(list, s.srv.folder(n.name))
			}
		}
		return list, err
	case pl.server.serverID == "":
		servers, err := s.srv.tree.serverFolders(ctx, pl.node.nodeID)
		for _, srv := range servers {
			if g.On(access.FilesRead, srv.nodeID, srv.serverID) {
				list = append(list, s.srv.folder(srv.name))
			}
		}
		return list, err
	}
	res, err := s.srv.files.List(ctx, pl.node.nodeID, pl.server.serverID, pl.path)
	for _, f := range res.GetFiles() {
		list = append(list, toInfo(f))
	}
	return list, err
}

// stat tells about a file or folder, as the listing of its folder does.
func (s *session) stat(ctx context.Context, pl place) (os.FileInfo, error) {
	if pl.path == "" {
		return s.srv.folder(path.Base(pl.sftp)), nil
	}
	res, err := s.srv.files.List(ctx, pl.node.nodeID, pl.server.serverID, path.Dir(pl.path))
	for _, f := range res.GetFiles() {
		if f.GetName() == path.Base(pl.path) {
			return toInfo(f), nil
		}
	}
	if err == nil {
		err = errNotFound
	}
	return nil, err
}

// Fileread opens a file to read it, as the file manager shows it.
func (s *session) Fileread(r *sftp.Request) (io.ReaderAt, error) {
	ctx := r.Context()
	user, g, err := s.authorize(ctx)
	if err != nil {
		return nil, err
	}
	pl, err := s.resolve(ctx, g, r.Filepath)
	if err == nil && pl.path == "" {
		err = errFolder
	}
	if err == nil {
		err = s.opened()
	}
	var rd *reader
	if err == nil {
		rd, err = newReader(ctx, func(ctx context.Context, offset int64) (*noryxv1.ReadFileResponse, stream, error) {
			return s.srv.files.Read(ctx, pl.node.nodeID, &noryxv1.ReadFileRequest{ServerId: pl.server.serverID, Path: pl.path, Offset: offset})
		}, s.closed)
		if err != nil {
			s.closed()
		}
	}
	s.audit(user, pl, "Download file", err)
	if err != nil {
		return nil, toStatus(err)
	}
	return rd, nil
}

// Filewrite opens a file to write it, which the agent replaces once the client closes it.
// Without truncating, it is a new file, as SFTP doesn't change parts of files.
func (s *session) Filewrite(r *sftp.Request) (io.WriterAt, error) {
	ctx := r.Context()
	user, g, err := s.authorize(ctx)
	if err != nil {
		return nil, err
	}
	pl, err := s.resolve(ctx, g, r.Filepath)
	if err == nil {
		err = s.writable(g, pl)
	}
	if err == nil {
		err = s.opened()
	}
	if err != nil {
		s.audit(user, pl, "Upload file", err)
		return nil, toStatus(err)
	}
	flags := r.Pflags()
	header := &noryxv1.WriteFileHeader{ServerId: pl.server.serverID, Path: pl.path, Overwrite: flags.Trunc && !flags.Excl}
	return newWriter(ctx, &s.early, func(ctx context.Context, content io.Reader) error {
		_, err := s.srv.files.Write(ctx, pl.node.nodeID, header, content)
		return err
	}, func(err error) {
		s.closed()
		s.audit(user, pl, "Upload file", err)
	}), nil
}

// Filecmd changes files and folders. Times, permissions and owners stay as the agent sets them,
// though: clients that set them after an upload, e.g. to keep the time of the original, get
// no error, as the file manager can't set them either.
func (s *session) Filecmd(r *sftp.Request) error {
	ctx, cancel := context.WithTimeout(r.Context(), opTimeout)
	defer cancel()
	user, g, err := s.authorize(ctx)
	if err != nil {
		return err
	}
	pl, err := s.resolve(ctx, g, r.Filepath)
	if err == nil && r.Method != "Link" && r.Method != "Symlink" {
		err = s.writable(g, pl)
	}
	switch r.Method {
	case "Setstat":
		if err == nil && r.AttrFlags().Size {
			err = errTruncate
		}
		return toStatus(err)
	case "Mkdir":
		if err == nil {
			err = s.srv.files.CreateDirectory(ctx, pl.node.nodeID, pl.server.serverID, pl.path)
		}
		s.audit(user, pl, "Create folder", err)
	case "Rename":
		var to place
		if err == nil {
			to, err = s.resolve(ctx, g, r.Target)
		}
		if err == nil && to.server != pl.server {
			err = errElsewhere
		}
		if err == nil {
			err = s.writable(g, to)
		}
		if err == nil {
			err = s.srv.files.Move(ctx, pl.node.nodeID, pl.server.serverID, pl.path, to.path)
		}
		s.audit(user, pl, "Move file", err, slog.String("to", to.shown()))
	case "Remove", "Rmdir":
		if err == nil {
			err = s.removable(ctx, pl, r.Method == "Rmdir")
		}
		if err == nil {
			err = s.srv.files.Delete(ctx, pl.node.nodeID, pl.server.serverID, pl.path)
		}
		s.audit(user, pl, "Delete file", err)
	default:
		return errLinks
	}
	return toStatus(err)
}

// removable refuses to remove a folder as a file, and a folder that isn't empty, as SFTP
// removes what is in a folder first.
func (s *session) removable(ctx context.Context, pl place, folder bool) error {
	if !folder {
		info, err := s.stat(ctx, pl)
		if err == nil && info.IsDir() {
			err = errFolder
		}
		return err
	}
	res, err := s.srv.files.List(ctx, pl.node.nodeID, pl.server.serverID, pl.path)
	if err == nil && len(res.GetFiles()) > 0 {
		err = errNotEmpty
	}
	return err
}

// opened counts a file that is opened, unless too many are open.
func (s *session) opened() error {
	if s.open.Add(1) > maxOpen {
		s.open.Add(-1)
		return errTooMany
	}
	return nil
}

func (s *session) closed() { s.open.Add(-1) }

// audit logs what a user did or tried with the files of a server, as the file manager does:
// with the server and the path in its folder, or the path of SFTP outside the folders of servers.
func (s *session) audit(user auth.User, pl place, action string, err error, attrs ...slog.Attr) {
	level, message := slog.LevelInfo, action
	if err = toStatus(err); errors.Is(err, sftp.ErrSSHFxPermissionDenied) {
		level, message = slog.LevelWarn, action+" denied"
	} else if err != nil {
		message = action + " failed"
	}
	attrs = append(attrs, logging.Files, slog.String("via", "sftp"), slog.String("ip", s.ip), slog.String("path", pl.shown()))
	if pl.node.nodeID != "" {
		attrs = append(attrs, slog.String(logging.KeyNode, pl.node.nodeID), slog.String(logging.KeyNodeName, pl.node.label))
	}
	if pl.server.serverID != "" {
		attrs = append(attrs, slog.String(logging.KeyServer, pl.server.serverID), slog.String(logging.KeyServerName, pl.server.label))
	}
	if err != nil {
		attrs = append(attrs, slog.String("err", err.Error()))
	}
	slog.LogAttrs(context.Background(), level, message, append(attrs, user.LogAttrs()...)...)
}

// folder describes a folder of a node or server.
func (s *Server) folder(name string) os.FileInfo {
	return fileInfo{name: name, dir: true, modified: s.started}
}

func toInfo(f *noryxv1.FileInfo) os.FileInfo {
	return fileInfo{name: f.GetName(), dir: f.GetDirectory(), size: f.GetSize(), modified: time.Unix(f.GetModifiedUnix(), 0)}
}

type fileInfo struct {
	name     string
	dir      bool
	size     int64
	modified time.Time
}

func (f fileInfo) Name() string       { return f.name }
func (f fileInfo) Size() int64        { return f.size }
func (f fileInfo) ModTime() time.Time { return f.modified }
func (f fileInfo) IsDir() bool        { return f.dir }
func (f fileInfo) Sys() any           { return nil }

func (f fileInfo) Mode() fs.FileMode {
	if f.dir {
		return fs.ModeDir | 0o755
	}
	return 0o644
}

// listerAt lists files and folders.
type listerAt []os.FileInfo

func (l listerAt) ListAt(list []os.FileInfo, offset int64) (int, error) {
	if offset >= int64(len(l)) {
		return 0, io.EOF
	}
	n := copy(list, l[offset:])
	if n < len(list) {
		return n, io.EOF
	}
	return n, nil
}
