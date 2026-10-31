package overlay

import (
	"errors"
	"net/netip"
	"slices"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
)

// A test connects only to the address of a peer, at ports the master configured as published
// by it for this node, and only as often as the limit allows.
func TestTestOverlayPeer(t *testing.T) {
	m := newMember(t)
	first, second := peer(t, 1), peer(t, 2)
	first.Ports = []uint32{3306, 25570, 25570}
	for p := range uint32(32) {
		first.Ports = append(first.Ports, 30000+p)
	}
	m.configure(first, second)
	m.k.listening = []netip.AddrPort{netip.MustParseAddrPort("10.213.0.1:25570")}

	test := func(key string, ports ...uint32) (*noryxv1.TestOverlayPeerResponse, error) {
		return m.s.TestOverlayPeer(t.Context(), &noryxv1.TestOverlayPeerRequest{PublicKey: key, Ports: ports})
	}
	res, err := test(first.GetPublicKey(), 25570, 3306)
	if err != nil {
		t.Fatal(err)
	}
	if r := res.GetResults(); len(r) != 2 || r[0].GetPort() != 25570 || r[0].GetError() != "" || r[1].GetPort() != 3306 || r[1].GetError() != "connection refused" {
		t.Fatalf("results %v", r)
	}

	refused := map[string]struct {
		key   string
		ports []uint32
		code  codes.Code
	}{
		"a port not published for this node": {first.GetPublicKey(), []uint32{25571}, codes.InvalidArgument},
		"a port of another peer":             {second.GetPublicKey(), []uint32{25570}, codes.InvalidArgument},
		"a port out of range":                {first.GetPublicKey(), []uint32{25570 + 65536}, codes.InvalidArgument},
		"no port":                            {first.GetPublicKey(), nil, codes.InvalidArgument},
		"too many ports":                     {first.GetPublicKey(), first.GetPorts(), codes.InvalidArgument},
		"the key of no peer":                 {peer(t, 1).GetPublicKey(), []uint32{25570}, codes.NotFound},
		"an address instead of a key":        {"10.0.0.1", []uint32{22}, codes.NotFound},
	}
	for name, c := range refused {
		if _, err := test(c.key, c.ports...); status.Code(err) != c.code {
			t.Errorf("%s: %v", name, err)
		}
	}
	// The interface must send the address to the peer.
	applied := m.k.applied.Peers
	m.k.applied.Peers = nil
	if _, err := test(first.GetPublicKey(), 25570); status.Code(err) != codes.FailedPrecondition {
		t.Errorf("without the peer on the interface: %v", err)
	}
	m.k.applied.Peers = applied
	for _, addr := range m.k.dialed {
		if addr.Addr() != netip.MustParseAddr("10.213.0.1") {
			t.Fatalf("dialed %s", addr)
		}
	}

	// 2 of the burst are taken; two tests of 32 ports exceed it.
	many := first.GetPorts()[3:]
	if _, err := test(first.GetPublicKey(), many...); err != nil {
		t.Fatal(err)
	}
	if _, err := test(first.GetPublicKey(), many...); status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("beyond the limit: %v", err)
	}
}

// The ports a peer publishes are kept sorted and once; invalid ones are refused.
func TestPeerPorts(t *testing.T) {
	m := newMember(t)
	first := peer(t, 1)
	first.Ports = []uint32{25570, 3306, 25570}
	m.configure(first)
	if got := m.k.applied.Peers[0].Ports; !slices.Equal(got, []uint16{3306, 25570}) {
		t.Fatalf("ports %v", got)
	}
	for _, port := range []uint32{0, 65536} {
		first.Ports = []uint32{port}
		req := &noryxv1.ConfigureOverlayRequest{Address: "10.213.0.3/24", Port: 51820, Mtu: 1420, Peers: []*noryxv1.OverlayPeer{first}}
		if _, err := m.s.ConfigureOverlay(t.Context(), req); status.Code(err) != codes.InvalidArgument {
			t.Errorf("port %d: %v", port, err)
		}
	}
}

// The status names the ports the node publishes, for whom, and whether the firewall is in place.
func TestPublishedAndFirewall(t *testing.T) {
	m := newMember(t)
	first, second := peer(t, 1), peer(t, 2)
	m.configure(first, second)
	m.admit("survival", 25570, first)
	m.admit(DatastorePrefix+"main", 3306, first, second)

	res, err := m.s.GetOverlay(t.Context(), &noryxv1.GetOverlayRequest{})
	if err != nil {
		t.Fatal(err)
	}
	clients := func(peers ...*noryxv1.OverlayPeer) []*noryxv1.OverlayClient {
		var list []*noryxv1.OverlayClient
		for _, p := range peers {
			list = append(list, &noryxv1.OverlayClient{Address: p.GetAddress(), PublicKey: p.GetPublicKey()})
		}
		return list
	}
	want := []*noryxv1.OverlayPublished{
		{Port: 3306, DatastoreId: "main", Clients: clients(first, second)},
		{Port: 25570, ServerId: "survival", Clients: clients(first)},
	}
	if !slices.EqualFunc(res.GetPublished(), want, func(a, b *noryxv1.OverlayPublished) bool { return proto.Equal(a, b) }) {
		t.Fatalf("published %v", res.GetPublished())
	}
	if res.GetFirewall() != noryxv1.OverlayFirewall_OVERLAY_FIREWALL_IN_PLACE || res.GetFirewallProblem() != "" {
		t.Fatalf("firewall %v %q", res.GetFirewall(), res.GetFirewallProblem())
	}
	m.k.firewall = ErrNoTable
	if res, _ := m.s.GetOverlay(t.Context(), &noryxv1.GetOverlayRequest{}); res.GetFirewall() != noryxv1.OverlayFirewall_OVERLAY_FIREWALL_MISSING || res.GetFirewallProblem() != "" {
		t.Errorf("without the table: %v %q", res.GetFirewall(), res.GetFirewallProblem())
	}
	m.k.firewall = errors.New("the chain input is missing")
	if res, _ := m.s.GetOverlay(t.Context(), &noryxv1.GetOverlayRequest{}); res.GetFirewall() != noryxv1.OverlayFirewall_OVERLAY_FIREWALL_INCOMPLETE || res.GetFirewallProblem() != m.k.firewall.Error() {
		t.Errorf("without a chain: %v %q", res.GetFirewall(), res.GetFirewallProblem())
	}
	if m.k.applied.Peers[0].Ports != nil {
		t.Errorf("ports of a peer that publishes none: %v", m.k.applied.Peers[0].Ports)
	}
}
