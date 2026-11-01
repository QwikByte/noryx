package e2e

import (
	"net/http"
	"slices"
	"strings"
	"testing"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	"github.com/QwikByte/noryx/internal/agent/runtime"
	"github.com/QwikByte/noryx/internal/master/datastore"
	"github.com/QwikByte/noryx/internal/master/network"
)

const velocity, paper = noryxv1.ServerType_SERVER_TYPE_VELOCITY, noryxv1.ServerType_SERVER_TYPE_PAPER

// Removing a node takes its servers out of their networks first, once that is confirmed: no
// server of another node keeps trusting a proxy of the node, and no proxy keeps sending players
// to its servers. The node itself, which may be lost, isn't contacted.
func TestRemoveNodeOfNetworks(t *testing.T) {
	m := startMaster(t)
	a1, a2, a3 := m.startAgent(t, "node-1"), m.startAgent(t, "node-2"), m.startAgent(t, "node-3")
	proxy, lobby, survival := m.createServer(t, a1, "Proxy", velocity, 25577), m.createServer(t, a1, "Lobby", paper, 25565), m.createServer(t, a2, "Survival", paper, 25570)
	hub, skyblock, creative := m.createServer(t, a2, "Hub", velocity, 25577), m.createServer(t, a1, "Skyblock", paper, 25566), m.createServer(t, a2, "Creative", paper, 25571)
	edge, minigames := m.createServer(t, a3, "Edge", velocity, 25577), m.createServer(t, a1, "Minigames", paper, 25567)
	api := apiClient{t: t, url: m.panel(t).URL}
	var main, other network.Network
	api.do("POST", "/api/networks", map[string]any{"name": "Main", "proxy": proxy, "servers": []network.Ref{lobby, survival}}, http.StatusCreated, &main)
	api.do("POST", "/api/networks", map[string]any{"name": "Other", "proxy": hub, "servers": []network.Ref{skyblock, creative}}, http.StatusCreated, &other)
	api.do("POST", "/api/networks", map[string]any{"name": "Arcade", "proxy": edge, "servers": []network.Ref{minigames}}, http.StatusCreated, nil)
	api.do("PUT", "/api/networks/"+other.ID, network.Change{
		Name: "Other", Forwarding: network.Modern, Backends: other.Backends, Try: []string{"skyblock", "creative"},
		ForcedHosts: []network.ForcedHost{{Host: "sky.example.com", Servers: []string{"skyblock"}}, {Host: "play.example.com", Servers: []string{"skyblock", "creative"}}},
	}, http.StatusOK, nil)

	secret := a2.runtime.network(creative.ServerID).ForwardingSecret

	// The node stays until its networks may let go of it, which names them.
	path := "/api/nodes/" + a1.node.ID
	var refusal struct{ Error, Code string }
	api.do("DELETE", path, nil, http.StatusConflict, &refusal)
	if refusal.Code != network.ErrOnNode || !strings.Contains(refusal.Error, `"Arcade", "Main", "Other"`) {
		t.Fatalf("refusal = %+v", refusal)
	}
	// Taking them out needs the permission to manage networks too.
	remover := m.withGrants(t, map[string]any{"name": "Removers", "permissions": []string{"nodes.view", "nodes.delete"}})
	remover.do("DELETE", path+"?release=true", nil, http.StatusForbidden, nil)
	// A network that would be deleted with it keeps its datastores on other nodes.
	var ds datastore.View
	api.do("POST", "/api/networks/"+main.ID+"/datastores", map[string]any{"name": "main", "nodeId": a2.node.ID, "engine": "mariadb", "memoryMb": 512}, http.StatusCreated, &ds)
	if body := api.do("DELETE", path+"?release=true", nil, http.StatusConflict, nil); !strings.Contains(body, "has datastores on other nodes") {
		t.Fatalf("with datastores: %s", body)
	}
	api.do("DELETE", "/api/datastores/"+ds.ID, nil, http.StatusNoContent, nil)

	// A server of another node that can't be configured keeps the node and its networks.
	setDown(a2, true)
	api.do("DELETE", path+"?release=true", nil, http.StatusBadGateway, nil)
	setDown(a2, false)
	api.do("GET", path, nil, http.StatusOK, nil)
	api.do("GET", "/api/networks/"+main.ID, nil, http.StatusOK, nil)

	// A lost node is removed without being reached.
	gone := listen(t)
	gone.Close()
	api.do("PUT", path, map[string]any{"name": "node-1", "address": gone.Addr().String(), "defaultStorage": "default"}, http.StatusOK, nil)
	api.do("DELETE", path+"?release=true", nil, http.StatusNoContent, nil)
	api.do("GET", path, nil, http.StatusNotFound, nil)

	// The servers of Main elsewhere no longer trust its proxy, and Arcade's proxy, which lost
	// all its servers, no longer forwards.
	if got := a2.runtime.network(survival.ServerID); got.Forwarding != runtime.ForwardingNone || got.ForwardingSecret != "" {
		t.Fatalf("survival network = %+v", got)
	}
	if got := a3.runtime.network(edge.ServerID); got.Forwarding != runtime.ForwardingNone || len(got.Backends) != 0 {
		t.Fatalf("edge network = %+v", got)
	}
	// Other forgets skyblock, also where players join and in its host names, and so does its proxy.
	var networks []network.Network
	api.do("GET", "/api/networks", nil, http.StatusOK, &networks)
	if len(networks) != 1 || networks[0].ID != other.ID {
		t.Fatalf("networks = %+v", networks)
	}
	if n := networks[0]; len(n.Backends) != 1 || n.Backends[0].Ref != creative || !slices.Equal(n.Try, []string{"creative"}) ||
		len(n.ForcedHosts) != 1 || n.ForcedHosts[0].Host != "play.example.com" || !slices.Equal(n.ForcedHosts[0].Servers, []string{"creative"}) {
		t.Fatalf("other = %+v", n)
	}
	want := []runtime.NetworkBackend{{Name: "creative", ServerID: creative.ServerID}}
	if got := a2.runtime.network(hub.ServerID); !slices.Equal(got.Backends, want) || !slices.Equal(got.Try, []string{"creative"}) {
		t.Fatalf("hub network = %+v", got)
	}
	// Its forwarding secret, which skyblock knew, is a new one on its proxy and servers.
	if got, proxy := a2.runtime.network(creative.ServerID).ForwardingSecret, a2.runtime.network(hub.ServerID).ForwardingSecret; secret == "" || got == secret || got != proxy {
		t.Fatalf("forwarding secret %q before, %q on creative and %q on the proxy after", secret, got, proxy)
	}
}

// A network whose proxy's node is offline can still be deleted: its servers on other nodes
// become standalone, and the answer tells that the proxy and the servers on its node stay as
// they are. While the node answers, a call to it that fails keeps the network.
func TestDeleteNetworkOfOfflineProxy(t *testing.T) {
	m := startMaster(t)
	a1, a2 := m.startAgent(t, "node-1"), m.startAgent(t, "node-2")
	proxy, lobby, survival := m.createServer(t, a1, "Proxy", velocity, 25577), m.createServer(t, a1, "Lobby", paper, 25565), m.createServer(t, a2, "Survival", paper, 25570)
	api := apiClient{t: t, url: m.panel(t).URL}
	var n network.Network
	api.do("POST", "/api/networks", map[string]any{"name": "Main", "proxy": proxy, "servers": []network.Ref{lobby, survival}}, http.StatusCreated, &n)

	setDown(a1, true)
	api.do("DELETE", "/api/networks/"+n.ID, nil, http.StatusBadGateway, nil)
	setDown(a1, false)
	api.do("GET", "/api/networks/"+n.ID, nil, http.StatusOK, nil)

	gone := listen(t)
	gone.Close()
	api.do("PUT", "/api/nodes/"+a1.node.ID, map[string]any{"name": "node-1", "address": gone.Addr().String(), "defaultStorage": "default"}, http.StatusOK, nil)
	var deleted struct{ Warning string }
	api.do("DELETE", "/api/networks/"+n.ID, nil, http.StatusOK, &deleted)
	if !strings.HasPrefix(deleted.Warning, "node-1 can't be reached") {
		t.Fatalf("warning = %q", deleted.Warning)
	}
	if got := a2.runtime.network(survival.ServerID); got.Forwarding != runtime.ForwardingNone || got.ForwardingSecret != "" {
		t.Fatalf("survival network = %+v", got)
	}
	if body := api.do("GET", "/api/networks", nil, http.StatusOK, nil); body != "[]\n" {
		t.Fatalf("networks = %s", body)
	}
}

// setDown makes Docker unreachable on a node, or reachable again, while its agent answers.
func setDown(a agent, down bool) {
	a.runtime.mu.Lock()
	defer a.runtime.mu.Unlock()
	a.runtime.down = down
}
