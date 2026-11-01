package network

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	"github.com/QwikByte/noryx/internal/agent/datadir"
	"github.com/QwikByte/noryx/internal/agent/runtime"
)

// Excerpt of the default configuration of Velocity 4.2.
const defaultVelocity = `
# Config version. Do not change this
config-version = "2.9"
bind = "0.0.0.0:25565"
motd = "<#09add3>My Network"
player-info-forwarding-mode = "NONE"
forwarding-secret-file = "forwarding.secret"

[servers]
lobby = "127.0.0.1:30066"
factions = "127.0.0.1:30067"
try = [
    "lobby"
]

[forced-hosts]
"lobby.example.com" = [
    "lobby"
]

[advanced]
compression-threshold = 256
`

// Excerpt of the default configuration of BungeeCord.
const defaultBungee = `
online_mode: true
ip_forward: false
servers:
  lobby:
    motd: '&1Just another BungeeCord - Forced Host'
    address: localhost:25565
    restricted: false
listeners:
- query_port: 25577
  motd: '&1Another Bungee server'
  forced_hosts:
    pvp.md-5.net: pvp
  priorities:
  - lobby
  host: 0.0.0.0:25577
  max_players: 1
permissions:
  default:
  - bungeecord.command.server
`

var network = runtime.Network{
	Forwarding: runtime.ForwardingModern, ForwardingSecret: "s3cret",
	Backends: []runtime.NetworkBackend{
		{Name: "lobby", Address: "noryx-a:25565"},
		{Name: "survival", Address: "203.0.113.7:25566", Restricted: true, Motd: "Survival"},
	},
	Try:         []string{"lobby", "survival"},
	ForcedHosts: []runtime.ForcedHost{{Host: "survival.example.com", Servers: []string{"survival", "lobby"}}},
}

// dataDir returns a server's data directory with the given files.
func dataDir(t *testing.T, files map[string]string) *datadir.Dir {
	t.Helper()
	path := t.TempDir()
	for name, content := range files {
		if err := os.MkdirAll(filepath.Join(path, filepath.Dir(name)), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	dir, err := datadir.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { dir.Close() })
	return dir
}

// read returns the settings of a file, which must exist.
func read(t *testing.T, dir *datadir.Dir, name string) map[string]any {
	t.Helper()
	data, err := dir.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	settings, err := parse(name, data)
	if err != nil {
		t.Fatal(err)
	}
	return settings
}

// want compares the values at the given paths of nested settings.
func want(t *testing.T, settings map[string]any, values map[string]any) {
	t.Helper()
	for path, value := range values {
		var got any = settings
		for _, key := range strings.Split(path, "/") {
			got = got.(map[string]any)[key]
		}
		if !reflect.DeepEqual(got, value) {
			t.Errorf("%s = %#v, want %#v", path, got, value)
		}
	}
}

func TestWriteProxyVelocity(t *testing.T) {
	for name, files := range map[string]map[string]string{"new proxy": nil, "existing configuration": {"velocity.toml": defaultVelocity}} {
		t.Run(name, func(t *testing.T) {
			dir := dataDir(t, files)
			changed, removed, err := WriteProxy(dir, noryxv1.ServerType_SERVER_TYPE_VELOCITY, network)
			if err != nil || !changed {
				t.Fatalf("changed = %v, err = %v", changed, err)
			}
			if wantRemoved := files != nil; removed != wantRemoved { // factions went away
				t.Errorf("removed = %v, want %v", removed, wantRemoved)
			}
			got := read(t, dir, "velocity.toml")
			want(t, got, map[string]any{
				"config-version":              "2.9",
				"player-info-forwarding-mode": "MODERN",
				"forwarding-secret-file":      "forwarding.secret",
				"servers":                     map[string]any{"lobby": "noryx-a:25565", "survival": "203.0.113.7:25566", "try": []any{"lobby", "survival"}},
				"forced-hosts":                map[string]any{"survival.example.com": []any{"survival", "lobby"}},
			})
			if files != nil {
				want(t, got, map[string]any{"motd": "<#09add3>My Network", "advanced/compression-threshold": int64(256)})
			}
			if secret, _ := dir.ReadFile(ForwardingSecretFile); string(secret) != "s3cret" {
				t.Errorf("secret = %q", secret)
			}
			// Applying the same network again changes nothing, so the proxy doesn't reload.
			if changed, removed, err := WriteProxy(dir, noryxv1.ServerType_SERVER_TYPE_VELOCITY, network); changed || removed || err != nil {
				t.Fatalf("second run: changed = %v, removed = %v, err = %v", changed, removed, err)
			}
			// A proxy that leaves its network loses the secret, which the network may keep with
			// another proxy.
			if changed, _, err := WriteProxy(dir, noryxv1.ServerType_SERVER_TYPE_VELOCITY, runtime.Network{}); !changed || err != nil {
				t.Fatalf("leaving: changed = %v, err = %v", changed, err)
			}
			if _, err := dir.Lstat(ForwardingSecretFile); !os.IsNotExist(err) {
				t.Errorf("the secret stayed after leaving: %v", err)
			}
		})
	}
}

func TestWriteProxyVelocityLegacy(t *testing.T) {
	dir := dataDir(t, map[string]string{"velocity.toml": defaultVelocity})
	legacy := runtime.Network{Forwarding: runtime.ForwardingLegacy, Backends: []runtime.NetworkBackend{{Name: "lobby", Address: "127.0.0.1:30066"}}, Try: []string{"lobby"}}
	if _, _, err := WriteProxy(dir, noryxv1.ServerType_SERVER_TYPE_VELOCITY, legacy); err != nil {
		t.Fatal(err)
	}
	want(t, read(t, dir, "velocity.toml"), map[string]any{"player-info-forwarding-mode": "LEGACY", "forced-hosts": map[string]any{}})
	if _, err := dir.ReadFile(ForwardingSecretFile); err == nil {
		t.Error("legacy forwarding wrote a secret")
	}
}

func TestWriteProxyBungee(t *testing.T) {
	dir := dataDir(t, map[string]string{"config.yml": defaultBungee})
	changed, removed, err := WriteProxy(dir, noryxv1.ServerType_SERVER_TYPE_BUNGEECORD, network)
	if err != nil || !changed || removed {
		t.Fatalf("changed = %v, removed = %v, err = %v", changed, removed, err)
	}
	got := read(t, dir, "config.yml")
	want(t, got, map[string]any{
		"ip_forward":  true,
		"online_mode": true,
		"servers": map[string]any{
			"lobby":    map[string]any{"address": "noryx-a:25565", "motd": "&1Another Bungee server", "restricted": false},
			"survival": map[string]any{"address": "203.0.113.7:25566", "motd": "Survival", "restricted": true},
		},
	})
	listener := got["listeners"].([]any)[0].(map[string]any)
	want(t, listener, map[string]any{
		"priorities":   []any{"lobby", "survival"},
		"forced_hosts": map[string]any{"survival.example.com": "survival"},
		"host":         "0.0.0.0:25577",
		"max_players":  1,
	})

	// BungeeCord can't reload without a server it had.
	smaller := network
	smaller.Backends, smaller.Try, smaller.ForcedHosts = network.Backends[:1], []string{"lobby"}, nil
	if _, removed, err := WriteProxy(dir, noryxv1.ServerType_SERVER_TYPE_BUNGEECORD, smaller); !removed || err != nil {
		t.Fatalf("removed = %v, err = %v", removed, err)
	}

	// A proxy that leaves its network keeps its servers, as BungeeCord needs one.
	if _, _, err := WriteProxy(dir, noryxv1.ServerType_SERVER_TYPE_BUNGEECORD, runtime.Network{}); err != nil {
		t.Fatal(err)
	}
	got = read(t, dir, "config.yml")
	if got["ip_forward"] != false || got["servers"].(map[string]any)["lobby"] == nil {
		t.Fatalf("configuration after leaving = %v", got)
	}
}

func TestProxyBind(t *testing.T) {
	dir := dataDir(t, map[string]string{"velocity.toml": defaultVelocity})
	if changed, err := ProxyBind(dir, noryxv1.ServerType_SERVER_TYPE_VELOCITY, 25577); !changed || err != nil {
		t.Fatalf("changed = %v, err = %v", changed, err)
	}
	want(t, read(t, dir, "velocity.toml"), map[string]any{"bind": "0.0.0.0:25577", "motd": "<#09add3>My Network"})
	if changed, _ := ProxyBind(dir, noryxv1.ServerType_SERVER_TYPE_VELOCITY, 25577); changed {
		t.Fatal("the same bind changed the configuration")
	}

	// A new Velocity proxy gets a configuration; BungeeCord's image downloads one.
	dir = dataDir(t, nil)
	if _, err := ProxyBind(dir, noryxv1.ServerType_SERVER_TYPE_VELOCITY, 25565); err != nil {
		t.Fatal(err)
	}
	want(t, read(t, dir, "velocity.toml"), map[string]any{"config-version": "2.9", "bind": "0.0.0.0:25565"})
	if changed, err := ProxyBind(dir, noryxv1.ServerType_SERVER_TYPE_BUNGEECORD, 25577); changed || err != nil {
		t.Fatalf("changed = %v, err = %v", changed, err)
	}
}
