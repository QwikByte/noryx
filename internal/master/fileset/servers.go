package fileset

import (
	"cmp"
	"context"
	"slices"
	"sync"
	"time"

	"google.golang.org/grpc"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	"github.com/QwikByte/noryx/internal/master/datastore"
	"github.com/QwikByte/noryx/internal/master/httpapi"
	"github.com/QwikByte/noryx/internal/master/network"
	"github.com/QwikByte/noryx/internal/master/node"
	"github.com/QwikByte/noryx/internal/master/tag"
)

const queryTimeout = 10 * time.Second

// Nodes gives access to the agents of the nodes.
type Nodes interface {
	List(ctx context.Context) ([]node.Node, error)
	Conn(ctx context.Context, id string) (grpc.ClientConnInterface, error)
}

// Networks are the networks whose servers sets are for.
type Networks interface {
	List(ctx context.Context) ([]network.Network, error)
	// RollingRestart restarts game servers of a network a few at a time, so that it stays open.
	RollingRestart(ctx context.Context, n network.Network, batch int, only ...network.Ref) error
}

// Tags are the tags of servers.
type Tags interface {
	All(ctx context.Context) (map[tag.Server][]string, error)
}

// Datastores tell how servers reach the databases of the datastores of their networks.
type Datastores interface {
	Connections(ctx context.Context) (datastore.Connections, error)
}

// Moves are the servers that move to another node, which are left alone meanwhile.
type Moves interface {
	Check(serverID string) error
}

// survey is what the nodes tell about their servers and the sets on them, and how the master
// groups the servers.
type survey struct {
	nodes    map[string]string // names by ID
	servers  map[tag.Server]*noryxv1.Server
	applied  map[tag.Server][]*noryxv1.AppliedFileSet
	down     map[string]error // nodes that couldn't be asked
	tags     map[tag.Server][]string
	networks []network.Network
	reach    datastore.Connections
}

// survey asks all nodes about their servers and the sets on them, all at the same time.
func (s *Service) survey(ctx context.Context) (*survey, error) {
	sv := &survey{
		nodes: map[string]string{}, servers: map[tag.Server]*noryxv1.Server{}, applied: map[tag.Server][]*noryxv1.AppliedFileSet{},
		down: map[string]error{},
	}
	nodes, err := s.nodes.List(ctx)
	if err == nil {
		sv.tags, err = s.tags.All(ctx)
	}
	if err == nil {
		sv.networks, err = s.networks.List(ctx)
	}
	if err == nil {
		sv.reach, err = s.datastores.Connections(ctx)
	}
	if err != nil {
		return nil, err
	}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, n := range nodes {
		sv.nodes[n.ID] = n.Name
		wg.Go(func() {
			servers, sets, err := s.ask(ctx, n.ID)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				sv.down[n.ID] = err
				return
			}
			for _, srv := range servers {
				sv.servers[tag.Server{NodeID: n.ID, ServerID: srv.GetId()}] = srv
			}
			for _, srv := range sets.GetServers() {
				sv.applied[tag.Server{NodeID: n.ID, ServerID: srv.GetServerId()}] = srv.GetSets()
			}
		})
	}
	wg.Wait()
	return sv, nil
}

// ask lists the servers of a node and the sets on them.
func (s *Service) ask(ctx context.Context, nodeID string) ([]*noryxv1.Server, *noryxv1.ListFileSetsResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()
	conn, err := s.nodes.Conn(ctx, nodeID)
	if err != nil {
		return nil, nil, err
	}
	servers, err := noryxv1.NewServerServiceClient(conn).ListServers(ctx, &noryxv1.ListServersRequest{})
	if err != nil {
		return nil, nil, err
	}
	sets, err := noryxv1.NewFileSetServiceClient(conn).ListFileSets(ctx, &noryxv1.ListFileSetsRequest{})
	if err != nil {
		return nil, nil, err
	}
	return servers.GetServers(), sets, nil
}

// targets returns the servers that targets name, sorted. Servers that a node listed must
// exist; those of nodes that couldn't be asked are taken as they are.
func (sv *survey) targets(targets []Target) []tag.Server {
	var refs []tag.Server
	for _, t := range targets {
		switch t.Kind {
		case KindTag:
			for srv, tags := range sv.tags {
				if slices.Contains(tags, t.Value) {
					refs = append(refs, srv)
				}
			}
		case KindNetwork:
			for _, n := range sv.networks {
				switch {
				case n.ID != t.Value:
				case t.Role == RoleProxy:
					refs = append(refs, tag.Server(n.Proxy))
				default:
					for _, b := range n.Backends {
						refs = append(refs, tag.Server(b.Ref))
					}
				}
			}
		}
	}
	refs = slices.DeleteFunc(refs, func(r tag.Server) bool { return sv.down[r.NodeID] == nil && sv.servers[r] == nil })
	slices.SortFunc(refs, compareServers)
	return slices.Compact(refs)
}

// set returns what a server has of a set, or nil.
func (sv *survey) set(srv tag.Server, id string) *noryxv1.AppliedFileSet {
	i := slices.IndexFunc(sv.applied[srv], func(a *noryxv1.AppliedFileSet) bool { return a.GetSetId() == id })
	if i < 0 {
		return nil
	}
	return sv.applied[srv][i]
}

// holders returns the servers that have files of a set, sorted.
func (sv *survey) holders(id string) []tag.Server {
	var refs []tag.Server
	for srv := range sv.applied {
		if sv.set(srv, id) != nil {
			refs = append(refs, srv)
		}
	}
	slices.SortFunc(refs, compareServers)
	return refs
}

// member returns what the placeholders of a server need.
func (sv *survey) member(ref tag.Server) member {
	m := member{Server: ref, Name: sv.servers[ref].GetName(), Port: sv.servers[ref].GetPort(), Tags: sv.tags[ref]}
	for _, n := range sv.networks {
		if tag.Server(n.Proxy) == ref {
			m.NetworkID = n.ID
		}
		for _, b := range n.Backends {
			if tag.Server(b.Ref) == ref {
				m.Network, m.NetworkID = b.Name, n.ID
			}
		}
	}
	networkID := m.NetworkID
	m.connect = func(name, database string) (datastore.Connection, error) {
		return sv.reach.Connect(networkID, ref.NodeID, name, database)
	}
	return m
}

func compareServers(a, b tag.Server) int {
	return cmp.Or(cmp.Compare(a.NodeID, b.NodeID), cmp.Compare(a.ServerID, b.ServerID))
}

// States of a set on a server.
const (
	Current     = "current"     // the newest state of the set
	Outdated    = "outdated"    // an older version, or other values of its variables or secrets
	Changed     = "changed"     // files changed on the server since they were written
	Missing     = "missing"     // the server has none of the set's files
	Left        = "left"        // no longer a target, but still has files of the set
	Unreachable = "unreachable" // its node couldn't be asked
)

// ServerStatus is the state of a set on a server.
type ServerStatus struct {
	NodeID   string `json:"nodeId"`
	NodeName string `json:"nodeName"`
	ServerID string `json:"serverId"`
	Name     string `json:"name,omitempty"`
	Type     string `json:"type,omitempty"`
	Running  bool   `json:"running"`
	State    string `json:"state"`
	// Version is the version the server has, 0 for none.
	Version int64 `json:"version,omitempty"`
	// Changed are the files that changed on the server since they were written.
	Changed []string `json:"changed,omitempty"`
	// Problem tells why the set can't be applied to the server, e.g. a secret or variable
	// without value, or a database it can't reach.
	Problem string `json:"problem,omitempty"`
}

// status returns the state of a set on each server it is for or that still has its files.
func (sv *survey) status(set Set, values map[string]string) []ServerStatus {
	targets := sv.targets(set.Targets)
	refs := slices.Concat(targets, slices.DeleteFunc(sv.holders(set.ID), func(r tag.Server) bool { return slices.Contains(targets, r) }))
	list := make([]ServerStatus, 0, len(refs))
	for _, ref := range refs {
		srv := sv.servers[ref]
		st := ServerStatus{
			NodeID: ref.NodeID, NodeName: sv.nodes[ref.NodeID], ServerID: ref.ServerID, Name: srv.GetName(), Type: srv.GetType().Slug(),
			Running: srv.GetState() == noryxv1.ServerState_SERVER_STATE_RUNNING,
		}
		applied := sv.set(ref, set.ID)
		st.Version = applied.GetVersion()
		for _, f := range applied.GetFiles() {
			if f.GetChanged() && (!f.GetOnlyIfMissing() || f.GetMissing()) {
				st.Changed = append(st.Changed, f.GetPath())
			}
		}
		r, err := render(set, set.Files, values, sv.member(ref))
		if err != nil {
			st.Problem = httpapi.Message(err)
		}
		switch {
		case sv.down[ref.NodeID] != nil:
			st.State, st.Problem = Unreachable, httpapi.Message(sv.down[ref.NodeID])
		case !slices.Contains(targets, ref):
			st.State, st.Problem = Left, ""
		case applied == nil:
			st.State = Missing
		case applied.GetRevision() != r.revision:
			st.State = Outdated
		case len(st.Changed) > 0:
			st.State = Changed
		default:
			st.State = Current
		}
		list = append(list, st)
	}
	return list
}
