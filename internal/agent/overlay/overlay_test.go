package overlay

import (
	"context"
	"fmt"
	"maps"
	"net"
	"net/netip"
	"os"
	"reflect"
	"slices"
	"sync"
	"syscall"
	"testing"

	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
)

// fakeKernel keeps the state that was applied last, and lets connections reach the
// addresses in listening.
type fakeKernel struct {
	applied  State
	firewall error

	mu        sync.Mutex
	listening []netip.AddrPort
	dialed    []netip.AddrPort
}

func (*fakeKernel) Check() error { return nil }

func (k *fakeKernel) Apply(st State, _ wgtypes.Key, _ *State) error {
	k.applied = st
	return nil
}

func (k *fakeKernel) Peers() ([]PeerStatus, error) {
	var peers []PeerStatus
	for _, p := range k.applied.Peers {
		peers = append(peers, PeerStatus{PublicKey: p.PublicKey, AllowedIPs: []netip.Prefix{netip.PrefixFrom(p.Address, 32)}})
	}
	return peers, nil
}

func (*fakeKernel) Remove() error { return nil }

func (*fakeKernel) HostNets() ([]netip.Prefix, error) { return nil, nil }

func (k *fakeKernel) Firewall(State) error { return k.firewall }

func (k *fakeKernel) Connect(_ context.Context, addr netip.AddrPort) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.dialed = append(k.dialed, addr)
	if !slices.Contains(k.listening, addr) {
		return &net.OpError{Op: "dial", Net: "tcp4", Err: os.NewSyscallError("connect", syscall.ECONNREFUSED)}
	}
	return nil
}

// member is the agent of a member at 10.213.0.3, with the kernel it configures.
type member struct {
	t *testing.T
	s *Service
	k *fakeKernel
}

func newMember(t *testing.T) member {
	t.Helper()
	dataDir := t.TempDir()
	if err := Allow(dataDir); err != nil {
		t.Fatal(err)
	}
	k := &fakeKernel{}
	return member{t, NewService(dataDir, k), k}
}

// peer returns another node at 10.213.0.n with a new key.
func peer(t *testing.T, n int) *noryxv1.OverlayPeer {
	t.Helper()
	key, err := wgtypes.GeneratePrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	return &noryxv1.OverlayPeer{PublicKey: key.PublicKey().String(), Address: fmt.Sprintf("10.213.0.%d", n), Endpoint: fmt.Sprintf("203.0.113.%d:51820", n)}
}

func (m member) configure(peers ...*noryxv1.OverlayPeer) {
	m.t.Helper()
	req := &noryxv1.ConfigureOverlayRequest{Address: "10.213.0.3/24", Port: 51820, Mtu: 1420, Peers: peers}
	if _, err := m.s.ConfigureOverlay(m.t.Context(), req); err != nil {
		m.t.Fatal(err)
	}
}

// admit lets the peers reach a port, with the keys they have now.
func (m member) admit(id string, port uint32, peers ...*noryxv1.OverlayPeer) {
	m.t.Helper()
	var clients, keys []string
	for _, p := range peers {
		clients, keys = append(clients, p.GetAddress()), append(keys, p.GetPublicKey())
	}
	if _, err := m.s.Admit(id, port, clients, keys); err != nil {
		m.t.Fatal(err)
	}
}

// expect fails unless the clients are those applied and stored.
func (m member) expect(want map[string]Client) {
	m.t.Helper()
	st, err := m.s.state()
	equal := func(clients map[string]Client) bool {
		return maps.EqualFunc(clients, want, func(a, b Client) bool { return reflect.DeepEqual(a, b) })
	}
	if err != nil || !equal(m.k.applied.Clients) || !equal(st.Clients) {
		m.t.Fatalf("clients applied %+v, stored %+v (%v), want %+v", m.k.applied.Clients, st, err, want)
	}
}

func client(port uint16, peers ...*noryxv1.OverlayPeer) Client {
	var addrs []netip.Addr
	var keys []string
	for _, p := range peers {
		addrs, keys = append(addrs, netip.MustParseAddr(p.GetAddress())), append(keys, p.GetPublicKey())
	}
	return newClient(port, addrs, keys)
}

// A node that leaves the network no longer reaches the ports published for it, as a node
// that joins later may get its address.
func TestClientsLeave(t *testing.T) {
	m := newMember(t)
	first, second := peer(t, 1), peer(t, 2)
	m.configure(first, second)
	m.admit("server", 25570, first)
	m.admit("datastore", 3306, first, second)
	m.expect(map[string]Client{"server": client(25570, first), "datastore": client(3306, first, second)})

	m.configure(second)
	want := map[string]Client{"datastore": client(3306, second)}
	m.expect(want)
	// The address may go to another node, which reaches none of the ports.
	m.configure(second, peer(t, 1))
	m.expect(want)
}

// A node that gets the address of one removed while this node was offline, so that it
// never saw a configuration without that one, reaches none of the ports published for the
// removed one. A peer that keeps its key keeps its access.
func TestClientsBoundToKeys(t *testing.T) {
	m := newMember(t)
	removed, kept := peer(t, 1), peer(t, 2)
	m.configure(removed, kept)
	m.admit("server", 25570, removed)
	m.admit("datastore", 3306, removed, kept)

	// The peers come again, one of them at another endpoint.
	moved := &noryxv1.OverlayPeer{PublicKey: kept.GetPublicKey(), Address: kept.GetAddress(), Endpoint: "198.51.100.2:51820"}
	m.configure(removed, moved)
	m.expect(map[string]Client{"server": client(25570, removed), "datastore": client(3306, removed, kept)})

	joined := peer(t, 1)
	m.configure(joined, moved)
	m.expect(map[string]Client{"datastore": client(3306, kept)})
	// Its own key doesn't let the removed node back in either: only the master admits it again.
	m.configure(removed, moved)
	m.expect(map[string]Client{"datastore": client(3306, kept)})
}

// The agent admits only peers with the keys the master sent.
func TestAdmitChecksKeys(t *testing.T) {
	m := newMember(t)
	first, second := peer(t, 1), peer(t, 2)
	m.configure(first, second)
	for name, keys := range map[string][]string{
		"no key":         nil,
		"an empty key":   {""},
		"another key":    {second.GetPublicKey()},
		"keys of others": {first.GetPublicKey(), second.GetPublicKey()},
	} {
		if _, err := m.s.Admit("server", 25570, []string{"10.213.0.1"}, keys); err == nil {
			t.Errorf("%s: admitted", name)
		}
	}
	if _, err := m.s.Admit("server", 25570, []string{"10.213.0.4"}, []string{first.GetPublicKey()}); err == nil {
		t.Error("admitted an address of no peer")
	}
	m.expect(map[string]Client{})
}
