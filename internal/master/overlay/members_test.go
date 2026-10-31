package overlay

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	"github.com/QwikByte/noryx/internal/master/access"
	"github.com/QwikByte/noryx/internal/master/database"
	"github.com/QwikByte/noryx/internal/master/httpapi"
	"github.com/QwikByte/noryx/internal/master/node/nodetest"
	"github.com/QwikByte/noryx/internal/master/operation"
)

// peerAgent is the agent of a node: its key, the interface as the master configured it last,
// and what it publishes for others.
type peerAgent struct {
	noryxv1.UnimplementedOverlayServiceServer

	mu          sync.Mutex
	allowed     bool
	unsupported string
	key         string
	config      *noryxv1.ConfigureOverlayRequest
	refuse      error // of configurations
	down        bool
	left        bool
	handshakes  map[string]int64 // by the keys of peers
	published   []*noryxv1.OverlayPublished
	firewall    noryxv1.OverlayFirewall
	tested      *noryxv1.TestOverlayPeerRequest
}

func (a *peerAgent) set(fn func(a *peerAgent)) {
	a.mu.Lock()
	defer a.mu.Unlock()
	fn(a)
}

func (a *peerAgent) GetOverlay(context.Context, *noryxv1.GetOverlayRequest) (*noryxv1.GetOverlayResponse, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.down {
		return nil, status.Error(codes.Unavailable, "connection refused")
	}
	res := &noryxv1.GetOverlayResponse{Allowed: a.allowed, Unsupported: a.unsupported, PublicKey: a.key, Published: a.published, Firewall: a.firewall}
	if a.config != nil {
		res.Address, _, _ = strings.Cut(a.config.GetAddress(), "/")
		for _, p := range a.config.GetPeers() {
			res.Peers = append(res.Peers, &noryxv1.OverlayPeerStatus{PublicKey: p.GetPublicKey(), Endpoint: p.GetEndpoint(), LatestHandshakeUnix: a.handshakes[p.GetPublicKey()]})
		}
	}
	return res, nil
}

func (a *peerAgent) ConfigureOverlay(_ context.Context, req *noryxv1.ConfigureOverlayRequest) (*noryxv1.ConfigureOverlayResponse, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.down {
		return nil, status.Error(codes.Unavailable, "connection refused")
	}
	if a.refuse != nil {
		return nil, a.refuse
	}
	a.config, a.left = req, false
	return &noryxv1.ConfigureOverlayResponse{}, nil
}

func (a *peerAgent) RotateOverlayKey(context.Context, *noryxv1.RotateOverlayKeyRequest) (*noryxv1.RotateOverlayKeyResponse, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.key += "'"
	return &noryxv1.RotateOverlayKeyResponse{PublicKey: a.key}, nil
}

func (a *peerAgent) LeaveOverlay(context.Context, *noryxv1.LeaveOverlayRequest) (*noryxv1.LeaveOverlayResponse, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.down {
		return nil, status.Error(codes.Unavailable, "connection refused")
	}
	a.config, a.left = nil, true
	return &noryxv1.LeaveOverlayResponse{}, nil
}

// TestOverlayPeer answers for two ports at most, the first reached in 1.5 ms, the others refused.
func (a *peerAgent) TestOverlayPeer(_ context.Context, req *noryxv1.TestOverlayPeerRequest) (*noryxv1.TestOverlayPeerResponse, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.tested = req
	res := &noryxv1.TestOverlayPeerResponse{}
	for i, port := range req.GetPorts()[:min(2, len(req.GetPorts()))] {
		r := &noryxv1.OverlayPortTest{Port: port, DurationMicros: 1500}
		if i > 0 {
			r.Error = "connection refused"
		}
		res.Results = append(res.Results, r)
	}
	return res, nil
}

// peers describes the peers that the master configured, as address, key, endpoint and ports.
func (a *peerAgent) peers() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	var peers []string
	for _, p := range a.config.GetPeers() {
		peers = append(peers, fmt.Sprintf("%s %s %s %v", p.GetAddress(), p.GetPublicKey(), p.GetEndpoint(), p.GetPorts()))
	}
	slices.Sort(peers)
	return peers
}

// mesh is a master whose nodes n1, n2 and n3 may join the private network, while n4 runs an
// agent of an older version and nothing answers for n5.
type mesh struct {
	*Service
	agents map[string]*peerAgent
}

func newMesh(t *testing.T) *mesh {
	t.Helper()
	db, err := database.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	nodes := nodetest.New(t, db)
	p := &mesh{Service: NewService(db, nodes), agents: map[string]*peerAgent{}}
	for _, id := range []string{"n1", "n2", "n3"} {
		a := &peerAgent{allowed: true, key: "key-" + id, handshakes: map[string]int64{}}
		p.agents[id] = a
		nodes.Add(t, id, func(s *grpc.Server) { noryxv1.RegisterOverlayServiceServer(s, a) })
	}
	nodes.Add(t, "n4", func(*grpc.Server) {})
	nodes.Add(t, "n5", nil)
	return p
}

// join lets a node join, or fails the test.
func (p *mesh) join(t *testing.T, nodeID string) Member {
	t.Helper()
	m, err := p.Join(t.Context(), nodeID, nodeID+".example.com:51820")
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// members returns the members as node, address and key, with their problems if any.
func (p *mesh) members(t *testing.T) []string {
	t.Helper()
	members, err := p.Members(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var list []string
	for _, m := range members {
		list = append(list, strings.TrimSpace(fmt.Sprintf("%s %s %s %s %q", m.NodeID, m.Address, m.PublicKey, m.Problem, m.Unreached)))
	}
	return list
}

// statusOf is the HTTP status of an error of the API, or 0.
func statusOf(err error) int {
	var apiErr *httpapi.Error
	if errors.As(err, &apiErr) {
		return apiErr.Status
	}
	return 0
}

// A node joins with the lowest free address once its administrator allowed it and the others
// can reach it; it and the others get each other as peers. A node that can't take the
// configuration doesn't join.
func TestJoin(t *testing.T) {
	p := newMesh(t)
	n1, n2, n3 := p.agents["n1"], p.agents["n2"], p.agents["n3"]
	n1.set(func(a *peerAgent) { a.allowed = false })
	if _, err := p.Join(t.Context(), "n1", "n1.example.com:51820"); statusOf(err) != http.StatusConflict || !strings.Contains(err.Error(), "noryx-agent overlay allow") {
		t.Fatalf("Join without being allowed = %v", err)
	}
	n1.set(func(a *peerAgent) { a.allowed = true })
	for endpoint, want := range map[string]string{
		"":                     "The other nodes can't reach n1 at 127.0.0.1.",
		"n1.example.com":       "Enter where the other nodes reach n1 as host:port",
		"n1.example.com:0":     "Enter where the other nodes reach n1 as host:port",
		"n1 example.com:51820": "Enter where the other nodes reach n1 as host:port",
	} {
		if _, err := p.Join(t.Context(), "n1", endpoint); statusOf(err) != http.StatusBadRequest || !strings.Contains(err.Error(), want) {
			t.Errorf("Join at %q = %v", endpoint, err)
		}
	}
	m := p.join(t, "n1")
	if m.Address != "10.213.0.1" || m.PublicKey != "key-n1" || m.Endpoint != "n1.example.com:51820" {
		t.Fatalf("member %+v", m)
	}
	n1.set(func(a *peerAgent) {
		if c := a.config; c.GetAddress() != "10.213.0.1/24" || c.GetPort() != 51820 || c.GetMtu() != 1420 || len(c.GetPeers()) != 0 {
			t.Fatalf("n1 configured with %v", c)
		}
	})
	p.join(t, "n2")
	if got := n1.peers(); !slices.Equal(got, []string{"10.213.0.2 key-n2 n2.example.com:51820 []"}) {
		t.Errorf("peers of n1 %q", got)
	}
	if got := n2.peers(); !slices.Equal(got, []string{"10.213.0.1 key-n1 n1.example.com:51820 []"}) {
		t.Errorf("peers of n2 %q", got)
	}

	for _, tc := range []struct {
		name   string
		node   string
		agent  func(a *peerAgent)
		status int
		want   string
	}{
		{"a member", "n1", nil, http.StatusConflict, "n1 is part of the private network already, or another node has its key."},
		{"the key of another", "n3", func(a *peerAgent) { a.key = "key-n2" }, http.StatusConflict, "another node has its key"},
		{"without WireGuard", "n3", func(a *peerAgent) { a.key, a.unsupported = "key-n3", "the kernel has no WireGuard." }, http.StatusConflict,
			"n3 can't join: the kernel has no WireGuard."},
		{"a refused configuration", "n3", func(a *peerAgent) {
			a.unsupported, a.refuse = "", status.Error(codes.FailedPrecondition, "10.213.0.0/24 overlaps a network of the node.")
		}, http.StatusConflict, "n3 can't join: 10.213.0.0/24 overlaps a network of the node."},
		{"an older agent", "n4", nil, http.StatusNotImplemented, "Update the agent of the node to let it join the private network."},
		{"an unknown node", "n9", nil, http.StatusNotFound, "Node not found."},
	} {
		if tc.agent != nil {
			n3.set(tc.agent)
		}
		if _, err := p.Join(t.Context(), tc.node, tc.node+".example.com:51820"); statusOf(err) != tc.status || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v", tc.name, err)
		}
	}
	if _, err := p.Join(t.Context(), "n5", "n5.example.com:51820"); status.Code(err) != codes.Unavailable {
		t.Errorf("Join of a node that doesn't answer = %v", err)
	}
	if got := p.members(t); !slices.Equal(got, []string{`n1 10.213.0.1 key-n1  []`, `n2 10.213.0.2 key-n2  []`}) {
		t.Fatalf("members %q", got)
	}

	// The lowest free address, which a member left.
	if err := p.Leave(t.Context(), "n1"); err != nil {
		t.Fatal(err)
	}
	n3.set(func(a *peerAgent) { a.refuse = nil })
	if m := p.join(t, "n3"); m.Address != "10.213.0.1" {
		t.Errorf("n3 joined at %s", m.Address)
	}
	members := []Member{{Address: "10.0.0.1"}, {Address: "10.0.0.2"}}
	if got := free(netip.MustParsePrefix("10.0.0.0/30"), members); got != "" {
		t.Errorf("free address of a full range: %q", got)
	}
}

// Every few minutes, the master configures each member with the others as peers: with the key
// that their agents have, also a new one, at their endpoints or the host of their agent's
// address, and with the ports they publish for it with its current key. It keeps which members
// it can't configure and which had no handshake for a while.
func TestReconcile(t *testing.T) {
	p := newMesh(t)
	n1, n2, n3 := p.agents["n1"], p.agents["n2"], p.agents["n3"]
	for _, id := range []string{"n1", "n2", "n3"} {
		p.join(t, id)
	}
	n2.set(func(a *peerAgent) {
		a.key = "key-n2-restored"
		a.published = []*noryxv1.OverlayPublished{
			{Port: 25570, ServerId: "survival", Clients: []*noryxv1.OverlayClient{{Address: "10.213.0.1", PublicKey: "key-n1"}}},
			{Port: 25571, ServerId: "skyblock", Clients: []*noryxv1.OverlayClient{{Address: "10.213.0.1", PublicKey: "key-n1-before"}}},
			{Port: 3306, DatastoreId: "main", Clients: []*noryxv1.OverlayClient{{Address: "10.213.0.3", PublicKey: "key-n3"}}},
		}
	})
	n3.set(func(a *peerAgent) { a.down = true })
	for _, query := range []string{`UPDATE overlay_nodes SET joined_at = 0`, `UPDATE overlay_nodes SET endpoint = '' WHERE node_id = 'n2'`} {
		if _, err := p.db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	p.Reconcile(t.Context())
	if got := n1.peers(); !slices.Equal(got, []string{"10.213.0.2 key-n2-restored 127.0.0.1:51820 [25570]", "10.213.0.3 key-n3 n3.example.com:51820 []"}) {
		t.Errorf("peers of n1 %q", got)
	}
	if got := n3.peers(); !slices.Contains(got, "10.213.0.2 key-n2 n2.example.com:51820 []") {
		t.Errorf("n3 was configured while it didn't answer: %q", got)
	}
	// n1 had a handshake with the new key of n2 since, but nobody with n3.
	n1.set(func(a *peerAgent) { a.handshakes["key-n2-restored"] = time.Now().Unix() })
	p.Reconcile(t.Context())
	unreachable := "n3 can't be reached. Check that its agent is running and that the master can connect to it."
	if got := p.members(t); !slices.Equal(got, []string{
		`n1 10.213.0.1 key-n1  ["n3"]`, `n2 10.213.0.2 key-n2-restored  ["n1" "n3"]`, `n3 10.213.0.3 key-n3 ` + unreachable + ` ["n1" "n2"]`,
	}) {
		t.Fatalf("members %q", got)
	}
	n3.set(func(a *peerAgent) { a.down = false })
	p.Reconcile(t.Context())
	if got := n3.peers(); !slices.Contains(got, "10.213.0.2 key-n2-restored 127.0.0.1:51820 [3306]") {
		t.Errorf("peers of n3 %q", got)
	}
}

// The range of the network only changes while no node is a member; other settings apply to the
// members right away.
func TestUpdateSettings(t *testing.T) {
	p := newMesh(t)
	for _, s := range []Settings{
		{"8.8.8.0/24", 51820, 1420}, {"10.213.0.0/30", 51820, 1420}, {"10.213.0.1/24", 51820, 1420}, {"10.0.0.0/8", 51820, 1420}, {"fd00::/64", 51820, 1420},
		{"10.213.0.0/24", 80, 1420}, {"10.213.0.0/24", 51820, 9000},
	} {
		if _, err := p.UpdateSettings(t.Context(), s); statusOf(err) != http.StatusBadRequest {
			t.Errorf("%+v: %v", s, err)
		}
	}
	if s, err := p.UpdateSettings(t.Context(), Settings{" 10.99.0.0/16 ", 51821, 1380}); err != nil || s.Subnet != "10.99.0.0/16" {
		t.Fatalf("UpdateSettings = %+v, %v", s, err)
	}
	if m := p.join(t, "n1"); m.Address != "10.99.0.1" {
		t.Fatalf("n1 joined at %s", m.Address)
	}
	if _, err := p.UpdateSettings(t.Context(), Settings{"10.98.0.0/16", 51821, 1380}); statusOf(err) != http.StatusConflict {
		t.Fatalf("UpdateSettings of the range with members = %v", err)
	}
	if _, err := p.UpdateSettings(t.Context(), Settings{"10.99.0.0/16", 51822, 1400}); err != nil {
		t.Fatal(err)
	}
	p.agents["n1"].set(func(a *peerAgent) {
		if c := a.config; c.GetAddress() != "10.99.0.1/16" || c.GetPort() != 51822 || c.GetMtu() != 1400 {
			t.Errorf("n1 configured with %v", c)
		}
	})
	if s, err := p.Settings(t.Context()); err != nil || s != (Settings{"10.99.0.0/16", 51822, 1400}) {
		t.Errorf("Settings = %+v, %v", s, err)
	}
}

// A new key of a member reaches the others; a member that leaves loses its interface if its
// agent answers, and the others lose it as peer.
func TestRotateAndLeave(t *testing.T) {
	p := newMesh(t)
	n1, n2 := p.agents["n1"], p.agents["n2"]
	p.join(t, "n1")
	p.join(t, "n2")
	if m, err := p.Rotate(t.Context(), "n1"); err != nil || m.PublicKey != "key-n1'" {
		t.Fatalf("Rotate = %+v, %v", m, err)
	}
	if got := n2.peers(); !slices.Equal(got, []string{"10.213.0.1 key-n1' n1.example.com:51820 []"}) {
		t.Errorf("peers of n2 %q", got)
	}
	if m, err := p.SetEndpoint(t.Context(), "n1", "203.0.113.1:51820"); err != nil || m.Endpoint != "203.0.113.1:51820" {
		t.Fatalf("SetEndpoint = %+v, %v", m, err)
	}
	if got := n2.peers(); !slices.Equal(got, []string{"10.213.0.1 key-n1' 203.0.113.1:51820 []"}) {
		t.Errorf("peers of n2 %q", got)
	}
	for name, err := range map[string]error{
		"Rotate":      func() error { _, err := p.Rotate(t.Context(), "n3"); return err }(),
		"SetEndpoint": func() error { _, err := p.SetEndpoint(t.Context(), "n3", "203.0.113.3:51820"); return err }(),
		"Leave":       p.Leave(t.Context(), "n3"),
	} {
		if !errors.Is(err, errNotMember) {
			t.Errorf("%s of a node that isn't a member = %v", name, err)
		}
	}

	if err := p.Leave(t.Context(), "n2"); err != nil {
		t.Fatal(err)
	}
	n2.set(func(a *peerAgent) {
		if !a.left {
			t.Error("n2 kept its interface")
		}
	})
	if got := n1.peers(); len(got) != 0 {
		t.Errorf("peers of n1 %q", got)
	}
	n1.set(func(a *peerAgent) { a.down = true })
	if err := p.Leave(t.Context(), "n1"); err != nil {
		t.Fatalf("Leave of a node that doesn't answer = %v", err)
	}
	if got := p.members(t); len(got) != 0 {
		t.Errorf("members %q", got)
	}
}

// The status of a node tells its part in the network, its peers with their handshakes, and who
// reaches the ports it publishes, also those with a key that changed since.
func TestStatus(t *testing.T) {
	p := newMesh(t)
	n1, n2 := p.agents["n1"], p.agents["n2"]
	p.join(t, "n1")
	p.join(t, "n2")
	handshake := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	n1.set(func(a *peerAgent) {
		a.handshakes["key-n2"], a.firewall = handshake.Unix(), noryxv1.OverlayFirewall_OVERLAY_FIREWALL_IN_PLACE
		a.published = []*noryxv1.OverlayPublished{{Port: 25570, ServerId: "survival", Clients: []*noryxv1.OverlayClient{
			{Address: "10.213.0.2", PublicKey: "key-n2"}, {Address: "10.213.0.9", PublicKey: "key-gone"},
		}}}
	})
	st, err := p.Status(t.Context(), "n1")
	if err != nil || !st.Allowed || st.Fingerprint != "key-n1" || st.Member == nil || st.Member.Address != "10.213.0.1" || st.Endpoint != "n1.example.com:51820" ||
		st.Firewall != "in_place" || len(st.Peers) != 1 || st.Peers[0].NodeID != "n2" || !st.Peers[0].LatestHandshake.Equal(handshake) {
		t.Fatalf("Status = %+v, %v", st, err)
	}
	clients := func() []Client {
		st, err := p.Status(t.Context(), "n1")
		if err != nil || len(st.Published) != 1 || st.Published[0].ServerID != "survival" {
			t.Fatalf("Status = %+v, %v", st, err)
		}
		return st.Published[0].Clients
	}
	if got := clients(); !slices.Equal(got, []Client{{NodeID: "n2", Address: "10.213.0.2"}, {Address: "10.213.0.9"}}) {
		t.Errorf("clients %+v", got)
	}
	if _, err := p.Rotate(t.Context(), "n2"); err != nil {
		t.Fatal(err)
	}
	if got := clients(); !got[0].Stale {
		t.Errorf("clients after n2 rotated its key %+v", got)
	}
	n2.set(func(a *peerAgent) { a.down = true })
	if _, err := p.Status(t.Context(), "n2"); status.Code(err) != codes.Unavailable {
		t.Errorf("Status of a node that doesn't answer = %v", err)
	}
	if st, err := p.Status(t.Context(), "n3"); err != nil || st.Member != nil || st.Endpoint != "" || len(st.Peers) != 0 {
		t.Errorf("Status of a node that isn't a member = %+v, %v", st, err)
	}
}

// Testing connects from a member to the ports that another publishes for it, once it is
// configured with them.
func TestPortTest(t *testing.T) {
	p := newMesh(t)
	n1, n2 := p.agents["n1"], p.agents["n2"]
	p.join(t, "n1")
	p.join(t, "n2")
	p.join(t, "n3")
	n2.set(func(a *peerAgent) {
		a.firewall = noryxv1.OverlayFirewall_OVERLAY_FIREWALL_IN_PLACE
		a.published = []*noryxv1.OverlayPublished{
			{Port: 25570, ServerId: "survival", Clients: []*noryxv1.OverlayClient{{Address: "10.213.0.1", PublicKey: "key-n1"}}},
			{Port: 3306, DatastoreId: "main", Clients: []*noryxv1.OverlayClient{{Address: "10.213.0.1", PublicKey: "key-n1"}}},
			{Port: 25571, ServerId: "skyblock", Clients: []*noryxv1.OverlayClient{{Address: "10.213.0.1", PublicKey: "key-n1"}}},
			{Port: 25572, ServerId: "other", Clients: []*noryxv1.OverlayClient{{Address: "10.213.0.3", PublicKey: "key-n3"}}},
		}
	})
	tests, err := p.Test(t.Context(), "n1", "n2")
	if err != nil || !slices.Equal(tests, []PortTest{
		{Port: 25570, ServerID: "survival", Millis: 1.5}, {Port: 3306, DatastoreID: "main", Error: "connection refused", Millis: 1.5},
		{Port: 25571, ServerID: "skyblock", Error: "The node sent no result."},
	}) {
		t.Fatalf("Test = %+v, %v", tests, err)
	}
	if got := n1.peers(); !slices.Contains(got, "10.213.0.2 key-n2 n2.example.com:51820 [25570 3306 25571]") {
		t.Errorf("peers of n1 %q", got)
	}
	n1.set(func(a *peerAgent) {
		if a.tested.GetPublicKey() != "key-n2" || !slices.Equal(a.tested.GetPorts(), []uint32{25570, 3306, 25571}) {
			t.Errorf("tested %v", a.tested)
		}
	})

	for _, tc := range []struct {
		name, from, to string
		agent          func(a *peerAgent)
		status         int
	}{
		{"itself", "n1", "n1", nil, http.StatusNotFound},
		{"from a node that isn't a member", "n4", "n2", nil, http.StatusNotFound},
		{"to a node that isn't a member", "n1", "n4", nil, http.StatusNotFound},
		{"to an older agent", "n1", "n2", func(a *peerAgent) { a.firewall = noryxv1.OverlayFirewall_OVERLAY_FIREWALL_UNSPECIFIED }, http.StatusNotImplemented},
		{"to a node that isn't configured", "n1", "n2", func(a *peerAgent) { a.config = nil }, http.StatusConflict},
	} {
		if tc.agent != nil {
			n2.set(tc.agent)
		}
		if _, err := p.Test(t.Context(), tc.from, tc.to); statusOf(err) != tc.status {
			t.Errorf("%s: %v", tc.name, err)
		}
	}
}

// networks are those that reach servers over the private network.
type networks struct {
	mu      sync.Mutex
	refuse  error
	applied []string
}

func (n *networks) CheckLeave(context.Context, string) error { return n.refuse }

func (n *networks) ApplyAcross(_ context.Context, nodeID string) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.applied = append(n.applied, nodeID)
	return nil
}

// The API: members join, rotate and leave, which applies the networks across the node again
// unless one would be exposed then.
func TestHandler(t *testing.T) {
	p := newMesh(t)
	nets := &networks{}
	mux := http.NewServeMux()
	NewHandler(p.Service, nets, operation.New(time.Minute)).Register(access.NewMux(mux))
	serve := func(method, path, body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequestWithContext(access.WithGrants(t.Context(), access.Admin()), method, path, strings.NewReader(body)))
		return rec
	}
	for _, id := range []string{"n1", "n2"} {
		if rec := serve("POST", "/api/nodes/"+id+"/overlay", `{"endpoint": "`+id+`.example.com:51820"}`); rec.Code != http.StatusOK {
			t.Fatalf("join %s: status %d: %s", id, rec.Code, rec.Body)
		}
	}
	var overview Overview
	if rec := serve("GET", "/api/overlay", ""); rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &overview) != nil || len(overview.Members) != 2 || overview.Subnet != "10.213.0.0/24" {
		t.Fatalf("overview: status %d: %s", rec.Code, rec.Body)
	}
	if rec := serve("POST", "/api/nodes/n1/overlay/rotate", ""); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"publicKey":"key-n1'"`) {
		t.Fatalf("rotate: status %d: %s", rec.Code, rec.Body)
	}
	nets.refuse = httpapi.Errorf(http.StatusConflict, "The network Main forwards the legacy way.")
	if rec := serve("DELETE", "/api/nodes/n2/overlay", ""); rec.Code != http.StatusConflict {
		t.Fatalf("leave of an exposing node: status %d: %s", rec.Code, rec.Body)
	}
	nets.refuse = nil
	if rec := serve("DELETE", "/api/nodes/n2/overlay", ""); rec.Code != http.StatusNoContent {
		t.Fatalf("leave: status %d: %s", rec.Code, rec.Body)
	}
	nets.mu.Lock()
	if !slices.Equal(nets.applied, []string{"n1", "n2"}) {
		t.Errorf("networks applied across %q", nets.applied)
	}
	nets.mu.Unlock()
	var st Status
	if rec := serve("GET", "/api/nodes/n1/overlay", ""); rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &st) != nil || st.Member == nil || len(st.Peers) != 0 {
		t.Fatalf("status: status %d: %s", rec.Code, rec.Body)
	}
	if rec := serve("POST", "/api/nodes/n9/overlay/rotate", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("rotate of an unknown node: status %d: %s", rec.Code, rec.Body)
	}
}
