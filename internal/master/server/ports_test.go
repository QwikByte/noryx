package server

import (
	"context"
	"testing"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	"github.com/QwikByte/noryx/internal/master/httpapi"
	"github.com/QwikByte/noryx/internal/master/node"
)

// rangeNodes is a node whose servers take ports from 25565 to 25600.
type rangeNodes struct{ Nodes }

func (rangeNodes) Get(_ context.Context, id string) (node.Node, error) {
	return node.Node{ID: id, Name: "Agent 1", PortMin: new(uint32(25565)), PortMax: new(uint32(25600))}, nil
}

// Extra ports follow the rules of the agent, lie in the node's port range and, where the servers
// of the node are given, e.g. for a move, are used by none of them.
func TestCheckExtraPorts(t *testing.T) {
	h := NewHandler(Deps{Nodes: rangeNodes{}, Moves: NewMoves()})
	extra := func(port uint32) []*noryxv1.ExtraPort {
		return []*noryxv1.ExtraPort{{Port: port, Protocol: "tcp", Label: "Map"}}
	}
	servers := []*noryxv1.Server{{Name: "Survival", Port: 25566, ExtraPorts: []*noryxv1.ExtraPort{{Port: 25590, Protocol: "udp"}}}}
	for _, tc := range []struct {
		name    string
		ports   []*noryxv1.ExtraPort
		servers []*noryxv1.Server
		want    string
	}{
		{"none", nil, servers, ""},
		{"in the port range", extra(25580), nil, ""},
		{"outside the port range", extra(8100), nil, "Choose extra ports from 25565 to 25600, the port range of Agent 1."},
		{"the console port", extra(25575), nil, "The server's container uses port 25575 itself. Choose another extra port."},
		{"the server's port", extra(25570), nil, "Port 25570 is the server's own port. Choose another extra port."},
		{"free on the node", extra(25580), servers, ""},
		{"the port of another server", extra(25566), servers, `Port 25566 of the server is used by "Survival" on Agent 1. Change its extra ports first.`},
		{"an extra port of another server", extra(25590), servers, `Port 25590 of the server is used by "Survival" on Agent 1. Change its extra ports first.`},
	} {
		got := ""
		if err := h.checkExtraPorts(t.Context(), "n1", tc.servers, tc.ports, 25570, 0); err != nil {
			got = httpapi.Message(err)
		}
		if got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}
}
