package e2e

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	masterapp "github.com/QwikByte/noryx/internal/master/app"
	"github.com/QwikByte/noryx/internal/master/preference"
)

// Each user keeps the layout of their overview, the servers they pinned, what pops up for them,
// their settings, what they hid of Needs attention, the groups they folded and the views they
// saved, which every browser of the user gets. A deleted server is unpinned.
func TestPreferences(t *testing.T) {
	m := startMaster(t)
	svc := m.services(t)
	srv := httptest.NewTLSServer(masterapp.Handler(svc))
	t.Cleanup(srv.Close)
	a := m.startAgent(t, "node-1")
	lobby := m.createServer(t, a, "Lobby", noryxv1.ServerType_SERVER_TYPE_PAPER, 25565)
	game := m.createServer(t, a, "Game", noryxv1.ServerType_SERVER_TYPE_PAPER, 25566)
	admin, err := svc.Users.CreateUser(t.Context(), "admin", "the-admins-password")
	check(t, err)
	check(t, svc.Access.MakeAdmin(t.Context(), admin.ID))
	_, err = svc.Users.CreateUser(t.Context(), "other", "the-others-password")
	check(t, err)
	login := func(username, password string) apiClient {
		b := browser(t, srv)
		b.do("POST", "/api/auth/login", map[string]string{"username": username, "password": password}, http.StatusOK, nil)
		return b
	}
	browser(t, srv).do("GET", "/api/preferences", nil, http.StatusUnauthorized, nil)

	first := login("admin", "the-admins-password")
	if body := first.do("GET", "/api/preferences", nil, http.StatusOK, nil); strings.TrimSpace(body) != `{"dashboard":[],"pinned":[],`+
		`"alerts":{"level":"warn","only":false,"pinned":false,"nodes":[],"servers":[],"categories":[]},"settings":{},"hidden":[],"folded":[],"views":[]}` {
		t.Fatalf("preferences of a new user = %s", body)
	}
	want := preference.Preferences{
		Dashboard: []preference.Widget{{ID: "servers", Columns: 2}, {ID: "nodes", Columns: 1, Hidden: true, Options: map[string]string{"nodes": a.node.ID}}},
		Pinned:    []preference.Server{{NodeID: game.NodeID, ServerID: game.ServerID}, {NodeID: lobby.NodeID, ServerID: lobby.ServerID}},
		Alerts: preference.Alerts{Level: "error", Only: true, Nodes: []string{a.node.ID}, Servers: []string{lobby.ServerID},
			Categories: []string{"servers"}},
		Settings: preference.Settings{"theme": "dark", "clock": "12h", "timeZone": "Europe/Berlin"},
		Hidden:   []preference.HiddenItem{{Key: "offline/" + a.node.ID, State: "k2m4", Until: time.Now().Add(time.Hour).UTC().Truncate(time.Second)}},
		Folded:   []string{"network/none", "tag/lobby"},
		Views:    []preference.View{{Name: "Lobbies on node 1", Search: map[string]string{"tag": "lobby", "node": a.node.ID, "sort": "network"}}},
	}
	var got preference.Preferences
	first.do("PUT", "/api/preferences/dashboard", map[string]any{"widgets": want.Dashboard}, http.StatusOK, nil)
	first.do("PATCH", "/api/preferences/settings", map[string]any{"theme": "dark", "serverView": "table"}, http.StatusOK, nil)
	first.do("PATCH", "/api/preferences/settings", map[string]any{"clock": "12h", "serverView": nil, "timeZone": "Europe/Berlin"}, http.StatusOK, nil)
	first.do("PUT", "/api/preferences/alerts", want.Alerts, http.StatusOK, nil)
	first.do("PUT", "/api/preferences/hidden", map[string]any{"items": want.Hidden}, http.StatusOK, nil)
	first.do("PUT", "/api/preferences/folded", map[string]any{"groups": want.Folded}, http.StatusOK, nil)
	first.do("PUT", "/api/preferences/views", map[string]any{"views": want.Views}, http.StatusOK, nil)
	first.do("PUT", "/api/preferences/pinned", map[string]any{"servers": want.Pinned}, http.StatusOK, &got)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("preferences = %+v, want %+v", got, want)
	}
	for path, body := range map[string]any{
		"dashboard": map[string]any{"widgets": []preference.Widget{{ID: "servers", Columns: 4}}},
		"pinned":    map[string]any{"servers": append(want.Pinned, want.Pinned[0])},
		"alerts":    preference.Alerts{Level: "debug"},
		"hidden":    map[string]any{"items": []preference.HiddenItem{{Key: "offline/../" + a.node.ID, Until: want.Hidden[0].Until}}},
		"folded":    map[string]any{"groups": []string{"servers/lobby"}},
		"views":     map[string]any{"views": []preference.View{{Name: "Lobbies", Search: map[string]string{"node": "../" + a.node.ID}}}},
	} {
		first.do("PUT", "/api/preferences/"+path, body, http.StatusBadRequest, nil)
	}
	for _, body := range []any{map[string]any{"theme": "blue"}, map[string]any{"userId": "2"}, map[string]any{"theme": 1}, []string{"theme"},
		map[string]any{"timeZone": "Local"}} {
		first.do("PATCH", "/api/preferences/settings", body, http.StatusBadRequest, nil)
	}
	first.do("PUT", "/api/preferences/pinned", map[string]any{"servers": []map[string]string{{"nodeId": "../" + game.NodeID, "serverId": game.ServerID}}}, http.StatusBadRequest, nil)
	first.do("PUT", "/api/preferences/dashboard", map[string]any{"widgets": want.Dashboard, "userId": 2}, http.StatusBadRequest, nil)
	first.do("PUT", "/api/preferences/dashboard", map[string]any{"widgets": []preference.Widget{{ID: "nodes", Columns: 1, Options: map[string]string{"nodes": "*"}}}}, http.StatusBadRequest, nil)

	// Another browser of the user gets them, another user doesn't.
	// Decoding into new values, as JSON merges objects into a map.
	var inSecond, others preference.Preferences
	second := login("admin", "the-admins-password")
	second.do("GET", "/api/preferences", nil, http.StatusOK, &inSecond)
	if !reflect.DeepEqual(inSecond, want) {
		t.Fatalf("preferences in another browser = %+v, want %+v", inSecond, want)
	}
	login("other", "the-others-password").do("GET", "/api/preferences", nil, http.StatusOK, &others)
	if len(others.Dashboard) != 0 || len(others.Pinned) != 0 || len(others.Settings) != 0 || others.Alerts.Only ||
		len(others.Hidden) != 0 || len(others.Folded) != 0 || len(others.Views) != 0 {
		t.Fatalf("preferences of another user = %+v", others)
	}

	second.do("DELETE", "/api/nodes/"+game.NodeID+"/servers/"+game.ServerID, nil, http.StatusNoContent, nil)
	first.do("GET", "/api/preferences", nil, http.StatusOK, &got)
	if !reflect.DeepEqual(got.Pinned, want.Pinned[1:]) {
		t.Fatalf("pinned after deleting a server = %+v", got.Pinned)
	}
}
