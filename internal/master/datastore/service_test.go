package datastore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	"github.com/QwikByte/noryx/internal/master/database"
	"github.com/QwikByte/noryx/internal/master/httpapi"
	"github.com/QwikByte/noryx/internal/master/network"
	"github.com/QwikByte/noryx/internal/master/node/nodetest"
	"github.com/QwikByte/noryx/internal/master/overlay"
)

// agent is the agent of a node that runs datastores, beside servers that use memory and ports
// of the node.
type agent struct {
	noryxv1.UnimplementedDatastoreServiceServer
	noryxv1.UnimplementedServerServiceServer
	noryxv1.UnimplementedNodeServiceServer

	mu         sync.Mutex
	datastores map[string]*noryxv1.Datastore
	passwords  map[string]string // of the users of databases, by datastore/database
	servers    []*noryxv1.Server
	memoryMB   uint64
	fail       map[string]error // makes calls fail, by method
	taken      map[uint32]bool  // ports that something else on the node uses
	published  []*noryxv1.PublishDatastoreRequest
	updated    []*noryxv1.UpdateDatastoreRequest
}

// do runs fn while nothing else uses the agent.
func (a *agent) do(fn func()) {
	a.mu.Lock()
	defer a.mu.Unlock()
	fn()
}

// failure returns the error of a call of method, or of a datastore that doesn't exist.
func (a *agent) failure(method, id string) error {
	if err := a.fail[method]; err != nil {
		return err
	}
	if _, ok := a.datastores[id]; !ok && method != "CreateDatastore" {
		return status.Error(codes.NotFound, "datastore not found")
	}
	return nil
}

func (a *agent) answer(id string) *noryxv1.Datastore {
	return proto.Clone(a.datastores[id]).(*noryxv1.Datastore)
}

func (a *agent) GetInfo(context.Context, *noryxv1.GetInfoRequest) (*noryxv1.GetInfoResponse, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return &noryxv1.GetInfoResponse{MemoryBytes: a.memoryMB << 20}, nil
}

func (a *agent) ListServers(context.Context, *noryxv1.ListServersRequest) (*noryxv1.ListServersResponse, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return &noryxv1.ListServersResponse{Servers: a.servers}, nil
}

func (a *agent) ListDatastores(context.Context, *noryxv1.ListDatastoresRequest) (*noryxv1.ListDatastoresResponse, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	res := &noryxv1.ListDatastoresResponse{Versions: []*noryxv1.DatastoreVersions{
		{Engine: noryxv1.DatastoreEngine_DATASTORE_ENGINE_MARIADB, Versions: []string{"11"}},
		{Engine: noryxv1.DatastoreEngine_DATASTORE_ENGINE_POSTGRES, Versions: []string{"16", "17"}},
	}}
	for id := range a.datastores {
		res.Datastores = append(res.Datastores, a.answer(id))
	}
	return res, nil
}

func (a *agent) CreateDatastore(_ context.Context, req *noryxv1.CreateDatastoreRequest) (*noryxv1.CreateDatastoreResponse, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.failure("CreateDatastore", req.GetId()); err != nil {
		return nil, err
	}
	a.datastores[req.GetId()] = &noryxv1.Datastore{
		Id: req.GetId(), Engine: req.GetEngine(), Version: req.GetVersion(), MemoryMb: req.GetMemoryMb(), CpuMillis: req.GetCpuMillis(),
		State: noryxv1.DatastoreState_DATASTORE_STATE_STOPPED, Parameters: req.GetParameters(),
	}
	return &noryxv1.CreateDatastoreResponse{Datastore: a.answer(req.GetId())}, nil
}

func (a *agent) StartDatastore(_ context.Context, req *noryxv1.StartDatastoreRequest) (*noryxv1.StartDatastoreResponse, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.failure("StartDatastore", req.GetId()); err != nil {
		return nil, err
	}
	a.datastores[req.GetId()].State = noryxv1.DatastoreState_DATASTORE_STATE_RUNNING
	return &noryxv1.StartDatastoreResponse{}, nil
}

func (a *agent) UpdateDatastore(_ context.Context, req *noryxv1.UpdateDatastoreRequest) (*noryxv1.UpdateDatastoreResponse, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.failure("UpdateDatastore", req.GetId()); err != nil {
		return nil, err
	}
	a.updated = append(a.updated, req)
	ds := a.datastores[req.GetId()]
	ds.MemoryMb, ds.CpuMillis, ds.Version, ds.Parameters = req.GetMemoryMb(), req.GetCpuMillis(), cmpOr(req.GetVersion(), ds.GetVersion()), req.GetParameters()
	return &noryxv1.UpdateDatastoreResponse{Datastore: a.answer(req.GetId())}, nil
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func (a *agent) DeleteDatastore(_ context.Context, req *noryxv1.DeleteDatastoreRequest) (*noryxv1.DeleteDatastoreResponse, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.failure("DeleteDatastore", req.GetId()); err != nil {
		return nil, err
	}
	delete(a.datastores, req.GetId())
	return &noryxv1.DeleteDatastoreResponse{}, nil
}

func (a *agent) EnsureDatabase(_ context.Context, req *noryxv1.EnsureDatabaseRequest) (*noryxv1.EnsureDatabaseResponse, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.failure("EnsureDatabase", req.GetId()); err != nil {
		return nil, err
	}
	a.passwords[req.GetId()+"/"+req.GetName()] = req.GetPassword()
	if ds := a.datastores[req.GetId()]; !slices.Contains(ds.GetDatabases(), req.GetName()) {
		ds.Databases = append(ds.Databases, req.GetName())
	}
	return &noryxv1.EnsureDatabaseResponse{}, nil
}

func (a *agent) DropDatabase(_ context.Context, req *noryxv1.DropDatabaseRequest) (*noryxv1.DropDatabaseResponse, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.failure("DropDatabase", req.GetId()); err != nil {
		return nil, err
	}
	delete(a.passwords, req.GetId()+"/"+req.GetName())
	ds := a.datastores[req.GetId()]
	ds.Databases = slices.DeleteFunc(ds.Databases, func(name string) bool { return name == req.GetName() })
	return &noryxv1.DropDatabaseResponse{}, nil
}

func (a *agent) PublishDatastore(_ context.Context, req *noryxv1.PublishDatastoreRequest) (*noryxv1.PublishDatastoreResponse, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.failure("PublishDatastore", req.GetId()); err != nil {
		return nil, err
	}
	if a.taken[req.GetPort()] {
		return nil, status.Errorf(codes.FailedPrecondition, "port %d is already in use on the node", req.GetPort())
	}
	a.published = append(a.published, req)
	return &noryxv1.PublishDatastoreResponse{}, nil
}

// master keeps the datastores of the networks net1 and net2. Its nodes n1, n2 and n3 have room
// for datastores beside their servers, and n4 doesn't answer.
// It is its own overlay, of members, and the networks, which it configures again.
type master struct {
	*Service
	db     *sql.DB
	agents map[string]*agent

	mu         sync.Mutex
	members    map[string]overlay.Member
	configured []string
}

func newMaster(t *testing.T) *master {
	t.Helper()
	db, err := database.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	nodes := nodetest.New(t, db)
	m := &master{db: db, agents: map[string]*agent{}}
	for _, id := range []string{"n1", "n2", "n3"} {
		a := &agent{
			datastores: map[string]*noryxv1.Datastore{}, passwords: map[string]string{}, memoryMB: 4096, fail: map[string]error{}, taken: map[uint32]bool{},
		}
		if id == "n1" { // 1536 MB, up to 25432
			a.servers = []*noryxv1.Server{{Id: "lobby", MemoryMb: 1024, Port: 25430, Type: noryxv1.ServerType_SERVER_TYPE_BEDROCK, MaxPlayers: 3}}
		}
		m.agents[id] = a
		nodes.Add(t, id, func(s *grpc.Server) {
			noryxv1.RegisterDatastoreServiceServer(s, a)
			noryxv1.RegisterServerServiceServer(s, a)
			noryxv1.RegisterNodeServiceServer(s, a)
		})
	}
	nodes.Add(t, "n4", nil)
	for _, id := range []string{"net1", "net2"} {
		if _, err := db.Exec(`INSERT INTO networks (id, name, proxy_node_id, proxy_server_id, forwarding_secret, created_at) VALUES (?, ?, 'n1', ?, 'secret', 0)`, id, id, id); err != nil {
			t.Fatal(err)
		}
	}
	m.Service = NewService(NewStore(db, nodes, m), nodes, m)
	return m
}

func (m *master) ByNode(context.Context) (map[string]overlay.Member, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.members, nil
}

func (m *master) Get(_ context.Context, id string) (network.Network, error) {
	if !strings.HasPrefix(id, "net") {
		return network.Network{}, httpapi.Errorf(http.StatusNotFound, "Network not found.")
	}
	return network.Network{ID: id}, nil
}

func (m *master) Configure(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.configured = append(m.configured, id)
	return nil
}

// create creates a datastore of net1 on a node, or fails the test.
func (m *master) create(t *testing.T, in Input) View {
	t.Helper()
	v, err := m.Create(t.Context(), "net1", in)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// stored returns a datastore as the master keeps it.
func (m *master) stored(t *testing.T, id string) Datastore {
	t.Helper()
	ds, err := m.store.get(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	return ds
}

// statusOf is the HTTP status of an error of the API, or 0.
func statusOf(err error) int {
	var apiErr *httpapi.Error
	if errors.As(err, &apiErr) {
		return apiErr.Status
	}
	return 0
}

// A new datastore runs the newest version of its engine that the agent has, unless another one
// is chosen, in the node's default storage location, and starts; the servers of the network join
// it then. It must fit the memory that the node's servers and datastores leave, and refused or
// failed ones are forgotten.
func TestCreate(t *testing.T) {
	m := newMaster(t)
	v := m.create(t, Input{Name: " main ", NodeID: "n1", Engine: "Postgres", MemoryMB: 512, CPUMillis: 1000, MaxConnections: 100})
	if v.Name != "main" || v.Engine != "postgres" || v.Version != "17" || v.Storage != "default" || v.State != "running" || v.NodeName != "n1" ||
		!slices.Equal(v.Endpoints, []Endpoint{{Host: "noryx-db-" + v.ID, Port: 5432}}) || len(v.Databases) != 0 || !slices.Equal(v.Versions, []string{"16", "17"}) {
		t.Fatalf("created %+v", v)
	}
	m.agents["n1"].do(func() {
		ds := m.agents["n1"].datastores[v.ID]
		if ds.GetMemoryMb() != 512 || ds.GetCpuMillis() != 1000 || ds.GetParameters().GetMaxConnections() != 100 || ds.GetEngine() != noryxv1.DatastoreEngine_DATASTORE_ENGINE_POSTGRES {
			t.Errorf("the agent created %v", ds)
		}
	})
	if !slices.Equal(m.configured, []string{"net1"}) {
		t.Errorf("networks configured again: %q", m.configured)
	}

	for _, tc := range []struct {
		name    string
		network string
		in      Input
		status  int
		want    string
	}{
		{"an invalid name", "net1", Input{Name: "Main", NodeID: "n1", Engine: "mariadb", MemoryMB: 512}, http.StatusBadRequest, "as name"},
		{"an unknown engine", "net1", Input{Name: "cache", NodeID: "n1", Engine: "mysql", MemoryMB: 512}, http.StatusBadRequest, "Choose MariaDB or PostgreSQL."},
		{"little memory", "net1", Input{Name: "cache", NodeID: "n1", Engine: "mariadb", MemoryMB: 128}, http.StatusBadRequest, "at least 256 MB"},
		{"few connections", "net1", Input{Name: "cache", NodeID: "n1", Engine: "mariadb", MemoryMB: 512, MaxConnections: 5}, http.StatusBadRequest, "allow from 10 to 512 connections"},
		{"an unknown network", "nope", Input{Name: "cache", NodeID: "n1", Engine: "mariadb", MemoryMB: 512}, http.StatusNotFound, "Network not found."},
		{"an unknown node", "net1", Input{Name: "cache", NodeID: "n9", Engine: "mariadb", MemoryMB: 512}, http.StatusNotFound, "Node not found."},
		{"an unknown version", "net1", Input{Name: "cache", NodeID: "n1", Engine: "postgres", Version: "15", MemoryMB: 512}, http.StatusBadRequest,
			"The agent of n1 runs postgres 16 and 17."},
		// 4096 MB, 1024 kept free, 1536 for the server and 512 for main.
		{"too much memory", "net1", Input{Name: "cache", NodeID: "n1", Engine: "mariadb", MemoryMB: 1100}, http.StatusConflict,
			"n1 has room for a datastore with up to 1024 MB of memory."},
		{"a taken name", "net1", Input{Name: "main", NodeID: "n2", Engine: "mariadb", MemoryMB: 512}, http.StatusConflict, `The network has a datastore named "main" already.`},
	} {
		if _, err := m.Create(t.Context(), tc.network, tc.in); statusOf(err) != tc.status || !strings.Contains(httpapi.Message(err), tc.want) {
			t.Errorf("%s: %v", tc.name, err)
		}
	}
	// The same name in another network, and memory on a node without a limit.
	if _, err := m.db.Exec(`UPDATE nodes SET memory_reserve_mb = NULL WHERE id = 'n1'`); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Create(t.Context(), "net2", Input{Name: "main", NodeID: "n1", Engine: "mariadb", MemoryMB: 8192}); err != nil {
		t.Fatal(err)
	}

	n2 := m.agents["n2"]
	n2.do(func() { n2.fail["CreateDatastore"] = status.Error(codes.Internal, "the image can't be pulled") })
	if _, err := m.Create(t.Context(), "net1", Input{Name: "cache", NodeID: "n2", Engine: "mariadb", MemoryMB: 512}); httpapi.Message(err) != "the image can't be pulled" {
		t.Fatalf("Create on a failing agent = %v", err)
	}
	n2.do(func() {
		n2.fail = map[string]error{"StartDatastore": status.Error(codes.Internal, "the port is in use")}
	})
	if _, err := m.Create(t.Context(), "net1", Input{Name: "cache", NodeID: "n2", Engine: "mariadb", MemoryMB: 512}); httpapi.Message(err) != "the port is in use" {
		t.Fatalf("Create of a datastore that doesn't start = %v", err)
	}
	// One that the agent created stays, and can be started later.
	if list, err := m.List(t.Context(), "net1"); err != nil || len(list) != 2 || list[0].Name != "cache" || list[0].State != "stopped" {
		t.Fatalf("datastores of net1 %+v, %v", list, err)
	}

	n2.do(func() { n2.fail = map[string]error{} })
	v, err := m.Create(t.Context(), "net1", Input{Name: "tuned", NodeID: "n2", Engine: "mariadb", MemoryMB: 512, BufferMB: 256})
	if err != nil || v.BufferMB != 256 || m.stored(t, v.ID).BufferMB != 256 {
		t.Fatalf("Create with buffers = %+v, %v", v, err)
	}

	for i := range noryxv1.MaxNetworkDatastores - 3 {
		if _, err := m.db.Exec(`INSERT INTO datastores (id, network_id, node_id, name, engine, version, memory_mb, cpu_millis, storage, created_at)
			VALUES (?, 'net1', 'n2', ?, 'mariadb', '11', 512, 0, 'default', 0)`, fmt.Sprint("d", i), fmt.Sprint("d", i)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := m.Create(t.Context(), "net1", Input{Name: "more", NodeID: "n2", Engine: "mariadb", MemoryMB: 512}); statusOf(err) != http.StatusConflict {
		t.Fatalf("Create beyond the limit = %v", err)
	}
}

// A change gives a datastore other limits and parameters, another version or a pulled image. It
// may only take memory that is left on its node, but can always get less; what its agent refuses
// changes nothing.
func TestUpdate(t *testing.T) {
	m := newMaster(t)
	n1 := m.agents["n1"]
	v := m.create(t, Input{Name: "main", NodeID: "n1", Engine: "postgres", Version: "16", MemoryMB: 512, CPUMillis: 1000, MaxConnections: 100})
	updated, err := m.Update(t.Context(), v.ID, Change{MemoryMB: 1024})
	if err != nil || updated.MemoryMB != 1024 || updated.CPUMillis != 1000 || updated.MaxConnections != 100 || updated.Version != "16" {
		t.Fatalf("Update of the memory = %+v, %v", updated, err)
	}
	for _, tc := range []struct {
		name   string
		change Change
		status int
		want   string
	}{
		{"too much memory", Change{MemoryMB: 1600}, http.StatusConflict, "n1 has room for a datastore with up to 1536 MB of memory."},
		{"little memory", Change{MemoryMB: 128}, http.StatusBadRequest, "at least 256 MB"},
		{"large buffers", Change{BufferMB: new(uint32(800))}, http.StatusBadRequest, "give the buffers from 32 to 768 MB"},
	} {
		if _, err := m.Update(t.Context(), v.ID, tc.change); statusOf(err) != tc.status || !strings.Contains(httpapi.Message(err), tc.want) {
			t.Errorf("%s: %v", tc.name, err)
		}
	}
	updated, err = m.Update(t.Context(), v.ID, Change{CPUMillis: new(uint32(0)), MaxConnections: new(uint32(0)), BufferMB: new(uint32(256)), Version: "17", UpdateImage: true})
	if err != nil || updated.CPUMillis != 0 || updated.MaxConnections != 0 || updated.BufferMB != 256 || updated.Version != "17" || updated.MemoryMB != 1024 {
		t.Fatalf("Update = %+v, %v", updated, err)
	}
	n1.do(func() {
		req := n1.updated[len(n1.updated)-1]
		if !req.GetUpdateImage() || req.GetVersion() != "17" || req.GetMemoryMb() != 1024 || req.GetParameters().GetBufferMb() != 256 || len(n1.updated) != 2 {
			t.Errorf("the agent was asked %v", req)
		}
		// The node is full now, but the datastore can always get less memory.
		n1.memoryMB = 1024
		n1.fail["UpdateDatastore"] = status.Error(codes.Internal, "the container can't be created")
	})
	if _, err := m.Update(t.Context(), v.ID, Change{MemoryMB: 512}); httpapi.Message(err) != "the container can't be created" || m.stored(t, v.ID).MemoryMB != 1024 {
		t.Fatalf("Update on a failing agent = %v", err)
	}
	n1.do(func() { n1.fail = map[string]error{} })
	updated, err = m.Update(t.Context(), v.ID, Change{MemoryMB: 512})
	if err != nil || updated.MemoryMB != 512 || updated.BufferMB != 256 || m.stored(t, v.ID).MemoryMB != 512 {
		t.Fatalf("Update to less memory on a full node = %+v, %v", updated, err)
	}
	if _, err := m.Update(t.Context(), "nope", Change{}); !errors.Is(err, errNotFound) {
		t.Errorf("Update of an unknown datastore = %v", err)
	}
}

// Deleting a datastore deletes it on its node and forgets it, also if the node lost it already,
// but not if the node can't delete it.
func TestDelete(t *testing.T) {
	m := newMaster(t)
	n1 := m.agents["n1"]
	main := m.create(t, Input{Name: "main", NodeID: "n1", Engine: "mariadb", MemoryMB: 512})
	lost := m.create(t, Input{Name: "lost", NodeID: "n1", Engine: "mariadb", MemoryMB: 256})
	n1.do(func() {
		delete(n1.datastores, lost.ID)
		n1.fail["DeleteDatastore"] = status.Error(codes.Internal, "the data can't be removed")
	})
	if err := m.Delete(t.Context(), main.ID); httpapi.Message(err) != "the data can't be removed" {
		t.Fatalf("Delete on a failing agent = %v", err)
	}
	n1.do(func() { n1.fail = map[string]error{} })
	for _, id := range []string{main.ID, lost.ID} {
		if err := m.Delete(t.Context(), id); err != nil {
			t.Fatal(err)
		}
	}
	if list, err := m.List(t.Context(), ""); err != nil || len(list) != 0 {
		t.Fatalf("datastores after deleting them: %+v, %v", list, err)
	}
	n1.do(func() {
		if len(n1.datastores) != 0 {
			t.Errorf("the agent runs %v", n1.datastores)
		}
	})
	if err := m.Delete(t.Context(), main.ID); !errors.Is(err, errNotFound) {
		t.Errorf("Delete again = %v", err)
	}
}

// A database gets a user with a password that only the master and the datastore know. Adding
// it again creates it again with the same password, e.g. after the datastore's data was
// replaced; a new one that the agent can't create is forgotten. A new password replaces the old
// one only once the user has it.
func TestDatabases(t *testing.T) {
	m := newMaster(t)
	n1 := m.agents["n1"]
	ds := m.create(t, Input{Name: "main", NodeID: "n1", Engine: "postgres", MemoryMB: 512})
	password := func(name string) (master, agent string) {
		master, err := m.Password(t.Context(), ds.ID, name)
		if err != nil {
			t.Fatalf("password of %s: %v", name, err)
		}
		n1.do(func() { agent = n1.passwords[ds.ID+"/"+name] })
		return master, agent
	}
	db, err := m.AddDatabase(t.Context(), ds.ID, "luckperms")
	first, onAgent := password("luckperms")
	if err != nil || db.Name != "luckperms" || !noryxv1.DatabasePassword.MatchString(first) || onAgent != first {
		t.Fatalf("AddDatabase = %+v, %v, password %q on the master, %q on the agent", db, err, first, onAgent)
	}
	n1.do(func() { n1.passwords = map[string]string{} }) // the datastore lost its data
	if _, err := m.AddDatabase(t.Context(), ds.ID, "luckperms"); err != nil {
		t.Fatal(err)
	}
	if again, onAgent := password("luckperms"); again != first || onAgent != first {
		t.Fatalf("password after adding the database again: %q, on the agent %q", again, onAgent)
	}

	for name, want := range map[string]int{"LuckPerms": http.StatusBadRequest, "postgres": http.StatusBadRequest, "pg_stats": http.StatusBadRequest} {
		if _, err := m.AddDatabase(t.Context(), ds.ID, name); statusOf(err) != want {
			t.Errorf("AddDatabase(%q) = %v", name, err)
		}
	}
	n1.do(func() { n1.fail["EnsureDatabase"] = status.Error(codes.Internal, "the datastore isn't running") })
	if _, err := m.AddDatabase(t.Context(), ds.ID, "coreprotect"); httpapi.Message(err) != "the datastore isn't running" {
		t.Fatalf("AddDatabase on a failing agent = %v", err)
	}
	if _, err := m.Password(t.Context(), ds.ID, "coreprotect"); statusOf(err) != http.StatusNotFound {
		t.Errorf("a database the agent didn't create was kept: %v", err)
	}
	// Rotating keeps the password the user has if the agent can't set the new one.
	if err := m.Rotate(t.Context(), ds.ID, "luckperms"); httpapi.Message(err) != "the datastore isn't running" {
		t.Fatalf("Rotate on a failing agent = %v", err)
	}
	if kept, _ := password("luckperms"); kept != first {
		t.Fatalf("password after a failed rotation %q, want %q", kept, first)
	}
	n1.do(func() { n1.fail = map[string]error{} })
	if err := m.Rotate(t.Context(), ds.ID, "luckperms"); err != nil {
		t.Fatal(err)
	}
	if rotated, onAgent := password("luckperms"); rotated == first || rotated != onAgent || !noryxv1.DatabasePassword.MatchString(rotated) {
		t.Fatalf("password after rotating %q, on the agent %q", rotated, onAgent)
	}
	if err := m.Rotate(t.Context(), ds.ID, "nope"); statusOf(err) != http.StatusNotFound {
		t.Errorf("Rotate of an unknown database = %v", err)
	}

	n1.do(func() { n1.fail["DropDatabase"] = status.Error(codes.Internal, "the database is in use") })
	if err := m.DropDatabase(t.Context(), ds.ID, "luckperms"); httpapi.Message(err) != "the database is in use" {
		t.Fatalf("DropDatabase on a failing agent = %v", err)
	}
	n1.do(func() { n1.fail = map[string]error{} })
	if err := m.DropDatabase(t.Context(), ds.ID, "luckperms"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Password(t.Context(), ds.ID, "luckperms"); statusOf(err) != http.StatusNotFound {
		t.Errorf("a dropped database was kept: %v", err)
	}
	n1.do(func() {
		if len(n1.passwords) != 0 {
			t.Errorf("the agent keeps %q", n1.passwords)
		}
	})

	for i := range noryxv1.MaxDatabases {
		if err := m.store.addDatabase(t.Context(), ds.ID, fmt.Sprint("db", i), "password"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := m.AddDatabase(t.Context(), ds.ID, "more"); statusOf(err) != http.StatusConflict {
		t.Errorf("AddDatabase beyond the limit = %v", err)
	}
}

// The master publishes a datastore in the private network for the other nodes of its network
// that are members, with their keys, and closes it for others. It gets a free port of its node
// the first time, which it keeps: from the top of the node's port range, or else from the base
// of its engine, past the ports of servers, of other datastores and those that the agent finds
// in use.
func TestPublish(t *testing.T) {
	m := newMaster(t)
	n1, n2 := m.agents["n1"], m.agents["n2"]
	main := m.create(t, Input{Name: "main", NodeID: "n1", Engine: "postgres", MemoryMB: 512})
	cache := m.create(t, Input{Name: "cache", NodeID: "n2", Engine: "mariadb", MemoryMB: 512})
	members := map[string]overlay.Member{"n1": {Address: "10.213.0.1", PublicKey: "key-1"}, "n2": {Address: "10.213.0.2", PublicKey: "key-2"}, "n3": {Address: "10.213.0.3", PublicKey: "key-3"}}
	m.mu.Lock()
	m.members = members
	m.mu.Unlock()
	if _, err := m.db.Exec(`UPDATE nodes SET port_min = 30000, port_max = 30003 WHERE id = 'n2'`); err != nil {
		t.Fatal(err)
	}
	n1.do(func() { n1.taken[25433] = true })
	n2.do(func() { n2.servers = []*noryxv1.Server{{Id: "hub", Port: 30003}} })
	published := func(a *agent) (got []string) {
		a.do(func() {
			for _, req := range a.published {
				got = append(got, fmt.Sprintf("%d %q %q", req.GetPort(), req.GetClients(), req.GetClientKeys()))
			}
			a.published = nil
		})
		return got
	}
	if err := m.store.Publish(t.Context(), "net1", []string{"n1", "n2", "n3", "n2", "n4"}, members); err != nil {
		t.Fatal(err)
	}
	// The server of n1 uses 25430 to 25432 for its players.
	if got := published(n1); !slices.Equal(got, []string{`25434 ["10.213.0.2" "10.213.0.3"] ["key-2" "key-3"]`}) || m.stored(t, main.ID).Port != 25434 {
		t.Errorf("published main: %q, port %d", got, m.stored(t, main.ID).Port)
	}
	if got := published(n2); !slices.Equal(got, []string{`30002 ["10.213.0.1" "10.213.0.3"] ["key-1" "key-3"]`}) || m.stored(t, cache.ID).Port != 30002 {
		t.Errorf("published cache: %q, port %d", got, m.stored(t, cache.ID).Port)
	}
	views, err := m.List(t.Context(), "net1")
	if err != nil || !slices.Equal(views[1].Endpoints, []Endpoint{{Host: "noryx-db-" + main.ID, Port: 5432}, {Host: "10.213.0.1", Port: 25434, Remote: true}}) {
		t.Errorf("endpoints of main: %+v, %v", views, err)
	}

	// A datastore keeps its port; without other nodes, it is closed.
	if err := m.store.Publish(t.Context(), "net1", []string{"n1", "n2"}, members); err != nil {
		t.Fatal(err)
	}
	if got := published(n1); !slices.Equal(got, []string{`25434 ["10.213.0.2"] ["key-2"]`}) {
		t.Errorf("published main again: %q", got)
	}
	if err := m.store.Publish(t.Context(), "net1", []string{"n1"}, members); err != nil {
		t.Fatal(err)
	}
	if got := published(n1); !slices.Equal(got, []string{`0 [] []`}) || m.stored(t, main.ID).Port != 25434 {
		t.Errorf("closed main: %q, port %d", got, m.stored(t, main.ID).Port)
	}

	// The ports of the range that the server and the datastore leave are in use otherwise.
	other := m.create(t, Input{Name: "other", NodeID: "n2", Engine: "mariadb", MemoryMB: 256})
	n2.do(func() { n2.taken = map[uint32]bool{30001: true, 30000: true} })
	if err := m.store.Publish(t.Context(), "net1", []string{"n1", "n2"}, members); statusOf(err) != http.StatusConflict ||
		!strings.HasPrefix(err.Error(), "other: n2 has no free port for the datastore other.") || m.stored(t, other.ID).Port != 0 {
		t.Errorf("Publish without a free port = %v", err)
	}
	if _, err := m.db.Exec(`UPDATE nodes SET port_min = 29000 WHERE id = 'n2'`); err != nil {
		t.Fatal(err)
	}
	if err := m.store.Publish(t.Context(), "net1", []string{"n1", "n2"}, members); err != nil {
		t.Fatal(err)
	}
	if got := m.stored(t, other.ID).Port; got != 29999 {
		t.Errorf("port of other %d", got)
	}
}

// A datastore shows what its agent reports, and why it can't if its node doesn't answer; backup
// jobs find the datastores that exist with their nodes.
func TestList(t *testing.T) {
	m := newMaster(t)
	main := m.create(t, Input{Name: "main", NodeID: "n1", Engine: "mariadb", MemoryMB: 512})
	if _, err := m.db.Exec(`INSERT INTO datastores (id, network_id, node_id, name, engine, version, memory_mb, cpu_millis, storage, created_at)
		VALUES ('away', 'net2', 'n4', 'away', 'mariadb', '11', 512, 0, 'default', 0), ('gone', 'net2', 'n2', 'gone', 'mariadb', '11', 512, 0, 'default', 0)`); err != nil {
		t.Fatal(err)
	}
	all, err := m.List(t.Context(), "")
	if err != nil || len(all) != 3 {
		t.Fatalf("List = %+v, %v", all, err)
	}
	for _, v := range all {
		switch {
		case v.ID == main.ID && (v.State != "running" || v.Problem != ""),
			v.ID == "away" && (v.State != "unknown" || v.Problem != "n4 can't be reached. Check that its agent is running and that the master can connect to it." || v.NodeName != "n4"),
			v.ID == "gone" && (v.State != "unknown" || !strings.Contains(v.Problem, "no such datastore")):
			t.Errorf("view %+v", v)
		}
	}
	found, err := m.store.Locate(t.Context(), []string{"gone", main.ID, "deleted"})
	if err != nil || len(found) != 2 || found[0].NodeName != "n2" || found[1].Name != "main" {
		t.Errorf("Locate = %+v, %v", found, err)
	}
}
