package e2e

import (
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	"github.com/QwikByte/noryx/internal/agent/runtime"
	"github.com/QwikByte/noryx/internal/master/network"
)

func TestNetwork(t *testing.T) {
	m := startMaster(t)
	a1, a2 := m.startAgent(t, "node-1"), m.startAgent(t, "node-2")
	proxy := m.createServer(t, a1, "Proxy", noryxv1.ServerType_SERVER_TYPE_VELOCITY, 25577)
	lobby := m.createServer(t, a1, "Lobby", noryxv1.ServerType_SERVER_TYPE_PAPER, 25565)
	vanilla := m.createServer(t, a1, "Vanilla", noryxv1.ServerType_SERVER_TYPE_VANILLA, 25566)
	fabric := m.createServer(t, a1, "Fabric", noryxv1.ServerType_SERVER_TYPE_FABRIC, 25567)
	survival := m.createServer(t, a2, "Survival", noryxv1.ServerType_SERVER_TYPE_PURPUR, 25570)
	api := apiClient{t: t, url: m.panel(t).URL}

	var n network.Network
	api.do("POST", "/api/networks", map[string]any{"name": "Main", "proxy": proxy, "servers": []network.Ref{lobby, survival}}, http.StatusCreated, &n)
	if n.Forwarding != network.Modern || n.ProxyType != "velocity" || !slices.Equal(n.Try, []string{"lobby"}) {
		t.Fatalf("network = %+v", n)
	}
	secret := a1.runtime.network(lobby.ServerID).ForwardingSecret
	if got := a1.runtime.network(lobby.ServerID); secret == "" || got.Forwarding != runtime.ForwardingModern || !got.ProxyOnNode {
		t.Fatalf("lobby network = %+v", got)
	}
	if got := a2.runtime.network(survival.ServerID); got.ForwardingSecret != secret || got.ProxyOnNode {
		t.Fatalf("survival network = %+v", got)
	}
	host, _, _ := net.SplitHostPort(a2.node.Address)
	lobbyBackend := runtime.NetworkBackend{Name: "lobby", ServerID: lobby.ServerID}
	survivalBackend := runtime.NetworkBackend{Name: "survival", Address: net.JoinHostPort(host, "25570")}
	wantProxy := func(forwarding runtime.Forwarding, try []string, hosts []runtime.ForcedHost, backends ...runtime.NetworkBackend) {
		t.Helper()
		got := a1.runtime.network(proxy.ServerID)
		if got.Forwarding != forwarding || !slices.Equal(got.Try, try) || !slices.EqualFunc(got.ForcedHosts, hosts, sameHost) ||
			!slices.Equal(got.Backends, backends) {
			t.Fatalf("proxy network = %+v, want %v %v %v %+v", got, forwarding, try, hosts, backends)
		}
	}
	wantProxy(runtime.ForwardingModern, []string{"lobby"}, nil, lobbyBackend, survivalBackend)

	// A Fabric server gets FabricProxy-Lite and the Fabric API it requires; renames, the
	// servers players try and the forced hosts go into the proxy's configuration.
	change := network.Change{
		Name: "Main", Forwarding: network.Modern,
		Backends: []network.Backend{
			{Ref: lobby, Name: "lobby"}, {Ref: survival, Name: "smp"}, {Ref: fabric, Name: "fabric"},
		},
		Try:         []string{"lobby", "smp"},
		ForcedHosts: []network.ForcedHost{{Host: "SMP.Example.com", Servers: []string{"smp", "lobby"}}},
	}
	api.do("PUT", "/api/networks/"+n.ID, change, http.StatusOK, &n)
	if !slices.Equal(n.ForcedHosts[0].Servers, []string{"smp", "lobby"}) || n.ForcedHosts[0].Host != "smp.example.com" {
		t.Fatalf("forced hosts = %+v", n.ForcedHosts)
	}
	if mods := files(t, filepath.Join(a1.runtime.dir, fabric.ServerID, "mods")); !slices.Equal(mods, []string{"8dI2tmqs-2.10.jar", "fabricapi-0.119.jar"}) {
		t.Fatalf("mods of the Fabric server = %q", mods)
	}
	smp := runtime.NetworkBackend{Name: "smp", Address: survivalBackend.Address}
	fabricBackend := runtime.NetworkBackend{Name: "fabric", ServerID: fabric.ServerID}
	wantProxy(runtime.ForwardingModern, []string{"lobby", "smp"}, []runtime.ForcedHost{{Host: "smp.example.com", Servers: []string{"smp", "lobby"}}},
		lobbyBackend, smp, fabricBackend)

	// Invalid changes are rejected, and the secret never leaves the master.
	invalid := func(status int, edit func(c *network.Change)) {
		t.Helper()
		c := change
		c.Backends, c.Try = slices.Clone(change.Backends), slices.Clone(change.Try)
		edit(&c)
		api.do("PUT", "/api/networks/"+n.ID, c, status, nil)
	}
	invalid(http.StatusBadRequest, func(c *network.Change) {
		c.Backends = append(c.Backends, network.Backend{Ref: vanilla, Name: "vanilla"})
	})
	invalid(http.StatusBadRequest, func(c *network.Change) { c.Backends = append(c.Backends, network.Backend{Ref: proxy, Name: "proxy"}) })
	invalid(http.StatusBadRequest, func(c *network.Change) { c.Try = []string{"nope"} })
	invalid(http.StatusBadRequest, func(c *network.Change) { c.Backends[1].Name = "lobby" })
	invalid(http.StatusBadRequest, func(c *network.Change) { c.Forwarding = network.Legacy; c.Firewalled = true }) // Fabric
	invalid(http.StatusConflict, func(c *network.Change) { c.Forwarding = network.Legacy; c.Backends = c.Backends[:2] })
	api.do("POST", "/api/networks", map[string]any{"name": "main", "proxy": proxy, "servers": []network.Ref{lobby}}, http.StatusConflict, nil)
	api.do("DELETE", "/api/nodes/"+lobby.NodeID+"/servers/"+lobby.ServerID, nil, http.StatusConflict, nil)
	if body := api.do("GET", "/api/networks", nil, http.StatusOK, nil); strings.Contains(body, secret) {
		t.Fatal("the API exposes the forwarding secret")
	}

	// A server that leaves loses its forwarding mod and accepts players directly again.
	change.Backends, change.Forwarding, change.Firewalled = change.Backends[:2], network.Legacy, true
	api.do("PUT", "/api/networks/"+n.ID, change, http.StatusOK, &n)
	if mods := files(t, filepath.Join(a1.runtime.dir, fabric.ServerID, "mods")); !slices.Equal(mods, []string{"fabricapi-0.119.jar"}) {
		t.Fatalf("mods of the Fabric server after leaving = %q", mods)
	}
	if got := a1.runtime.network(fabric.ServerID); got.Forwarding != runtime.ForwardingNone {
		t.Fatalf("fabric network after leaving = %+v", got)
	}
	// Legacy forwarding has no secret.
	if got := a2.runtime.network(survival.ServerID); got.Forwarding != runtime.ForwardingLegacy || got.ForwardingSecret != "" {
		t.Fatalf("survival network with legacy forwarding = %+v", got)
	}
	wantProxy(runtime.ForwardingLegacy, []string{"lobby", "smp"}, []runtime.ForcedHost{{Host: "smp.example.com", Servers: []string{"smp", "lobby"}}}, lobbyBackend, smp)

	// A server that moves to the proxy's node is reached there and still trusts the proxy.
	api.do("POST", "/api/nodes/"+survival.NodeID+"/servers/"+survival.ServerID+"/move", map[string]any{"node": a1.node.ID}, http.StatusAccepted, nil)
	if mv := waitForMove(t, api, survival.ServerID); mv.Phase != "done" || len(mv.Warnings) != 0 {
		t.Fatalf("move = %+v", mv)
	}
	smp = runtime.NetworkBackend{Name: "smp", ServerID: survival.ServerID}
	wantProxy(runtime.ForwardingLegacy, []string{"lobby", "smp"}, []runtime.ForcedHost{{Host: "smp.example.com", Servers: []string{"smp", "lobby"}}}, lobbyBackend, smp)
	if got := a1.runtime.network(survival.ServerID); got.Forwarding != runtime.ForwardingLegacy || !got.ProxyOnNode {
		t.Fatalf("moved survival network = %+v", got)
	}
	// Without the confirmation of a firewall, no server of a legacy network moves away.
	change.Firewalled = false
	change.Backends[1].Ref = network.Ref{NodeID: a1.node.ID, ServerID: survival.ServerID}
	api.do("PUT", "/api/networks/"+n.ID, change, http.StatusOK, &n)
	api.do("POST", "/api/nodes/"+lobby.NodeID+"/servers/"+lobby.ServerID+"/move", map[string]any{"node": a2.node.ID}, http.StatusConflict, nil)

	// Actions on the whole network.
	api.do("POST", "/api/networks/"+n.ID+"/start", nil, http.StatusNoContent, nil)
	for _, ref := range []network.Ref{proxy, lobby} {
		if state := serverState(t, api, ref); state != "running" {
			t.Fatalf("%s after starting the network: %s", ref.ServerID, state)
		}
	}
	api.do("POST", "/api/networks/"+n.ID+"/broadcast", map[string]string{"message": "Restart in 5 minutes"}, http.StatusNoContent, nil)
	if got := a1.runtime.consoleCommands(); !slices.Contains(got, "say Restart in 5 minutes") {
		t.Fatalf("console commands = %q", got)
	}
	api.do("POST", "/api/networks/"+n.ID+"/broadcast", map[string]string{"message": " "}, http.StatusBadRequest, nil)

	// The proxy's own settings are edited in its configuration, which it reloads.
	settingsPath := "/api/nodes/" + proxy.NodeID + "/servers/" + proxy.ServerID + "/proxy"
	var settings network.ProxySettings
	api.do("GET", settingsPath, nil, http.StatusOK, &settings)
	if settings.Exists || settings.File != "velocity.toml" {
		t.Fatalf("settings before the first start = %+v", settings)
	}
	check(t, os.WriteFile(filepath.Join(a1.runtime.dir, proxy.ServerID, "velocity.toml"), []byte("motd = \"Hi\"\nbind = \"0.0.0.0:25565\"\n[advanced]\nlogin-ratelimit = 3000\n"), 0o600))
	api.do("GET", settingsPath, nil, http.StatusOK, &settings)
	if string(settings.Settings["advanced.login-ratelimit"]) != "3000" || len(settings.Locked) != 1 || settings.Locked[0].Key != "bind" {
		t.Fatalf("settings = %+v", settings)
	}
	var updated struct{ Reloaded bool }
	api.do("PUT", settingsPath, map[string]any{"settings": map[string]any{"motd": "<green>Welcome", "advanced.login-ratelimit": 1000}}, http.StatusOK, &updated)
	if config, _ := os.ReadFile(filepath.Join(a1.runtime.dir, proxy.ServerID, "velocity.toml")); !updated.Reloaded ||
		!strings.Contains(string(config), "<green>Welcome") || !slices.Contains(a1.runtime.reloaded(), proxy.ServerID) {
		t.Fatalf("reloaded = %v, velocity.toml = %s", updated.Reloaded, config)
	}
	api.do("PUT", settingsPath, map[string]any{"settings": map[string]any{"bind": "0.0.0.0:1"}}, http.StatusBadRequest, nil)
	api.do("PUT", settingsPath, map[string]any{"settings": map[string]any{"advanced.login-ratelimit": "fast"}}, http.StatusBadRequest, nil)

	api.do("POST", "/api/networks/"+n.ID+"/stop", nil, http.StatusNoContent, nil)
	if state := serverState(t, api, proxy); state != "stopped" {
		t.Fatalf("proxy after stopping the network: %s", state)
	}

	api.do("DELETE", "/api/networks/"+n.ID, nil, http.StatusOK, nil)
	for _, ref := range []network.Ref{lobby, {NodeID: a1.node.ID, ServerID: survival.ServerID}, proxy} {
		if got := a1.runtime.network(ref.ServerID); got.Forwarding != runtime.ForwardingNone {
			t.Fatalf("%s still forwards after the network was deleted: %+v", ref.ServerID, got)
		}
	}
	if body := api.do("GET", "/api/networks", nil, http.StatusOK, nil); body != "[]\n" {
		t.Fatalf("networks after delete = %s", body)
	}
}

func TestBungeeNetwork(t *testing.T) {
	m := startMaster(t)
	a := m.startAgent(t, "node-1")
	proxy := m.createServer(t, a, "Proxy", noryxv1.ServerType_SERVER_TYPE_BUNGEECORD, 25577)
	lobby := m.createServer(t, a, "Lobby", noryxv1.ServerType_SERVER_TYPE_PAPER, 25565)
	forge := m.createServer(t, a, "Forge", noryxv1.ServerType_SERVER_TYPE_FORGE, 25566)
	api := apiClient{t: t, url: m.panel(t).URL}

	api.do("POST", "/api/networks", map[string]any{"name": "Bungee", "proxy": proxy, "forwarding": "modern", "servers": []network.Ref{lobby}}, http.StatusBadRequest, nil)
	var n network.Network
	api.do("POST", "/api/networks", map[string]any{"name": "Bungee", "proxy": proxy, "servers": []network.Ref{lobby}}, http.StatusCreated, &n)
	if n.Forwarding != network.Legacy || n.ProxyType != "bungeecord" {
		t.Fatalf("network = %+v", n)
	}
	change := network.Change{
		Name: n.Name, Forwarding: network.Legacy,
		Backends:    []network.Backend{{Ref: lobby, Name: "lobby", Motd: "&aLobby", Restricted: true}},
		Try:         []string{"lobby"},
		ForcedHosts: []network.ForcedHost{{Host: "play.example.com", Servers: []string{"lobby"}}},
	}
	api.do("PUT", "/api/networks/"+n.ID, change, http.StatusOK, &n)
	got := a.runtime.network(proxy.ServerID)
	if want := (runtime.NetworkBackend{Name: "lobby", ServerID: lobby.ServerID, Restricted: true, Motd: "&aLobby"}); got.Forwarding != runtime.ForwardingLegacy || !slices.Equal(got.Backends, []runtime.NetworkBackend{want}) {
		t.Fatalf("proxy network = %+v", got)
	}
	// BungeeCord sends a host name to one server. A Forge server can't join before its mod
	// is found on Modrinth, which the fake doesn't have.
	change.ForcedHosts[0].Servers = []string{"lobby", "lobby"}
	api.do("PUT", "/api/networks/"+n.ID, change, http.StatusBadRequest, nil)
	change.ForcedHosts = nil
	change.Backends = append(change.Backends, network.Backend{Ref: forge, Name: "forge"})
	api.do("PUT", "/api/networks/"+n.ID, change, http.StatusBadGateway, nil)
}

func sameHost(a, b runtime.ForcedHost) bool {
	return a.Host == b.Host && slices.Equal(a.Servers, b.Servers)
}

// files returns the names of the files in a folder.
func files(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	check(t, err)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

// serverState returns the state of a server as the panel shows it.
func serverState(t *testing.T, api apiClient, ref network.Ref) string {
	t.Helper()
	var servers []struct{ ID, State string }
	api.do("GET", "/api/nodes/"+ref.NodeID+"/servers", nil, http.StatusOK, &servers)
	for _, s := range servers {
		if s.ID == ref.ServerID {
			return s.State
		}
	}
	return ""
}

// The proxy follows a server on another node to its new port, and the servers of a node to
// the node's new address, without applying the network again by hand.
func TestNetworkFollowsAddresses(t *testing.T) {
	m := startMaster(t)
	a1, a2 := m.startAgent(t, "node-1"), m.startAgent(t, "node-2")
	proxy := m.createServer(t, a1, "Proxy", noryxv1.ServerType_SERVER_TYPE_VELOCITY, 25577)
	survival := m.createServer(t, a2, "Survival", noryxv1.ServerType_SERVER_TYPE_PAPER, 25570)
	api := apiClient{t: t, url: m.panel(t).URL}
	api.do("POST", "/api/networks", map[string]any{"name": "Main", "proxy": proxy, "servers": []network.Ref{survival}}, http.StatusCreated, nil)
	wantAddress := func(host, port string) {
		t.Helper()
		if got := a1.runtime.network(proxy.ServerID).Backends[0].Address; got != net.JoinHostPort(host, port) {
			t.Fatalf("the proxy reaches survival at %s, want %s", got, net.JoinHostPort(host, port))
		}
	}
	host, port, _ := net.SplitHostPort(a2.node.Address)
	wantAddress(host, "25570")

	var updated struct{ Port int }
	api.do("PUT", "/api/nodes/"+a2.node.ID+"/servers/"+survival.ServerID,
		map[string]any{"name": "Survival", "version": "LATEST", "memoryMb": 1024, "port": 25571, "restartPolicy": "always"}, http.StatusOK, &updated)
	wantAddress(host, "25571")

	var node struct{ Status, Warning string }
	api.do("PUT", "/api/nodes/"+a2.node.ID, map[string]any{"name": "node-2", "address": net.JoinHostPort("localhost", port), "defaultStorage": "default"}, http.StatusOK, &node)
	if node.Status != "online" || node.Warning != "" {
		t.Fatalf("node = %+v", node)
	}
	wantAddress("localhost", "25571")

	// While the proxy's node is offline, the change is saved with a warning, and the network
	// tells that its proxy may be out of date until it is applied again.
	gone := listen(t)
	gone.Close()
	proxyNode := map[string]any{"name": "node-1", "address": gone.Addr().String(), "defaultStorage": "default"}
	api.do("PUT", "/api/nodes/"+a1.node.ID, proxyNode, http.StatusOK, nil)
	var saved struct{ Warning string }
	api.do("PUT", "/api/nodes/"+a2.node.ID+"/servers/"+survival.ServerID,
		map[string]any{"name": "Survival", "version": "LATEST", "memoryMb": 1024, "port": 25572, "restartPolicy": "always"}, http.StatusOK, &saved)
	var n network.Network
	api.do("GET", "/api/networks", nil, http.StatusOK, &[]*network.Network{&n})
	if !strings.Contains(saved.Warning, "the proxy could not be configured") || n.ApplyError != saved.Warning {
		t.Fatalf("warning %q, network %+v", saved.Warning, n)
	}
	proxyNode["address"] = a1.node.Address
	api.do("PUT", "/api/nodes/"+a1.node.ID, proxyNode, http.StatusOK, nil)
	api.do("POST", "/api/networks/"+n.ID+"/apply", nil, http.StatusOK, nil)
	var applied network.Network
	api.do("GET", "/api/networks/"+n.ID, nil, http.StatusOK, &applied)
	if applied.ApplyError != "" {
		t.Fatalf("network = %+v", applied)
	}
	wantAddress("localhost", "25572")
}

// A network moves from its BungeeCord proxy to another, which takes over its configuration and
// the Maintenance plugin, then to Velocity on another node, which forwards the modern way.
func TestSwapProxy(t *testing.T) {
	m := startMaster(t)
	a1, a2 := m.startAgent(t, "node-1"), m.startAgent(t, "node-2")
	old := m.createServer(t, a1, "Old BungeeCord", noryxv1.ServerType_SERVER_TYPE_BUNGEECORD, 25577)
	bungee := m.createServer(t, a1, "BungeeCord", noryxv1.ServerType_SERVER_TYPE_BUNGEECORD, 25578)
	velocity := m.createServer(t, a2, "Velocity", noryxv1.ServerType_SERVER_TYPE_VELOCITY, 25579)
	lobby := m.createServer(t, a1, "Lobby", noryxv1.ServerType_SERVER_TYPE_PAPER, 25565)
	survival := m.createServer(t, a2, "Survival", noryxv1.ServerType_SERVER_TYPE_PAPER, 25570)
	api := apiClient{t: t, url: m.panel(t).URL}
	var n network.Network
	api.do("POST", "/api/networks", map[string]any{"name": "Main", "proxy": old, "firewalled": true, "servers": []network.Ref{lobby, survival}}, http.StatusCreated, &n)
	file := func(a agent, ref network.Ref, name string) string {
		data, _ := os.ReadFile(filepath.Join(a.runtime.dir, ref.ServerID, name))
		return string(data)
	}
	write := func(a agent, ref network.Ref, name, content string) {
		path := filepath.Join(a.runtime.dir, ref.ServerID, name)
		check(t, os.MkdirAll(filepath.Dir(path), 0o750))
		check(t, os.WriteFile(path, []byte(content), 0o600))
	}
	config, players := "motd: '&aWelcome'\nip_forward: true\n", "8667ba71-b85a-4004-af54-457a9734eed7: Steve\n"
	write(a1, old, "config.yml", config)
	write(a1, old, "plugins/Maintenance/config.yml", "maintenance-enabled: true\n")
	write(a1, old, "plugins/Maintenance/WhitelistedPlayers.yml", players)
	check(t, a1.runtime.Start(t.Context(), old.ServerID))
	swap := func(sw network.Swap, status int) {
		t.Helper()
		api.do("POST", "/api/networks/"+n.ID+"/proxy", sw, status, &n)
	}

	// The new proxy must be another one, a proxy, and free.
	swap(network.Swap{Proxy: old}, http.StatusBadRequest)
	swap(network.Swap{Proxy: lobby}, http.StatusBadRequest)
	swap(network.Swap{Proxy: bungee}, http.StatusConflict) // survival on another node needs the firewall

	swap(network.Swap{Proxy: bungee, Firewalled: true}, http.StatusOK)
	if n.Proxy != bungee || n.ProxyType != "bungeecord" || n.Forwarding != network.Legacy {
		t.Fatalf("network = %+v", n)
	}
	if file(a1, bungee, "config.yml") != config || file(a1, bungee, "plugins/Maintenance/WhitelistedPlayers.yml") != players ||
		file(a1, bungee, "plugins/VCAqN1ln-5.1.0.jar") == "" {
		t.Fatalf("files of BungeeCord = %q", files(t, filepath.Join(a1.runtime.dir, bungee.ServerID, "plugins")))
	}
	var maintenance network.Maintenance
	api.do("GET", "/api/networks/"+n.ID+"/maintenance", nil, http.StatusOK, &maintenance)
	if !maintenance.Enabled || len(maintenance.Players) != 1 || !maintenance.ProxyRunning {
		t.Fatalf("maintenance = %+v", maintenance)
	}
	if got := a1.runtime.network(old.ServerID); got.Forwarding != runtime.ForwardingNone || serverState(t, api, old) != "stopped" {
		t.Fatalf("the old proxy after the swap: %+v, %s", got, serverState(t, api, old))
	}
	if got := a1.runtime.network(bungee.ServerID); got.Forwarding != runtime.ForwardingLegacy || len(got.Backends) != 2 {
		t.Fatalf("BungeeCord network = %+v", got)
	}

	// Velocity reads another configuration, and the servers restart for modern forwarding.
	swap(network.Swap{Proxy: velocity}, http.StatusOK)
	if n.Proxy != velocity || n.Forwarding != network.Modern || file(a2, velocity, "velocity.toml") != "" ||
		file(a2, velocity, "plugins/maintenance/config.yml") != "maintenance-enabled: true\n" {
		t.Fatalf("network = %+v, files of Velocity = %q", n, files(t, filepath.Join(a2.runtime.dir, velocity.ServerID, "plugins")))
	}
	if got := a2.runtime.network(survival.ServerID); got.Forwarding != runtime.ForwardingModern || !got.ProxyOnNode {
		t.Fatalf("survival network = %+v", got)
	}
	if got := a1.runtime.network(lobby.ServerID); got.Forwarding != runtime.ForwardingModern || got.ProxyOnNode || got.ForwardingSecret == "" {
		t.Fatalf("lobby network = %+v", got)
	}
	if serverState(t, api, bungee) != "stopped" || serverState(t, api, velocity) != "running" {
		t.Fatalf("BungeeCord %s, Velocity %s", serverState(t, api, bungee), serverState(t, api, velocity))
	}
}
