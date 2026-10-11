package e2e

import (
	"net/http"
	"testing"
	"time"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	"github.com/QwikByte/noryx/internal/master/network"
	"github.com/QwikByte/noryx/internal/master/player"
	"github.com/QwikByte/noryx/internal/master/settings"
	"github.com/QwikByte/noryx/internal/master/usage"
)

// The master notes where players were online from the names the agents measure, and only shows
// the sightings on the servers a user may see.
func TestPlayerSightings(t *testing.T) {
	m := startMaster(t)
	a := m.startAgent(t, "node-1")
	proxy := m.createServer(t, a, "Proxy", noryxv1.ServerType_SERVER_TYPE_VELOCITY, 25577)
	lobby := m.createServer(t, a, "Lobby", noryxv1.ServerType_SERVER_TYPE_PAPER, 25565)
	game := m.createServer(t, a, "Game", noryxv1.ServerType_SERVER_TYPE_PAPER, 25566)
	for _, s := range []network.Ref{proxy, lobby, game} {
		check(t, a.runtime.Start(t.Context(), s.ServerID))
	}
	a.runtime.mu.Lock()
	a.runtime.online = map[string][]string{lobby.ServerID: {"Alex", "Steve"}, game.ServerID: {"Alex"}}
	a.runtime.mu.Unlock()
	api := apiClient{t: t, url: m.panel(t).URL}

	// The usage store passes each measurement on, as it does every minute.
	sightings := player.NewSightings(m.db, m.settings)
	stats, err := usage.NewStore(m.db, m.nodes, m.settings).Latest(t.Context(), a.node.ID)
	check(t, err)
	now := time.Now().Truncate(time.Second)
	for i := range 3 {
		check(t, sightings.Record(t.Context(), a.node.ID, now.Add(time.Duration(i)*time.Minute), stats.GetServers()))
	}

	var seen struct {
		Players   []player.Player
		Total     int
		Recording bool
	}
	api.do("GET", "/api/players/seen", nil, http.StatusOK, &seen)
	if !seen.Recording || seen.Total != 2 || seen.Players[0].Name != "Alex" || seen.Players[0].Minutes != 6 || len(seen.Players[0].Servers) != 2 {
		t.Fatalf("seen = %+v", seen)
	}
	api.do("GET", "/api/players/seen?q=ste", nil, http.StatusOK, &seen)
	if seen.Total != 1 || seen.Players[0].Name != "Steve" {
		t.Fatalf("seen Steve = %+v", seen)
	}
	var history player.History
	api.do("GET", "/api/players/seen/alex", nil, http.StatusOK, &history)
	if history.Name != "Alex" || history.Minutes != 6 || len(history.Days) == 0 || !history.LastSeen.Equal(now.Add(2*time.Minute)) {
		t.Fatalf("history = %+v", history)
	}
	api.do("GET", "/api/players/seen/@a", nil, http.StatusBadRequest, nil)

	// Users only see the sightings on the servers they may see.
	viewer := m.withGrants(t, map[string]any{"name": "Game", "permissions": []string{"servers.view"}, "targets": []network.Ref{game}})
	viewer.do("GET", "/api/players/seen", nil, http.StatusOK, &seen)
	if seen.Total != 1 || seen.Players[0].Name != "Alex" || seen.Players[0].Minutes != 3 {
		t.Fatalf("seen by the viewer = %+v", seen)
	}
	viewer.do("GET", "/api/players/seen/Steve", nil, http.StatusOK, &history)
	if len(history.Servers) != 0 || history.Minutes != 0 {
		t.Fatalf("history of Steve for the viewer = %+v", history)
	}

	// The sightings of a deleted server go with it.
	check(t, a.runtime.Stop(t.Context(), lobby.ServerID))
	api.do("DELETE", "/api/nodes/"+lobby.NodeID+"/servers/"+lobby.ServerID, nil, http.StatusNoContent, nil)
	api.do("GET", "/api/players/seen", nil, http.StatusOK, &seen)
	if seen.Total != 1 || seen.Players[0].Name != "Alex" || seen.Players[0].Minutes != 3 {
		t.Fatalf("seen after deleting the lobby = %+v", seen)
	}

	// While the settings turn recording off, nobody new is noted. Deleting all sightings needs
	// the permission to change the settings.
	var s struct{ Settings settings.Settings }
	api.do("GET", "/api/settings", nil, http.StatusOK, &s)
	s.Settings.Retention.SeenPlayers = false
	api.do("PUT", "/api/settings", s.Settings, http.StatusOK, nil)
	check(t, sightings.Record(t.Context(), a.node.ID, now.Add(3*time.Minute), stats.GetServers()))
	api.do("GET", "/api/players/seen", nil, http.StatusOK, &seen)
	if seen.Recording || seen.Total != 1 || seen.Players[0].Minutes != 3 {
		t.Fatalf("seen while not recording = %+v", seen)
	}
	viewer.do("DELETE", "/api/players/seen", nil, http.StatusForbidden, nil)
	api.do("DELETE", "/api/players/seen", nil, http.StatusNoContent, nil)
	api.do("GET", "/api/players/seen", nil, http.StatusOK, &seen)
	if seen.Total != 0 || len(seen.Players) != 0 {
		t.Fatalf("seen after deleting all = %+v", seen)
	}
}
