package e2e

import (
	"net/http"
	"slices"
	"testing"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	"github.com/QwikByte/noryx/internal/agent/runtime"
	"github.com/QwikByte/noryx/internal/master/network"
	masterserver "github.com/QwikByte/noryx/internal/master/server"
)

// A server publishes extra ports for its plugins, which its settings change, which no other server
// of its node may take, which copies don't get and which move with it to a node where they are free.
func TestExtraPorts(t *testing.T) {
	m := startMaster(t)
	a, b := m.startAgent(t, "node-1"), m.startAgent(t, "node-2")
	api := apiClient{t: t, url: m.panel(t).URL}
	lobby := m.createServer(t, a, "Lobby", noryxv1.ServerType_SERVER_TYPE_PAPER, 25566)
	path := func(r network.Ref) string { return "/api/nodes/" + r.NodeID + "/servers/" + r.ServerID }
	type listed struct {
		ID         string
		ExtraPorts []masterserver.ExtraPort
	}
	get := func(r network.Ref) listed {
		var servers []listed
		api.do("GET", "/api/nodes/"+r.NodeID+"/servers", nil, http.StatusOK, &servers)
		return servers[slices.IndexFunc(servers, func(s listed) bool { return s.ID == r.ServerID })]
	}
	if s := get(lobby); s.ExtraPorts == nil || len(s.ExtraPorts) != 0 {
		t.Fatalf("new server = %+v", s)
	}

	ports := []masterserver.ExtraPort{{Port: 8100, Protocol: "tcp", Label: " Map "}, {Port: 24454, Protocol: "udp", Label: "Voice chat"}}
	settings := func(ports ...masterserver.ExtraPort) map[string]any {
		return map[string]any{"name": "Lobby", "memoryMb": 1024, "port": 25566, "extraPorts": ports}
	}
	api.do("PUT", path(lobby), settings(ports...), http.StatusOK, nil)
	want := []runtime.ExtraPort{{Port: 8100, Protocol: "tcp", Label: "Map"}, {Port: 24454, Protocol: "udp", Label: "Voice chat"}}
	if s, spec := get(lobby), a.runtime.spec(lobby.ServerID); len(s.ExtraPorts) != 2 || s.ExtraPorts[0].Label != "Map" || !slices.Equal(spec.ExtraPorts, want) {
		t.Fatalf("server = %+v, spec = %+v", s, spec.ExtraPorts)
	}
	for _, bad := range []masterserver.ExtraPort{{Port: 80, Protocol: "tcp"}, {Port: 25575, Protocol: "tcp"}, {Port: 25566, Protocol: "udp"}, {Port: 8101, Protocol: "sctp"}} {
		api.do("PUT", path(lobby), settings(bad), http.StatusBadRequest, nil)
	}

	// Other servers of the node can't take them, as their own port or as extra ports, but those of
	// other nodes can.
	create := func(on agent, port uint32, extra []masterserver.ExtraPort, status int) {
		t.Helper()
		api.do("POST", "/api/nodes/"+on.node.ID+"/servers", map[string]any{
			"name": "Survival", "type": "paper", "memoryMb": 1024, "port": port, "acceptEula": true, "extraPorts": extra,
		}, status, nil)
	}
	create(a, 8100, nil, http.StatusConflict)
	create(a, 25567, []masterserver.ExtraPort{{Port: 24454, Protocol: "udp"}}, http.StatusConflict)
	create(b, 25567, []masterserver.ExtraPort{{Port: 8100, Protocol: "tcp", Label: "Map"}}, http.StatusCreated)

	// A copy stays on the node, where the original keeps the extra ports.
	var copied listed
	api.do("POST", path(lobby)+"/duplicate", map[string]any{"name": "Lobby 2", "port": 25567}, http.StatusCreated, &copied)
	if copied.ExtraPorts == nil || len(copied.ExtraPorts) != 0 {
		t.Fatalf("copy = %+v", copied)
	}

	// A move to a node where they are used is refused before the server stops; once they are
	// free there, the server takes them along.
	api.do("POST", path(lobby)+"/move", map[string]any{"node": b.node.ID}, http.StatusConflict, nil)
	api.do("PUT", path(lobby), settings(ports[1]), http.StatusOK, nil)
	api.do("POST", path(lobby)+"/move", map[string]any{"node": b.node.ID}, http.StatusAccepted, nil)
	moved := network.Ref{NodeID: b.node.ID, ServerID: lobby.ServerID}
	if mv := waitForMove(t, api, lobby.ServerID); mv.Phase != "done" || len(mv.Warnings) != 0 || !slices.Equal(b.runtime.spec(lobby.ServerID).ExtraPorts, want[1:]) {
		t.Fatalf("move = %+v, extra ports = %+v", mv, get(moved).ExtraPorts)
	}
}
