package network

import (
	"slices"
	"strconv"
	"testing"
	"time"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
)

func TestBackendName(t *testing.T) {
	existing := []Backend{{Name: "survival"}, {Name: "survival-2"}}
	for server, want := range map[string]string{
		"Lobby":                                "lobby",
		"Survival":                             "survival-3",
		"  Skyblock #1 ":                       "skyblock-1",
		"Try":                                  "try-server",
		"Ünïcode ✓":                            "n-code",
		"!!!":                                  "server",
		"A very long server name that is long": "a-very-long-server-name-that",
	} {
		if got := backendName(server, existing); got != want {
			t.Errorf("backendName(%q) = %q, want %q", server, got, want)
		}
	}
}

func TestCopyName(t *testing.T) {
	existing := []Backend{{Name: "lobby"}, {Name: "lobby-2"}, {Name: "game"}, {Name: "a-very-long-name-of-a-server-123"}}
	for original, want := range map[string]string{
		"lobby":                            "lobby-3",
		"lobby-2":                          "lobby-3",
		"game":                             "game-2",
		"skyblock-1":                       "skyblock-2",
		"a-very-long-name-of-a-server-123": "a-very-long-name-of-a-server-2",
		"2024":                             "2024-2",
	} {
		if got := copyName(original, existing); got != want || !noryxv1.ValidBackendName(got) {
			t.Errorf("copyName(%q) = %q, want %q", original, got, want)
		}
	}
	if got := after([]string{"lobby", "game"}, "lobby", "lobby-2"); !slices.Equal(got, []string{"lobby", "lobby-2", "game"}) {
		t.Errorf("after = %q", got)
	}
	if got := after([]string{"game"}, "lobby", "lobby-2"); !slices.Equal(got, []string{"game"}) {
		t.Errorf("after without the original = %q", got)
	}
}

func TestMaintenanceRequests(t *testing.T) {
	changes := func(c MaintenanceChange) []string {
		reqs, err := c.requests()
		if err != nil {
			return []string{err.Error()}
		}
		var out []string
		for _, r := range reqs {
			out = append(out, r.GetChange().String()+" "+r.GetServer()+" "+strconv.Itoa(int(r.GetMinutes()))+" "+strconv.Itoa(int(r.GetDurationMinutes())))
		}
		return out
	}
	for _, tc := range []struct {
		change MaintenanceChange
		want   []string
	}{
		{MaintenanceChange{Enabled: true}, []string{"MAINTENANCE_CHANGE_ON  0 0"}},
		{MaintenanceChange{Enabled: true, Server: "lobby", Duration: 60}, []string{"MAINTENANCE_CHANGE_ON lobby 0 0", "MAINTENANCE_CHANGE_END_TIMER lobby 60 0"}},
		{MaintenanceChange{Enabled: true, Delay: 5}, []string{"MAINTENANCE_CHANGE_START_TIMER  5 0"}},
		{MaintenanceChange{Enabled: true, Delay: 5, Duration: 60}, []string{"MAINTENANCE_CHANGE_SCHEDULE  5 60"}},
		{MaintenanceChange{Server: "lobby"}, []string{"MAINTENANCE_CHANGE_OFF lobby 0 0"}},
		{MaintenanceChange{Delay: 30}, []string{"MAINTENANCE_CHANGE_END_TIMER  30 0"}},
		{MaintenanceChange{Duration: 30}, []string{"Choose times from 1 minute to 28 days."}},
		{MaintenanceChange{Enabled: true, Delay: noryxv1.MaxMaintenanceMinutes + 1}, []string{"Choose times from 1 minute to 28 days."}},
		{MaintenanceChange{Enabled: true, Server: "global"}, []string{"The Maintenance plugin reads global as the whole network. Rename the server in the network to change its maintenance alone."}},
	} {
		if got := changes(tc.change); !slices.Equal(got, tc.want) {
			t.Errorf("%+v = %q, want %q", tc.change, got, tc.want)
		}
	}
}

func TestValidate(t *testing.T) {
	proxy, other := Ref{"n1", "proxy"}, Ref{"n2", "survival"}
	valid := func(proxyType string) Network {
		return Network{
			Name: "Main", Proxy: proxy, ProxyType: proxyType, Forwarding: Modern,
			Backends:    []Backend{{Ref: Ref{"n1", "lobby"}, Name: "lobby", Restricted: true, Motd: "Hi"}, {Ref: other, Name: "survival"}},
			Try:         []string{"lobby"},
			ForcedHosts: []ForcedHost{{Host: " Survival.Example.com ", Servers: []string{"survival", "lobby"}}},
		}
	}
	n := valid("velocity")
	if err := n.validate(nil); err != nil {
		t.Fatal(err)
	}
	// Host names are compared in lower case, and Velocity has no settings of BungeeCord.
	if n.ForcedHosts[0].Host != "survival.example.com" || n.Backends[0].Restricted || n.Backends[0].Motd != "" {
		t.Fatalf("canonical network = %+v", n)
	}
	for name, edit := range map[string]func(n *Network){
		"no name":               func(n *Network) { n.Name = " " },
		"unknown forwarding":    func(n *Network) { n.Forwarding = "none" },
		"modern BungeeCord":     func(n *Network) { n.ProxyType = "bungeecord"; n.ForcedHosts[0].Servers = n.ForcedHosts[0].Servers[:1] },
		"no servers":            func(n *Network) { n.Backends, n.Try, n.ForcedHosts = nil, nil, nil },
		"nobody joins":          func(n *Network) { n.Try = nil },
		"unknown server in try": func(n *Network) { n.Try = []string{"nope"} },
		"server twice in try":   func(n *Network) { n.Try = []string{"lobby", "lobby"} },
		"name in capitals":      func(n *Network) { n.Backends[0].Name, n.Try = "Lobby", []string{"Lobby"} },
		"name try":              func(n *Network) { n.Backends[0].Name, n.Try = "try", []string{"try"} },
		"same name twice":       func(n *Network) { n.Backends[1].Name = "lobby" },
		"same server twice":     func(n *Network) { n.Backends[1].Ref = n.Backends[0].Ref },
		"proxy as server":       func(n *Network) { n.Backends[1].Ref = proxy },
		"host name with port":   func(n *Network) { n.ForcedHosts[0].Host = "a.example.com:25565" },
		"host name twice": func(n *Network) {
			n.ForcedHosts = append(n.ForcedHosts, ForcedHost{"SURVIVAL.example.com", []string{"lobby"}})
		},
		"host name without server": func(n *Network) { n.ForcedHosts[0].Servers = nil },
		"exposed legacy servers":   func(n *Network) { n.Forwarding = Legacy },
	} {
		n := valid("velocity")
		edit(&n)
		if n.validate(nil) == nil {
			t.Errorf("%s: accepted", name)
		}
	}

	// BungeeCord sends a host name to one server and keeps the settings of the servers.
	n = valid("waterfall")
	n.Forwarding, n.Firewalled = Legacy, true
	n.ForcedHosts[0].Servers = []string{"survival"}
	if err := n.validate(nil); err != nil || !n.Backends[0].Restricted || n.Backends[0].Motd != "Hi" {
		t.Fatalf("BungeeCord network = %+v, %v", n, err)
	}
	n.ForcedHosts[0].Servers = []string{"survival", "lobby"}
	if n.validate(nil) == nil {
		t.Error("BungeeCord accepted two servers for a host name")
	}
}

func TestExposed(t *testing.T) {
	n := Network{Proxy: Ref{"n1", "proxy"}, Forwarding: Legacy, Backends: []Backend{{Ref: Ref{"n1", "lobby"}}}}
	if n.exposed(nil) {
		t.Fatal("a server on the proxy's node is exposed")
	}
	// Moving the server, or the proxy, to another node exposes the server, unless a firewall
	// or the private network protects it.
	for _, move := range []string{"lobby", "proxy"} {
		moved := n
		moved.Backends = slices.Clone(n.Backends)
		moved.moved(move, "n1", "n2")
		if !moved.exposed(nil) || !moved.exposed(members{"n1": {Address: "10.213.0.1"}}) {
			t.Errorf("moving %s didn't expose the server", move)
		}
		if moved.exposed(members{"n1": {Address: "10.213.0.1"}, "n2": {Address: "10.213.0.2"}}) {
			t.Errorf("moving %s exposed the server in the private network", move)
		}
		if moved.Firewalled = true; moved.exposed(nil) {
			t.Errorf("moving %s exposed the server behind a firewall", move)
		}
	}
	if n.Forwarding = Modern; n.exposed(nil) {
		t.Fatal("modern forwarding is exposed")
	}
}

// A backend that its proxy reaches over the private network publishes its port there for the
// proxy's node with its key; one on the proxy's node, or outside the network, doesn't.
func TestRequest(t *testing.T) {
	n := Network{Proxy: Ref{"n1", "proxy"}, Forwarding: Modern, secret: "s3cret"}
	m := members{"n1": {Address: "10.213.0.1", PublicKey: "key-1"}, "n2": {Address: "10.213.0.2", PublicKey: "key-2"}}
	for _, tc := range []struct {
		ref       Ref
		want, key string
	}{
		{Ref{"n2", "survival"}, "10.213.0.1", "key-1"},
		{Ref{"n3", "skyblock"}, "", ""},
		{Ref{"n1", "lobby"}, "", ""},
		{n.Proxy, "", ""},
	} {
		if req := n.request(tc.ref, m); req.GetOverlayClient() != tc.want || req.GetOverlayClientKey() != tc.key {
			t.Errorf("%s: overlay client %q with key %q, want %q with %q", tc.ref.ServerID, req.GetOverlayClient(), req.GetOverlayClientKey(), tc.want, tc.key)
		}
	}
}

func TestSendCommand(t *testing.T) {
	backends := []Backend{{Name: "lobby"}, {Name: "survival-2"}}
	for _, tt := range []struct {
		proxy, player string
		alone         bool
	}{
		{"velocity", "Steve", true},
		{"velocity", "Lobby", true},
		{"velocity", "all", false},
		{"bungeecord", "Current", false},
		{"bungeecord", "Lobby", false}, // BungeeCord sends the players of the server lobby
		{"waterfall", "survival_2", true},
	} {
		n := Network{ProxyType: tt.proxy, Backends: backends}
		if command, alone := n.SendCommand(tt.player, "survival-2"); command != "send "+tt.player+" survival-2" || alone != tt.alone {
			t.Errorf("%s: SendCommand(%q) = %q, %v", tt.proxy, tt.player, command, alone)
		}
	}
}

// Restarting some servers safely and moving their players only takes different game servers of
// the network, which the panel names by their names in it.
func TestCheckBackends(t *testing.T) {
	lobby, game := Ref{"n1", "lobby"}, Ref{"n2", "game"}
	n := Network{Proxy: Ref{"n1", "proxy"}, Backends: []Backend{{Ref: lobby, Name: "lobby"}, {Ref: game, Name: "game"}}}
	if err := n.checkBackends([]Ref{game, lobby}); err != nil || !slices.Equal(n.names([]Ref{game, lobby}), []string{"lobby", "game"}) {
		t.Fatalf("servers of the network: %v, names %q", err, n.names([]Ref{game, lobby}))
	}
	for _, bad := range [][]Ref{{n.Proxy}, {Ref{"n3", "other"}}, {Ref{"n2", "lobby"}}, {lobby, lobby}, {{}}} {
		if n.checkBackends(bad) == nil {
			t.Errorf("accepted %v", bad)
		}
	}
}

// A rolling restart gets for each group the longest stop timeout among its servers and the
// time to start again, and an hour at least.
func TestRollingTimeout(t *testing.T) {
	a, b, c := Backend{Ref: Ref{"n1", "a"}}, Backend{Ref: Ref{"n1", "b"}}, Backend{Ref: Ref{"n2", "c"}}
	listed := map[Ref]*noryxv1.Server{a.Ref: {StopTimeoutSeconds: 600}, b.Ref: {StopTimeoutSeconds: 30}} // c with the default
	if got := rollingTimeout([][]Backend{{a, b}, {c}}, listed); got != time.Hour {
		t.Errorf("a few servers: %s", got)
	}
	groups := slices.Repeat([][]Backend{{b, a}, {c}}, 5)
	if got, want := rollingTimeout(groups, listed), 5*(10*time.Minute+time.Minute+2*groupAllowance); got != want {
		t.Errorf("many servers: %s, want %s", got, want)
	}
}
