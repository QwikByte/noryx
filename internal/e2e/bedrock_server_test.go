package e2e

import (
	"net/http"
	"slices"
	"testing"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	"github.com/QwikByte/noryx/internal/master/network"
	"github.com/QwikByte/noryx/internal/master/player"
)

// A Bedrock server is created with the players it lets join, whose ports no other server takes,
// and takes console commands; it refuses the settings of Java, networks and bans.
func TestBedrockServer(t *testing.T) {
	m := startMaster(t)
	a := m.startAgent(t, "node-1")
	api := apiClient{t: t, url: m.panel(t).URL}
	servers := "/api/nodes/" + a.node.ID + "/servers"

	var node struct {
		Info struct{ ServerTypes []string }
	}
	api.do("GET", "/api/nodes/"+a.node.ID, nil, http.StatusOK, &node)
	if !slices.Contains(node.Info.ServerTypes, "bedrock") || !slices.Contains(node.Info.ServerTypes, "paper") {
		t.Fatalf("server types = %q", node.Info.ServerTypes)
	}

	var created struct {
		ID         string
		Type       string
		Port       uint32
		MaxPlayers uint32
	}
	api.do("POST", servers, map[string]any{
		"name": "Bedrock", "type": "bedrock", "memoryMb": 1024, "port": 19132, "maxPlayers": 5, "acceptEula": true,
	}, http.StatusCreated, &created)
	if spec := a.runtime.spec(created.ID); created.Type != "bedrock" || created.MaxPlayers != 5 || spec.Type != noryxv1.ServerType_SERVER_TYPE_BEDROCK ||
		spec.MaxPlayers != 5 || spec.Version != "LATEST" {
		t.Fatalf("created %+v, spec %+v", created, spec)
	}
	bedrock := network.Ref{NodeID: a.node.ID, ServerID: created.ID}

	// The ports of its players are taken, and Java's settings refused.
	api.do("POST", servers, map[string]any{"name": "Lobby", "type": "paper", "memoryMb": 1024, "port": 19136, "acceptEula": true}, http.StatusConflict, nil)
	for _, invalid := range []map[string]any{
		{"name": "Java", "type": "bedrock", "memoryMb": 1024, "port": 19200, "acceptEula": true, "java": "21"},
		{"name": "Java", "type": "bedrock", "memoryMb": 1024, "port": 19200, "acceptEula": true, "version": "1.21.4"},
		{"name": "World", "type": "bedrock", "memoryMb": 1024, "port": 19200, "acceptEula": true, "properties": map[string]string{"gamemode": "creative"}},
		{"name": "Paper", "type": "paper", "memoryMb": 1024, "port": 25565, "acceptEula": true, "maxPlayers": 20},
	} {
		api.do("POST", servers, invalid, http.StatusBadRequest, nil)
	}

	// Its console takes commands.
	check(t, a.runtime.Start(t.Context(), created.ID))
	api.do("POST", servers+"/"+created.ID+"/command", map[string]any{"command": "list"}, http.StatusOK, nil)
	if got := a.runtime.commandsTo(created.ID); !slices.Equal(got, []string{"list"}) {
		t.Fatalf("console commands = %q", got)
	}

	// It can't join a network.
	proxy := m.createServer(t, a, "Proxy", noryxv1.ServerType_SERVER_TYPE_VELOCITY, 25577)
	api.do("POST", "/api/networks", map[string]any{"name": "Main", "proxy": proxy, "servers": []network.Ref{bedrock}}, http.StatusBadRequest, nil)

	// It kicks with its own command, and has no bans.
	var changed struct{ Results []player.Result }
	api.do("POST", "/api/players/actions", map[string]any{"action": "kick", "name": "Steve", "servers": []network.Ref{bedrock}}, http.StatusOK, &changed)
	if r := changed.Results; len(r) != 1 || r[0].Error != "" {
		t.Fatalf("kick = %+v", r)
	}
	api.do("POST", "/api/players/actions", map[string]any{"action": "ban", "name": "Steve", "servers": []network.Ref{bedrock}}, http.StatusOK, &changed)
	if r := changed.Results; len(r) != 1 || r[0].Error != "Bedrock servers have no bans. Take the player off the allowlist instead." {
		t.Fatalf("ban = %+v", r)
	}
	if got := a.runtime.commandsTo(created.ID); !slices.Equal(got, []string{"list", "kick Steve"}) {
		t.Fatalf("console commands = %q", got)
	}
}
