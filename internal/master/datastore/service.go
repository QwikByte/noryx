// Package datastore keeps the datastores of networks: MariaDB and PostgreSQL servers that the
// agent of a node runs, with databases for the plugins of the network's servers. The master
// keeps the password of each database's user, which only those who manage datastores see, to
// enter it into the configuration of the plugins; the superuser's password never leaves the
// node. They can also look into the tables of a database, which the agent only reads, and upload
// dumps made elsewhere, which the agent checks and loads like its own.
package datastore

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/base32"
	"errors"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	"github.com/QwikByte/noryx/internal/master/httpapi"
	"github.com/QwikByte/noryx/internal/master/network"
	"github.com/QwikByte/noryx/internal/master/node"
	"github.com/QwikByte/noryx/internal/master/operation"
)

const (
	queryTimeout  = 10 * time.Second
	actionTimeout = 3 * time.Minute
	minMemoryMB   = 256
)

// Datastore is a MariaDB or PostgreSQL server of a network on a node.
type Datastore struct {
	ID        string `json:"id"`
	NetworkID string `json:"networkId"`
	NodeID    string `json:"nodeId"`
	Name      string `json:"name"`
	Engine    string `json:"engine"` // mariadb or postgres
	Version   string `json:"version"`
	MemoryMB  uint32 `json:"memoryMb"`
	CPUMillis uint32 `json:"cpuMillis"`
	Storage   string `json:"storage"`
	// Port is published at the node's address in the private network of the nodes, for the
	// network's servers on other nodes; 0 until one needs it.
	Port      uint32     `json:"port"`
	CreatedAt time.Time  `json:"createdAt"`
	Databases []Database `json:"databases"`
	// MaxConnections and BufferMB tune its engine, each 0 for its default, as
	// noryxv1.DatastoreParameters describes them.
	MaxConnections uint32 `json:"maxConnections"`
	BufferMB       uint32 `json:"bufferMb"`
}

// parameters returns the parameters of its engine.
func (ds Datastore) parameters() *noryxv1.DatastoreParameters {
	return &noryxv1.DatastoreParameters{MaxConnections: ds.MaxConnections, BufferMb: ds.BufferMB}
}

// Database is a database of a datastore, with a user of the same name.
type Database struct {
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"createdAt"`
	password  string
}

// View is a datastore as its agent reports it.
type View struct {
	Datastore
	NodeName string `json:"nodeName"`
	State    string `json:"state"` // stopped, starting, running, unhealthy, or unknown if its node can't be reached
	Size     int64  `json:"size"`
	// Previous is the version whose data an upgrade left, until it is removed.
	Previous string `json:"previous,omitempty"`
	// Versions are the major versions of its engine that the agent runs, oldest first.
	Versions []string `json:"versions"`
	// Missing are its databases that the datastore lacks, e.g. after its data was replaced.
	Missing []string `json:"missing"`
	Problem string   `json:"problem,omitempty"`
	// Endpoints are where the servers of the network reach it.
	Endpoints []Endpoint `json:"endpoints"`
	// Warning tells what a creation or change left out, e.g. on an agent of an older version.
	Warning string `json:"warning,omitempty"`
}

// Endpoint is where servers reach a datastore: those on its node by the name of its
// container, those of other nodes at its node's address in the private network of the nodes.
type Endpoint struct {
	Host   string `json:"host"`
	Port   uint32 `json:"port"`
	Remote bool   `json:"remote"` // for the servers of other nodes
}

// Input is a new datastore.
type Input struct {
	Name      string `json:"name"`
	NodeID    string `json:"nodeId"`
	Engine    string `json:"engine"`
	Version   string `json:"version"` // empty for the newest
	MemoryMB  uint32 `json:"memoryMb"`
	CPUMillis uint32 `json:"cpuMillis"`
	Storage   string `json:"storage"`
	// Each 0 for its default.
	MaxConnections uint32 `json:"maxConnections"`
	BufferMB       uint32 `json:"bufferMb"`
}

// Change changes a datastore: its limits and parameters, its image or major version, or the
// data of the previous version.
type Change struct {
	MemoryMB       uint32  `json:"memoryMb"` // 0 keeps it
	CPUMillis      *uint32 `json:"cpuMillis"`
	Version        string  `json:"version"`
	UpdateImage    bool    `json:"updateImage"`
	RemovePrevious bool    `json:"removePrevious"`
	// Each 0 for its default; nil keeps it.
	MaxConnections *uint32 `json:"maxConnections"`
	BufferMB       *uint32 `json:"bufferMb"`
}

// Networks are the networks of the datastores.
type Networks interface {
	Get(ctx context.Context, id string) (network.Network, error)
	// Configure configures the servers of a network again, which join its datastores.
	Configure(ctx context.Context, id string) error
}

type Service struct {
	store    *Store
	nodes    Nodes
	networks Networks
	mu       sync.Mutex // one new datastore or database at a time, as their number is limited
}

func NewService(store *Store, nodes Nodes, networks Networks) *Service {
	return &Service{store: store, nodes: nodes, networks: networks}
}

var engines = map[string]noryxv1.DatastoreEngine{
	"mariadb":  noryxv1.DatastoreEngine_DATASTORE_ENGINE_MARIADB,
	"postgres": noryxv1.DatastoreEngine_DATASTORE_ENGINE_POSTGRES,
}

// List returns the datastores of a network, or all if networkID is empty, as their agents
// report them.
func (s *Service) List(ctx context.Context, networkID string) ([]View, error) {
	list, err := s.store.list(ctx, networkID)
	if err != nil {
		return nil, err
	}
	nodes, err := s.nodes.List(ctx)
	if err != nil {
		return nil, err
	}
	members, err := s.store.overlay.ByNode(ctx)
	if err != nil {
		return nil, err
	}
	reports := map[string]*noryxv1.ListDatastoresResponse{}
	errs := map[string]error{}
	asked := map[string]bool{} // only this goroutine uses it, the others write the reports
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, ds := range list {
		if !asked[ds.NodeID] {
			asked[ds.NodeID] = true
			wg.Go(func() {
				res, err := s.ask(ctx, ds.NodeID)
				mu.Lock()
				defer mu.Unlock()
				reports[ds.NodeID], errs[ds.NodeID] = res, err
			})
		}
	}
	wg.Wait()
	views := make([]View, len(list))
	for i, ds := range list {
		views[i] = view(ds, reports[ds.NodeID], errs[ds.NodeID])
		views[i].Endpoints = endpoints(ds, members[ds.NodeID].Address)
		if j := slices.IndexFunc(nodes, func(n node.Node) bool { return n.ID == ds.NodeID }); j >= 0 {
			views[i].NodeName = nodes[j].Name
		}
	}
	return views, nil
}

func (s *Service) ask(ctx context.Context, nodeID string) (*noryxv1.ListDatastoresResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()
	conn, err := s.nodes.Conn(ctx, nodeID)
	if err != nil {
		return nil, err
	}
	return noryxv1.NewDatastoreServiceClient(conn).ListDatastores(ctx, &noryxv1.ListDatastoresRequest{})
}

// datastorePrefix names the containers of datastores, which the agent creates, and
// enginePort is the port of the engines in them.
const datastorePrefix = "noryx-db-"

var enginePort = map[string]uint32{"mariadb": 3306, "postgres": 5432}

// endpoints returns where the servers of a datastore's network reach it: from other nodes
// only once it has a port and its node is in the private network of the nodes.
func endpoints(ds Datastore, address string) []Endpoint {
	list := []Endpoint{{Host: datastorePrefix + ds.ID, Port: enginePort[ds.Engine]}}
	if ds.Port != 0 && address != "" {
		list = append(list, Endpoint{Host: address, Port: ds.Port, Remote: true})
	}
	return list
}

// view combines a datastore with what its agent reports.
func view(ds Datastore, res *noryxv1.ListDatastoresResponse, err error) View {
	v := View{Datastore: ds, State: "unknown", Versions: []string{}, Missing: []string{}}
	for _, versions := range res.GetVersions() {
		if versions.GetEngine() == engines[ds.Engine] {
			v.Versions = versions.GetVersions()
		}
	}
	i := slices.IndexFunc(res.GetDatastores(), func(d *noryxv1.Datastore) bool { return d.GetId() == ds.ID })
	switch {
	case err != nil:
		v.Problem = httpapi.Message(err)
		return v
	case i < 0:
		v.Problem = "The node has no such datastore. Delete it, or restore the node's data."
		return v
	}
	d := res.GetDatastores()[i]
	v.State, v.Size, v.Previous = d.GetState().Slug(), d.GetSize(), d.GetPreviousVersion()
	if d.GetState() == noryxv1.DatastoreState_DATASTORE_STATE_RUNNING {
		for _, db := range ds.Databases {
			if !slices.Contains(d.GetDatabases(), db.Name) {
				v.Missing = append(v.Missing, db.Name)
			}
		}
	}
	return v
}

// Create creates a datastore of a network on a node and starts it, and configures the servers
// of the network to join it.
func (s *Service) Create(ctx context.Context, networkID string, in Input) (View, error) {
	in.Name, in.Engine = strings.TrimSpace(in.Name), strings.ToLower(in.Engine)
	engine, ok := engines[in.Engine]
	switch problem := (&noryxv1.DatastoreParameters{MaxConnections: in.MaxConnections, BufferMb: in.BufferMB}).Problem(engine, in.MemoryMB); {
	case !noryxv1.DatastoreName.MatchString(in.Name):
		return View{}, httpapi.Errorf(http.StatusBadRequest, "Use up to 32 lower-case letters, digits and -, starting with a letter or digit, as name.")
	case !ok:
		return View{}, httpapi.Errorf(http.StatusBadRequest, "Choose MariaDB or PostgreSQL.")
	case in.MemoryMB < minMemoryMB:
		return View{}, httpapi.Errorf(http.StatusBadRequest, "Give the datastore at least %d MB of memory.", minMemoryMB)
	case problem != "":
		return View{}, httpapi.Errorf(http.StatusBadRequest, "%s", problem)
	}
	if _, err := s.networks.Get(ctx, networkID); err != nil {
		return View{}, err
	}
	n, err := s.nodes.Get(ctx, in.NodeID)
	if err != nil {
		return View{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	existing, err := s.store.list(ctx, networkID)
	if err != nil {
		return View{}, err
	}
	if len(existing) >= noryxv1.MaxNetworkDatastores {
		return View{}, httpapi.Errorf(http.StatusConflict, "A network has at most %d datastores.", noryxv1.MaxNetworkDatastores)
	}
	res, err := s.ask(ctx, n.ID)
	if status.Code(err) == codes.Unimplemented {
		return View{}, httpapi.Errorf(http.StatusNotImplemented, "Update the agent of %s to run datastores.", n.Name)
	}
	if err != nil {
		return View{}, err
	}
	i := slices.IndexFunc(res.GetVersions(), func(v *noryxv1.DatastoreVersions) bool { return v.GetEngine() == engine })
	var versions []string
	if i >= 0 {
		versions = res.GetVersions()[i].GetVersions()
	}
	if in.Version == "" && len(versions) > 0 {
		in.Version = versions[len(versions)-1]
	}
	if !slices.Contains(versions, in.Version) {
		return View{}, httpapi.Errorf(http.StatusBadRequest, "The agent of %s runs %s %s.", n.Name, in.Engine, strings.Join(versions, " and "))
	}
	if err := s.checkMemory(ctx, n, "", in.MemoryMB); err != nil {
		return View{}, err
	}
	ds := Datastore{
		ID: strings.ToLower(rand.Text()), NetworkID: networkID, NodeID: n.ID, Name: in.Name, Engine: in.Engine, Version: in.Version,
		MemoryMB: in.MemoryMB, CPUMillis: in.CPUMillis, Storage: cmp.Or(in.Storage, n.DefaultStorage), CreatedAt: time.Now(), Databases: []Database{},
		MaxConnections: in.MaxConnections, BufferMB: in.BufferMB,
	}
	if err := s.store.insert(ctx, ds); err != nil {
		return View{}, err
	}
	ctx = context.WithoutCancel(ctx)
	var created *noryxv1.Datastore
	err = s.agent(ctx, ds.NodeID, func(ctx context.Context, c noryxv1.DatastoreServiceClient) error {
		res, err := c.CreateDatastore(ctx, &noryxv1.CreateDatastoreRequest{
			Id: ds.ID, Engine: engine, Version: ds.Version, MemoryMb: ds.MemoryMB, CpuMillis: ds.CPUMillis, Storage: ds.Storage, Parameters: ds.parameters(),
		})
		created = res.GetDatastore()
		return err
	})
	if err != nil {
		return View{}, undone(err, s.store.delete(ctx, ds.ID))
	}
	ds, warning := taken(ds, created)
	if warning != "" {
		if err := s.store.update(ctx, ds); err != nil {
			return View{}, err
		}
	}
	operation.Step(ctx, "start")
	if err := s.call(ctx, ds.NodeID, func(ctx context.Context, c noryxv1.DatastoreServiceClient) error {
		_, err := c.StartDatastore(ctx, &noryxv1.StartDatastoreRequest{Id: ds.ID})
		return err
	}); err != nil {
		return View{}, err
	}
	operation.Step(ctx, "network")
	if err := s.networks.Configure(ctx, networkID); err != nil {
		return View{}, err
	}
	v, err := s.one(ctx, ds.ID)
	v.Warning = warning
	return v, err
}

// taken returns a datastore with the parameters that its agent took: none if the agent is of an
// earlier version, which doesn't know them, with a warning if it was asked for some.
func taken(ds Datastore, got *noryxv1.Datastore) (Datastore, string) {
	if got.GetParameters() != nil || ds.MaxConnections == 0 && ds.BufferMB == 0 {
		return ds, ""
	}
	ds.MaxConnections, ds.BufferMB = 0, 0
	return ds, "The agent of the node is too old for the connections and buffers of datastores, so the datastore keeps the defaults of its image. Update the agent first, then set them in the datastore's settings."
}

// one returns a datastore as its agent reports it.
func (s *Service) one(ctx context.Context, id string) (View, error) {
	ds, err := s.store.get(ctx, id)
	if err != nil {
		return View{}, err
	}
	views, err := s.List(ctx, ds.NetworkID)
	i := slices.IndexFunc(views, func(v View) bool { return v.ID == id })
	if err == nil && i < 0 {
		err = errNotFound
	}
	if err != nil {
		return View{}, err
	}
	return views[i], nil
}

// checkMemory enforces the memory limit of a node, which its servers and datastores share,
// for a datastore that is created (id is empty) or gets more memory.
func (s *Service) checkMemory(ctx context.Context, n node.Node, id string, memoryMB uint32) error {
	if n.MemoryReserveMB == nil {
		return nil
	}
	conn, err := s.nodes.Conn(ctx, n.ID)
	if err != nil {
		return err
	}
	info, err := noryxv1.NewNodeServiceClient(conn).GetInfo(ctx, &noryxv1.GetInfoRequest{})
	if err != nil || info.GetMemoryBytes() == 0 { // the runtime is down, creating fails anyway
		return err
	}
	servers, err := noryxv1.NewServerServiceClient(conn).ListServers(ctx, &noryxv1.ListServersRequest{})
	if err != nil {
		return err
	}
	datastores, err := noryxv1.NewDatastoreServiceClient(conn).ListDatastores(ctx, &noryxv1.ListDatastoresRequest{})
	if err != nil {
		return err
	}
	left := int64(info.GetMemoryBytes()>>20) - int64(*n.MemoryReserveMB) //nolint:gosec // memory sizes fit easily
	for _, srv := range servers.GetServers() {
		left -= noryxv1.ContainerMemoryMB(srv.GetMemoryMb())
	}
	for _, ds := range datastores.GetDatastores() {
		if ds.GetId() != id {
			left -= int64(ds.GetMemoryMb())
		} else if memoryMB <= ds.GetMemoryMb() {
			return nil
		}
	}
	if int64(memoryMB) > left {
		return httpapi.Errorf(http.StatusConflict, "%s has room for a datastore with up to %d MB of memory. Choose less memory, or change the memory limit in the node's settings.", n.Name, max(left, 0))
	}
	return nil
}

// Update changes the limits of a datastore, pulls its image again, moves it to a newer major
// version or removes the data of the previous one.
func (s *Service) Update(ctx context.Context, id string, c Change) (View, error) {
	ds, err := s.store.get(ctx, id)
	if err != nil {
		return View{}, err
	}
	next := ds
	next.MemoryMB, next.Version = cmp.Or(c.MemoryMB, ds.MemoryMB), cmp.Or(c.Version, ds.Version)
	if c.CPUMillis != nil {
		next.CPUMillis = *c.CPUMillis
	}
	if c.MaxConnections != nil {
		next.MaxConnections = *c.MaxConnections
	}
	if c.BufferMB != nil {
		next.BufferMB = *c.BufferMB
	}
	if next.MemoryMB < minMemoryMB {
		return View{}, httpapi.Errorf(http.StatusBadRequest, "Give the datastore at least %d MB of memory.", minMemoryMB)
	}
	if problem := next.parameters().Problem(engines[ds.Engine], next.MemoryMB); problem != "" {
		return View{}, httpapi.Errorf(http.StatusBadRequest, "%s", problem)
	}
	n, err := s.nodes.Get(ctx, ds.NodeID)
	if err == nil {
		err = s.checkMemory(ctx, n, ds.ID, next.MemoryMB)
	}
	if err != nil {
		return View{}, err
	}
	ctx = context.WithoutCancel(ctx)
	var updated *noryxv1.Datastore
	err = s.agent(ctx, ds.NodeID, func(ctx context.Context, client noryxv1.DatastoreServiceClient) error {
		res, err := client.UpdateDatastore(ctx, &noryxv1.UpdateDatastoreRequest{
			Id: ds.ID, MemoryMb: next.MemoryMB, CpuMillis: next.CPUMillis, UpdateImage: c.UpdateImage, Version: c.Version, RemovePrevious: c.RemovePrevious,
			Parameters: next.parameters(),
		})
		updated = res.GetDatastore()
		return err
	})
	if err != nil {
		return View{}, err
	}
	next, warning := taken(next, updated)
	if err := s.store.update(ctx, next); err != nil {
		return View{}, err
	}
	v, err := s.one(ctx, id)
	v.Warning = warning
	return v, err
}

// Start starts a datastore, or stops it.
func (s *Service) Start(ctx context.Context, id string, start bool) error {
	ds, err := s.store.get(ctx, id)
	if err != nil {
		return err
	}
	return s.call(ctx, ds.NodeID, func(ctx context.Context, c noryxv1.DatastoreServiceClient) error {
		if start {
			_, err := c.StartDatastore(ctx, &noryxv1.StartDatastoreRequest{Id: id})
			return err
		}
		_, err := c.StopDatastore(ctx, &noryxv1.StopDatastoreRequest{Id: id})
		return err
	})
}

// Delete removes a datastore with its data and dumps from its node, and forgets it.
func (s *Service) Delete(ctx context.Context, id string) error {
	ds, err := s.store.get(ctx, id)
	if err != nil {
		return err
	}
	err = s.call(context.WithoutCancel(ctx), ds.NodeID, func(ctx context.Context, c noryxv1.DatastoreServiceClient) error {
		_, err := c.DeleteDatastore(ctx, &noryxv1.DeleteDatastoreRequest{Id: id})
		return err
	})
	if err != nil && status.Code(err) != codes.NotFound {
		return err
	}
	return s.store.delete(ctx, id)
}

// newPassword returns 32 random characters from a-z and 2-7, 160 bits, which need no quoting
// in SQL or configuration files.
func newPassword() string {
	b := make([]byte, 20)
	_, _ = rand.Read(b)
	return strings.ToLower(base32.StdEncoding.EncodeToString(b))
}

// AddDatabase creates a database with its user, which gets a new password. A database that
// the datastore lacks is created again with its password.
func (s *Service) AddDatabase(ctx context.Context, id, name string) (Database, error) {
	if problem := noryxv1.DatabaseNameProblem(name); problem != "" {
		return Database{}, httpapi.Errorf(http.StatusBadRequest, "%s", problem)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ds, err := s.store.get(ctx, id)
	if err != nil {
		return Database{}, err
	}
	i := slices.IndexFunc(ds.Databases, func(db Database) bool { return db.Name == name })
	db := Database{Name: name, CreatedAt: time.Now(), password: newPassword()}
	switch {
	case i >= 0:
		db = ds.Databases[i]
	case len(ds.Databases) >= noryxv1.MaxDatabases:
		return Database{}, httpapi.Errorf(http.StatusConflict, "A datastore has at most %d databases.", noryxv1.MaxDatabases)
	default:
		if err := s.store.addDatabase(ctx, id, name, db.password); err != nil {
			return Database{}, err
		}
	}
	if err := s.ensure(ctx, ds, db); err != nil {
		if i < 0 {
			err = undone(err, s.store.dropDatabase(context.WithoutCancel(ctx), id, name))
		}
		return Database{}, err
	}
	return db, nil
}

// undone returns the error of a change, and that of undoing it if that failed too. Joining a
// nil error would wrap an error of an agent, whose message the panel then shows as "rpc error:
// code = … desc = …".
func undone(err, undoErr error) error {
	if undoErr == nil {
		return err
	}
	return errors.Join(err, undoErr)
}

func (s *Service) ensure(ctx context.Context, ds Datastore, db Database) error {
	return s.call(ctx, ds.NodeID, func(ctx context.Context, c noryxv1.DatastoreServiceClient) error {
		_, err := c.EnsureDatabase(ctx, &noryxv1.EnsureDatabaseRequest{Id: ds.ID, Name: db.Name, Password: db.password})
		return err
	})
}

// DropDatabase drops a database of the datastore with its user and forgets it.
func (s *Service) DropDatabase(ctx context.Context, id, name string) error {
	ds, _, err := s.database(ctx, id, name)
	if err != nil {
		return err
	}
	if err := s.call(ctx, ds.NodeID, func(ctx context.Context, c noryxv1.DatastoreServiceClient) error {
		_, err := c.DropDatabase(ctx, &noryxv1.DropDatabaseRequest{Id: id, Name: name})
		return err
	}); err != nil {
		return err
	}
	return s.store.dropDatabase(ctx, id, name)
}

// database returns a datastore with one of its databases.
func (s *Service) database(ctx context.Context, id, name string) (Datastore, Database, error) {
	ds, err := s.store.get(ctx, id)
	if err != nil {
		return ds, Database{}, err
	}
	i := slices.IndexFunc(ds.Databases, func(db Database) bool { return db.Name == name })
	if i < 0 {
		return ds, Database{}, httpapi.Errorf(http.StatusNotFound, "Database not found.")
	}
	return ds, ds.Databases[i], nil
}

// Password returns the password of a database's user, which the plugins that use the
// database need in their configuration.
func (s *Service) Password(ctx context.Context, id, name string) (string, error) {
	_, db, err := s.database(ctx, id, name)
	return db.password, err
}

// Rotate gives the user of a database a new password, which the plugins that use it need
// then. It is one change at a time so that the master keeps the password the user has, which
// it sets again if it can't keep the new one.
func (s *Service) Rotate(ctx context.Context, id, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ds, db, err := s.database(ctx, id, name)
	if err != nil {
		return err
	}
	ctx, next := context.WithoutCancel(ctx), db
	next.password = newPassword()
	if err := s.ensure(ctx, ds, next); err != nil {
		return err
	}
	if err := s.store.setPassword(ctx, ds.ID, name, next.password); err != nil {
		return errors.Join(err, s.ensure(ctx, ds, db))
	}
	return nil
}

// call calls the agent of a node, for what answers soon.
func (s *Service) call(ctx context.Context, nodeID string, fn func(context.Context, noryxv1.DatastoreServiceClient) error) error {
	ctx, cancel := context.WithTimeout(ctx, actionTimeout)
	defer cancel()
	conn, err := s.nodes.Conn(ctx, nodeID)
	if err != nil {
		return err
	}
	return fn(ctx, noryxv1.NewDatastoreServiceClient(conn))
}

// agent calls the agent of a node within an operation, which follows the call's progress.
func (s *Service) agent(ctx context.Context, nodeID string, fn func(context.Context, noryxv1.DatastoreServiceClient) error) error {
	conn, err := s.nodes.Conn(ctx, nodeID)
	if err != nil {
		return err
	}
	ctx, stop := operation.Agent(ctx, conn)
	defer stop()
	return fn(ctx, noryxv1.NewDatastoreServiceClient(conn))
}
