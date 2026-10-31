// Package backup implements the BackupService of the agent, which keeps backups of the
// servers in the storage locations of the node. Backups are only reachable through the
// agent: containers don't mount them, and the master can't choose where they are kept
// beyond the locations the node's administrator allowed.
package backup

import (
	"archive/zip"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	"github.com/QwikByte/noryx/internal/agent/archive"
	"github.com/QwikByte/noryx/internal/agent/datadir"
	"github.com/QwikByte/noryx/internal/agent/fileset"
	"github.com/QwikByte/noryx/internal/agent/grpcerr"
	"github.com/QwikByte/noryx/internal/agent/runtime"
	"github.com/QwikByte/noryx/internal/agent/secrets"
	"github.com/QwikByte/noryx/internal/agent/storage"
	"github.com/QwikByte/noryx/internal/logging"

	"github.com/QwikByte/noryx/internal/agent/progress"
)

const (
	chunkSize  = 256 << 10
	maxLabel   = 64
	maxEntries = 5000 // listed of a folder of a backup
)

var jobPattern = regexp.MustCompile(`^[a-z0-9]{1,64}$`)

type Service struct {
	noryxv1.UnimplementedBackupServiceServer
	rt    runtime.Runtime
	store Store

	mu   sync.Mutex
	busy map[string]bool // servers with a backup or restore in progress
}

func NewService(rt runtime.Runtime, locations *storage.Locations) *Service {
	return &Service{rt: rt, store: Store{locations}, busy: map[string]bool{}}
}

func (s *Service) ListBackups(ctx context.Context, req *noryxv1.ListBackupsRequest) (*noryxv1.ListBackupsResponse, error) {
	if _, err := s.find(ctx, req.GetServerId()); err != nil {
		return nil, err
	}
	backups, err := s.store.List(req.GetServerId())
	if err != nil {
		return nil, toStatus(err)
	}
	res := &noryxv1.ListBackupsResponse{}
	for _, b := range backups {
		res.Backups = append(res.Backups, b.Proto())
	}
	return res, nil
}

func (s *Service) CreateBackup(ctx context.Context, req *noryxv1.CreateBackupRequest) (*noryxv1.CreateBackupResponse, error) {
	label := strings.TrimSpace(req.GetLabel())
	retention := noryxv1.Retention(req.GetKeep(), req.GetRetention())
	if err := CheckDetails(label, req.GetJobId(), retention); err != nil {
		return nil, err
	}
	srv, release, err := s.lock(ctx, req.GetServerId())
	if err != nil {
		return nil, err
	}
	defer release()
	data, err := s.rt.Data(ctx, srv.ID)
	if err != nil {
		return nil, toStatus(err)
	}
	defer data.Close()
	paths, exclude, err := selected(data, srv.Type, req.GetSelection())
	if err != nil {
		return nil, toStatus(err)
	}
	switch {
	case len(paths) == 0 && req.GetSkipWithoutData():
		return &noryxv1.CreateBackupResponse{}, nil
	case len(paths) == 0:
		return nil, status.Error(codes.FailedPrecondition, "The server has none of the selected data.")
	}
	d := Details{Label: label, Created: time.Now(), Paths: paths, Exclude: exclude, JobID: req.GetJobId()}
	b, err := s.backUp(ctx, data, srv, cmp.Or(req.GetLocation(), storage.Default), d)
	if err != nil {
		return nil, err
	}
	if req.GetJobId() != "" {
		if err := s.store.Prune(srv.ID, req.GetJobId(), retention); err != nil {
			slog.Warn("Can't delete old backups", logging.Backups, logging.KeyServer, srv.ID, "job", req.GetJobId(), "err", err)
		}
	}
	return &noryxv1.CreateBackupResponse{Backup: b.Proto()}, nil
}

// backUp archives the data of a server that d describes. A running game server writes its
// worlds to disk first and doesn't save meanwhile.
func (s *Service) backUp(ctx context.Context, data *datadir.Dir, srv runtime.Server, location string, d Details) (Archive, error) {
	resume, err := runtime.PauseSaving(ctx, s.rt, srv)
	if errors.Is(err, runtime.ErrNotReady) {
		return Archive{}, status.Error(codes.FailedPrecondition, "Wait until the server has started, or stop it, to back it up.")
	}
	if err != nil {
		return Archive{}, toStatus(err)
	}
	defer resume()
	b, err := s.store.create(ctx, data, srv.ID, location, d)
	return b, toStatus(err)
}

// RestoreBackup extracts the backup, and backs up what it replaces if asked, before it stops
// the server, so the server is only down while the files are swapped. The restored data
// keeps the secrets of the server, of file sets and of the server's network, and its
// forwarding settings, as they are now.
func (s *Service) RestoreBackup(ctx context.Context, req *noryxv1.RestoreBackupRequest) (*noryxv1.RestoreBackupResponse, error) {
	srv, release, err := s.lock(ctx, req.GetServerId())
	if err != nil {
		return nil, err
	}
	defer release()
	b, err := s.store.Find(srv.ID, req.GetBackupId())
	if err != nil {
		return nil, toStatus(err)
	}
	f, err := os.Open(b.Path())
	if err != nil {
		return nil, toStatus(err)
	}
	defer f.Close()
	zr, err := zipOf(f, b)
	if err != nil {
		return nil, toStatus(err)
	}
	paths, err := chosen(zr, b, req.GetPaths())
	if err != nil {
		return nil, err
	}
	data, err := s.rt.Data(ctx, srv.ID)
	if err != nil {
		return nil, toStatus(err)
	}
	defer data.Close()
	var staged string
	if b.Untrusted {
		staged, err = stageUntrusted(ctx, data, b, paths)
	} else {
		staged, err = stage(ctx, data, zr, paths)
	}
	if staged != "" {
		defer data.RemoveAll(staged) //nolint:errcheck // best effort; the result of restoring matters
	}
	if err != nil {
		return nil, toStatus(err)
	}
	res := &noryxv1.RestoreBackupResponse{}
	if req.GetSnapshotFirst() {
		if res.Snapshot, err = s.snapshot(ctx, data, srv, b, paths); err != nil {
			return nil, err
		}
	}
	running := srv.State != noryxv1.ServerState_SERVER_STATE_STOPPED
	if running {
		progress.Step(ctx, "stop", 0)
		if err := s.rt.Stop(ctx, srv.ID); err != nil {
			return nil, toStatus(err)
		}
	}
	err = keep(data, srv.Type, staged, b.Untrusted)
	if err == nil {
		err = swap(data, staged, paths, kept(data, b))
	}
	if running { // also after a failure, which leaves the server as it was or partly restored
		progress.Step(ctx, "start", 0)
		err = errors.Join(err, s.rt.Start(context.WithoutCancel(ctx), srv.ID))
	}
	return res, toStatus(err)
}

// snapshot backs up what restoring paths of a backup replaces, as a backup made by hand
// next to it; none if the server has none of the paths.
func (s *Service) snapshot(ctx context.Context, data *datadir.Dir, srv runtime.Server, b Archive, paths []string) (*noryxv1.Backup, error) {
	paths = slices.DeleteFunc(slices.Clone(paths), func(p string) bool { return !exists(data, p) })
	if len(paths) == 0 {
		return nil, nil
	}
	exclude := slices.DeleteFunc(slices.Clone(b.Exclude), func(e string) bool { return !slices.ContainsFunc(paths, within(e)) })
	label := []rune("Before restoring " + cmp.Or(b.Label, b.ID))
	d := Details{Label: string(label[:min(len(label), maxLabel)]), Created: time.Now(), Paths: paths, Exclude: exclude}
	snapshot, err := s.backUp(ctx, data, srv, b.Location, d)
	if err != nil {
		return nil, err
	}
	return snapshot.Proto(), nil
}

func (s *Service) DeleteBackup(ctx context.Context, req *noryxv1.DeleteBackupRequest) (*noryxv1.DeleteBackupResponse, error) {
	srv, release, err := s.lock(ctx, req.GetServerId())
	if err != nil {
		return nil, err
	}
	defer release()
	b, err := s.store.Find(srv.ID, req.GetBackupId())
	if err == nil {
		err = s.store.Remove(b)
	}
	return &noryxv1.DeleteBackupResponse{}, toStatus(err)
}

func (s *Service) UpdateBackup(ctx context.Context, req *noryxv1.UpdateBackupRequest) (*noryxv1.UpdateBackupResponse, error) {
	srv, release, err := s.lock(ctx, req.GetServerId())
	if err != nil {
		return nil, err
	}
	defer release()
	b, err := s.store.Find(srv.ID, req.GetBackupId())
	if err != nil {
		return nil, toStatus(err)
	}
	if req.Label != nil {
		b.Label = strings.TrimSpace(req.GetLabel())
	}
	if req.Kept != nil {
		b.Kept = req.GetKept()
	}
	if err := CheckDetails(b.Label, "", nil); err != nil {
		return nil, err
	}
	if err := s.store.Update(b); err != nil {
		return nil, toStatus(err)
	}
	return &noryxv1.UpdateBackupResponse{Backup: b.Proto()}, nil
}

// ListBackupFiles lists a folder of a backup like the file manager lists one of the server:
// what only holds secrets of the server now isn't listed.
func (s *Service) ListBackupFiles(ctx context.Context, req *noryxv1.ListBackupFilesRequest) (*noryxv1.ListBackupFilesResponse, error) {
	folder, ok := datadir.Name(req.GetPath())
	if !ok {
		return nil, status.Error(codes.InvalidArgument, "invalid path")
	}
	srv, err := s.find(ctx, req.GetServerId())
	if err != nil {
		return nil, err
	}
	b, err := s.store.Find(srv.ID, req.GetBackupId())
	if err != nil {
		return nil, toStatus(err)
	}
	data, err := s.rt.Data(ctx, srv.ID)
	if err != nil {
		return nil, toStatus(err)
	}
	hidden := fileset.Read(data).Secrets()
	data.Close()
	f, err := os.Open(b.Path())
	if err != nil {
		return nil, toStatus(err)
	}
	defer f.Close()
	zr, err := zipOf(f, b)
	if err != nil {
		return nil, toStatus(err)
	}
	files, ok := list(zr, folder, hidden)
	if !ok {
		return nil, status.Error(codes.NotFound, "The backup has no such folder.")
	}
	return &noryxv1.ListBackupFilesResponse{Files: files[:min(len(files), maxEntries)], Truncated: len(files) > maxEntries}, nil
}

func (s *Service) DownloadBackup(req *noryxv1.DownloadBackupRequest, stream noryxv1.BackupService_DownloadBackupServer) error {
	srv, err := s.find(stream.Context(), req.GetServerId())
	if err != nil {
		return err
	}
	b, err := s.store.Find(srv.ID, req.GetBackupId())
	if err != nil {
		return toStatus(err)
	}
	f, err := os.Open(b.Path())
	if err != nil {
		return toStatus(err)
	}
	defer f.Close()
	r, size := io.Reader(f), b.Size
	if req.GetHideSecrets() {
		// What the server marks now is hidden, also in backups from before.
		data, err := s.rt.Data(stream.Context(), srv.ID)
		if err != nil {
			return toStatus(err)
		}
		hidden := fileset.Read(data).Secrets()
		data.Close()
		zr, err := zipOf(f, b)
		if err != nil {
			return toStatus(err)
		}
		pr, pw := io.Pipe()
		defer pr.Close() // stops CopyZip if the client goes away
		go func() { pw.CloseWithError(datadir.CopyZip(pw, zr, hidden.Censor("."))) }()
		r, size = pr, 0
	}
	return Send(r, size, func(size int64, data []byte) error {
		return stream.Send(&noryxv1.DownloadBackupResponse{Size: size, Data: data})
	})
}

// zipOf reads the directory of the archive of a backup in f. That of an untrusted one, from
// elsewhere, is read within the limits of backups, which it passed when it was added.
func zipOf(f *os.File, b Archive) (*zip.Reader, error) {
	if b.Untrusted {
		return archive.OpenZip(f, b.Size, archive.Backups)
	}
	return zip.NewReader(f, b.Size)
}

// Send sends what r reads in chunks, the first with size, until its end.
func Send(r io.Reader, size int64, send func(size int64, data []byte) error) error {
	buf := make([]byte, chunkSize)
	for first := true; ; first = false {
		n, err := io.ReadFull(r, buf)
		if n > 0 || first {
			if err := send(size, buf[:n]); err != nil {
				return err
			}
			size = 0
		}
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return nil
		}
		if err != nil {
			return toStatus(err)
		}
	}
}

// CheckDetails checks the label of a backup, the job that creates it and which of the
// job's backups are kept.
func CheckDetails(label, jobID string, retention *noryxv1.BackupRetention) error {
	switch {
	case utf8.RuneCountInString(label) > maxLabel || strings.ContainsFunc(label, unicode.IsControl):
		return status.Errorf(codes.InvalidArgument, "Enter a label with up to %d characters.", maxLabel)
	case jobID != "" && !jobPattern.MatchString(jobID):
		return status.Error(codes.InvalidArgument, "invalid job ID")
	case retention.Problem() != "":
		return status.Error(codes.InvalidArgument, retention.Problem())
	}
	return nil
}

// ImportBackup adds a backup that a server had on another node, with its ID and Details.
func (s *Service) ImportBackup(stream noryxv1.BackupService_ImportBackupServer) error {
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	h := first.GetHeader()
	if h.GetBackup() == nil {
		return status.Error(codes.InvalidArgument, "the first message must describe the backup")
	}
	srv, release, err := s.lock(stream.Context(), h.GetServerId())
	if err != nil {
		return err
	}
	defer release()
	imported, err := s.importArchive(srv.ID, h.GetBackup(), func() ([]byte, error) {
		msg, err := stream.Recv()
		return msg.GetData(), err
	})
	if err != nil {
		return err
	}
	return stream.SendAndClose(&noryxv1.ImportBackupResponse{Backup: imported.Proto()})
}

// ImportCopy keeps a copy of a backup of a server of another node, apart from the backups of
// the servers here, which the master relays from there.
func (s *Service) ImportCopy(stream noryxv1.BackupService_ImportCopyServer) error {
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	h := first.GetHeader()
	switch {
	case h.GetBackup() == nil:
		return status.Error(codes.InvalidArgument, "the first message must describe the backup")
	case !runtime.ValidID(h.GetServerId()):
		return status.Error(codes.InvalidArgument, "invalid server ID")
	}
	imported, err := s.importArchive(copies(h.GetServerId()), h.GetBackup(), func() ([]byte, error) {
		msg, err := stream.Recv()
		return msg.GetData(), err
	})
	if err != nil {
		return err
	}
	return stream.SendAndClose(&noryxv1.ImportCopyResponse{Backup: imported.Proto()})
}

// importArchive adds the archive that recv receives, until io.EOF, as a backup of owner that
// b describes. It came from elsewhere, so it is untrusted: it may have the size that b tells
// at most, while 1 GB stays free, and is kept once it passed the checks of an uploaded backup
// with the limits of backups.
func (s *Service) importArchive(owner string, b *noryxv1.Backup, recv func() ([]byte, error)) (Archive, error) {
	d, err := importedDetails(b)
	if err != nil {
		return Archive{}, status.Error(codes.InvalidArgument, err.Error())
	}
	d.Untrusted = true
	imported, err := s.store.AddFile(owner, cmp.Or(b.GetLocation(), storage.Default), b.GetId(), d, b.GetSize(), func(f *os.File, _ *Details) error {
		w, size := storage.Guard(f, f), int64(0)
		for {
			data, err := recv()
			switch {
			case errors.Is(err, io.EOF):
				return checkImported(f, size)
			case err != nil:
				return err
			}
			if size += int64(len(data)); size > b.GetSize() {
				return status.Error(codes.InvalidArgument, "The archive of the backup is larger than announced.")
			}
			if _, err := w.Write(data); err != nil {
				return err
			}
		}
	})
	return imported, toStatus(err)
}

// checkImported checks the archive of a backup from elsewhere in f, which has size bytes, like
// an uploaded one, with the limits of backups.
func checkImported(f *os.File, size int64) error {
	a, err := archive.Open(f, size, archive.Backups)
	if err == nil && !a.Plain() {
		err = status.Error(codes.InvalidArgument, "The archive of the backup isn't one of a backup.")
	}
	return err
}

// DownloadCopy sends the archive of a copy that ImportCopy keeps.
func (s *Service) DownloadCopy(req *noryxv1.DownloadCopyRequest, stream noryxv1.BackupService_DownloadCopyServer) error {
	if !runtime.ValidID(req.GetServerId()) {
		return status.Error(codes.InvalidArgument, "invalid server ID")
	}
	b, err := s.store.Find(copies(req.GetServerId()), req.GetBackupId())
	if err != nil {
		return toStatus(err)
	}
	f, err := os.Open(b.Path())
	if err != nil {
		return toStatus(err)
	}
	defer f.Close()
	return Send(f, b.Size, func(size int64, data []byte) error {
		return stream.Send(&noryxv1.DownloadCopyResponse{Size: size, Data: data})
	})
}

// DeleteCopy deletes a copy that ImportCopy keeps.
func (s *Service) DeleteCopy(_ context.Context, req *noryxv1.DeleteCopyRequest) (*noryxv1.DeleteCopyResponse, error) {
	if !runtime.ValidID(req.GetServerId()) {
		return nil, status.Error(codes.InvalidArgument, "invalid server ID")
	}
	b, err := s.store.Find(copies(req.GetServerId()), req.GetBackupId())
	if err == nil {
		err = s.store.Remove(b)
	}
	return &noryxv1.DeleteCopyResponse{}, toStatus(err)
}

// UploadBackup adds an archive from elsewhere as an untrusted backup of a server once it checked
// it like an archive of the file manager.
func (s *Service) UploadBackup(stream noryxv1.BackupService_UploadBackupServer) error {
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	h := first.GetHeader()
	if h == nil {
		return status.Error(codes.InvalidArgument, "the first message must describe the backup")
	}
	label := strings.TrimSpace(h.GetLabel())
	if err := CheckDetails(label, "", nil); err != nil {
		return err
	}
	srv, err := s.find(stream.Context(), h.GetServerId())
	if err != nil {
		return err
	}
	d := Details{Label: label, Created: time.Now(), Untrusted: true}
	b, err := s.store.AddFile(srv.ID, cmp.Or(h.GetLocation(), storage.Default), noryxv1.NewBackupID(d.Created), d, h.GetSize(), func(f *os.File, d *Details) error {
		_, err := archive.Receive(stream.Recv, storage.Guard(f, f))
		if err == nil {
			d.Paths, err = uploadedPaths(f)
		}
		return err
	})
	if err != nil {
		return toStatus(err)
	}
	return stream.SendAndClose(&noryxv1.UploadBackupResponse{Backup: b.Proto()})
}

// uploadedPaths checks the archive of an uploaded backup in f like one of the file manager,
// and returns the files and folders at its top, which restoring it replaces, without those a
// server never takes from an archive. Its entries have clean names, as listing and restoring
// parts of backups need them.
func uploadedPaths(f *os.File) ([]string, error) {
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	a, err := archive.Open(f, info.Size(), archive.Uploads)
	switch {
	case err != nil:
		return nil, err
	case !a.Plain():
		return nil, status.Error(codes.InvalidArgument, "Upload a ZIP archive of files and folders of a server, e.g. a backup downloaded before, whose paths don't start with ./ or use backslashes.")
	}
	foreign := archive.Foreign(secrets.With(fileset.ManifestFile))
	var paths []string
	for _, e := range a.Entries {
		top, _, _ := strings.Cut(e.Name, "/")
		if !foreign(top) {
			paths = append(paths, filepath.FromSlash(top))
		}
	}
	slices.Sort(paths)
	if paths = slices.Compact(paths); len(paths) == 0 {
		return nil, status.Error(codes.InvalidArgument, "The archive holds no files of a server.")
	}
	return paths, nil
}

// importedDetails validates a backup from another node like those made here.
func importedDetails(b *noryxv1.Backup) (Details, error) {
	d := Details{Label: b.GetLabel(), Created: time.Unix(b.GetCreatedUnix(), 0), JobID: b.GetJobId(), Kept: b.GetKept(), Untrusted: b.GetUntrusted()}
	for _, p := range b.GetPaths() {
		name, ok := datadir.Name(p)
		if !ok {
			return d, fmt.Errorf("invalid path %q in backup", p)
		}
		d.Paths = append(d.Paths, name)
	}
	for _, p := range b.GetExclude() {
		name, ok := datadir.Name(p)
		if !ok || name == "." {
			return d, fmt.Errorf("invalid path %q in backup", p)
		}
		d.Exclude = append(d.Exclude, name)
	}
	switch {
	case !idPattern.MatchString(b.GetId()):
		return d, errors.New("invalid backup ID")
	case len(d.Paths) == 0:
		return d, errors.New("the backup has no files")
	case utf8.RuneCountInString(d.Label) > maxLabel || strings.ContainsFunc(d.Label, unicode.IsControl):
		return d, errors.New("invalid label")
	case d.JobID != "" && !jobPattern.MatchString(d.JobID):
		return d, errors.New("invalid job ID")
	}
	return d, nil
}

// RemoveAll deletes all backups of a server, which is done when the server is deleted.
func (s *Service) RemoveAll(serverID string) error {
	if !runtime.ValidID(serverID) {
		return errors.New("invalid server ID")
	}
	return s.store.RemoveAll(serverID)
}

func (s *Service) find(ctx context.Context, id string) (runtime.Server, error) {
	if !runtime.ValidID(id) {
		return runtime.Server{}, status.Error(codes.InvalidArgument, "invalid server ID")
	}
	srv, err := runtime.Find(ctx, s.rt, id)
	return srv, toStatus(err)
}

// lock finds a server and reserves it for one backup, restore or deletion at a time.
func (s *Service) lock(ctx context.Context, id string) (runtime.Server, func(), error) {
	srv, err := s.find(ctx, id)
	if err != nil {
		return srv, nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.busy[id] {
		return srv, nil, status.Error(codes.FailedPrecondition, "Another backup or restore of this server is in progress. Try again when it is done.")
	}
	s.busy[id] = true
	return srv, func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		delete(s.busy, id)
	}, nil
}

// known are the errors of backups that the service answers with a code and a text.
var known = []grpcerr.Known{
	{Err: fs.ErrNotExist, Code: codes.NotFound, Text: "Backup not found."},
	{Err: fs.ErrExist, Code: codes.AlreadyExists, Text: "The backup exists already."},
}

func toStatus(err error) error { return grpcerr.Status(err, known...) }
