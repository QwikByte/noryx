package datastore

import (
	"cmp"
	"context"
	"database/sql"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"
	"time"

	"google.golang.org/grpc"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	"github.com/QwikByte/noryx/internal/master/backup"
	"github.com/QwikByte/noryx/internal/master/httpapi"
	"github.com/QwikByte/noryx/internal/master/node"
	"github.com/QwikByte/noryx/internal/master/overlay"
)

// publishTimeout covers creating the container of a datastore again with another port.
const publishTimeout = 3 * time.Minute

// Nodes provide the nodes and the connections to their agents.
type Nodes interface {
	List(ctx context.Context) ([]node.Node, error)
	Get(ctx context.Context, id string) (node.Node, error)
	Conn(ctx context.Context, id string) (grpc.ClientConnInterface, error)
}

// Overlay is the private network of the nodes, over which the servers of other nodes reach
// a datastore.
type Overlay interface {
	// ByNode returns its members, by node.
	ByNode(ctx context.Context) (map[string]overlay.Member, error)
}

// Store keeps the datastores and the passwords of their databases. It tells the networks
// where their datastores are, publishes them for the servers of other nodes, and gives backup
// jobs the datastores they dump.
type Store struct {
	db      *sql.DB
	nodes   Nodes
	overlay Overlay
}

func NewStore(db *sql.DB, nodes Nodes, overlay Overlay) *Store {
	return &Store{db: db, nodes: nodes, overlay: overlay}
}

var errNotFound = httpapi.Errorf(http.StatusNotFound, "Datastore not found.")

// list returns the datastores of a network, or all if networkID is empty, by name, with
// their databases.
func (s *Store) list(ctx context.Context, networkID string) ([]Datastore, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, network_id, node_id, name, engine, version, memory_mb, cpu_millis, storage, port, created_at, max_connections, buffer_mb
		FROM datastores WHERE ? IN ('', network_id) ORDER BY name, id`, networkID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	list, index := []Datastore{}, map[string]int{}
	for rows.Next() {
		ds := Datastore{Databases: []Database{}}
		var created int64
		if err := rows.Scan(&ds.ID, &ds.NetworkID, &ds.NodeID, &ds.Name, &ds.Engine, &ds.Version, &ds.MemoryMB, &ds.CPUMillis, &ds.Storage, &ds.Port, &created, &ds.MaxConnections, &ds.BufferMB); err != nil {
			return nil, err
		}
		ds.CreatedAt, index[ds.ID] = time.Unix(created, 0), len(list)
		list = append(list, ds)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	databases, err := s.db.QueryContext(ctx, `
		SELECT d.datastore_id, d.name, d.password, d.created_at FROM datastore_databases d JOIN datastores s ON s.id = d.datastore_id
		WHERE ? IN ('', s.network_id) ORDER BY d.name`, networkID)
	if err != nil {
		return nil, err
	}
	defer databases.Close()
	for databases.Next() {
		var id string
		var db Database
		var created int64
		if err := databases.Scan(&id, &db.Name, &db.password, &created); err != nil {
			return nil, err
		}
		db.CreatedAt = time.Unix(created, 0)
		if i, ok := index[id]; ok {
			list[i].Databases = append(list[i].Databases, db)
		}
	}
	return list, databases.Err()
}

func (s *Store) get(ctx context.Context, id string) (Datastore, error) {
	list, err := s.list(ctx, "")
	i := slices.IndexFunc(list, func(ds Datastore) bool { return ds.ID == id })
	if err == nil && i < 0 {
		err = errNotFound
	}
	if err != nil {
		return Datastore{}, err
	}
	return list[i], nil
}

func (s *Store) insert(ctx context.Context, ds Datastore) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO datastores (id, network_id, node_id, name, engine, version, memory_mb, cpu_millis, storage, port, created_at, max_connections, buffer_mb)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?, ?, ?)`,
		ds.ID, ds.NetworkID, ds.NodeID, ds.Name, ds.Engine, ds.Version, ds.MemoryMB, ds.CPUMillis, ds.Storage, ds.CreatedAt.Unix(), ds.MaxConnections, ds.BufferMB)
	if err != nil && strings.Contains(err.Error(), "UNIQUE") {
		return httpapi.Errorf(http.StatusConflict, "The network has a datastore named %q already.", ds.Name)
	}
	return err
}

// update stores the settings of a datastore that may change.
func (s *Store) update(ctx context.Context, ds Datastore) error {
	_, err := s.db.ExecContext(ctx, `UPDATE datastores SET version = ?, memory_mb = ?, cpu_millis = ?, max_connections = ?, buffer_mb = ? WHERE id = ?`,
		ds.Version, ds.MemoryMB, ds.CPUMillis, ds.MaxConnections, ds.BufferMB, ds.ID)
	return err
}

func (s *Store) delete(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM datastores WHERE id = ?`, id)
	return err
}

func (s *Store) addDatabase(ctx context.Context, id, name, password string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO datastore_databases (datastore_id, name, password, created_at) VALUES (?, ?, ?, ?)`,
		id, name, password, time.Now().Unix())
	return err
}

func (s *Store) setPassword(ctx context.Context, id, name, password string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE datastore_databases SET password = ? WHERE datastore_id = ? AND name = ?`, password, id, name)
	return err
}

func (s *Store) dropDatabase(ctx context.Context, id, name string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM datastore_databases WHERE datastore_id = ? AND name = ?`, id, name)
	return err
}

// Placed returns the IDs of the datastores of a network, by node.
func (s *Store) Placed(ctx context.Context, networkID string) (map[string][]string, error) {
	list, err := s.list(ctx, networkID)
	placed := map[string][]string{}
	for _, ds := range list {
		placed[ds.NodeID] = append(placed[ds.NodeID], ds.ID)
	}
	return placed, err
}

// reaches reports whether the servers of node from reach a datastore of node to over the
// private network of the nodes, whose members are m.
func reaches(m map[string]overlay.Member, from, to string) bool {
	return from != to && m[from].Address != "" && m[to].Address != ""
}

// Publish publishes the datastores of a network at the address of their nodes in the private
// network, for the other nodes of its servers that are members, and closes them for others.
// A datastore gets a port of its node the first time it is published, and keeps it.
func (s *Store) Publish(ctx context.Context, networkID string, nodes []string, m map[string]overlay.Member) error {
	list, err := s.list(ctx, networkID)
	if err != nil {
		return err
	}
	for _, ds := range list {
		var clients []overlay.Member
		for _, nodeID := range nodes {
			if reaches(m, nodeID, ds.NodeID) && !slices.ContainsFunc(clients, func(c overlay.Member) bool { return c.Address == m[nodeID].Address }) {
				clients = append(clients, m[nodeID])
			}
		}
		slices.SortFunc(clients, func(a, b overlay.Member) int { return strings.Compare(a.Address, b.Address) })
		if err := s.publishFor(ctx, ds, clients); err != nil {
			return fmt.Errorf("%s: %w", ds.Name, err)
		}
	}
	return nil
}

// tries is how many free ports publishing a datastore tries, as one may be in use by
// something else on the node.
const tries = 3

// publishFor publishes a datastore for clients, the members with their current keys, or for
// none. One without a port yet gets a free one of its node, which it keeps once the agent
// published it.
func (s *Store) publishFor(ctx context.Context, ds Datastore, clients []overlay.Member) error {
	req := &noryxv1.PublishDatastoreRequest{Id: ds.ID, Port: ds.Port}
	for _, c := range clients {
		req.Clients, req.ClientKeys = append(req.Clients, c.Address), append(req.ClientKeys, c.PublicKey)
	}
	if len(clients) == 0 || ds.Port != 0 {
		if len(clients) == 0 {
			req.Port = 0
		}
		return s.publish(ctx, ds.NodeID, req)
	}
	refused := map[uint32]bool{}
	var err error
	for range tries {
		if req.Port, err = s.freePort(ctx, ds, refused); err != nil {
			return err
		}
		if err = s.publish(ctx, ds.NodeID, req); err == nil {
			_, err = s.db.ExecContext(ctx, `UPDATE datastores SET port = ? WHERE id = ?`, req.Port, ds.ID)
			return err
		}
		if !strings.Contains(err.Error(), "already") { // used by a server, or by something else on the node
			return err
		}
		refused[req.Port] = true
	}
	return err
}

func (s *Store) publish(ctx context.Context, nodeID string, req *noryxv1.PublishDatastoreRequest) error {
	ctx, cancel := context.WithTimeout(ctx, publishTimeout)
	defer cancel()
	conn, err := s.nodes.Conn(ctx, nodeID)
	if err == nil {
		_, err = noryxv1.NewDatastoreServiceClient(conn).PublishDatastore(ctx, req)
	}
	return err
}

// portBase is where the search for a port of a datastore starts on nodes without a port
// range, away from the ports of Minecraft servers and of the databases on the host itself.
var portBase = map[string]uint32{"mariadb": 23306, "postgres": 25432}

// freePort chooses a free port of the node of a datastore, but none refused: from the top of
// its port range, as servers take theirs from the bottom.
func (s *Store) freePort(ctx context.Context, ds Datastore, refused map[uint32]bool) (uint32, error) {
	n, err := s.nodes.Get(ctx, ds.NodeID)
	if err != nil {
		return 0, err
	}
	conn, err := s.nodes.Conn(ctx, ds.NodeID)
	if err != nil {
		return 0, err
	}
	servers, err := noryxv1.NewServerServiceClient(conn).ListServers(ctx, &noryxv1.ListServersRequest{})
	if err != nil {
		return 0, err
	}
	used := maps.Clone(refused)
	for _, srv := range servers.GetServers() {
		used[srv.GetPort()], used[srv.GetBedrockPort()] = true, true
		for _, p := range srv.GetExtraPorts() {
			used[p.GetPort()] = true
		}
	}
	rows, err := s.db.QueryContext(ctx, `SELECT port FROM datastores WHERE node_id = ? AND port <> 0`, ds.NodeID)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	for rows.Next() {
		var port uint32
		if err := rows.Scan(&port); err != nil {
			return 0, err
		}
		used[port] = true
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	port, end, next := portBase[ds.Engine], uint32(65535), func(p uint32) uint32 { return p + 1 }
	if n.PortMin != nil {
		port, end, next = *n.PortMax, *n.PortMin, func(p uint32) uint32 { return p - 1 }
	}
	for ; used[port]; port = next(port) {
		if port == end {
			return 0, httpapi.Errorf(http.StatusConflict, "%s has no free port for the datastore %s. Widen its port range.", n.Name, ds.Name)
		}
	}
	return port, nil
}

// Locate returns those of the datastores with the IDs that exist, for backup jobs.
func (s *Store) Locate(ctx context.Context, ids []string) ([]backup.Datastore, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	list, err := s.list(ctx, "")
	if err != nil {
		return nil, err
	}
	nodes, err := s.nodes.List(ctx)
	if err != nil {
		return nil, err
	}
	var found []backup.Datastore
	for _, ds := range list {
		if slices.Contains(ids, ds.ID) {
			i := slices.IndexFunc(nodes, func(n node.Node) bool { return n.ID == ds.NodeID })
			name := ds.NodeID
			if i >= 0 {
				name = nodes[i].Name
			}
			found = append(found, backup.Datastore{ID: ds.ID, Name: ds.Name, NodeID: ds.NodeID, NodeName: cmp.Or(name, ds.NodeID)})
		}
	}
	return found, nil
}
