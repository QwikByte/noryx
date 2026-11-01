package logs

import (
	"context"
	"maps"
	"sync"
	"time"

	"google.golang.org/grpc"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	"github.com/QwikByte/noryx/internal/master/node"
)

const (
	namesTTL     = time.Minute
	namesRetry   = 5 * time.Second
	namesTimeout = 2 * time.Second
)

// Nodes provides the nodes and the connections to their agents.
type Nodes interface {
	List(ctx context.Context) ([]node.Node, error)
	Get(ctx context.Context, id string) (node.Node, error)
	Conn(ctx context.Context, id string) (grpc.ClientConnInterface, error)
}

// Names finds the names of nodes and servers for entries that only have their IDs. Server
// names are asked from the agents and kept for a minute. An agent is asked at most every
// few seconds, so an unreachable one barely delays entries, and names of deleted servers
// are kept for the entries that come in after the deletion.
type Names struct {
	nodes Nodes

	mu      sync.Mutex
	servers map[string]*serverNames // by node ID
}

type serverNames struct {
	at time.Time
	// listed are the servers of the agent's latest answer, names those of the answer before
	// too: names of deleted servers stay for a while, but not forever, as an agent could
	// answer with new servers each time.
	listed, names map[string]string
}

func NewNames(nodes Nodes) *Names { return &Names{nodes: nodes, servers: map[string]*serverNames{}} }

// Node returns the name of a node, or "" if it doesn't exist.
func (n *Names) Node(ctx context.Context, id string) string {
	node, err := n.nodes.Get(ctx, id)
	if err != nil {
		return ""
	}
	return node.Name
}

// Server returns the name of a server, or "" if it is unknown.
func (n *Names) Server(ctx context.Context, nodeID, id string) string {
	n.mu.Lock()
	known := n.servers[nodeID]
	n.mu.Unlock()
	if known != nil {
		if age := time.Since(known.at); age < namesRetry || age < namesTTL && known.names[id] != "" {
			return known.names[id]
		}
	} else if n.Node(ctx, nodeID) == "" { // requests may name any node, which the cache mustn't keep
		return ""
	}
	fresh := &serverNames{at: time.Now(), listed: map[string]string{}, names: map[string]string{}}
	if known != nil {
		fresh.listed, fresh.names = known.listed, known.names
	}
	ctx, cancel := context.WithTimeout(ctx, namesTimeout)
	defer cancel()
	if conn, err := n.nodes.Conn(ctx, nodeID); err == nil {
		if res, err := noryxv1.NewServerServiceClient(conn).ListServers(ctx, &noryxv1.ListServersRequest{}); err == nil {
			listed := map[string]string{}
			for _, srv := range res.GetServers() {
				listed[srv.GetId()] = srv.GetName()
			}
			fresh.names = maps.Clone(fresh.listed)
			maps.Copy(fresh.names, listed)
			fresh.listed = listed
		}
	}
	n.mu.Lock()
	n.servers[nodeID] = fresh
	n.mu.Unlock()
	return fresh.names[id]
}
