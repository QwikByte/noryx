package e2e

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	"github.com/QwikByte/noryx/internal/master/access"
	masterapp "github.com/QwikByte/noryx/internal/master/app"
	"github.com/QwikByte/noryx/internal/master/auth"
	"github.com/QwikByte/noryx/internal/master/network"
	"github.com/QwikByte/noryx/internal/master/player"
)

func TestUsersGroupsAndPermissions(t *testing.T) {
	m := startMaster(t)
	a := m.startAgent(t, "node-1")
	lobby := m.createServer(t, a, "Lobby", noryxv1.ServerType_SERVER_TYPE_PAPER, 25565)
	survival := m.createServer(t, a, "Survival", noryxv1.ServerType_SERVER_TYPE_PAPER, 25566)
	svc := m.services(t)
	srv := httptest.NewTLSServer(masterapp.Handler(svc)) // HTTPS for the secure session cookie
	t.Cleanup(srv.Close)
	path := func(s network.Ref) string { return "/api/nodes/" + s.NodeID + "/servers/" + s.ServerID }

	admin, err := svc.Users.CreateUser(t.Context(), "admin", "the-admins-password")
	check(t, err)
	check(t, svc.Access.MakeAdmin(t.Context(), admin.ID))
	adminPath := "/api/users/" + strconv.FormatInt(admin.ID, 10)
	root := browser(t, srv)
	root.do("GET", "/api/servers", nil, http.StatusUnauthorized, nil)
	root.do("POST", "/api/auth/login", map[string]string{"username": "admin", "password": "the-admins-password"}, http.StatusOK, nil)

	// A group for the moderators of the lobby gets the permissions its permissions need.
	var mods access.Group
	root.do("POST", "/api/groups", map[string]any{
		"name": "Lobby moderators", "permissions": []string{"servers.restart", "console.commands"},
		"targets": []network.Ref{lobby},
	}, http.StatusCreated, &mods)
	if want := []access.Permission{"console.commands", "console.view", "servers.restart", "servers.view"}; !slices.Equal(mods.Permissions, want) {
		t.Fatalf("permissions = %v, want %v", mods.Permissions, want)
	}

	// An invited moderator sets a password with the setup link.
	var invited struct {
		User      auth.User      `json:"user"`
		SetupLink auth.SetupLink `json:"setupLink"`
	}
	root.do("POST", "/api/users", map[string]any{"username": "mod", "groups": []string{mods.ID}}, http.StatusCreated, &invited)
	modPath := "/api/users/" + strconv.FormatInt(invited.User.ID, 10)
	mod := browser(t, srv)
	var who auth.User
	mod.do("POST", "/api/auth/setup/check", map[string]string{"token": invited.SetupLink.Token}, http.StatusOK, &who)
	if who.Username != "mod" {
		t.Fatalf("setup link of %q", who.Username)
	}
	mod.do("POST", "/api/auth/setup", map[string]string{"token": invited.SetupLink.Token, "password": "the-mods-password"}, http.StatusOK, nil)
	mod.do("PUT", "/api/auth/password", map[string]string{"current": "the-mods-password", "new": "the-mods-new-password"}, http.StatusNoContent, nil)

	// The moderator only sees and acts on the lobby.
	var servers, nodes []struct{ ID string }
	mod.do("GET", "/api/servers", nil, http.StatusOK, &servers)
	mod.do("GET", "/api/nodes", nil, http.StatusOK, &nodes)
	if len(servers) != 1 || servers[0].ID != lobby.ServerID || len(nodes) != 1 {
		t.Fatalf("servers = %v, nodes = %v", servers, nodes)
	}
	mod.do("POST", path(lobby)+"/restart", nil, http.StatusNoContent, nil)
	mod.do("POST", path(lobby)+"/command", map[string]string{"command": "say hi"}, http.StatusOK, nil)
	// Who restarts servers learns how their players are warned, without seeing the settings.
	mod.do("GET", "/api/servers/warnings", nil, http.StatusOK, nil)
	for _, denied := range []struct{ method, path string }{
		{"POST", path(survival) + "/restart"},
		{"POST", path(lobby) + "/stop"},
		{"POST", path(lobby) + "/update-image"},
		{"DELETE", path(lobby)},
		{"GET", path(lobby) + "/files"},
		{"GET", "/api/networks"},
		{"GET", "/api/settings"},
		{"GET", "/api/users"},
		{"PUT", "/api/nodes/" + a.node.ID},
		{"POST", "/api/nodes/" + a.node.ID + "/servers"},
		{"GET", "/api/servers/defaults"},
	} {
		mod.do(denied.method, denied.path, map[string]any{}, http.StatusForbidden, nil)
	}
	mod.do("POST", "/api/terminal", map[string]string{"target": a.node.ID, "command": "status"}, http.StatusForbidden, nil)
	mod.do("GET", "/api/terminal/commands?target="+a.node.ID, nil, http.StatusForbidden, nil)
	// Bulk requests need the permission on every server they name.
	mod.do("POST", "/api/servers/actions", map[string]any{"action": "restart", "servers": []network.Ref{lobby}}, http.StatusOK, nil)
	mod.do("POST", "/api/servers/actions", map[string]any{"action": "restart", "servers": []network.Ref{lobby, survival}}, http.StatusForbidden, nil)
	mod.do("POST", "/api/servers/actions", map[string]any{"action": "stop", "servers": []network.Ref{lobby}}, http.StatusForbidden, nil)
	mod.do("POST", "/api/servers/tags", map[string]any{"servers": []network.Ref{lobby}, "add": []string{"lobby"}}, http.StatusForbidden, nil)
	// Players have their own permission, and the lists only show the servers one may see.
	mod.do("POST", "/api/players/actions", map[string]any{"action": "ban", "name": "Alex", "servers": []network.Ref{lobby}}, http.StatusForbidden, nil)
	var lists player.Lists
	mod.do("GET", "/api/players/lists", nil, http.StatusOK, &lists)
	if len(lists.Servers) != 1 || lists.Servers[0].Ref != lobby {
		t.Fatalf("lists of the moderator = %+v", lists.Servers)
	}
	mod.do("GET", "/api/players/lists?node="+lobby.NodeID+"&server="+lobby.ServerID, nil, http.StatusOK, nil)
	mod.do("GET", "/api/players/lists?node="+survival.NodeID+"&server="+survival.ServerID, nil, http.StatusForbidden, nil)

	// Nor the details of the node, which only those who may see the node get.
	type nodeView struct {
		Address              string
		CertificateExpiresAt string
		Info                 struct {
			AgentVersion, OS string
			Storage          []struct{ Name, Path string }
		}
	}
	var seen nodeView
	mod.do("GET", "/api/nodes/"+a.node.ID, nil, http.StatusOK, &seen)
	if seen.Address != "" || seen.CertificateExpiresAt != "" || seen.Info.AgentVersion != "" || len(seen.Info.Storage) != 1 || seen.Info.Storage[0].Path != "" {
		t.Fatalf("node as the moderator sees it = %+v", seen)
	}
	seen = nodeView{}
	root.do("GET", "/api/nodes/"+a.node.ID, nil, http.StatusOK, &seen)
	if seen.Address == "" || seen.CertificateExpiresAt == "" || seen.Info.AgentVersion == "" || len(seen.Info.Storage) != 1 || seen.Info.Storage[0].Path == "" {
		t.Fatalf("node as the administrator sees it = %+v", seen)
	}

	// Managers hand out at most what they have, and can't manage stronger users.
	var managers access.Group
	root.do("POST", "/api/groups", map[string]any{
		"name": "Managers", "permissions": []string{"users.manage", "groups.manage", "terminal.use"}, "allServers": true,
	}, http.StatusCreated, &managers)
	root.do("PUT", modPath, map[string]any{"groups": []string{mods.ID, managers.ID}}, http.StatusNoContent, nil)
	mod.do("POST", "/api/groups", map[string]any{"name": "Wider", "permissions": []string{"servers.restart"}, "allServers": true}, http.StatusForbidden, nil)
	mod.do("POST", "/api/groups", map[string]any{"name": "Networks", "permissions": []string{"networks.view"}}, http.StatusForbidden, nil)
	mod.do("POST", "/api/groups", map[string]any{"name": "Restarts", "permissions": []string{"servers.restart"}, "targets": []network.Ref{lobby}}, http.StatusCreated, nil)
	mod.do("PUT", modPath, map[string]any{"groups": []string{mods.ID, managers.ID, access.AdminGroup}}, http.StatusForbidden, nil)
	mod.do("POST", adminPath+"/setup-link", nil, http.StatusForbidden, nil)
	mod.do("PUT", "/api/groups/"+access.AdminGroup, map[string]any{"name": "Admins"}, http.StatusBadRequest, nil)

	// In the terminal, every command needs its own permission.
	for command, want := range map[string]string{
		"server restart " + lobby.ServerID:    "",
		"server restart " + survival.ServerID: `"Restart servers"`,
		"server list":                         `"See servers"`, // lists all servers of the node
		"storage add ssd /mnt":                `unknown command`,
	} {
		if _, err := runTerminal(t, mod.client, srv.URL, a.node.ID, command); !strings.Contains(err, want) || want == "" && err != "" {
			t.Errorf("%s: error %q, want %q", command, err, want)
		}
	}
	if _, err := runTerminal(t, mod.client, srv.URL, "master", "status"); !strings.Contains(err, "See the master's settings") {
		t.Errorf("master status: error %q", err)
	}
	// It only offers the commands the user may run, here on the lobby.
	for target, want := range map[string]string{
		a.node.ID: "help, server, server command, server logs, server restart",
		"master":  "help, node, node list",
	} {
		var commands []terminalCommand
		mod.do("GET", "/api/terminal/commands?target="+target, nil, http.StatusOK, &commands)
		if got := strings.Join(commandPaths(commands, ""), ", "); got != want {
			t.Errorf("commands of %s: %s, want %s", target, got, want)
		}
	}

	// There is always an enabled administrator.
	root.do("PUT", adminPath, map[string]any{"disabled": true, "groups": []string{access.AdminGroup}}, http.StatusBadRequest, nil)
	root.do("PUT", adminPath, map[string]any{"groups": []string{}}, http.StatusConflict, nil)
	root.do("DELETE", adminPath, nil, http.StatusBadRequest, nil)
	root.do("DELETE", "/api/groups/"+access.AdminGroup, nil, http.StatusBadRequest, nil)

	// Deleted servers leave the scopes, and disabled users are signed out.
	root.do("DELETE", path(lobby), nil, http.StatusNoContent, nil)
	root.do("GET", "/api/groups/"+mods.ID, nil, http.StatusOK, &mods)
	if len(mods.Targets) != 0 {
		t.Fatalf("targets after deleting the server = %v", mods.Targets)
	}
	root.do("PUT", modPath, map[string]any{"disabled": true, "groups": []string{mods.ID}}, http.StatusNoContent, nil)
	mod.do("GET", "/api/access/me", nil, http.StatusUnauthorized, nil)
}

// Inviting preselects the groups of the settings that exist and that the inviter may give,
// and the invitation still checks the groups it gets.
func TestInviteGroups(t *testing.T) {
	m := startMaster(t)
	svc := m.services(t)
	srv := httptest.NewTLSServer(masterapp.Handler(svc))
	t.Cleanup(srv.Close)
	admin, err := svc.Users.CreateUser(t.Context(), "admin", "the-admins-password")
	check(t, err)
	check(t, svc.Access.MakeAdmin(t.Context(), admin.ID))
	root := browser(t, srv)
	root.do("POST", "/api/auth/login", map[string]string{"username": "admin", "password": "the-admins-password"}, http.StatusOK, nil)
	var players, managers, gone access.Group
	root.do("POST", "/api/groups", map[string]any{"name": "Players", "permissions": []string{"servers.view"}, "allServers": true}, http.StatusCreated, &players)
	root.do("POST", "/api/groups", map[string]any{"name": "Managers", "permissions": []string{"users.manage", "servers.view"}, "allServers": true}, http.StatusCreated, &managers)
	root.do("POST", "/api/groups", map[string]any{"name": "Gone"}, http.StatusCreated, &gone)
	root.do("PUT", "/api/settings", map[string]any{"inviteGroups": []string{players.ID, access.AdminGroup, gone.ID}}, http.StatusOK, nil)
	root.do("DELETE", "/api/groups/"+gone.ID, nil, http.StatusNoContent, nil)
	var preselected struct{ Groups []string }
	root.do("GET", "/api/users/invite", nil, http.StatusOK, &preselected)
	if want := slices.Sorted(slices.Values([]string{players.ID, access.AdminGroup})); !slices.Equal(preselected.Groups, want) {
		t.Fatalf("preselected for the administrator = %v, want %v", preselected.Groups, want)
	}

	var invited struct {
		SetupLink auth.SetupLink `json:"setupLink"`
	}
	root.do("POST", "/api/users", map[string]any{"username": "manager", "groups": []string{managers.ID}}, http.StatusCreated, &invited)
	manager := browser(t, srv)
	manager.do("POST", "/api/auth/setup", map[string]string{"token": invited.SetupLink.Token, "password": "the-managers-password"}, http.StatusOK, nil)
	manager.do("GET", "/api/users/invite", nil, http.StatusOK, &preselected)
	if !slices.Equal(preselected.Groups, []string{players.ID}) {
		t.Fatalf("preselected for the manager = %v", preselected.Groups)
	}
	manager.do("POST", "/api/users", map[string]any{"username": "other", "groups": []string{players.ID, access.AdminGroup}}, http.StatusForbidden, nil)
	root.do("POST", "/api/users", map[string]any{"username": "player", "groups": []string{players.ID}}, http.StatusCreated, &invited)
	player := browser(t, srv)
	player.do("POST", "/api/auth/setup", map[string]string{"token": invited.SetupLink.Token, "password": "the-players-password"}, http.StatusOK, nil)
	player.do("GET", "/api/users/invite", nil, http.StatusForbidden, nil)
}
