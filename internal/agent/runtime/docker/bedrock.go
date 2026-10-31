package docker

import (
	"fmt"
	"net"
	"net/netip"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	"github.com/QwikByte/noryx/internal/agent/runtime"
)

// udpPorts returns the value of SERVER_UDP_PORTS of a Bedrock server: the UDP ports of its
// players, the same inside its container as on the node, which the server tells players at
// addr, the node's address at which they reach it. In its container, the server only knows its
// address there, which players can't reach. Without addr, it tells that one.
func udpPorts(spec runtime.Spec, addr netip.Addr) string {
	ports := fmt.Sprint(spec.Port)
	if last := noryxv1.LastPort(spec.Type, spec.Port, spec.MaxPlayers); last != spec.Port {
		ports += fmt.Sprintf("-%d", last)
	}
	if !addr.IsValid() {
		return ports
	}
	return addr.String() + ":" + ports + ":" + ports
}

// publicAddress returns the IPv4 address at which players reach the node: the one its operator
// gave the agent, or else the node's own on its route to the internet.
func (d *Docker) publicAddress() netip.Addr {
	if d.public.IsValid() {
		return d.public
	}
	return routeAddress()
}

// routeAddress returns the node's IPv4 address on its default route, or an invalid address if
// it has none. Connecting a UDP socket only chooses its route; nothing is sent.
func routeAddress() netip.Addr {
	c, err := net.Dial("udp4", "192.0.2.1:9") // TEST-NET-1, an address of documentation
	if err != nil {
		return netip.Addr{}
	}
	defer c.Close()
	addr, err := netip.ParseAddrPort(c.LocalAddr().String())
	if err != nil {
		return netip.Addr{}
	}
	return addr.Addr().Unmap()
}
