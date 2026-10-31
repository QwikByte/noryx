// Package datastore implements the DatastoreService of the agent, which runs the MariaDB and
// PostgreSQL servers of networks with the runtime, keeps dumps of their databases like the
// backups of servers, also those uploaded from elsewhere, and shows the rows of their tables.
// The agent never stores the passwords of the databases' users: it restores dumps as the users
// themselves, and upgrades keep the hashes of their passwords, so that backing up and restoring
// also works with the local CLI alone.
package datastore

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
	"path"
	"slices"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	"github.com/QwikByte/noryx/internal/agent/backup"
	"github.com/QwikByte/noryx/internal/agent/grpcerr"
	"github.com/QwikByte/noryx/internal/agent/overlay"
	"github.com/QwikByte/noryx/internal/agent/progress"
	"github.com/QwikByte/noryx/internal/agent/runtime"
	"github.com/QwikByte/noryx/internal/agent/storage"
	"github.com/QwikByte/noryx/internal/logging"
)

const (
	minMemoryMB = 256
	minPort     = 1024
	// Rows of a table that a page has by default, and at most.
	defaultRows = 50
	maxRows     = 200
	// overlayPrefix keeps the datastores apart from the servers among the clients of the
	// private network of the nodes.
	overlayPrefix = overlay.DatastorePrefix
)

// Runtime runs the datastores; the servers it lists keep their ports.
type Runtime interface {
	runtime.Datastores
	List(ctx context.Context) ([]runtime.Server, error)
}

// Overlay publishes the ports of datastores in the private network of the nodes.
type Overlay interface {
	Admit(id string, port uint32, clients, keys []string) (string, error)
	Dismiss(id string) error
}

type Service struct {
	noryxv1.UnimplementedDatastoreServiceServer
	rt      Runtime
	dumps   backup.Store
	overlay Overlay

	mu   sync.Mutex
	busy map[string]bool // datastores with a change, dump or restore in progress
}

func NewService(rt Runtime, locations *storage.Locations, overlay Overlay) *Service {
	return &Service{rt: rt, dumps: backup.NewStore(locations), overlay: overlay, busy: map[string]bool{}}
}

// owner is the folder of the dumps of a datastore among the backups.
func owner(id string) string { return path.Join("datastores", id) }

func (s *Service) ListDatastores(ctx context.Context, _ *noryxv1.ListDatastoresRequest) (*noryxv1.ListDatastoresResponse, error) {
	list, err := s.rt.ListDatastores(ctx)
	if err != nil {
		return nil, toStatus(err)
	}
	res := &noryxv1.ListDatastoresResponse{}
	for _, ds := range list {
		p := toProto(ds)
		if ds.State == noryxv1.DatastoreState_DATASTORE_STATE_RUNNING {
			if p.Databases, err = s.rt.Databases(ctx, ds.ID); err != nil {
				slog.DebugContext(ctx, "Can't list the databases", logging.Databases, "datastore", ds.ID, "err", err)
			}
		}
		res.Datastores = append(res.Datastores, p)
	}
	for engine, versions := range s.rt.DatastoreVersions() {
		res.Versions = append(res.Versions, &noryxv1.DatastoreVersions{Engine: engine, Versions: versions})
	}
	slices.SortFunc(res.Versions, func(a, b *noryxv1.DatastoreVersions) int { return cmp.Compare(a.GetEngine(), b.GetEngine()) })
	return res, nil
}

func toProto(ds runtime.Datastore) *noryxv1.Datastore {
	return &noryxv1.Datastore{
		Id: ds.ID, Engine: ds.Engine, Version: ds.Version, MemoryMb: ds.MemoryMB, CpuMillis: ds.CPUMillis, Storage: ds.Storage,
		State: ds.State, Size: ds.Size, PreviousVersion: ds.Previous, Port: ds.Port,
		Parameters: &noryxv1.DatastoreParameters{MaxConnections: ds.MaxConnections, BufferMb: ds.BufferMB},
	}
}

func (s *Service) CreateDatastore(ctx context.Context, req *noryxv1.CreateDatastoreRequest) (*noryxv1.CreateDatastoreResponse, error) {
	spec := runtime.DatastoreSpec{
		ID: req.GetId(), Engine: req.GetEngine(), Version: req.GetVersion(), MemoryMB: req.GetMemoryMb(), CPUMillis: req.GetCpuMillis(), Storage: req.GetStorage(),
		MaxConnections: req.GetParameters().GetMaxConnections(), BufferMB: req.GetParameters().GetBufferMb(),
	}
	switch problem := req.GetParameters().Problem(spec.Engine, spec.MemoryMB); {
	case !runtime.ValidID(spec.ID):
		return nil, status.Error(codes.InvalidArgument, "invalid datastore ID")
	case !slices.Contains(s.rt.DatastoreVersions()[spec.Engine], spec.Version):
		return nil, status.Errorf(codes.InvalidArgument, "This agent doesn't run %s %s.", spec.Engine.Slug(), spec.Version)
	case spec.MemoryMB < minMemoryMB:
		return nil, status.Errorf(codes.InvalidArgument, "Give the datastore at least %d MB of memory.", minMemoryMB)
	case problem != "":
		return nil, status.Error(codes.InvalidArgument, problem)
	}
	release, err := s.reserve(spec.ID)
	if err != nil {
		return nil, err
	}
	defer release()
	if _, err := s.find(ctx, spec.ID); status.Code(err) != codes.NotFound {
		return nil, cmp.Or(err, status.Error(codes.AlreadyExists, "The datastore exists already."))
	}
	if err := s.rt.CreateDatastore(ctx, spec); err != nil {
		return nil, toStatus(err)
	}
	ds, err := s.find(ctx, spec.ID)
	return &noryxv1.CreateDatastoreResponse{Datastore: toProto(ds)}, err
}

func (s *Service) StartDatastore(ctx context.Context, req *noryxv1.StartDatastoreRequest) (*noryxv1.StartDatastoreResponse, error) {
	_, release, err := s.lock(ctx, req.GetId())
	if err != nil {
		return nil, err
	}
	defer release()
	return &noryxv1.StartDatastoreResponse{}, toStatus(s.rt.StartDatastore(ctx, req.GetId()))
}

func (s *Service) StopDatastore(ctx context.Context, req *noryxv1.StopDatastoreRequest) (*noryxv1.StopDatastoreResponse, error) {
	_, release, err := s.lock(ctx, req.GetId())
	if err != nil {
		return nil, err
	}
	defer release()
	return &noryxv1.StopDatastoreResponse{}, toStatus(s.rt.StopDatastore(ctx, req.GetId()))
}

func (s *Service) UpdateDatastore(ctx context.Context, req *noryxv1.UpdateDatastoreRequest) (*noryxv1.UpdateDatastoreResponse, error) {
	ds, release, err := s.lock(ctx, req.GetId())
	if err != nil {
		return nil, err
	}
	defer release()
	spec := ds.DatastoreSpec
	spec.MemoryMB, spec.CPUMillis, spec.Version = req.GetMemoryMb(), req.GetCpuMillis(), cmp.Or(req.GetVersion(), spec.Version)
	spec.MaxConnections, spec.BufferMB = req.GetParameters().GetMaxConnections(), req.GetParameters().GetBufferMb()
	if req.GetRemovePrevious() {
		spec.Previous = ""
	}
	switch problem := req.GetParameters().Problem(spec.Engine, spec.MemoryMB); {
	case spec.MemoryMB < minMemoryMB:
		return nil, status.Errorf(codes.InvalidArgument, "Give the datastore at least %d MB of memory.", minMemoryMB)
	case !slices.Contains(s.rt.DatastoreVersions()[spec.Engine], spec.Version):
		return nil, status.Errorf(codes.InvalidArgument, "This agent doesn't run %s %s.", spec.Engine.Slug(), spec.Version)
	case problem != "":
		return nil, status.Error(codes.InvalidArgument, problem)
	}
	if spec != ds.DatastoreSpec || req.GetUpdateImage() {
		if err := s.rt.UpdateDatastore(ctx, spec, req.GetUpdateImage()); err != nil {
			return nil, toStatus(err)
		}
	}
	ds, err = s.find(ctx, spec.ID)
	return &noryxv1.UpdateDatastoreResponse{Datastore: toProto(ds)}, err
}

// PublishDatastore publishes the port of a datastore at the node's address in the private
// network, for the clients alone, and creates its container again if the port moved.
func (s *Service) PublishDatastore(ctx context.Context, req *noryxv1.PublishDatastoreRequest) (*noryxv1.PublishDatastoreResponse, error) {
	ds, release, err := s.lock(ctx, req.GetId())
	if err != nil {
		return nil, err
	}
	defer release()
	spec, port := ds.DatastoreSpec, req.GetPort()
	spec.Port, spec.Overlay = 0, ""
	if port != 0 && len(req.GetClients()) > 0 {
		if err := s.free(ctx, ds.ID, port); err != nil {
			return nil, err
		}
		addr, err := s.overlay.Admit(overlayPrefix+ds.ID, port, req.GetClients(), req.GetClientKeys())
		if err != nil {
			return nil, status.Error(codes.FailedPrecondition, err.Error())
		}
		spec.Port, spec.Overlay = port, addr
	} else if err := s.overlay.Dismiss(overlayPrefix + ds.ID); err != nil {
		return nil, toStatus(err)
	}
	if spec != ds.DatastoreSpec {
		if err := s.rt.UpdateDatastore(ctx, spec, false); err != nil {
			return nil, toStatus(err)
		}
	}
	return &noryxv1.PublishDatastoreResponse{Address: spec.Overlay}, nil
}

// free fails unless no server or other datastore of the node uses a port.
func (s *Service) free(ctx context.Context, id string, port uint32) error {
	if port < minPort || port > 65535 {
		return status.Errorf(codes.InvalidArgument, "The port must be between %d and 65535.", minPort)
	}
	servers, err := s.rt.List(ctx)
	if err != nil {
		return toStatus(err)
	}
	if i := slices.IndexFunc(servers, func(srv runtime.Server) bool { return srv.Uses(port) }); i >= 0 {
		return status.Errorf(codes.AlreadyExists, "Port %d is already used by %q.", port, servers[i].Name)
	}
	datastores, err := s.rt.ListDatastores(ctx)
	if err != nil {
		return toStatus(err)
	}
	if slices.ContainsFunc(datastores, func(ds runtime.Datastore) bool { return ds.ID != id && ds.Port == port }) {
		return status.Errorf(codes.AlreadyExists, "Port %d is already used by another datastore.", port)
	}
	return nil
}

func (s *Service) DeleteDatastore(ctx context.Context, req *noryxv1.DeleteDatastoreRequest) (*noryxv1.DeleteDatastoreResponse, error) {
	ds, release, err := s.lock(ctx, req.GetId())
	if err != nil {
		return nil, err
	}
	defer release()
	if err := s.rt.RemoveDatastore(ctx, ds.ID); err != nil {
		return nil, toStatus(err)
	}
	return &noryxv1.DeleteDatastoreResponse{}, toStatus(errors.Join(s.overlay.Dismiss(overlayPrefix+ds.ID), s.dumps.RemoveAll(owner(ds.ID))))
}

func (s *Service) EnsureDatabase(ctx context.Context, req *noryxv1.EnsureDatabaseRequest) (*noryxv1.EnsureDatabaseResponse, error) {
	if problem := noryxv1.DatabaseNameProblem(req.GetName()); problem != "" {
		return nil, status.Error(codes.InvalidArgument, problem)
	}
	if !noryxv1.DatabasePassword.MatchString(req.GetPassword()) {
		return nil, status.Error(codes.InvalidArgument, "invalid password")
	}
	ds, release, err := s.lock(ctx, req.GetId())
	if err != nil {
		return nil, err
	}
	defer release()
	names, err := s.rt.Databases(ctx, ds.ID)
	if err != nil {
		return nil, toStatus(err)
	}
	if !slices.Contains(names, req.GetName()) && len(names) >= noryxv1.MaxDatabases {
		return nil, status.Errorf(codes.ResourceExhausted, "A datastore has at most %d databases.", noryxv1.MaxDatabases)
	}
	return &noryxv1.EnsureDatabaseResponse{}, toStatus(s.rt.EnsureDatabase(ctx, ds.ID, req.GetName(), req.GetPassword()))
}

func (s *Service) DropDatabase(ctx context.Context, req *noryxv1.DropDatabaseRequest) (*noryxv1.DropDatabaseResponse, error) {
	if problem := noryxv1.DatabaseNameProblem(req.GetName()); problem != "" { // also the engine's own
		return nil, status.Error(codes.InvalidArgument, problem)
	}
	ds, release, err := s.lock(ctx, req.GetId())
	if err != nil {
		return nil, err
	}
	defer release()
	return &noryxv1.DropDatabaseResponse{}, toStatus(s.rt.DropDatabase(ctx, ds.ID, req.GetName()))
}

// CreateDump dumps databases into a ZIP archive with one <database>.sql each. A running
// datastore keeps running, and each dump is consistent in itself.
func (s *Service) CreateDump(ctx context.Context, req *noryxv1.CreateDumpRequest) (*noryxv1.CreateDumpResponse, error) {
	label := strings.TrimSpace(req.GetLabel())
	retention := noryxv1.Retention(req.GetKeep(), req.GetRetention())
	if err := backup.CheckDetails(label, req.GetJobId(), retention); err != nil {
		return nil, err
	}
	ds, release, err := s.lock(ctx, req.GetId())
	if err != nil {
		return nil, err
	}
	defer release()
	all, err := s.rt.Databases(ctx, ds.ID)
	if err != nil {
		return nil, toStatus(err)
	}
	names, err := chosen(all, req.GetDatabases())
	if err != nil {
		return nil, err
	}
	d := backup.Details{Label: label, Created: time.Now(), Paths: names, JobID: req.GetJobId()}
	progress.Step(ctx, "dump", 0)
	b, err := s.dumps.Add(owner(ds.ID), cmp.Or(req.GetLocation(), storage.Default), noryxv1.NewBackupID(d.Created), d, ds.Size, func(w io.Writer) error {
		zw := zip.NewWriter(w)
		for _, name := range names {
			f, err := zw.CreateHeader(&zip.FileHeader{Name: name + ".sql", Method: zip.Deflate, Modified: d.Created})
			if err != nil {
				return err
			}
			if err := s.rt.Dump(ctx, ds.ID, name, f); err != nil {
				return fmt.Errorf("dump %s: %w", name, err)
			}
		}
		return zw.Close()
	})
	if err != nil {
		return nil, toStatus(err)
	}
	if req.GetJobId() != "" {
		if err := s.dumps.Prune(owner(ds.ID), req.GetJobId(), retention); err != nil {
			slog.Warn("Can't delete old dumps", logging.Databases, "datastore", ds.ID, "job", req.GetJobId(), "err", err)
		}
	}
	return &noryxv1.CreateDumpResponse{Dump: b.Proto()}, nil
}

// chosen returns the databases of those there are that a request chose, all for none.
func chosen(there, requested []string) ([]string, error) {
	if len(there) == 0 {
		return nil, status.Error(codes.FailedPrecondition, "There are no databases.")
	}
	if len(requested) == 0 {
		return there, nil
	}
	for _, name := range requested {
		if !slices.Contains(there, name) {
			return nil, status.Errorf(codes.NotFound, "There is no database %q.", name)
		}
	}
	return slices.Compact(slices.Sorted(slices.Values(requested))), nil
}

func (s *Service) ListDumps(ctx context.Context, req *noryxv1.ListDumpsRequest) (*noryxv1.ListDumpsResponse, error) {
	if _, err := s.find(ctx, req.GetId()); err != nil {
		return nil, err
	}
	dumps, err := s.dumps.List(owner(req.GetId()))
	if err != nil {
		return nil, toStatus(err)
	}
	res := &noryxv1.ListDumpsResponse{}
	for _, b := range dumps {
		res.Dumps = append(res.Dumps, b.Proto())
	}
	return res, nil
}

// RestoreDump creates databases again from a dump and loads it into them as their users. It
// reads the whole dump first, so that a damaged one leaves the databases as they are.
func (s *Service) RestoreDump(ctx context.Context, req *noryxv1.RestoreDumpRequest) (*noryxv1.RestoreDumpResponse, error) {
	ds, release, err := s.lock(ctx, req.GetId())
	if err != nil {
		return nil, err
	}
	defer release()
	b, err := s.dumps.Find(owner(ds.ID), req.GetDumpId())
	if err != nil {
		return nil, toStatus(err)
	}
	names, err := chosen(b.Paths, req.GetDatabases())
	if err != nil {
		return nil, err
	}
	zr, err := zip.OpenReader(b.Path())
	if err != nil {
		return nil, toStatus(err)
	}
	defer zr.Close()
	progress.Step(ctx, "check", 0)
	files := map[string]*zip.File{}
	for _, f := range zr.File {
		if name, ok := strings.CutSuffix(f.Name, ".sql"); ok && slices.Contains(names, name) {
			files[name] = f
			if err := verify(f); err != nil {
				return nil, status.Errorf(codes.DataLoss, "The dump of %s is damaged: %v", name, err)
			}
		}
	}
	progress.Step(ctx, "load", 0)
	for _, name := range names {
		f := files[name]
		if f == nil {
			return nil, status.Errorf(codes.DataLoss, "The dump lacks %s.", name)
		}
		r, err := f.Open()
		if err == nil {
			err = errors.Join(s.rt.Load(ctx, ds.ID, name, r), r.Close())
		}
		if err != nil {
			return nil, toStatus(fmt.Errorf("restore %s: %w", name, err))
		}
	}
	return &noryxv1.RestoreDumpResponse{}, nil
}

// verify reads a file of a ZIP archive to its end, which checks its checksum.
func verify(f *zip.File) error {
	r, err := f.Open()
	if err != nil {
		return err
	}
	_, err = io.Copy(io.Discard, r) //nolint:gosec // the agent wrote the dump, and nothing is kept
	return errors.Join(err, r.Close())
}

func (s *Service) DeleteDump(ctx context.Context, req *noryxv1.DeleteDumpRequest) (*noryxv1.DeleteDumpResponse, error) {
	if _, err := s.find(ctx, req.GetId()); err != nil {
		return nil, err
	}
	b, err := s.dumps.Find(owner(req.GetId()), req.GetDumpId())
	if err == nil {
		err = s.dumps.Remove(b)
	}
	return &noryxv1.DeleteDumpResponse{}, toStatus(err)
}

func (s *Service) DownloadDump(req *noryxv1.DownloadDumpRequest, stream noryxv1.DatastoreService_DownloadDumpServer) error {
	if _, err := s.find(stream.Context(), req.GetId()); err != nil {
		return err
	}
	b, err := s.dumps.Find(owner(req.GetId()), req.GetDumpId())
	if err != nil {
		return toStatus(err)
	}
	f, err := os.Open(b.Path())
	if err != nil {
		return toStatus(err)
	}
	defer f.Close()
	return backup.Send(f, b.Size, func(size int64, data []byte) error {
		return stream.Send(&noryxv1.DownloadDumpResponse{Size: size, Data: data})
	})
}

func (s *Service) ListTables(ctx context.Context, req *noryxv1.ListTablesRequest) (*noryxv1.ListTablesResponse, error) {
	ds, err := s.database(ctx, req.GetId(), req.GetDatabase())
	if err != nil {
		return nil, err
	}
	tables, err := s.rt.Tables(ctx, ds.ID, req.GetDatabase())
	return &noryxv1.ListTablesResponse{Tables: tables}, toStatus(err)
}

// BrowseTable returns rows of a table, which the runtime only reads.
func (s *Service) BrowseTable(ctx context.Context, req *noryxv1.BrowseTableRequest) (*noryxv1.BrowseTableResponse, error) {
	switch {
	case !noryxv1.TableName.MatchString(req.GetTable()) || req.GetSchema() != "" && !noryxv1.TableName.MatchString(req.GetSchema()):
		return nil, status.Error(codes.InvalidArgument, "Noryx only shows tables whose names have letters, digits, _, $ and -.")
	case req.GetSort() != "" && !noryxv1.TableName.MatchString(req.GetSort()):
		return nil, toStatus(runtime.ErrNoColumn)
	case req.GetFilter() != nil && req.GetFilter().Problem() != "":
		return nil, status.Error(codes.InvalidArgument, req.GetFilter().Problem())
	}
	ds, err := s.database(ctx, req.GetId(), req.GetDatabase())
	if err != nil {
		return nil, err
	}
	req.Limit = min(cmp.Or(req.GetLimit(), defaultRows), maxRows)
	res, err := s.rt.Browse(ctx, ds.ID, req)
	return res, toStatus(err)
}

// database finds a datastore with a database.
func (s *Service) database(ctx context.Context, id, name string) (runtime.Datastore, error) {
	if problem := noryxv1.DatabaseNameProblem(name); problem != "" {
		return runtime.Datastore{}, status.Error(codes.InvalidArgument, problem)
	}
	ds, err := s.find(ctx, id)
	if err != nil {
		return ds, err
	}
	names, err := s.rt.Databases(ctx, ds.ID)
	if err == nil && !slices.Contains(names, name) {
		err = status.Errorf(codes.NotFound, "There is no database %q.", name)
	}
	return ds, toStatus(err)
}

func (s *Service) find(ctx context.Context, id string) (runtime.Datastore, error) {
	if !runtime.ValidID(id) {
		return runtime.Datastore{}, status.Error(codes.InvalidArgument, "invalid datastore ID")
	}
	list, err := s.rt.ListDatastores(ctx)
	if err != nil {
		return runtime.Datastore{}, toStatus(err)
	}
	i := slices.IndexFunc(list, func(ds runtime.Datastore) bool { return ds.ID == id })
	if i < 0 {
		return runtime.Datastore{}, toStatus(runtime.ErrNotFound)
	}
	return list[i], nil
}

// reserve reserves a datastore for one change, dump or restore at a time.
func (s *Service) reserve(id string) (func(), error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.busy[id] {
		return nil, status.Error(codes.FailedPrecondition, "Another change, dump or restore of this datastore is in progress. Try again when it is done.")
	}
	s.busy[id] = true
	return func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		delete(s.busy, id)
	}, nil
}

// lock finds a datastore and reserves it.
func (s *Service) lock(ctx context.Context, id string) (runtime.Datastore, func(), error) {
	if !runtime.ValidID(id) {
		return runtime.Datastore{}, nil, status.Error(codes.InvalidArgument, "invalid datastore ID")
	}
	release, err := s.reserve(id)
	if err != nil {
		return runtime.Datastore{}, nil, err
	}
	ds, err := s.find(ctx, id)
	if err != nil {
		release()
		return ds, nil, err
	}
	return ds, release, nil
}

// known are the errors of datastores that the service answers with a code and a text. The
// runtime tells of a missing datastore as of a missing server.
var known = []grpcerr.Known{
	{Err: runtime.ErrNotFound, Code: codes.NotFound, Text: "Datastore not found."},
	{Err: runtime.ErrNoTable, Code: codes.NotFound, Text: "Table not found."},
	{Err: runtime.ErrNoColumn, Code: codes.InvalidArgument, Text: "The table has no such column."},
	{Err: runtime.ErrDatastoreNotRunning, Code: codes.FailedPrecondition, Text: "The datastore isn't ready. Start it, or look at its log for why it isn't."},
	{Err: fs.ErrNotExist, Code: codes.NotFound, Text: "Dump not found."},
}

func toStatus(err error) error { return grpcerr.Status(err, known...) }
