package e2e

import (
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	"github.com/QwikByte/noryx/internal/master/network"
	"github.com/QwikByte/noryx/internal/master/player"
	"github.com/QwikByte/noryx/internal/master/schedule"
)

func TestPlayers(t *testing.T) {
	m := startMaster(t)
	a := m.startAgent(t, "node-1")
	proxy := m.createServer(t, a, "Proxy", noryxv1.ServerType_SERVER_TYPE_VELOCITY, 25577)
	lobby := m.createServer(t, a, "Lobby", noryxv1.ServerType_SERVER_TYPE_PAPER, 25565)
	game := m.createServer(t, a, "Game", noryxv1.ServerType_SERVER_TYPE_PAPER, 25566)
	api := apiClient{t: t, url: m.panel(t).URL}
	var n network.Network
	api.do("POST", "/api/networks", map[string]any{"name": "Main", "proxy": proxy, "servers": []network.Ref{lobby, game}}, http.StatusCreated, &n)
	check(t, a.runtime.Start(t.Context(), lobby.ServerID))

	// A ban reaches the running server at once, and the stopped one once it runs.
	var changed struct{ Results []player.Result }
	ban := map[string]any{"action": "ban", "name": "Alex", "reason": "Cheating", "servers": []network.Ref{lobby, game}}
	api.do("POST", "/api/players/actions", ban, http.StatusOK, &changed)
	if r := changed.Results; len(r) != 2 || r[0].Output != "ran minecraft:ban Alex Cheating" || r[0].Pending || !r[1].Pending || r[1].Error != "" {
		t.Fatalf("results = %+v", r)
	}
	if got := a.runtime.commandsTo(lobby.ServerID); !slices.Equal(got, []string{"minecraft:ban Alex Cheating"}) {
		t.Fatalf("lobby commands = %q", got)
	}
	for _, invalid := range []map[string]any{
		{"action": "ban", "name": "@a", "servers": []network.Ref{lobby}},
		{"action": "pardon", "name": "Alex", "reason": "sorry", "servers": []network.Ref{lobby}},
		{"action": "whitelist_on", "name": "Alex", "servers": []network.Ref{lobby}},
		{"action": "smite", "name": "Alex", "servers": []network.Ref{lobby}},
		{"action": "kick", "name": "Alex", "servers": []network.Ref{}},
	} {
		api.do("POST", "/api/players/actions", invalid, http.StatusBadRequest, nil)
	}

	// The lists of a network join those of its servers, with the changes that wait, and tell
	// when temporary bans end.
	check(t, os.WriteFile(filepath.Join(a.runtime.dir, lobby.ServerID, "banned-players.json"),
		[]byte(`[{"uuid":"u1","name":"Alex","created":"2026-05-01 10:00:00 +0000","source":"Server","expires":"forever","reason":"Cheating"},
			{"uuid":"u2","name":"Kai","created":"2026-05-01 10:00:00 +0000","source":"Essentials","expires":"2026-05-08 10:00:00 +0000","reason":"Spam"}]`), 0o600))
	var lists player.Lists
	api.do("GET", "/api/players/lists?network="+n.ID, nil, http.StatusOK, &lists)
	if len(lists.Banned) != 2 || lists.Banned[0].Reason != "Cheating" || !slices.Equal(lists.Banned[0].Servers, []network.Ref{lobby}) || lists.Banned[0].Until != nil {
		t.Fatalf("banned = %+v", lists.Banned)
	}
	if until := lists.Banned[1].Until; until == nil || !until.Equal(time.Date(2026, 5, 8, 10, 0, 0, 0, time.UTC)) {
		t.Fatalf("temporary ban ends at %v", until)
	}
	if s := lists.Servers; len(s) != 2 || len(s[0].Pending) != 0 || len(s[1].Pending) != 1 || s[1].Pending[0].Action != "ban" {
		t.Fatalf("servers = %+v", s)
	}
	api.do("GET", "/api/players/lists", nil, http.StatusOK, &lists)
	if len(lists.Servers) != 2 {
		t.Fatalf("all game servers = %+v", lists.Servers)
	}
	api.do("GET", "/api/players/lists?node="+game.NodeID+"&server="+game.ServerID, nil, http.StatusOK, &lists)
	if len(lists.Servers) != 1 || lists.Servers[0].Ref != game || len(lists.Servers[0].Pending) != 1 || len(lists.Banned) != 0 {
		t.Fatalf("lists of one server = %+v", lists)
	}
	api.do("GET", "/api/players/lists?server="+game.ServerID, nil, http.StatusBadRequest, nil)

	// Bedrock players are whitelisted with their ID from GeyserMC, as the servers behind the
	// proxy can't look them up: the file of a running server changes, and it reloads it.
	whitelist := map[string]any{"action": "whitelist_add", "name": ".Tim_203", "servers": []network.Ref{lobby, game}}
	var added struct{ Results []player.Result }
	api.do("POST", "/api/players/actions", whitelist, http.StatusOK, &added)
	if r := added.Results; len(r) != 2 || r[0].Output != "Added .Tim_203 to the whitelist" || !r[1].Pending || r[1].Error != "" {
		t.Fatalf("results = %+v", r)
	}
	api.do("GET", "/api/players/lists?network="+n.ID, nil, http.StatusOK, &lists)
	if w := lists.Whitelisted; len(w) != 1 || w[0].UUID != "00000000-0000-0000-0009-01f64f65c7c3" || !slices.Equal(w[0].Servers, []network.Ref{lobby}) {
		t.Fatalf("whitelisted = %+v", w)
	}
	if got := a.runtime.commandsTo(lobby.ServerID); got[len(got)-1] != "minecraft:whitelist reload" {
		t.Fatalf("lobby commands = %q", got)
	}
	whitelist["name"] = ".Nobody"
	api.do("POST", "/api/players/actions", whitelist, http.StatusNotFound, nil)

	// The proxy sends players to servers of its network.
	api.do("POST", "/api/networks/"+n.ID+"/players/send", map[string]string{"name": "Alex", "server": "game"}, http.StatusNoContent, nil)
	if got := a.runtime.commandsTo(proxy.ServerID); !slices.Equal(got, []string{"send Alex game"}) {
		t.Fatalf("proxy commands = %q", got)
	}
	api.do("POST", "/api/networks/"+n.ID+"/players/send", map[string]string{"name": "Alex", "server": "elsewhere"}, http.StatusBadRequest, nil)
	api.do("POST", "/api/networks/"+n.ID+"/players/send", map[string]string{"name": "Alex lobby", "server": "game"}, http.StatusBadRequest, nil)
	api.do("POST", "/api/networks/"+n.ID+"/players/send", map[string]string{"name": "all", "server": "game"}, http.StatusBadRequest, nil) // everyone
	if got := a.runtime.commandsTo(proxy.ServerID); len(got) != 1 {
		t.Fatalf("proxy commands = %q", got)
	}
}

func TestMaintenance(t *testing.T) {
	m := startMaster(t)
	a := m.startAgent(t, "node-1")
	proxy := m.createServer(t, a, "Proxy", noryxv1.ServerType_SERVER_TYPE_VELOCITY, 25577)
	lobby := m.createServer(t, a, "Lobby", noryxv1.ServerType_SERVER_TYPE_PAPER, 25565)
	api := apiClient{t: t, url: m.panel(t).URL}
	var n network.Network
	api.do("POST", "/api/networks", map[string]any{"name": "Main", "proxy": proxy, "servers": []network.Ref{lobby}}, http.StatusCreated, &n)
	path := "/api/networks/" + n.ID + "/maintenance"

	// The proxy acts like the Maintenance plugin: it loads once it restarted with the plugin.
	// The agent calls it, so it leaves failures to the checks of the test.
	folder := filepath.Join(a.runtime.dir, proxy.ServerID, "plugins", "maintenance")
	write := func(name, content string) { _ = os.WriteFile(filepath.Join(folder, name), []byte(content), 0o600) }
	a.runtime.on = func(id, what string) {
		switch {
		case id != proxy.ServerID:
		case what == "restart":
			if _, err := os.Stat(filepath.Join(folder, "..", "VCAqN1ln-5.1.0.jar")); err == nil {
				_ = os.MkdirAll(folder, 0o750)
				write("config.yml", "maintenance-enabled: false\n")
			}
		case what == "maintenance on":
			write("config.yml", "maintenance-enabled: true\n")
		case what == "maintenance off":
			write("config.yml", "maintenance-enabled: false\n")
		case what == "maintenance add Steve":
			write("WhitelistedPlayers.yml", "8667ba71-b85a-4004-af54-457a9734eed7: Steve\n")
		case what == "maintenance on lobby":
			write("config.yml", "maintenance-enabled: false\nproxied-maintenance-servers:\n  lobby: default\n")
		}
	}

	var got network.Maintenance
	api.do("GET", path, nil, http.StatusOK, &got)
	if got.Installed || got.Enabled || got.ProxyRunning {
		t.Fatalf("maintenance of a new network = %+v", got)
	}
	api.do("POST", path, map[string]bool{"enabled": true}, http.StatusConflict, nil)

	// The first time, the plugin is installed and the proxy restarts to load it.
	check(t, a.runtime.Start(t.Context(), proxy.ServerID))
	api.do("POST", path, map[string]bool{"enabled": true}, http.StatusOK, &got)
	if !got.Installed || !got.Enabled || !slices.Equal(a.runtime.restarted(), []string{proxy.ServerID}) {
		t.Fatalf("maintenance = %+v, restarts %q", got, a.runtime.restarted())
	}
	api.do("POST", path+"/players", map[string]any{"name": "Steve", "add": true}, http.StatusOK, &got)
	if len(got.Players) != 1 || got.Players[0].Name != "Steve" {
		t.Fatalf("players = %+v", got.Players)
	}
	api.do("POST", path+"/players", map[string]any{"name": "@a", "add": true}, http.StatusBadRequest, nil)
	api.do("POST", path, map[string]bool{"enabled": false}, http.StatusOK, &got)
	if got.Enabled || len(a.runtime.restarted()) != 1 {
		t.Fatalf("maintenance after turning it off = %+v", got)
	}
	want := []string{"maintenance on", "maintenance add Steve", "maintenance off"}
	if cmds := a.runtime.commandsTo(proxy.ServerID); !slices.Equal(cmds, want) {
		t.Fatalf("proxy commands = %q, want %q", cmds, want)
	}

	// A server of the network goes into maintenance on its own for an hour: the plugin saves
	// that it is, and keeps the timer that ends it to itself.
	check(t, os.WriteFile(filepath.Join(a.runtime.dir, proxy.ServerID, "velocity.toml"), []byte("[servers]\nlobby = \"lobby:25565\"\ntry = [\"lobby\"]\n"), 0o600))
	api.do("POST", path, map[string]any{"enabled": true, "server": "lobby", "duration": 60}, http.StatusOK, &got)
	if got.Enabled || !slices.Equal(got.Servers, []string{"lobby"}) {
		t.Fatalf("maintenance of lobby = %+v", got)
	}
	// The whole network goes into maintenance in 5 minutes, and a timer is aborted.
	api.do("POST", path, map[string]any{"enabled": true, "delay": 5}, http.StatusOK, &got)
	api.do("POST", path+"/abort", map[string]any{"server": "lobby"}, http.StatusOK, &got)
	// Only servers of the network, and times of up to 28 days.
	for _, invalid := range []map[string]any{
		{"enabled": true, "server": "elsewhere"},
		{"enabled": true, "delay": 40321},
		{"enabled": false, "duration": 60},
		{"enabled": true, "delay": "5"},
	} {
		api.do("POST", path, invalid, http.StatusBadRequest, nil)
	}
	api.do("POST", path+"/abort", map[string]any{"server": "elsewhere"}, http.StatusBadRequest, nil)
	want = append(want, "maintenance on lobby", "maintenance aborttimer lobby", "maintenance endtimer lobby 60",
		"maintenance aborttimer", "maintenance starttimer 5", "maintenance aborttimer lobby")
	if cmds := a.runtime.commandsTo(proxy.ServerID); !slices.Equal(cmds, want) {
		t.Fatalf("proxy commands = %q, want %q", cmds, want)
	}
}

func TestRollingRestart(t *testing.T) {
	m := startMaster(t)
	a := m.startAgent(t, "node-1")
	proxy := m.createServer(t, a, "Proxy", noryxv1.ServerType_SERVER_TYPE_VELOCITY, 25577)
	lobby := m.createServer(t, a, "Lobby", noryxv1.ServerType_SERVER_TYPE_PAPER, 25565)
	games := []network.Ref{
		m.createServer(t, a, "Game 1", noryxv1.ServerType_SERVER_TYPE_PAPER, 25566),
		m.createServer(t, a, "Game 2", noryxv1.ServerType_SERVER_TYPE_PAPER, 25567),
		m.createServer(t, a, "Game 3", noryxv1.ServerType_SERVER_TYPE_PAPER, 25568),
	}
	solo := m.createServer(t, a, "Solo", noryxv1.ServerType_SERVER_TYPE_PAPER, 25569)
	api := apiClient{t: t, url: m.panel(t).URL}
	var n network.Network
	api.do("POST", "/api/networks", map[string]any{"name": "Main", "proxy": proxy, "servers": append([]network.Ref{lobby}, games...)}, http.StatusCreated, &n)
	path := "/api/networks/" + n.ID + "/rolling-restart"
	api.do("POST", path, map[string]int{"batch": 1}, http.StatusConflict, nil) // nothing runs
	for _, s := range []network.Ref{proxy, lobby, games[0], games[1], solo} {
		check(t, a.runtime.Start(t.Context(), s.ServerID))
	}
	a.runtime.mu.Lock()
	a.runtime.online = map[string][]string{lobby.ServerID: {}, games[0].ServerID: {"Alex"}, games[1].ServerID: {}, solo.ServerID: {}}
	a.runtime.mu.Unlock()

	// The running game servers restart two at a time, the lobby players join last; the
	// stopped one stays stopped, and the proxy keeps running.
	api.do("POST", path, map[string]int{"batch": 0}, http.StatusBadRequest, nil)
	api.do("POST", path, map[string]int{"batch": 2}, http.StatusNoContent, nil)
	got := a.runtime.restarted()
	if len(got) != 3 || got[2] != lobby.ServerID || !slices.Contains(got, games[0].ServerID) || !slices.Contains(got, games[1].ServerID) {
		t.Fatalf("restarts = %q", got)
	}

	// One server restarts safely: its players move to the lobby first. Only game servers of the
	// network restart so.
	api.do("POST", path, map[string]any{"batch": 1, "servers": []network.Ref{games[0]}}, http.StatusNoContent, nil)
	if got := a.runtime.restarted()[3:]; !slices.Equal(got, []string{games[0].ServerID}) {
		t.Fatalf("restarts = %q", got)
	}
	if got := a.runtime.commandsTo(proxy.ServerID); got[len(got)-1] != "send Alex lobby" {
		t.Fatalf("proxy commands = %q", got)
	}
	for _, bad := range [][]network.Ref{{proxy}, {solo}, {games[1], games[1]}} {
		api.do("POST", path, map[string]any{"batch": 1, "servers": bad}, http.StatusBadRequest, nil)
	}

	// The players of a server move to another one without a restart.
	move := "/api/networks/" + n.ID + "/players/move"
	var moved struct{ Players int }
	api.do("POST", move, map[string]any{"server": games[0]}, http.StatusOK, &moved)
	if got := a.runtime.commandsTo(proxy.ServerID); moved.Players != 1 || got[len(got)-1] != "send Alex lobby" || len(a.runtime.restarted()) != 4 {
		t.Fatalf("moved %d players, proxy commands %q", moved.Players, got)
	}
	api.do("POST", move, map[string]any{"server": games[2]}, http.StatusConflict, nil) // it doesn't run
	api.do("POST", move, map[string]any{"server": proxy}, http.StatusBadRequest, nil)

	// A schedule restarts the game servers of the network server by server, the lobby last,
	// then the proxy; the server outside of networks restarts at once.
	var task schedule.Task
	api.do("POST", "/api/policies", map[string]any{
		"name": "Nightly", "enabled": true, "settings": map[string]any{"action": "restart", "rolling": 1},
		"schedule": map[string]any{"times": []string{"04:00"}, "timeZone": "UTC"}, "targets": []map[string]string{{"nodeId": a.node.ID}},
	}, http.StatusCreated, &task)
	commands := len(a.runtime.commandsTo(proxy.ServerID))
	run(t, api, "/api/policies/"+task.ID)
	got = a.runtime.restarted()[4:]
	inNetwork := slices.DeleteFunc(slices.Clone(got), func(id string) bool { return id == solo.ServerID })
	if want := []string{games[0].ServerID, games[1].ServerID, lobby.ServerID, proxy.ServerID}; len(got) != 5 || !slices.Equal(inNetwork, want) {
		t.Fatalf("restarts = %q", got)
	}
	if got := a.runtime.commandsTo(proxy.ServerID)[commands:]; !slices.Equal(got, []string{"send Alex lobby"}) {
		t.Fatalf("proxy commands = %q", got)
	}

	// A schedule for the network does so too, without the server outside of it.
	api.do("POST", "/api/policies", map[string]any{
		"name": "Main", "enabled": true, "settings": map[string]any{"action": "restart", "rolling": 1},
		"schedule": map[string]any{"times": []string{"04:00"}, "timeZone": "UTC"}, "targets": []map[string]string{{"kind": "network", "value": n.ID}},
	}, http.StatusCreated, &task)
	run(t, api, "/api/policies/"+task.ID)
	if got := a.runtime.restarted()[9:]; !slices.Equal(got, []string{games[0].ServerID, games[1].ServerID, lobby.ServerID, proxy.ServerID}) {
		t.Fatalf("restarts = %q", got)
	}
}
