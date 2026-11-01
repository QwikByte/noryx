package docker

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	"github.com/QwikByte/noryx/internal/agent/datadir"
	mcnet "github.com/QwikByte/noryx/internal/agent/network"
	"github.com/QwikByte/noryx/internal/agent/runtime"
)

func TestStandalone(t *testing.T) {
	// A copied backend no longer trusts the proxy of the original, authenticates its
	// players again, and demands signed chat messages again if Bedrock players joined the
	// original.
	backend := t.TempDir()
	dir, err := datadir.Open(backend)
	if err != nil {
		t.Fatal(err)
	}
	defer dir.Close()
	if _, err := mcnet.WriteBackend(dir, noryxv1.ServerType_SERVER_TYPE_PAPER, runtime.ForwardingModern, "s3cretS3cretS3cret"); err != nil {
		t.Fatal(err)
	}
	if err := dir.WriteFile("server.properties", []byte("online-mode=false\nenforce-secure-profile=false\n")); err != nil {
		t.Fatal(err)
	}
	if err := standalone(backend, runtime.Spec{Type: noryxv1.ServerType_SERVER_TYPE_PAPER, BehindProxy: true, BedrockPlayers: true}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(backend, "config", "paper-global.yml"))
	if err != nil || strings.Contains(string(data), "s3cret") || !strings.Contains(string(data), "enabled: false") {
		t.Fatalf("paper-global.yml = %s, %v", data, err)
	}
	if data, err := os.ReadFile(filepath.Join(backend, "server.properties")); err != nil ||
		!strings.Contains(string(data), "online-mode=true") || !strings.Contains(string(data), "enforce-secure-profile=true") {
		t.Fatalf("server.properties = %s, %v", data, err)
	}

	// A copied proxy loses the forwarding secret, Floodgate's key and Geyser's sign-ins;
	// Velocity and Floodgate create new ones.
	proxy := t.TempDir()
	secrets := []string{mcnet.ForwardingSecretFile, mcnet.FloodgateKeyFile, "plugins/Geyser-Velocity/saved-auth-chains.json"}
	for _, secret := range secrets {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(proxy, secret)), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(proxy, secret), []byte("s3cret"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	spec := runtime.Spec{Type: noryxv1.ServerType_SERVER_TYPE_VELOCITY}
	if err := standalone(proxy, spec); err != nil {
		t.Fatal(err)
	}
	for _, secret := range secrets {
		if _, err := os.Stat(filepath.Join(proxy, secret)); !os.IsNotExist(err) {
			t.Fatalf("%s was copied: %v", secret, err)
		}
	}
	if err := standalone(proxy, spec); err != nil {
		t.Fatalf("proxy without a secret: %v", err)
	}

	// A copied BungeeCord proxy stops forwarding, as only the original may reach the backends.
	bungee := t.TempDir()
	if err := os.WriteFile(filepath.Join(bungee, "config.yml"), []byte("ip_forward: true\nservers:\n  lobby:\n    address: 203.0.113.7:25565\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := standalone(bungee, runtime.Spec{Type: noryxv1.ServerType_SERVER_TYPE_BUNGEECORD}); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(filepath.Join(bungee, "config.yml")); err != nil || !strings.Contains(string(data), "ip_forward: false") {
		t.Fatalf("config.yml = %s, %v", data, err)
	}
}
