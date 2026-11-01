package overlay

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"slices"
	"strconv"
	"time"

	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
)

// State is the node's part of the network, as the master configured it.
type State struct {
	// Address is the node's address with the prefix of the network, e.g. 10.213.0.3/24.
	Address netip.Prefix `json:"address"`
	Port    uint16       `json:"port"`
	MTU     int          `json:"mtu"`
	Peers   []Peer       `json:"peers"`
	// Clients may reach the ports that servers publish in the network, by server ID.
	Clients map[string]Client `json:"clients,omitempty"`
}

// Peer is another node of the network.
type Peer struct {
	PublicKey string         `json:"publicKey"`
	Address   netip.Addr     `json:"address"`
	Endpoint  netip.AddrPort `json:"endpoint"`
	// Ports are the TCP ports at which it publishes servers or datastores for this node, as
	// the master configured them: the only ones that tests connect to.
	Ports []uint16 `json:"ports,omitempty"`
}

// Client is the address in the network of the node of a server's proxy, the only one that
// reaches the port the server publishes in it, or those of the nodes whose servers reach a
// datastore. Each address is bound to the public key of its peer when the client was
// admitted, so that a node that gets the address later reaches nothing of the one before.
type Client struct {
	Port    uint16       `json:"port"`
	Address netip.Addr   `json:"address"`
	Others  []netip.Addr `json:"others,omitempty"`
	// Keys are the public keys of the peers at Address and Others, in this order.
	Keys []string `json:"keys"`
}

// newClient returns the client of a port with its addresses and their keys, of which there
// is at least one.
func newClient(port uint16, addrs []netip.Addr, keys []string) Client {
	c := Client{Port: port, Address: addrs[0], Keys: keys}
	if len(addrs) > 1 {
		c.Others = addrs[1:]
	}
	return c
}

func (c Client) addresses() []netip.Addr { return append([]netip.Addr{c.Address}, c.Others...) }

// private are the IPv4 ranges of private networks (RFC 1918).
var private = []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8"), netip.MustParsePrefix("172.16.0.0/12"), netip.MustParsePrefix("192.168.0.0/16")}

const resolveTimeout = 5 * time.Second

// parse reads a configuration of the master, resolving the endpoints of the peers.
func parse(ctx context.Context, req *noryxv1.ConfigureOverlayRequest) (State, error) {
	address, err := netip.ParsePrefix(req.GetAddress())
	if err != nil || req.GetPort() > 65535 || req.GetMtu() > 65535 {
		return State{}, errors.New("invalid address, port or MTU")
	}
	st := State{Address: address, Port: uint16(req.GetPort()), MTU: int(req.GetMtu())} //nolint:gosec // checked
	for _, p := range req.GetPeers() {
		addr, err := netip.ParseAddr(p.GetAddress())
		if _, keyErr := wgtypes.ParseKey(p.GetPublicKey()); err != nil || keyErr != nil {
			return st, fmt.Errorf("invalid peer %q", p.GetAddress())
		}
		endpoint, err := resolve(ctx, p.GetEndpoint())
		if err != nil {
			return st, fmt.Errorf("peer %s: %w", addr, err)
		}
		var ports []uint16
		for _, port := range p.GetPorts() {
			if port == 0 || port > 65535 {
				return st, fmt.Errorf("peer %s: invalid port %d", addr, port)
			}
			ports = append(ports, uint16(port))
		}
		slices.Sort(ports)
		st.Peers = append(st.Peers, Peer{p.GetPublicKey(), addr, endpoint, slices.Compact(ports)})
	}
	return st, nil
}

// resolve returns the address of an endpoint, host:port, preferring IPv4.
func resolve(ctx context.Context, endpoint string) (netip.AddrPort, error) {
	host, port, err := net.SplitHostPort(endpoint)
	n, portErr := strconv.ParseUint(port, 10, 16)
	if err != nil || portErr != nil || n == 0 {
		return netip.AddrPort{}, fmt.Errorf("invalid endpoint %q", endpoint)
	}
	ctx, cancel := context.WithTimeout(ctx, resolveTimeout)
	defer cancel()
	ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return netip.AddrPort{}, fmt.Errorf("can't resolve %s: %w", host, err)
	}
	i := max(slices.IndexFunc(ips, netip.Addr.Is4), 0)
	return netip.AddrPortFrom(ips[i].Unmap(), uint16(n)), nil
}

// check fails unless the state is safe for the host. The network must be a private IPv4
// range that overlaps none of the host's addresses and routes, and each peer gets a single
// address in it, so that the master can't pull other traffic of the host into the tunnel.
func (st State) check(self string, host []netip.Prefix) error {
	network, addr := st.Address.Masked(), st.Address.Addr()
	switch {
	case !addr.Is4() || !slices.ContainsFunc(private, func(p netip.Prefix) bool { return p.Bits() <= network.Bits() && p.Contains(addr) }):
		return errors.New("the network must be a private IPv4 range, in 10.0.0.0/8, 172.16.0.0/12 or 192.168.0.0/16")
	case network.Bits() < 16 || network.Bits() > 29:
		return errors.New("the network must have a prefix from /16 to /29")
	case !hostAddress(network, addr):
		return fmt.Errorf("%s is no host address of %s", addr, network)
	case st.Port < 1024:
		return errors.New("choose a UDP port from 1024 to 65535")
	case st.MTU < 1280 || st.MTU > 1500:
		return errors.New("choose an MTU from 1280 to 1500")
	}
	if i := slices.IndexFunc(host, network.Overlaps); i >= 0 {
		return fmt.Errorf("the network %s overlaps %s of this node, choose another network in the panel", network, host[i])
	}
	addresses, keys := map[netip.Addr]bool{addr: true}, map[string]bool{self: true}
	for _, p := range st.Peers {
		switch {
		case !hostAddress(network, p.Address):
			return fmt.Errorf("the peer address %s is no host address of %s", p.Address, network)
		case addresses[p.Address] || keys[p.PublicKey]:
			return fmt.Errorf("the address or key of the peer %s is there twice", p.Address)
		}
		addresses[p.Address], keys[p.PublicKey] = true, true
	}
	return nil
}

// hostAddress reports whether addr is in network, but neither its first nor its last address.
func hostAddress(network netip.Prefix, addr netip.Addr) bool {
	if !addr.Is4() || !network.Contains(addr) {
		return false
	}
	a, mask := addr.As4(), uint32(1)<<(32-network.Bits())-1
	host := binary.BigEndian.Uint32(a[:]) & mask
	return host != 0 && host != mask
}

// key returns the public key of the peer at addr, or "" if no peer has it.
func (st State) key(addr netip.Addr) string {
	if i := slices.IndexFunc(st.Peers, func(p Peer) bool { return p.Address == addr }); i >= 0 {
		return st.Peers[i].PublicKey
	}
	return ""
}

// peerClients returns the clients with only the addresses whose peer still has the key it had
// when it was admitted. A node that left the network is no client anymore, nor one that joins
// later with its address, even if this node never saw the configuration without the first.
func (st State) peerClients(clients map[string]Client) map[string]Client {
	kept := map[string]Client{}
	for id, c := range clients {
		var addrs []netip.Addr
		var keys []string
		for i, a := range c.addresses() {
			if i < len(c.Keys) && c.Keys[i] != "" && st.key(a) == c.Keys[i] {
				addrs, keys = append(addrs, a), append(keys, c.Keys[i])
			}
		}
		if len(addrs) > 0 {
			kept[id] = newClient(c.Port, addrs, keys)
		}
	}
	return kept
}
