// Package overlay manages the private WireGuard network of the nodes, over which proxies
// reach the backends of other nodes. The master assigns each member an address, gives every
// member the others as peers and keeps them configured; the private keys never leave the
// nodes, and only nodes whose administrator allowed it join.
package overlay

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	"github.com/QwikByte/noryx/internal/logging"
	"github.com/QwikByte/noryx/internal/master/database"
	"github.com/QwikByte/noryx/internal/master/httpapi"
	"github.com/QwikByte/noryx/internal/master/node"
)

const (
	callTimeout = 10 * time.Second
	// reconcileEvery lets members that were offline catch up.
	reconcileEvery = 5 * time.Minute
	// stale is how long peers may go without a handshake, which keepalives renew every two minutes.
	stale = 5 * time.Minute
)

var (
	errNotMember = httpapi.Errorf(http.StatusNotFound, "The node isn't part of the private network.")
	hostname     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.-]{0,252}$`)
	private      = []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8"), netip.MustParsePrefix("172.16.0.0/12"), netip.MustParsePrefix("192.168.0.0/16")}
)

// Settings apply to all members.
type Settings struct {
	// Subnet is the private IPv4 range of the network, e.g. 10.213.0.0/24. It only changes
	// while no node is a member.
	Subnet string `json:"subnet"`
	// Port is the UDP port at which the members listen.
	Port uint32 `json:"port"`
	MTU  uint32 `json:"mtu"`
}

// Member is a node of the network.
type Member struct {
	NodeID    string `json:"nodeId"`
	Address   string `json:"address"` // e.g. 10.213.0.3
	PublicKey string `json:"publicKey"`
	// Endpoint is host:port at which the others reach it; empty for the host of its agent's
	// address and the network's port.
	Endpoint string    `json:"endpoint"`
	JoinedAt time.Time `json:"joinedAt"`
	// Problem tells why it was last configured in vain, and Unreached names the members it
	// had no handshake with for a while, e.g. as a firewall blocks the port.
	Problem   string   `json:"problem,omitempty"`
	Unreached []string `json:"unreached,omitempty"`
}

// Nodes are the nodes that join the network.
type Nodes interface {
	Get(ctx context.Context, id string) (node.Node, error)
	Conn(ctx context.Context, id string) (grpc.ClientConnInterface, error)
}

type Service struct {
	db    *sql.DB
	nodes Nodes
	mu    sync.Mutex // one change at a time

	healthMu sync.Mutex
	health   map[string]Member // the problems found when the members were configured last
}

func NewService(db *sql.DB, nodes Nodes) *Service {
	return &Service{db: db, nodes: nodes, health: map[string]Member{}}
}

func (s *Service) Settings(ctx context.Context) (Settings, error) {
	var st Settings
	err := s.db.QueryRowContext(ctx, `SELECT subnet, port, mtu FROM overlay_settings`).Scan(&st.Subnet, &st.Port, &st.MTU)
	return st, err
}

// UpdateSettings changes the settings and configures the members with them.
func (s *Service) UpdateSettings(ctx context.Context, next Settings) (Settings, error) {
	subnet, err := netip.ParsePrefix(strings.TrimSpace(next.Subnet))
	switch {
	case err != nil || !subnet.Addr().Is4() || subnet != subnet.Masked() || subnet.Bits() < 16 || subnet.Bits() > 29 ||
		!slices.ContainsFunc(private, func(p netip.Prefix) bool { return p.Bits() <= subnet.Bits() && p.Contains(subnet.Addr()) }):
		return next, httpapi.Errorf(http.StatusBadRequest, "Enter a private IPv4 range from /16 to /29, e.g. 10.213.0.0/24.")
	case next.Port < 1024 || next.Port > 65535:
		return next, httpapi.Errorf(http.StatusBadRequest, "Enter a UDP port from 1024 to 65535.")
	case next.MTU < 1280 || next.MTU > 1500:
		return next, httpapi.Errorf(http.StatusBadRequest, "Enter an MTU from 1280 to 1500.")
	}
	next.Subnet = subnet.String()
	s.mu.Lock()
	defer s.mu.Unlock()
	current, err := s.Settings(ctx)
	if err != nil {
		return next, err
	}
	members, err := s.Members(ctx)
	if err != nil {
		return next, err
	}
	if next.Subnet != current.Subnet && len(members) > 0 {
		return next, httpapi.Errorf(http.StatusConflict, "The range of the private network only changes while no node is part of it.")
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE overlay_settings SET subnet = ?, port = ?, mtu = ?`, next.Subnet, next.Port, next.MTU); err != nil {
		return next, err
	}
	s.reconcile(context.WithoutCancel(ctx))
	return next, nil
}

// Members returns the members with the problems found when they were configured last.
func (s *Service) Members(ctx context.Context) ([]Member, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT node_id, address, public_key, endpoint, joined_at FROM overlay_nodes ORDER BY joined_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	members := []Member{}
	for rows.Next() {
		var m Member
		var joined int64
		if err := rows.Scan(&m.NodeID, &m.Address, &m.PublicKey, &m.Endpoint, &joined); err != nil {
			return nil, err
		}
		m.JoinedAt = time.Unix(joined, 0)
		s.healthMu.Lock()
		m.Problem, m.Unreached = s.health[m.NodeID].Problem, s.health[m.NodeID].Unreached
		s.healthMu.Unlock()
		members = append(members, m)
	}
	return members, rows.Err()
}

// ByNode returns the members, by node: with their addresses in the network and their public
// keys, which the others bind the access they give a member to.
func (s *Service) ByNode(ctx context.Context) (map[string]Member, error) {
	members, err := s.Members(ctx)
	byNode := make(map[string]Member, len(members))
	for _, m := range members {
		byNode[m.NodeID] = m
	}
	return byNode, err
}

func (s *Service) member(ctx context.Context, nodeID string) (Member, error) {
	members, err := s.Members(ctx)
	if i := slices.IndexFunc(members, func(m Member) bool { return m.NodeID == nodeID }); err == nil && i >= 0 {
		return members[i], nil
	}
	return Member{}, cmp.Or(err, errNotMember)
}

// Join adds a node, whose administrator allowed it, with the lowest free address. The others
// reach it at endpoint, or else at the host of its agent's address. Joining restarts nothing:
// networks move to the network when they are applied again.
func (s *Service) Join(ctx context.Context, nodeID, endpoint string) (Member, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n, err := s.nodes.Get(ctx, nodeID)
	if err != nil {
		return Member{}, err
	}
	if err := checkEndpoint(endpoint, n); err != nil {
		return Member{}, err
	}
	res, err := s.get(ctx, nodeID)
	switch {
	case err != nil:
		return Member{}, err
	case !res.GetAllowed():
		return Member{}, httpapi.Errorf(http.StatusConflict, "The administrator of %s hasn't allowed it to join yet. Run on the node: noryx-agent overlay allow", n.Name)
	case res.GetUnsupported() != "":
		return Member{}, httpapi.Errorf(http.StatusConflict, "%s can't join: %s", n.Name, res.GetUnsupported())
	}
	settings, err := s.Settings(ctx)
	if err != nil {
		return Member{}, err
	}
	members, err := s.Members(ctx)
	if err != nil {
		return Member{}, err
	}
	m := Member{NodeID: nodeID, PublicKey: res.GetPublicKey(), Endpoint: endpoint, JoinedAt: time.Now()}
	if m.Address = free(netip.MustParsePrefix(settings.Subnet), members); m.Address == "" {
		return Member{}, httpapi.Errorf(http.StatusConflict, "The private network %s has no free address. Remove a node or choose a larger range.", settings.Subnet)
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO overlay_nodes (node_id, address, public_key, endpoint, joined_at) VALUES (?, ?, ?, ?, ?)`,
		m.NodeID, m.Address, m.PublicKey, m.Endpoint, m.JoinedAt.Unix()); err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return Member{}, httpapi.Errorf(http.StatusConflict, "%s is part of the private network already, or another node has its key.", n.Name)
		}
		return Member{}, err
	}
	// A node that can't take the configuration, e.g. as the range overlaps its own networks, doesn't join.
	ctx = context.WithoutCancel(ctx)
	if err := s.configure(ctx, settings, append(members, m), m, nil); err != nil {
		_, dbErr := s.db.ExecContext(ctx, `DELETE FROM overlay_nodes WHERE node_id = ?`, nodeID)
		return Member{}, errors.Join(httpapi.Errorf(http.StatusConflict, "%s can't join: %s", n.Name, httpapi.Message(err)), dbErr)
	}
	s.reconcile(ctx)
	return s.member(ctx, nodeID)
}

// free returns the lowest host address of subnet that no member has, or "" if none is free.
func free(subnet netip.Prefix, members []Member) string {
	for a, n := subnet.Addr().Next(), 1<<(32-subnet.Bits())-2; n > 0; a, n = a.Next(), n-1 {
		if !slices.ContainsFunc(members, func(m Member) bool { return m.Address == a.String() }) {
			return a.String()
		}
	}
	return ""
}

// checkEndpoint fails unless the others reach a node at endpoint, or at the host of its
// agent's address if endpoint is empty. That is no loopback address, e.g. of a node on the
// master's machine.
func checkEndpoint(endpoint string, n node.Node) error {
	if endpoint != "" {
		host, port, err := net.SplitHostPort(endpoint)
		p, portErr := strconv.ParseUint(port, 10, 16)
		if err != nil || portErr != nil || p == 0 || !hostname.MatchString(host) && net.ParseIP(host) == nil {
			return httpapi.Errorf(http.StatusBadRequest, "Enter where the other nodes reach %s as host:port, e.g. 203.0.113.10:51820.", n.Name)
		}
		return nil
	}
	host, _, _ := net.SplitHostPort(n.Address)
	if ip, err := netip.ParseAddr(host); host == "localhost" || err == nil && (ip.IsLoopback() || ip.IsUnspecified()) {
		return httpapi.Errorf(http.StatusBadRequest, "The other nodes can't reach %s at %s. Enter the address at which they reach it.", n.Name, host)
	}
	return nil
}

// SetEndpoint changes where the others reach a member.
func (s *Service) SetEndpoint(ctx context.Context, nodeID, endpoint string) (Member, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n, err := s.nodes.Get(ctx, nodeID)
	if err == nil {
		err = checkEndpoint(endpoint, n)
	}
	if err != nil {
		return Member{}, err
	}
	res, err := s.db.ExecContext(ctx, `UPDATE overlay_nodes SET endpoint = ? WHERE node_id = ?`, endpoint, nodeID)
	if err == nil && database.RowsAffected(res) == 0 {
		err = errNotMember
	}
	if err != nil {
		return Member{}, err
	}
	s.reconcile(context.WithoutCancel(ctx))
	return s.member(ctx, nodeID)
}

// Rotate replaces the key of a member. Until the others have the new public key, which
// takes a few seconds, they don't reach it.
func (s *Service) Rotate(ctx context.Context, nodeID string) (Member, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.member(ctx, nodeID); err != nil {
		return Member{}, err
	}
	var res *noryxv1.RotateOverlayKeyResponse
	err := s.call(ctx, nodeID, func(ctx context.Context, c noryxv1.OverlayServiceClient) (err error) {
		res, err = c.RotateOverlayKey(ctx, &noryxv1.RotateOverlayKeyRequest{})
		return err
	})
	if err != nil {
		return Member{}, err
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE overlay_nodes SET public_key = ? WHERE node_id = ?`, res.GetPublicKey(), nodeID); err != nil {
		return Member{}, err
	}
	s.reconcile(context.WithoutCancel(ctx))
	return s.member(ctx, nodeID)
}

// Leave removes a member: the others drop it as peer, and it removes its interface if it is
// reachable. The networks that reached its servers over the network must be applied again.
func (s *Service) Leave(ctx context.Context, nodeID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	res, err := s.db.ExecContext(ctx, `DELETE FROM overlay_nodes WHERE node_id = ?`, nodeID)
	if err == nil && database.RowsAffected(res) == 0 {
		err = errNotMember
	}
	if err != nil {
		return err
	}
	ctx = context.WithoutCancel(ctx)
	err = s.call(ctx, nodeID, func(ctx context.Context, c noryxv1.OverlayServiceClient) error {
		_, err := c.LeaveOverlay(ctx, &noryxv1.LeaveOverlayRequest{})
		return err
	})
	if err != nil {
		slog.Warn("A node that left the private network kept its interface, but no peers", logging.Nodes, logging.KeyNode, nodeID, "err", err)
	}
	s.reconcile(ctx)
	return nil
}

// Run configures the members every few minutes, so that those that were offline catch up.
func (s *Service) Run(ctx context.Context) {
	for {
		s.Reconcile(ctx)
		select {
		case <-ctx.Done():
			return
		case <-time.After(reconcileEvery):
		}
	}
}

// Reconcile configures every member with its address and the others as peers, e.g. after a
// node was removed or its address changed. The problems of members are kept for the panel.
func (s *Service) Reconcile(ctx context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reconcile(ctx)
}

func (s *Service) reconcile(ctx context.Context) {
	settings, err := s.Settings(ctx)
	members, membersErr := s.Members(ctx)
	if err = cmp.Or(err, membersErr); err != nil {
		slog.Error("Can't configure the private network of the nodes", logging.Nodes, "err", err)
		return
	}
	reports, errs := s.reports(ctx, members)
	health := make([]Member, len(members))
	for i, err := range errs {
		if err != nil {
			health[i].Problem = httpapi.Message(err)
		}
	}
	// A member whose agent lost its key, e.g. as its data was restored, made a new one.
	for i, m := range members {
		if key := reports[m.NodeID].GetPublicKey(); key != "" && key != m.PublicKey {
			if _, err := s.db.ExecContext(ctx, `UPDATE overlay_nodes SET public_key = ? WHERE node_id = ?`, key, m.NodeID); err == nil {
				members[i].PublicKey = key
			}
		}
	}
	for i, m := range members {
		health[i].Unreached = unreached(reports[m.NodeID], m, members)
	}
	each(members, func(i int, m Member) {
		if err := s.configure(ctx, settings, members, m, reports); err != nil && health[i].Problem == "" {
			health[i].Problem = httpapi.Message(err)
		}
	})
	s.healthMu.Lock()
	defer s.healthMu.Unlock()
	next := map[string]Member{}
	for i, m := range members {
		if health[i].Problem != "" && s.health[m.NodeID].Problem == "" {
			slog.Warn("A node of the private network could not be configured", logging.Nodes, logging.KeyNode, m.NodeID, "err", health[i].Problem)
		}
		next[m.NodeID] = health[i]
	}
	s.health = next
}

// unreached returns the members with which m had no handshake for a while, among those that
// joined before that.
func unreached(res *noryxv1.GetOverlayResponse, m Member, members []Member) []string {
	before := time.Now().Add(-stale)
	if m.JoinedAt.After(before) {
		return nil
	}
	var nodes []string
	for _, peer := range members {
		i := slices.IndexFunc(res.GetPeers(), func(p *noryxv1.OverlayPeerStatus) bool { return p.GetPublicKey() == peer.PublicKey })
		if peer.NodeID != m.NodeID && peer.JoinedAt.Before(before) && (i < 0 || time.Unix(res.GetPeers()[i].GetLatestHandshakeUnix(), 0).Before(before)) {
			nodes = append(nodes, peer.NodeID)
		}
	}
	return nodes
}

// reports asks the members for their part in the network, all at the same time. Those that
// don't answer have none, and their errors are in the order of members.
func (s *Service) reports(ctx context.Context, members []Member) (map[string]*noryxv1.GetOverlayResponse, []error) {
	list, errs := make([]*noryxv1.GetOverlayResponse, len(members)), make([]error, len(members))
	each(members, func(i int, m Member) { list[i], errs[i] = s.get(ctx, m.NodeID) })
	reports := make(map[string]*noryxv1.GetOverlayResponse, len(members))
	for i, m := range members {
		reports[m.NodeID] = list[i]
	}
	return reports, errs
}

// publishedFor returns the ports that a member, by its report, publishes for m.
func publishedFor(report *noryxv1.GetOverlayResponse, m Member) []*noryxv1.OverlayPublished {
	var list []*noryxv1.OverlayPublished
	for _, p := range report.GetPublished() {
		if p.GetPort() == 0 || p.GetPort() > 65535 { // which m would refuse, with all of its configuration
			continue
		}
		for _, c := range p.GetClients() {
			if c.GetAddress() == m.Address && c.GetPublicKey() == m.PublicKey {
				list = append(list, p)
				break
			}
		}
	}
	return list
}

// configure gives member m its address and the other members as peers, with the ports they
// publish for it by their reports, which tests connect to.
func (s *Service) configure(ctx context.Context, settings Settings, members []Member, m Member, reports map[string]*noryxv1.GetOverlayResponse) error {
	subnet := netip.MustParsePrefix(settings.Subnet)
	req := &noryxv1.ConfigureOverlayRequest{Address: fmt.Sprintf("%s/%d", m.Address, subnet.Bits()), Port: settings.Port, Mtu: settings.MTU}
	for _, peer := range members {
		if peer.NodeID == m.NodeID {
			continue
		}
		endpoint, err := s.endpoint(ctx, peer, settings)
		if err != nil {
			return err
		}
		p := &noryxv1.OverlayPeer{PublicKey: peer.PublicKey, Address: peer.Address, Endpoint: endpoint}
		for _, published := range publishedFor(reports[peer.NodeID], m) {
			p.Ports = append(p.Ports, published.GetPort())
		}
		req.Peers = append(req.Peers, p)
	}
	return s.call(ctx, m.NodeID, func(ctx context.Context, c noryxv1.OverlayServiceClient) error {
		_, err := c.ConfigureOverlay(ctx, req)
		return err
	})
}

// endpoint returns where the others reach a member.
func (s *Service) endpoint(ctx context.Context, m Member, settings Settings) (string, error) {
	if m.Endpoint != "" {
		return m.Endpoint, nil
	}
	n, err := s.nodes.Get(ctx, m.NodeID)
	if err != nil {
		return "", err
	}
	host, _, _ := net.SplitHostPort(n.Address)
	return net.JoinHostPort(host, strconv.FormatUint(uint64(settings.Port), 10)), nil
}

// Status is a node's part in the network, as its agent tells it.
type Status struct {
	// Allowed tells that the node's administrator lets it join, and Unsupported why it can't.
	Allowed     bool    `json:"allowed"`
	Unsupported string  `json:"unsupported,omitempty"`
	Member      *Member `json:"member,omitempty"`
	// Endpoint is where the others reach a member.
	Endpoint string `json:"endpoint,omitempty"`
	// Fingerprint is the beginning of the node's public key, e.g. to compare it with noryx-agent overlay status.
	Fingerprint string `json:"fingerprint,omitempty"`
	Peers       []Peer `json:"peers"`
	// Firewall tells whether the firewall rules of a member are in_place, missing or
	// incomplete, and FirewallProblem what differs; older agents don't tell.
	Firewall        string `json:"firewall,omitempty"`
	FirewallProblem string `json:"firewallProblem,omitempty"`
	// Published are the ports that a member publishes in the network.
	Published []Published `json:"published"`
}

// Published is a port that a member publishes in the network, for a server or a datastore.
type Published struct {
	Port        uint32   `json:"port"`
	ServerID    string   `json:"serverId,omitempty"`
	DatastoreID string   `json:"datastoreId,omitempty"`
	Clients     []Client `json:"clients"`
}

// Client is a member that reaches a published port.
type Client struct {
	// NodeID is the member at the address, if one has it.
	NodeID  string `json:"nodeId,omitempty"`
	Address string `json:"address"`
	// Stale tells that the member at the address has another key than the one let in, e.g.
	// as it was offline while that changed: it reaches the port once the network is applied again.
	Stale bool `json:"stale,omitempty"`
}

// Peer is another member, as a member sees it.
type Peer struct {
	NodeID          string     `json:"nodeId"`
	Address         string     `json:"address"`
	Endpoint        string     `json:"endpoint,omitempty"`
	LatestHandshake *time.Time `json:"latestHandshake,omitempty"`
	ReceivedBytes   uint64     `json:"receivedBytes"`
	SentBytes       uint64     `json:"sentBytes"`
}

// Status asks a node for its part in the network.
func (s *Service) Status(ctx context.Context, nodeID string) (Status, error) {
	res, err := s.get(ctx, nodeID)
	if err != nil {
		return Status{}, err
	}
	st := Status{
		Allowed: res.GetAllowed(), Unsupported: res.GetUnsupported(), Peers: []Peer{}, Fingerprint: res.GetPublicKey()[:min(len(res.GetPublicKey()), 8)],
		Firewall: res.GetFirewall().Slug(), FirewallProblem: res.GetFirewallProblem(), Published: []Published{},
	}
	members, err := s.Members(ctx)
	if err != nil {
		return st, err
	}
	for _, p := range res.GetPublished() {
		published := Published{Port: p.GetPort(), ServerID: p.GetServerId(), DatastoreID: p.GetDatastoreId(), Clients: []Client{}}
		for _, c := range p.GetClients() {
			client := Client{Address: c.GetAddress()}
			if i := slices.IndexFunc(members, func(m Member) bool { return m.Address == c.GetAddress() }); i >= 0 {
				client.NodeID, client.Stale = members[i].NodeID, members[i].PublicKey != c.GetPublicKey()
			}
			published.Clients = append(published.Clients, client)
		}
		st.Published = append(st.Published, published)
	}
	for _, m := range members {
		if m.NodeID == nodeID {
			st.Member = &m
			continue
		}
		i := slices.IndexFunc(res.GetPeers(), func(p *noryxv1.OverlayPeerStatus) bool { return p.GetPublicKey() == m.PublicKey })
		if i < 0 {
			continue
		}
		p := res.GetPeers()[i]
		peer := Peer{NodeID: m.NodeID, Address: m.Address, Endpoint: p.GetEndpoint(), ReceivedBytes: p.GetReceivedBytes(), SentBytes: p.GetSentBytes()}
		if t := p.GetLatestHandshakeUnix(); t > 0 {
			peer.LatestHandshake = new(time.Unix(t, 0))
		}
		st.Peers = append(st.Peers, peer)
	}
	if st.Member != nil {
		settings, err := s.Settings(ctx)
		if err == nil {
			st.Endpoint, err = s.endpoint(ctx, *st.Member, settings)
		}
		return st, err
	}
	return st, nil
}

// get asks a node for its part in the network.
func (s *Service) get(ctx context.Context, nodeID string) (res *noryxv1.GetOverlayResponse, err error) {
	return res, s.call(ctx, nodeID, func(ctx context.Context, c noryxv1.OverlayServiceClient) error {
		res, err = c.GetOverlay(ctx, &noryxv1.GetOverlayRequest{})
		return err
	})
}

// call calls the OverlayService of a node's agent.
func (s *Service) call(ctx context.Context, nodeID string, fn func(context.Context, noryxv1.OverlayServiceClient) error) error {
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	conn, err := s.nodes.Conn(ctx, nodeID)
	if err == nil {
		err = fn(ctx, noryxv1.NewOverlayServiceClient(conn))
	}
	if status.Code(err) == codes.Unimplemented {
		return httpapi.Errorf(http.StatusNotImplemented, "Update the agent of the node to let it join the private network.")
	}
	return err
}

// each calls fn for all members at the same time.
func each(members []Member, fn func(i int, m Member)) {
	var wg sync.WaitGroup
	for i, m := range members {
		wg.Go(func() { fn(i, m) })
	}
	wg.Wait()
}
