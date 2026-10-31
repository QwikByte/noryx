package sftp

import (
	"context"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"google.golang.org/grpc"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	"github.com/QwikByte/noryx/internal/master/node"
)

// serversTTL is how long the servers of a node are kept, so that walking through a server's
// files doesn't ask its agent for its servers at every step.
const serversTTL = 10 * time.Second

// idPattern matches the IDs of nodes and servers.
var idPattern = regexp.MustCompile(`^[a-z2-7]{26}$`)

// Nodes provides the nodes and the connections to their agents.
type Nodes interface {
	List(ctx context.Context) ([]node.Node, error)
	Conn(ctx context.Context, nodeID string) (grpc.ClientConnInterface, error)
}

// folder is a node or a server as a folder of SFTP.
type folder struct {
	// name names the folder in paths, label as the panel does.
	name, label      string
	nodeID, serverID string
}

func (f folder) id() string {
	if f.serverID != "" {
		return f.serverID
	}
	return f.nodeID
}

// tree knows the folders of the first two levels: the nodes, and the servers of each node,
// which it asks the node's agent for.
type tree struct {
	nodes Nodes

	mu      sync.Mutex
	servers map[string]listing // by node ID
}

type listing struct {
	at      time.Time
	folders []folder
}

func newTree(nodes Nodes) *tree { return &tree{nodes: nodes, servers: map[string]listing{}} }

// nodeFolders returns all nodes.
func (t *tree) nodeFolders(ctx context.Context) ([]folder, error) {
	nodes, err := t.nodes.List(ctx)
	folders := make([]folder, len(nodes))
	for i, n := range nodes {
		folders[i] = folder{label: n.Name, nodeID: n.ID}
	}
	return named(folders), err
}

// serverFolders returns all servers of a node. As the agent tells them, those with invalid IDs
// are left out.
func (t *tree) serverFolders(ctx context.Context, nodeID string) ([]folder, error) {
	t.mu.Lock()
	l, ok := t.servers[nodeID]
	t.mu.Unlock()
	if ok && time.Since(l.at) < serversTTL {
		return l.folders, nil
	}
	conn, err := t.nodes.Conn(ctx, nodeID)
	if err != nil {
		return nil, err
	}
	res, err := noryxv1.NewServerServiceClient(conn).ListServers(ctx, &noryxv1.ListServersRequest{})
	if err != nil {
		return nil, err
	}
	var folders []folder
	for _, s := range res.GetServers() {
		if idPattern.MatchString(s.GetId()) {
			folders = append(folders, folder{label: s.GetName(), nodeID: nodeID, serverID: s.GetId()})
		}
	}
	folders = named(folders)
	t.mu.Lock()
	t.servers[nodeID] = listing{time.Now(), folders}
	t.mu.Unlock()
	return folders, nil
}

// path returns the path of the folder of a server.
func (t *tree) path(ctx context.Context, nodeID, serverID string) (string, error) {
	nodes, err := t.nodeFolders(ctx)
	if err != nil {
		return "", err
	}
	servers, err := t.serverFolders(ctx, nodeID)
	if err != nil {
		return "", err
	}
	n, _ := find(nodes, nodeID)
	s, _ := find(servers, serverID)
	return "/" + n.name + "/" + s.name, nil
}

// named names the folders by their labels, unless the label is that of another folder too,
// regardless of case, or can't be a name in a path, e.g. as it holds a slash: those folders
// are named by their IDs. Labels that look like IDs are taken as such, so that no name is
// another folder's ID.
func named(folders []folder) []folder {
	labels := map[string]int{}
	for _, f := range folders {
		labels[strings.ToLower(f.label)]++
	}
	for i, f := range folders {
		folders[i].name = f.label
		if labels[strings.ToLower(f.label)] > 1 || f.label == "." || f.label == ".." || idPattern.MatchString(f.label) || !utf8.ValidString(f.label) ||
			f.label == "" || strings.ContainsFunc(f.label, func(r rune) bool { return r == '/' || unicode.IsControl(r) }) {
			folders[i].name = f.id()
		}
	}
	return folders
}

// find returns the folder of a name in a path, which is its name or ID.
func find(folders []folder, name string) (folder, bool) {
	for _, f := range folders {
		if f.name == name || f.id() == name {
			return f, true
		}
	}
	return folder{name: name}, false
}
