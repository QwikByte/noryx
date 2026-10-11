package datastore

import (
	"archive/zip"
	"bytes"
	"context"
	"os"
	"slices"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	"github.com/QwikByte/noryx/internal/agent/runtime"
	"github.com/QwikByte/noryx/internal/agent/runtime/runtimetest"
	"github.com/QwikByte/noryx/internal/agent/storage"
)

type fakeRuntime struct {
	*runtimetest.Datastores
	servers []runtime.Server
}

func (f *fakeRuntime) List(context.Context) ([]runtime.Server, error) { return f.servers, nil }

// fakeOverlay admits the clients it knows.
type fakeOverlay struct{ clients map[string][]string }

func (o *fakeOverlay) Admit(id string, _ uint32, clients, _ []string) (string, error) {
	o.clients[id] = clients
	return "10.213.0.3", nil
}

func (o *fakeOverlay) Dismiss(id string) error {
	delete(o.clients, id)
	return nil
}

const password = "abcdefghijklmnopqrstuvwxyz234567"

func newService(t *testing.T) (*Service, *fakeRuntime, *fakeOverlay, string) {
	t.Helper()
	return newServiceIn(t, t.TempDir())
}

// newServiceIn is newService with its default storage location in dir.
func newServiceIn(t *testing.T, dir string) (*Service, *fakeRuntime, *fakeOverlay, string) {
	t.Helper()
	rt := &fakeRuntime{Datastores: &runtimetest.Datastores{}, servers: []runtime.Server{{Spec: runtime.Spec{ID: "server", Name: "lobby", Port: 25565}}}}
	ov := &fakeOverlay{clients: map[string][]string{}}
	s := NewService(rt, storage.New(dir), ov)
	id := runtime.NewID()
	_, err := s.CreateDatastore(t.Context(), &noryxv1.CreateDatastoreRequest{Id: id, Engine: noryxv1.DatastoreEngine_DATASTORE_ENGINE_MARIADB, Version: "11.8", MemoryMb: 512})
	must(t, err)
	_, err = s.StartDatastore(t.Context(), &noryxv1.StartDatastoreRequest{Id: id})
	must(t, err)
	return s, rt, ov, id
}

func TestCreateChecks(t *testing.T) {
	s, _, _, id := newService(t)
	for _, req := range []*noryxv1.CreateDatastoreRequest{
		{Id: "../x", Engine: noryxv1.DatastoreEngine_DATASTORE_ENGINE_MARIADB, Version: "11.8", MemoryMb: 512},
		{Id: runtime.NewID(), Engine: noryxv1.DatastoreEngine_DATASTORE_ENGINE_MARIADB, Version: "10.6", MemoryMb: 512},
		{Id: runtime.NewID(), Engine: noryxv1.DatastoreEngine_DATASTORE_ENGINE_POSTGRES, Version: "18", MemoryMb: 64},
	} {
		if _, err := s.CreateDatastore(t.Context(), req); status.Code(err) != codes.InvalidArgument {
			t.Errorf("%v: %v", req, err)
		}
	}
	_, err := s.CreateDatastore(t.Context(), &noryxv1.CreateDatastoreRequest{Id: id, Engine: noryxv1.DatastoreEngine_DATASTORE_ENGINE_MARIADB, Version: "11.8", MemoryMb: 512})
	if status.Code(err) != codes.AlreadyExists {
		t.Errorf("created it twice: %v", err)
	}
	for _, name := range []string{"mysql", "postgres", "pg_x", "Shop", "shop;", ""} {
		if _, err := s.EnsureDatabase(t.Context(), &noryxv1.EnsureDatabaseRequest{Id: id, Name: name, Password: password}); status.Code(err) != codes.InvalidArgument {
			t.Errorf("database %q: %v", name, err)
		}
	}
	if _, err := s.EnsureDatabase(t.Context(), &noryxv1.EnsureDatabaseRequest{Id: id, Name: "shop", Password: "short"}); status.Code(err) != codes.InvalidArgument {
		t.Errorf("weak password: %v", err)
	}
	for _, name := range []string{"mysql", "sys", "postgres", "template1"} {
		if _, err := s.DropDatabase(t.Context(), &noryxv1.DropDatabaseRequest{Id: id, Name: name}); status.Code(err) != codes.InvalidArgument {
			t.Errorf("dropped %s: %v", name, err)
		}
	}
}

func TestDumpAndRestore(t *testing.T) {
	s, rt, _, id := newService(t)
	ctx := t.Context()
	for _, name := range []string{"shop", "perms"} {
		_, err := s.EnsureDatabase(ctx, &noryxv1.EnsureDatabaseRequest{Id: id, Name: name, Password: password})
		must(t, err)
		must(t, rt.Write(id, name, "data of "+name))
	}
	res, err := s.CreateDump(ctx, &noryxv1.CreateDumpRequest{Id: id, Label: "before"})
	must(t, err)
	if paths := res.GetDump().GetPaths(); !slices.Equal(paths, []string{"perms", "shop"}) {
		t.Errorf("dumped %v", paths)
	}
	if _, err := s.CreateDump(ctx, &noryxv1.CreateDumpRequest{Id: id, Databases: []string{"missing"}}); status.Code(err) != codes.NotFound {
		t.Errorf("dumped a missing database: %v", err)
	}

	must(t, rt.Write(id, "shop", "changed"))
	must(t, rt.Write(id, "perms", "changed"))
	_, err = s.RestoreDump(ctx, &noryxv1.RestoreDumpRequest{Id: id, DumpId: res.GetDump().GetId(), Databases: []string{"shop"}})
	must(t, err)
	if content, pw := rt.Content(id, "shop"); content != "data of shop" || pw != password {
		t.Errorf("restored %q with password %q", content, pw)
	}
	if content, _ := rt.Content(id, "perms"); content != "changed" {
		t.Errorf("restored perms too: %q", content)
	}

	// A damaged dump leaves the databases alone.
	b, err := s.dumps.Find(owner(id), res.GetDump().GetId())
	must(t, err)
	damage(t, b.Path())
	_, err = s.RestoreDump(ctx, &noryxv1.RestoreDumpRequest{Id: id, DumpId: res.GetDump().GetId()})
	if status.Code(err) != codes.DataLoss {
		t.Errorf("restored a damaged dump: %v", err)
	}
	if content, _ := rt.Content(id, "perms"); content != "changed" {
		t.Errorf("a damaged dump changed perms: %q", content)
	}

	_, err = s.DeleteDatastore(ctx, &noryxv1.DeleteDatastoreRequest{Id: id})
	must(t, err)
	if dumps, err := s.dumps.List(owner(id)); err != nil || len(dumps) > 0 {
		t.Errorf("dumps %v after deleting, %v", dumps, err)
	}
}

// damage flips a byte in the content of the first file of a ZIP archive.
func damage(t *testing.T, path string) {
	t.Helper()
	data, err := os.ReadFile(path) //nolint:gosec // in the test's directory
	must(t, err)
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	must(t, err)
	offset, err := zr.File[0].DataOffset()
	must(t, err)
	data[offset] ^= 0xff
	must(t, os.WriteFile(path, data, 0o600))
}

func TestPublish(t *testing.T) {
	s, rt, ov, id := newService(t)
	ctx := t.Context()
	if _, err := s.PublishDatastore(ctx, &noryxv1.PublishDatastoreRequest{Id: id, Port: 25565, Clients: []string{"10.213.0.1"}}); status.Code(err) != codes.AlreadyExists {
		t.Errorf("published the port of a server: %v", err)
	}
	res, err := s.PublishDatastore(ctx, &noryxv1.PublishDatastoreRequest{Id: id, Port: 25600, Clients: []string{"10.213.0.1", "10.213.0.2"}})
	must(t, err)
	list, _ := rt.ListDatastores(ctx)
	if res.GetAddress() != "10.213.0.3" || list[0].Port != 25600 || list[0].Overlay != "10.213.0.3" || len(ov.clients["db-"+id]) != 2 {
		t.Errorf("published at %q: %+v, clients %v", res.GetAddress(), list[0], ov.clients)
	}
	_, err = s.PublishDatastore(ctx, &noryxv1.PublishDatastoreRequest{Id: id})
	must(t, err)
	list, _ = rt.ListDatastores(ctx)
	if list[0].Port != 0 || ov.clients["db-"+id] != nil {
		t.Errorf("still published: %+v, clients %v", list[0], ov.clients)
	}
}

func TestUpgrade(t *testing.T) {
	s, _, _, id := newService(t)
	res, err := s.UpdateDatastore(t.Context(), &noryxv1.UpdateDatastoreRequest{Id: id, MemoryMb: 1024, Version: "12.3"})
	must(t, err)
	if ds := res.GetDatastore(); ds.GetVersion() != "12.3" || ds.GetPreviousVersion() != "11.8" || ds.GetMemoryMb() != 1024 {
		t.Errorf("after the upgrade: %v", ds)
	}
	res, err = s.UpdateDatastore(t.Context(), &noryxv1.UpdateDatastoreRequest{Id: id, MemoryMb: 1024, RemovePrevious: true})
	must(t, err)
	if res.GetDatastore().GetPreviousVersion() != "" {
		t.Errorf("kept the previous version: %v", res.GetDatastore())
	}
	if _, err := s.UpdateDatastore(t.Context(), &noryxv1.UpdateDatastoreRequest{Id: id, MemoryMb: 1024, Version: "13"}); status.Code(err) != codes.InvalidArgument {
		t.Errorf("moved to an unknown version: %v", err)
	}
}

// TestParameters checks the bounds of the parameters, also those of the memory, which the
// datastore keeps until they change.
func TestParameters(t *testing.T) {
	s, rt, _, id := newService(t)
	ctx := t.Context()
	for _, c := range []struct {
		params *noryxv1.DatastoreParameters
		ok     bool
	}{
		{&noryxv1.DatastoreParameters{MaxConnections: 9}, false},
		{&noryxv1.DatastoreParameters{MaxConnections: 10}, true},
		{&noryxv1.DatastoreParameters{MaxConnections: 512}, true}, // one for each MB on MariaDB
		{&noryxv1.DatastoreParameters{MaxConnections: 513}, false},
		{&noryxv1.DatastoreParameters{BufferMb: 31}, false},
		{&noryxv1.DatastoreParameters{BufferMb: 32}, true},
		{&noryxv1.DatastoreParameters{BufferMb: 384}, true}, // three quarters of the memory
		{&noryxv1.DatastoreParameters{BufferMb: 385}, false},
		{nil, true},
	} {
		_, err := s.UpdateDatastore(ctx, &noryxv1.UpdateDatastoreRequest{Id: id, MemoryMb: 512, Parameters: c.params})
		if c.ok && err != nil || !c.ok && status.Code(err) != codes.InvalidArgument {
			t.Errorf("%v: %v", c.params, err)
		}
	}
	params := &noryxv1.DatastoreParameters{MaxConnections: 600, BufferMb: 768}
	res, err := s.UpdateDatastore(ctx, &noryxv1.UpdateDatastoreRequest{Id: id, MemoryMb: 1024, Parameters: params})
	must(t, err)
	list, _ := rt.ListDatastores(ctx)
	if p := res.GetDatastore().GetParameters(); p.GetMaxConnections() != 600 || p.GetBufferMb() != 768 || list[0].MaxConnections != 600 || list[0].BufferMB != 768 {
		t.Errorf("changed to %v: %+v", p, list[0])
	}
	// Less memory needs fewer connections and smaller buffers.
	if _, err := s.UpdateDatastore(ctx, &noryxv1.UpdateDatastoreRequest{Id: id, MemoryMb: 512, Parameters: params}); status.Code(err) != codes.InvalidArgument {
		t.Errorf("kept them with less memory: %v", err)
	}

	// PostgreSQL takes a connection for each 2 MB, also when it is created.
	pg := &noryxv1.CreateDatastoreRequest{Id: runtime.NewID(), Engine: noryxv1.DatastoreEngine_DATASTORE_ENGINE_POSTGRES, Version: "18", MemoryMb: 512}
	pg.Parameters = &noryxv1.DatastoreParameters{MaxConnections: 257}
	if _, err := s.CreateDatastore(ctx, pg); status.Code(err) != codes.InvalidArgument {
		t.Errorf("created PostgreSQL with 257 connections: %v", err)
	}
	pg.Parameters = &noryxv1.DatastoreParameters{MaxConnections: 256, BufferMb: 64}
	created, err := s.CreateDatastore(ctx, pg)
	must(t, err)
	if p := created.GetDatastore().GetParameters(); p.GetMaxConnections() != 256 || p.GetBufferMb() != 64 {
		t.Errorf("created with %v", p)
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
