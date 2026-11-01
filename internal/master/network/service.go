// Package network groups servers into networks behind a proxy: Velocity or BungeeCord.
// The master stores the networks and configures their servers through the node
// agents, in the configuration files and the way the proxies and game servers document it.
package network

import (
	"cmp"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	"github.com/QwikByte/noryx/internal/logging"
	"github.com/QwikByte/noryx/internal/master/database"
	"github.com/QwikByte/noryx/internal/master/httpapi"
	"github.com/QwikByte/noryx/internal/master/node"
	"github.com/QwikByte/noryx/internal/master/operation"
	"github.com/QwikByte/noryx/internal/master/overlay"
	"github.com/QwikByte/noryx/internal/master/plugin"
)

const (
	queryTimeout = 10 * time.Second
	// configureTimeout covers recreating a running backend: a graceful stop, which may take the
	// longest stop timeout, and a start.
	configureTimeout = 2*time.Minute + noryxv1.MaxStopTimeout

	// Forwarding modes: Velocity's modern forwarding, or BungeeCord's, which Velocity calls legacy.
	Modern = "modern"
	Legacy = "legacy"

	maxBackends    = 256
	maxForcedHosts = 256
	maxMotd        = 256
)

var (
	errNotFound   = httpapi.Errorf(http.StatusNotFound, "Network not found.")
	errProxyType  = httpapi.Errorf(http.StatusBadRequest, "Choose a Velocity or BungeeCord proxy.")
	errExposed    = httpapi.Errorf(http.StatusConflict, "With legacy forwarding, anyone who reaches a server can join it as any player. Confirm that a firewall lets only the proxy's node reach the servers on other nodes.")
	errFabricMode = httpapi.Errorf(http.StatusBadRequest, "Fabric and Quilt servers only support Velocity's modern forwarding.")

	nonSlug     = regexp.MustCompile(`[^a-z0-9]+`)
	hostPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)*$`)
)

// mods are the Modrinth projects with which game servers that can't verify forwarded
// players themselves learn it: FabricProxy-Lite, which runs on Quilt too, and
// Proxy-Compatible-Forge.
var mods = map[noryxv1.ServerType]string{
	noryxv1.ServerType_SERVER_TYPE_FABRIC:   "8dI2tmqs",
	noryxv1.ServerType_SERVER_TYPE_QUILT:    "8dI2tmqs",
	noryxv1.ServerType_SERVER_TYPE_FORGE:    "vDyrHl8l",
	noryxv1.ServerType_SERVER_TYPE_NEOFORGE: "vDyrHl8l",
}

// Ref points to a server on a node.
type Ref struct {
	NodeID   string `json:"nodeId"`
	ServerID string `json:"serverId"`
}

// Backend is a game server behind the proxy.
type Backend struct {
	Ref
	Name string `json:"name"` // what players use, e.g. /server lobby
	// Restricted and Motd are settings of BungeeCord: only players with the permission
	// bungeecord.server.<name> may join a restricted server, and the MOTD is shown for host
	// names that lead to it; empty uses the proxy's.
	Restricted bool   `json:"restricted"`
	Motd       string `json:"motd"`
}

// ForcedHost sends players who connect through a host name to certain backends, tried in
// this order. BungeeCord sends them to one.
type ForcedHost struct {
	Host    string   `json:"host"`
	Servers []string `json:"servers"`
}

type Network struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Proxy Ref    `json:"proxy"`
	// ProxyType is the type of the proxy, e.g. velocity.
	ProxyType  string `json:"proxyType"`
	Forwarding string `json:"forwarding"`
	// Firewalled confirms that only the proxy's node reaches the backends on other nodes,
	// which legacy forwarding needs.
	Firewalled bool      `json:"firewalled"`
	Backends   []Backend `json:"backends"`
	// Try names the backends players join and fall back to, in this order.
	Try         []string     `json:"try"`
	ForcedHosts []ForcedHost `json:"forcedHosts"`
	// BedrockPort lets Bedrock players join through Geyser on the proxy at this UDP port; 0
	// for none.
	BedrockPort uint32 `json:"bedrockPort"`
	// ApplyError tells why its servers were last configured in vain, e.g. after the port of
	// a server changed while a node was offline; empty once they are configured again.
	ApplyError string    `json:"applyError,omitempty"`
	CreatedAt  time.Time `json:"createdAt"`
	secret     string
	// placed are the IDs of its datastores by node, while it is applied.
	placed map[string][]string
}

// Draft is a new network: its proxy and the servers behind it; players join the first.
type Draft struct {
	Name       string `json:"name"`
	Proxy      Ref    `json:"proxy"`
	Forwarding string `json:"forwarding"` // empty is modern for Velocity, legacy otherwise
	Firewalled bool   `json:"firewalled"`
	Servers    []Ref  `json:"servers"`
}

// Change replaces the settings of a network; its proxy stays.
type Change struct {
	Name        string       `json:"name"`
	Forwarding  string       `json:"forwarding"`
	Firewalled  bool         `json:"firewalled"`
	Backends    []Backend    `json:"backends"`
	Try         []string     `json:"try"`
	ForcedHosts []ForcedHost `json:"forcedHosts"`
	BedrockPort uint32       `json:"bedrockPort"`
}

// Nodes gives access to the nodes the servers of a network run on.
type Nodes interface {
	Get(ctx context.Context, id string) (node.Node, error)
	Conn(ctx context.Context, id string) (grpc.ClientConnInterface, error)
}

// Mods installs and removes the plugins and mods of networks: the forwarding mods of Fabric,
// Quilt, Forge and NeoForge servers, and those that proxies run, such as Geyser.
type Mods interface {
	Ensure(ctx context.Context, ref plugin.Ref, project string) error
	// Provide installs the newest releases, and reports whether it changed a file.
	Provide(ctx context.Context, ref plugin.Ref, projects []string) (bool, error)
	Uninstall(ctx context.Context, ref plugin.Ref, project string) error
}

// Overlay is the private network of the nodes, over which a proxy reaches the servers of
// another node if both nodes are members.
type Overlay interface {
	// ByNode returns its members, by node.
	ByNode(ctx context.Context) (map[string]overlay.Member, error)
}

// Datastores are the MariaDB and PostgreSQL servers of networks, which their servers reach.
type Datastores interface {
	// Placed returns the IDs of the datastores of a network, by node.
	Placed(ctx context.Context, networkID string) (map[string][]string, error)
	// Publish publishes the datastores of a network in the private network of the nodes for
	// those of nodes that reach them over it, and for no others.
	Publish(ctx context.Context, networkID string, nodes []string, members map[string]overlay.Member) error
}

// members are the nodes in the private network, by node.
type members map[string]overlay.Member

// private reports whether a proxy on node a reaches the servers of node b over the private
// network: their ports are only published there, and only for it.
func (m members) private(a, b string) bool { return a != b && m[a].Address != "" && m[b].Address != "" }

type Service struct {
	db         *sql.DB
	nodes      Nodes
	mods       Mods
	overlay    Overlay
	datastores Datastores
	mu         sync.Mutex // one change at a time, as changes reconfigure servers
}

func NewService(db *sql.DB, nodes Nodes, mods Mods, overlay Overlay, datastores Datastores) *Service {
	return &Service{db: db, nodes: nodes, mods: mods, overlay: overlay, datastores: datastores}
}

func (s *Service) List(ctx context.Context) ([]Network, error) { return s.load(ctx, "") }

func (s *Service) Get(ctx context.Context, id string) (Network, error) {
	networks, err := s.load(ctx, id)
	if err == nil && len(networks) == 0 {
		err = errNotFound
	}
	if err != nil {
		return Network{}, err
	}
	return networks[0], nil
}

// Create sets up a network with its proxy and servers.
func (s *Service) Create(ctx context.Context, d Draft) (Network, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	proxy, err := s.server(ctx, d.Proxy)
	if err == nil && !proxy.GetType().Proxy() {
		err = errProxyType
	}
	if err != nil {
		return Network{}, err
	}
	n := Network{
		ID: strings.ToLower(rand.Text()), Name: d.Name, Proxy: d.Proxy, ProxyType: proxy.GetType().Slug(),
		Forwarding: forwardingOr(d.Forwarding, proxy.GetType()), Firewalled: d.Firewalled, Backends: []Backend{}, ForcedHosts: []ForcedHost{},
		CreatedAt: time.Now(), secret: rand.Text(),
	}
	for _, ref := range d.Servers {
		srv, err := s.backend(ctx, ref, n.Forwarding)
		if err != nil {
			return n, err
		}
		n.Backends = append(n.Backends, Backend{Ref: ref, Name: backendName(srv.GetName(), n.Backends)})
	}
	if len(n.Backends) > 0 {
		n.Try = []string{n.Backends[0].Name}
	}
	m, err := s.members(ctx)
	if err == nil {
		err = n.validate(m)
	}
	if err != nil {
		return n, err
	}
	err = database.InTx(ctx, s.db, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO networks (id, name, proxy_node_id, proxy_server_id, proxy_type, forwarding_secret, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)`,
			n.ID, n.Name, n.Proxy.NodeID, n.Proxy.ServerID, n.ProxyType, n.secret, n.CreatedAt.Unix()); err != nil {
			return err
		}
		return save(ctx, tx, n)
	})
	if err != nil {
		return n, conflict(err, n.Name)
	}
	return n, s.apply(ctx, n, false)
}

// Update replaces the settings of a network and configures its servers: servers that
// left accept players directly again, the others and the proxy get the new settings.
func (s *Service) Update(ctx context.Context, id string, c Change) (Network, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, err := s.Get(ctx, id)
	if err != nil {
		return current, err
	}
	n := current
	n.Name, n.Forwarding, n.Firewalled = c.Name, c.Forwarding, c.Firewalled
	n.Backends, n.Try, n.ForcedHosts, n.BedrockPort = c.Backends, c.Try, c.ForcedHosts, c.BedrockPort
	return s.update(ctx, current, n)
}

// update gives a network new settings, while the lock is held, and returns the network as it
// is saved.
func (s *Service) update(ctx context.Context, current, n Network) (Network, error) {
	m, err := s.members(ctx)
	if err == nil {
		err = n.validate(m)
	}
	if err != nil {
		return current, err
	}
	if n.BedrockPort != current.BedrockPort {
		if err := s.checkBedrock(ctx, n.Proxy.NodeID, n.BedrockPort, n.Proxy.ServerID); err != nil {
			return current, err
		}
	}
	// Servers that join, or all if the forwarding changed, must support it.
	for _, b := range n.Backends {
		if n.Forwarding != current.Forwarding || !slices.ContainsFunc(current.Backends, b.same) {
			if _, err := s.backend(ctx, b.Ref, n.Forwarding); err != nil {
				return current, fmt.Errorf("%s: %w", b.Name, err)
			}
		}
	}
	// Servers that leave are made standalone first: one that still trusts the proxy must
	// not be forgotten.
	ctx = context.WithoutCancel(ctx)
	for _, b := range current.Backends {
		if !slices.ContainsFunc(n.Backends, b.same) {
			if err := s.leave(ctx, b); err != nil {
				return current, httpapi.Errorf(http.StatusBadGateway, "%s could not be made standalone again: %s", b.Name, httpapi.Message(err))
			}
		}
	}
	// The proxy loses Geyser and Floodgate before it restarts without the Bedrock port.
	if current.BedrockPort != 0 && n.BedrockPort == 0 {
		if err := s.removeBedrock(ctx, n); err != nil {
			return current, err
		}
	}
	if err := database.InTx(ctx, s.db, func(tx *sql.Tx) error { return save(ctx, tx, n) }); err != nil {
		return current, conflict(err, n.Name)
	}
	return n, s.apply(ctx, n, n.BedrockPort != current.BedrockPort)
}

// Delete makes all servers standalone again and removes the network. The proxy keeps
// running without forwarding. A network with datastores stays, as its data would be lost.
// If the proxy's node doesn't answer, e.g. as it is lost, the proxy and the servers on its
// node are skipped, which the warning tells: they only trust each other then.
func (s *Service) Delete(ctx context.Context, id string) (warning string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n, err := s.Get(ctx, id)
	if err != nil {
		return "", err
	}
	placed, err := s.datastores.Placed(ctx, id)
	if err != nil {
		return "", err
	}
	if len(placed) > 0 {
		return "", httpapi.Errorf(http.StatusConflict, "The network %q has databases. Delete its datastores first.", n.Name)
	}
	ctx = context.WithoutCancel(ctx)
	// offline tells, once a call to the proxy's node failed, that the node doesn't answer either.
	offline := false
	skipped := func(ref Ref) bool {
		offline = ref.NodeID == n.Proxy.NodeID && !s.reachable(ctx, ref.NodeID)
		return offline
	}
	operation.Step(ctx, "servers")
	for i, b := range n.Backends {
		operation.Count(ctx, int64(i), int64(len(n.Backends)), "servers")
		if offline && b.NodeID == n.Proxy.NodeID {
			continue
		}
		if err := s.leave(ctx, b); err != nil && !skipped(b.Ref) {
			return "", httpapi.Errorf(http.StatusBadGateway, "The network was not deleted, because %s could not be made standalone again: %s", b.Name, httpapi.Message(err))
		}
	}
	if !offline {
		if err := s.release(ctx, n, "proxy"); err != nil && !skipped(n.Proxy) {
			return "", httpapi.Errorf(http.StatusBadGateway, "The network was not deleted: %s", httpapi.Message(err))
		}
	}
	if _, err = s.db.ExecContext(ctx, `DELETE FROM networks WHERE id = ?`, n.ID); err != nil || !offline {
		return "", err
	}
	name := "the proxy's node"
	if nd, err := s.nodes.Get(ctx, n.Proxy.NodeID); err == nil {
		name = nd.Name
	}
	slog.Warn("A network was deleted without its proxy, whose node can't be reached", logging.Networks, "network", n.ID, logging.KeyNode, n.Proxy.NodeID)
	return fmt.Sprintf("%s can't be reached, so the proxy and the network's servers on it keep their settings and still trust each other. Once it is back, delete them or put them into a network again.", name), nil
}

// reachable reports whether the agent of a node answers, which tells a node that is offline,
// or removed, from one where a call failed.
func (s *Service) reachable(ctx context.Context, nodeID string) bool {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()
	conn, err := s.nodes.Conn(ctx, nodeID)
	if err == nil {
		_, err = noryxv1.NewNodeServiceClient(conn).GetInfo(ctx, &noryxv1.GetInfoRequest{})
	}
	return err == nil
}

// release takes the proxy out of a network, in the step of an operation: it loses Geyser and
// Floodgate, its backends and the forwarding, and keeps running. A deleted proxy needs nothing.
func (s *Service) release(ctx context.Context, n Network, step string) error {
	if n.BedrockPort != 0 {
		if err := ignoreMissing(s.removeBedrock(ctx, n)); err != nil {
			return err
		}
	}
	operation.Step(ctx, step)
	detach := &noryxv1.ConfigureNetworkRequest{Id: n.Proxy.ServerID, Forwarding: noryxv1.Forwarding_FORWARDING_NONE}
	if _, err := s.configure(ctx, n.Proxy, detach); ignoreMissing(err) != nil {
		return httpapi.Errorf(http.StatusBadGateway, "The proxy could not be configured: %s", httpapi.Message(err))
	}
	return nil
}

// CheckRemovable fails if a server is part of a network, which would break without it.
func (s *Service) CheckRemovable(ctx context.Context, nodeID, serverID string) error {
	n, err := s.find(ctx, serverID)
	if err != nil || n == nil || !n.has(Ref{nodeID, serverID}) {
		return err
	}
	return httpapi.Errorf(http.StatusConflict, "This server is part of the network %q. Remove it from the network first.", n.Name)
}

// ErrUnreachable is the code of the conflict of a server that would move to a node that
// doesn't reach the datastores of its network, which the client may confirm.
const ErrUnreachable = "datastores-unreachable"

// CheckMove fails if a server can't move to another node because its network forwards the
// legacy way and the operator didn't confirm that the servers on other nodes are protected,
// which the private network of the nodes only does for its members, or, unless confirmed,
// because the node doesn't reach the datastores of its network.
func (s *Service) CheckMove(ctx context.Context, serverID, from, to string, unreachable bool) error {
	n, err := s.find(ctx, serverID)
	if err != nil || n == nil {
		return err
	}
	n.moved(serverID, from, to)
	m, err := s.members(ctx)
	if err != nil {
		return err
	}
	if n.exposed(m) {
		return httpapi.Errorf(http.StatusConflict, "This server is part of the network %q, which forwards the legacy way. Confirm in the network that a firewall protects its servers on other nodes first.", n.Name)
	}
	placed, err := s.datastores.Placed(ctx, n.ID)
	if err != nil {
		return err
	}
	for nodeID := range placed {
		if nodeID != to && !m.private(nodeID, to) && !unreachable {
			return httpapi.Confirm(ErrUnreachable, "The network %q has databases on a node that the new node can't reach, as they aren't both in the private network of the nodes. The plugins of the server would lose them.", n.Name)
		}
	}
	// The proxy takes the Bedrock port of its network along.
	if n.Proxy.ServerID == serverID {
		return s.checkBedrock(ctx, to, n.BedrockPort, serverID)
	}
	return nil
}

// Move points the network of a server that moved to another node at its new place, and
// configures the network again, as the proxy reaches the server at another address. A
// server without a network needs nothing.
func (s *Service) Move(ctx context.Context, serverID, from, to string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var id string
	err := database.InTx(ctx, s.db, func(tx *sql.Tx) error {
		for _, query := range []string{
			`UPDATE networks SET proxy_node_id = ?1 WHERE proxy_node_id = ?2 AND proxy_server_id = ?3 RETURNING id`,
			`UPDATE network_backends SET node_id = ?1 WHERE node_id = ?2 AND server_id = ?3 RETURNING network_id`,
		} {
			if err := tx.QueryRowContext(ctx, query, to, from, serverID).Scan(&id); err != nil && !errors.Is(err, sql.ErrNoRows) {
				return err
			}
		}
		return nil
	})
	if err != nil || id == "" {
		return err
	}
	n, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	return s.apply(ctx, n, false)
}

// Reapply configures the network of a server again, if it is part of one, e.g. as the
// proxy reaches the server at its port, which changed.
func (s *Service) Reapply(ctx context.Context, serverID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	n, err := s.find(ctx, serverID)
	if err != nil || n == nil {
		return err
	}
	return s.apply(ctx, *n, false)
}

// ReapplyNode configures the networks again whose proxies reach servers of a node from
// another node, at the node's address, e.g. as it changed. It tells which ones failed.
func (s *Service) ReapplyNode(ctx context.Context, nodeID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	networks, err := s.List(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, n := range networks {
		if n.Proxy.NodeID == nodeID || !slices.ContainsFunc(n.Backends, func(b Backend) bool { return b.NodeID == nodeID }) {
			continue
		}
		if err := s.apply(ctx, n, false); err != nil {
			errs = append(errs, fmt.Errorf("%s: %s", n.Name, httpapi.Message(err)))
		}
	}
	return errors.Join(errs...)
}

// CheckLeave fails if a node can't leave the private network of the nodes, as a network
// with legacy forwarding would then reach servers at ports that no firewall protects.
func (s *Service) CheckLeave(ctx context.Context, nodeID string) error {
	m, err := s.members(ctx)
	if err != nil {
		return err
	}
	without := maps.Clone(m)
	delete(without, nodeID)
	networks, err := s.List(ctx)
	if err != nil {
		return err
	}
	for _, n := range networks {
		if n.exposed(without) && !n.exposed(m) {
			return httpapi.Errorf(http.StatusConflict, "The network %q forwards the legacy way and would reach servers outside of the private network. Confirm in the network that a firewall protects them first.", n.Name)
		}
	}
	return nil
}

// ApplyAcross configures the networks again that have servers or datastores on the node and
// on other nodes, e.g. as it joined or left the private network of the nodes. It tells which
// failed.
func (s *Service) ApplyAcross(ctx context.Context, nodeID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	networks, err := s.List(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, n := range networks {
		nodes := []string{n.Proxy.NodeID}
		for _, b := range n.Backends {
			nodes = append(nodes, b.NodeID)
		}
		placed, err := s.datastores.Placed(ctx, n.ID)
		if err != nil {
			return err
		}
		nodes = slices.AppendSeq(nodes, maps.Keys(placed))
		if !slices.Contains(nodes, nodeID) || !slices.ContainsFunc(nodes, func(id string) bool { return id != nodeID }) {
			continue
		}
		if err := s.apply(ctx, n, false); err != nil {
			errs = append(errs, fmt.Errorf("%s: %s", n.Name, httpapi.Message(err)))
		}
	}
	if err := errors.Join(errs...); err != nil {
		return httpapi.Errorf(http.StatusBadGateway, "%s", strings.ReplaceAll(err.Error(), "\n", " · "))
	}
	return nil
}

// members returns the nodes in the private network, by node.
func (s *Service) members(ctx context.Context) (members, error) {
	return s.overlay.ByNode(ctx)
}

// Configure configures all servers of a network again, e.g. as a datastore joined it.
func (s *Service) Configure(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	n, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	return s.apply(ctx, n, false)
}

// Apply configures all servers of a network again, e.g. after a node was offline, and
// updates Geyser and Floodgate.
func (s *Service) Apply(ctx context.Context, id string) (Network, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n, err := s.Get(ctx, id)
	if err != nil {
		return n, err
	}
	return n, s.apply(ctx, n, true)
}

// validate checks the settings of a network, whose proxy reaches servers of the members of
// the private network over it, and puts them into their canonical form.
func (n *Network) validate(m members) error {
	bungee := noryxv1.ParseServerType(n.ProxyType).Bungee()
	n.Name = strings.TrimSpace(n.Name)
	switch {
	case n.Name == "" || len(n.Name) > 64:
		return httpapi.Errorf(http.StatusBadRequest, "Enter a name with up to 64 characters.")
	case n.Forwarding != Modern && n.Forwarding != Legacy:
		return httpapi.Errorf(http.StatusBadRequest, "Choose modern or legacy forwarding.")
	case bungee && n.Forwarding != Legacy:
		return httpapi.Errorf(http.StatusBadRequest, "BungeeCord only forwards the legacy way.")
	case len(n.Backends) == 0:
		return httpapi.Errorf(http.StatusBadRequest, "A network needs at least one server. Delete the network instead.")
	case len(n.Backends) > maxBackends || len(n.ForcedHosts) > maxForcedHosts:
		return httpapi.Errorf(http.StatusBadRequest, "A network can have up to %d servers and %d host names.", maxBackends, maxForcedHosts)
	case len(n.Try) == 0:
		return httpapi.Errorf(http.StatusBadRequest, "Choose at least one server that players join.")
	case n.exposed(m):
		return errExposed
	case n.BedrockPort != 0 && (n.BedrockPort < minBedrockPort || n.BedrockPort > maxBedrockPort):
		return httpapi.Errorf(http.StatusBadRequest, "Choose a Bedrock port from %d to %d.", minBedrockPort, maxBedrockPort)
	}
	names := map[string]bool{}
	for i, b := range n.Backends {
		switch {
		case !noryxv1.ValidBackendName(b.Name):
			return httpapi.Errorf(http.StatusBadRequest, "%q can't name a server: use up to 32 lower-case letters, digits, - and _.", b.Name)
		case names[b.Name]:
			return httpapi.Errorf(http.StatusBadRequest, "Two servers are named %q.", b.Name)
		case slices.ContainsFunc(n.Backends[:i], b.same) || b.Ref == n.Proxy:
			return httpapi.Errorf(http.StatusBadRequest, "A server can only be part of the network once.")
		case len(b.Motd) > maxMotd || strings.ContainsFunc(b.Motd, unicode.IsControl):
			return httpapi.Errorf(http.StatusBadRequest, "The MOTD of %s may have up to %d characters, without line breaks.", b.Name, maxMotd)
		}
		names[b.Name] = true
		if !bungee {
			n.Backends[i].Restricted, n.Backends[i].Motd = false, ""
		}
	}
	if err := checkNames(n.Try, names, "players join"); err != nil {
		return err
	}
	hosts := map[string]bool{}
	for i, h := range n.ForcedHosts {
		h.Host = strings.ToLower(strings.TrimSpace(h.Host))
		switch {
		case !hostPattern.MatchString(h.Host) || len(h.Host) > 253:
			return httpapi.Errorf(http.StatusBadRequest, "%q is no host name, such as survival.example.com.", h.Host)
		case hosts[h.Host]:
			return httpapi.Errorf(http.StatusBadRequest, "The host name %s is there twice.", h.Host)
		case len(h.Servers) == 0 || bungee && len(h.Servers) > 1:
			return httpapi.Errorf(http.StatusBadRequest, "Choose %s for the host name %s.", map[bool]string{true: "one server", false: "at least one server"}[bungee], h.Host)
		}
		if err := checkNames(h.Servers, names, h.Host); err != nil {
			return err
		}
		hosts[h.Host], n.ForcedHosts[i].Host = true, h.Host
	}
	if n.ForcedHosts == nil {
		n.ForcedHosts = []ForcedHost{}
	}
	return nil
}

// exposed reports whether backends on other nodes than the proxy's can be reached by others
// than the proxy, which legacy forwarding can't tell apart from it: unless a firewall or the
// private network of the nodes protects them.
func (n *Network) exposed(m members) bool {
	return n.Forwarding == Legacy && !n.Firewalled &&
		slices.ContainsFunc(n.Backends, func(b Backend) bool { return b.NodeID != n.Proxy.NodeID && !m.private(n.Proxy.NodeID, b.NodeID) })
}

// forwardingOr returns the chosen forwarding, or else the default of a type of proxy:
// modern for Velocity, legacy for BungeeCord, which only knows that.
func forwardingOr(chosen string, proxy noryxv1.ServerType) string {
	if proxy.Bungee() {
		return cmp.Or(chosen, Legacy)
	}
	return cmp.Or(chosen, Modern)
}

// checkNames fails if a list of backends names one that doesn't exist, or one twice.
func checkNames(list []string, names map[string]bool, what string) error {
	seen := map[string]bool{}
	for _, name := range list {
		if !names[name] || seen[name] {
			return httpapi.Errorf(http.StatusBadRequest, "The servers for %s name %q, which isn't a server of the network or is there twice.", what, name)
		}
		seen[name] = true
	}
	return nil
}

// SendCommand returns the console command of the proxy that sends a player to a backend, and
// false if the proxy would read the name as other players too: both proxies read all and
// current so, and BungeeCord reads the name of a server as its players.
func (n *Network) SendCommand(player, backend string) (string, bool) {
	others := strings.EqualFold(player, "all") || strings.EqualFold(player, "current") ||
		noryxv1.ParseServerType(n.ProxyType).Bungee() && slices.ContainsFunc(n.Backends, func(b Backend) bool { return strings.EqualFold(b.Name, player) })
	return "send " + player + " " + backend, !others
}

func (b Backend) same(o Backend) bool { return b.Ref == o.Ref }

func (n *Network) has(ref Ref) bool {
	return n.Proxy == ref || slices.ContainsFunc(n.Backends, func(b Backend) bool { return b.Ref == ref })
}

// checkBackends checks that servers are different game servers of the network.
func (n *Network) checkBackends(servers []Ref) error {
	for i, ref := range servers {
		if slices.Contains(servers[:i], ref) || !slices.ContainsFunc(n.Backends, func(b Backend) bool { return b.Ref == ref }) {
			return httpapi.Errorf(http.StatusBadRequest, "Choose different game servers of the network.")
		}
	}
	return nil
}

// names returns the names of game servers of the network.
func (n *Network) names(servers []Ref) []string {
	var names []string
	for _, b := range n.Backends {
		if slices.Contains(servers, b.Ref) {
			names = append(names, b.Name)
		}
	}
	return names
}

// moved changes the node of a server of the network.
func (n *Network) moved(serverID, from, to string) {
	if n.Proxy == (Ref{from, serverID}) {
		n.Proxy.NodeID = to
	}
	for i, b := range n.Backends {
		if b.Ref == (Ref{from, serverID}) {
			n.Backends[i].NodeID = to
		}
	}
}

// apply configures the backends first, so that they accept the proxy before it sends
// players to them, then the proxy. Game servers only restart if their configuration
// changed, the proxy reloads its configuration. With refresh, the proxy gets the newest
// Geyser and Floodgate, which it restarts for; other changes don't disconnect players.
func (s *Service) apply(ctx context.Context, n Network, refresh bool) error {
	ctx = context.WithoutCancel(ctx) // finish even if the client goes away
	err := s.configureAll(ctx, n, refresh)
	if f := (*failed)(nil); errors.As(err, &f) {
		err = httpapi.Errorf(http.StatusBadGateway, "The change was saved, but %s Apply the network again once all nodes are online.", f)
	}
	var applyError any // NULL once applied
	if err != nil {
		applyError = httpapi.Message(err)
	}
	if _, dbErr := s.db.ExecContext(ctx, `UPDATE networks SET apply_error = ? WHERE id = ?`, applyError, n.ID); dbErr != nil {
		slog.Warn("Can't remember how applying a network ended", logging.Networks, "network", n.ID, "err", dbErr)
	}
	return err
}

// configureAll configures the servers of a network, which reach its datastores on their
// nodes, and publishes the datastores for the servers of other nodes.
func (s *Service) configureAll(ctx context.Context, n Network, refresh bool) error {
	m, err := s.members(ctx)
	if err != nil {
		return err
	}
	if n.placed, err = s.datastores.Placed(ctx, n.ID); err != nil {
		return err
	}
	if err := s.configureServers(ctx, n, m, refresh); err != nil || len(n.placed) == 0 {
		return err
	}
	nodes := []string{n.Proxy.NodeID}
	for _, b := range n.Backends {
		nodes = append(nodes, b.NodeID)
	}
	if err := s.datastores.Publish(ctx, n.ID, nodes, m); err != nil {
		return applyFailed("the datastores", err)
	}
	return nil
}

// configureServers configures the backends of a network and then its proxy.
func (s *Service) configureServers(ctx context.Context, n Network, m members, refresh bool) error {
	operation.Step(ctx, "servers")
	for i, b := range n.Backends {
		operation.Count(ctx, int64(i), int64(len(n.Backends)), "servers")
		if err := s.join(ctx, n, b, m); err != nil {
			return applyFailed(b.Name, err)
		}
	}
	backends := make([]*noryxv1.NetworkBackend, 0, len(n.Backends))
	for _, b := range n.Backends {
		target, err := s.target(ctx, n.Proxy, b, m)
		if err != nil {
			return applyFailed(b.Name, err)
		}
		backends = append(backends, target)
	}
	hosts := make([]*noryxv1.ForcedHost, 0, len(n.ForcedHosts))
	for _, h := range n.ForcedHosts {
		hosts = append(hosts, &noryxv1.ForcedHost{Host: h.Host, Servers: h.Servers})
	}
	req := n.request(n.Proxy, m)
	req.Backends, req.Try, req.ForcedHosts, req.BedrockPort = backends, n.Try, hosts, n.BedrockPort
	var plugins bool
	if n.BedrockPort != 0 && refresh {
		var err error
		if plugins, err = s.provideBedrock(ctx, n); err != nil {
			return applyFailed("the proxy", err)
		}
	}
	operation.Step(ctx, "proxy")
	res, err := s.configure(ctx, n.Proxy, req)
	if err != nil {
		return applyFailed("the proxy", err)
	}
	proxy, err := s.server(ctx, n.Proxy)
	switch {
	case err != nil:
		return applyFailed("the proxy", err)
	case plugins && !res.GetRestarted() && proxy.GetState() == noryxv1.ServerState_SERVER_STATE_RUNNING:
		operation.Step(ctx, "proxy-restart")
		if err := s.restart(ctx, []Backend{{Ref: n.Proxy, Name: "The proxy"}}); err != nil {
			return applyFailed("the proxy", err)
		}
	}
	return nil
}

// request returns the configuration of a server of the network. A backend that its proxy
// reaches over the private network publishes its port there, only for the proxy's node with
// its current key.
func (n *Network) request(ref Ref, m members) *noryxv1.ConfigureNetworkRequest {
	req := &noryxv1.ConfigureNetworkRequest{Id: ref.ServerID, Forwarding: noryxv1.Forwarding_FORWARDING_MODERN, ForwardingSecret: n.secret}
	if n.Forwarding == Legacy {
		req.Forwarding, req.ForwardingSecret = noryxv1.Forwarding_FORWARDING_LEGACY, ""
	}
	req.ProxyOnNode = ref != n.Proxy && ref.NodeID == n.Proxy.NodeID
	req.BedrockPlayers = ref != n.Proxy && n.BedrockPort != 0
	if ref != n.Proxy && m.private(n.Proxy.NodeID, ref.NodeID) {
		req.OverlayClient, req.OverlayClientKey = m[n.Proxy.NodeID].Address, m[n.Proxy.NodeID].PublicKey
	}
	req.Datastores = n.placed[ref.NodeID]
	return req
}

// join gives a backend its role in the network, after installing the forwarding mod it
// needs.
func (s *Service) join(ctx context.Context, n Network, b Backend, m members) error {
	srv, err := s.server(ctx, b.Ref)
	if err != nil {
		return err
	}
	if project, ok := mods[srv.GetType()]; ok {
		if err := s.mods.Ensure(ctx, plugin.Ref(b.Ref), project); err != nil {
			return fmt.Errorf("its forwarding mod could not be installed: %w", err)
		}
	}
	_, err = s.configure(ctx, b.Ref, n.request(b.Ref, m))
	return err
}

// leave makes a backend standalone again and removes its forwarding mod. A deleted server
// needs nothing.
func (s *Service) leave(ctx context.Context, b Backend) error {
	srv, err := s.server(ctx, b.Ref)
	if gone(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if project, ok := mods[srv.GetType()]; ok {
		if err := s.mods.Uninstall(ctx, plugin.Ref(b.Ref), project); err != nil {
			return fmt.Errorf("its forwarding mod could not be removed: %w", err)
		}
	}
	_, err = s.configure(ctx, b.Ref, &noryxv1.ConfigureNetworkRequest{Id: b.ServerID, Forwarding: noryxv1.Forwarding_FORWARDING_NONE})
	return ignoreMissing(err)
}

// target tells the proxy how to reach a backend: on its own node by server ID, as the
// agent knows the local route, otherwise at the node's address in the private network or,
// for nodes outside, the host of its agent's address, and the server's port.
func (s *Service) target(ctx context.Context, proxy Ref, b Backend, m members) (*noryxv1.NetworkBackend, error) {
	target := &noryxv1.NetworkBackend{Name: b.Name, Restricted: b.Restricted, Motd: b.Motd}
	if b.NodeID == proxy.NodeID {
		target.Target = &noryxv1.NetworkBackend_ServerId{ServerId: b.ServerID}
		return target, nil
	}
	srv, err := s.server(ctx, b.Ref)
	if err != nil {
		return nil, err
	}
	host := m[b.NodeID].Address
	if !m.private(proxy.NodeID, b.NodeID) {
		n, err := s.nodes.Get(ctx, b.NodeID)
		if err != nil {
			return nil, err
		}
		if host, _, err = net.SplitHostPort(n.Address); err != nil {
			return nil, err
		}
	}
	target.Target = &noryxv1.NetworkBackend_Address{Address: net.JoinHostPort(host, strconv.Itoa(int(srv.GetPort())))}
	return target, nil
}

func (s *Service) configure(ctx context.Context, ref Ref, req *noryxv1.ConfigureNetworkRequest) (*noryxv1.ConfigureNetworkResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, configureTimeout)
	defer cancel()
	conn, err := s.nodes.Conn(ctx, ref.NodeID)
	if err != nil {
		return nil, err
	}
	return noryxv1.NewServerServiceClient(conn).ConfigureNetwork(ctx, req)
}

var errServerNotFound = httpapi.Errorf(http.StatusNotFound, "Server not found.")

// gone reports whether an error tells that a server doesn't exist, e.g. as it was deleted.
func gone(err error) bool {
	return status.Code(err) == codes.NotFound || errors.Is(err, errServerNotFound)
}

// server looks up a server on its node.
func (s *Service) server(ctx context.Context, ref Ref) (*noryxv1.Server, error) {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()
	conn, err := s.nodes.Conn(ctx, ref.NodeID)
	if err != nil {
		return nil, err
	}
	res, err := noryxv1.NewServerServiceClient(conn).ListServers(ctx, &noryxv1.ListServersRequest{})
	if err != nil {
		return nil, err
	}
	i := slices.IndexFunc(res.GetServers(), func(srv *noryxv1.Server) bool { return srv.GetId() == ref.ServerID })
	if i < 0 {
		return nil, errServerNotFound
	}
	return res.GetServers()[i], nil
}

// backend checks that a server can join a network with the given forwarding.
func (s *Service) backend(ctx context.Context, ref Ref, forwarding string) (*noryxv1.Server, error) {
	srv, err := s.server(ctx, ref)
	if err != nil {
		return nil, err
	}
	switch typ := srv.GetType(); {
	case typ.Bedrock():
		return nil, httpapi.Errorf(http.StatusBadRequest, "Bedrock servers can't be part of a network. Bedrock players join a network through Geyser on its proxy instead.")
	case !typ.Backend():
		return nil, httpapi.Errorf(http.StatusBadRequest, "Only Paper and its forks, Fabric, Quilt, Forge and NeoForge servers can verify the players a proxy forwards.")
	case typ.Fabric() && forwarding != Modern:
		return nil, errFabricMode
	}
	return srv, nil
}

// find returns the network a server is part of, or nil.
func (s *Service) find(ctx context.Context, serverID string) (*Network, error) {
	var id string
	err := s.db.QueryRowContext(ctx, `
		SELECT id FROM networks WHERE proxy_server_id = ?1
		UNION SELECT network_id FROM network_backends WHERE server_id = ?1`, serverID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	n, err := s.Get(ctx, id)
	return &n, err
}

// load reads one network, or all networks when id is empty.
func (s *Service) load(ctx context.Context, id string) ([]Network, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, name, proxy_node_id, proxy_server_id, proxy_type, forwarding, firewalled, try, forced_hosts,
		       bedrock_port, COALESCE(apply_error, ''), forwarding_secret, created_at
		FROM networks WHERE ? IN ('', id) ORDER BY name`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	networks := []Network{}
	index := map[string]int{}
	for rows.Next() {
		n := Network{Backends: []Backend{}}
		var try, hosts string
		var createdAt int64
		if err := rows.Scan(&n.ID, &n.Name, &n.Proxy.NodeID, &n.Proxy.ServerID, &n.ProxyType, &n.Forwarding, &n.Firewalled,
			&try, &hosts, &n.BedrockPort, &n.ApplyError, &n.secret, &createdAt); err != nil {
			return nil, err
		}
		if err := errors.Join(json.Unmarshal([]byte(try), &n.Try), json.Unmarshal([]byte(hosts), &n.ForcedHosts)); err != nil {
			return nil, err
		}
		n.CreatedAt = time.Unix(createdAt, 0)
		index[n.ID] = len(networks)
		networks = append(networks, n)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	backends, err := s.db.QueryContext(ctx, `
		SELECT network_id, node_id, server_id, name, restricted, motd FROM network_backends
		WHERE ? IN ('', network_id) ORDER BY position`, id)
	if err != nil {
		return nil, err
	}
	defer backends.Close()
	for backends.Next() {
		var networkID string
		var b Backend
		if err := backends.Scan(&networkID, &b.NodeID, &b.ServerID, &b.Name, &b.Restricted, &b.Motd); err != nil {
			return nil, err
		}
		if i, ok := index[networkID]; ok {
			networks[i].Backends = append(networks[i].Backends, b)
		}
	}
	return networks, backends.Err()
}

// save stores the settings and backends of a network that exists.
func save(ctx context.Context, tx *sql.Tx, n Network) error {
	try, err := json.Marshal(n.Try)
	if err != nil {
		return err
	}
	hosts, err := json.Marshal(n.ForcedHosts)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE networks SET name = ?, forwarding = ?, firewalled = ?, try = ?, forced_hosts = ?, bedrock_port = ? WHERE id = ?`,
		n.Name, n.Forwarding, n.Firewalled, try, hosts, n.BedrockPort, n.ID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM network_backends WHERE network_id = ?`, n.ID); err != nil {
		return err
	}
	for position, b := range n.Backends {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO network_backends (network_id, node_id, server_id, name, position, restricted, motd) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			n.ID, b.NodeID, b.ServerID, b.Name, position, b.Restricted, b.Motd); err != nil {
			return err
		}
	}
	return nil
}

// backendName derives the name players use for a server, e.g. "Survival 2" becomes
// "survival-2", and keeps it unique within the network.
func backendName(serverName string, existing []Backend) string {
	slug := nonSlug.ReplaceAllString(strings.ToLower(serverName), "-")
	base := cmp.Or(strings.Trim(slug[:min(len(slug), 28)], "-"), "server")
	if base == "try" { // reserved for Velocity's list of servers to try
		base = "try-server"
	}
	name := base
	for i := 2; slices.ContainsFunc(existing, func(b Backend) bool { return b.Name == name }); i++ {
		name = fmt.Sprintf("%s-%d", base, i)
	}
	return name
}

func conflict(err error, name string) error {
	switch msg := err.Error(); {
	case strings.Contains(msg, "networks.name"):
		return httpapi.Errorf(http.StatusConflict, "A network named %q already exists.", name)
	case strings.Contains(msg, "proxy_server_id"):
		return httpapi.Errorf(http.StatusConflict, "This proxy already serves a network.")
	case strings.Contains(msg, "network_backends.server_id"):
		return httpapi.Errorf(http.StatusConflict, "A server already belongs to another network.")
	}
	return err
}

// failed tells which part of a network could not be configured, and why.
type failed struct {
	what string
	err  error
}

func (f *failed) Error() string {
	return fmt.Sprintf("%s could not be configured: %s", f.what, strings.TrimSuffix(httpapi.Message(f.err), ".")+".")
}

func applyFailed(what string, err error) error { return &failed{what, err} }

// message returns the message of an error of an agent or of the master.
// ignoreMissing ignores that the agent does not know a server, e.g. as it was deleted.
func ignoreMissing(err error) error {
	if status.Code(err) == codes.NotFound {
		return nil
	}
	return err
}
