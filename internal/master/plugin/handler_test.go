package plugin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/QwikByte/noryx/internal/master/access"
	"github.com/QwikByte/noryx/internal/master/httpapi"
	"github.com/QwikByte/noryx/internal/master/operation"
	"github.com/QwikByte/noryx/internal/master/tag"
)

// restarter restarts servers, but those that fail.
type restarter struct {
	mu        sync.Mutex
	restarted []tag.Server
	batch     int
	fail      map[tag.Server]bool
}

func (r *restarter) RestartServers(_ context.Context, servers []tag.Server, batch int) []error {
	r.mu.Lock()
	defer r.mu.Unlock()
	errs := make([]error, len(servers))
	for i, srv := range servers {
		if r.fail[srv] {
			errs[i] = httpapi.Errorf(http.StatusBadGateway, "The server didn't start again.")
			continue
		}
		r.restarted, r.batch = append(r.restarted, srv), batch
	}
	return errs
}

// The API of plugins: actions on many servers check the permissions for each and restart the
// running servers whose plugins changed if asked, a few of a network at a time; the routes of a
// server change its plugin files, and the catalogue tells versions and their changes.
func TestHandler(t *testing.T) {
	p := newPlugins(t)
	r := &restarter{fail: map[tag.Server]bool{}}
	mux := http.NewServeMux()
	NewHandler(p.Service, operation.New(time.Minute), r).Register(access.NewMux(mux))
	admin := access.Admin()
	serve := func(g access.Grants, method, path, body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequestWithContext(access.WithGrants(t.Context(), g), method, path, strings.NewReader(body)))
		return rec
	}
	results := func(rec *httptest.ResponseRecorder) []string {
		t.Helper()
		var res struct{ Results []Result }
		if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil || rec.Code != http.StatusOK {
			t.Fatalf("status %d: %s", rec.Code, rec.Body)
		}
		var out []string
		for _, r := range res.Results {
			desc := r.ServerID + ":"
			for _, i := range r.Installed {
				desc += " " + i.FileName
			}
			for _, name := range r.Removed {
				desc += " -" + name
			}
			if r.Restart {
				desc += ", restart"
			}
			if r.Restarted {
				desc += ", restarted"
			}
			if r.Error != "" {
				desc += ", " + r.Error
			}
			out = append(out, desc)
		}
		return out
	}
	const both = `"servers": [{"nodeId": "n1", "serverId": "lobby"}, {"nodeId": "n1", "serverId": "hub"}]`

	rec := serve(admin, "POST", "/api/plugins/install", `{"projects": ["luckperms"], `+both+`, "restart": true, "batch": 2}`)
	if got := results(rec); !slices.Equal(got, []string{"lobby: luckperms-2.0.jar vault-1.7.jar, restarted", "hub: luckperms-2.0.jar vault-1.7.jar"}) {
		t.Fatalf("install %q", got)
	}
	r.mu.Lock()
	if !slices.Equal(r.restarted, []tag.Server{tag.Server(lobby)}) || r.batch != 2 {
		t.Errorf("restarted %v, %d at a time", r.restarted, r.batch)
	}
	r.fail[tag.Server(lobby)] = true
	r.mu.Unlock()
	rec = serve(admin, "POST", "/api/plugins/install", `{"projects": ["via"], "servers": [{"nodeId": "n1", "serverId": "lobby"}], "restart": true}`)
	if got := results(rec); !slices.Equal(got, []string{"lobby: via-4.0.jar, restart, It couldn't restart: The server didn't start again."}) {
		t.Errorf("install with a failing restart %q", got)
	}

	for _, tc := range []struct {
		name, body string
		grants     access.Grants
		status     int
	}{
		{"no servers", `{"projects": ["vault"], "servers": []}`, admin, http.StatusBadRequest},
		{"a server twice", `{"projects": ["vault"], "servers": [{"nodeId": "n1", "serverId": "hub"}, {"nodeId": "n1", "serverId": "hub"}]}`, admin, http.StatusBadRequest},
		{"large batches", `{"projects": ["vault"], ` + both + `, "restart": true, "batch": 51}`, admin, http.StatusBadRequest},
		{"no projects", `{"projects": [], ` + both + `}`, admin, http.StatusBadRequest},
		{"an invalid project", `{"projects": ["../vault"], ` + both + `}`, admin, http.StatusBadRequest},
		{"an unknown channel", `{"projects": ["via"], "channels": {"via": "nightly"}, ` + both + `}`, admin, http.StatusBadRequest},
		{"a channel of another project", `{"projects": ["via"], "channels": {"vault": "beta"}, ` + both + `}`, admin, http.StatusBadRequest},
		{"a version of another project", `{"projects": ["via"], "versions": {"vault": "vault17"}, ` + both + `}`, admin, http.StatusBadRequest},
		{"without the permission for plugins", `{"projects": ["vault"], ` + both + `}`, admin.Only([]access.Permission{access.ServersView}), http.StatusForbidden},
		{"without the permission to restart", `{"projects": ["vault"], ` + both + `, "restart": true}`, admin.Only([]access.Permission{access.Plugins}), http.StatusForbidden},
	} {
		if rec := serve(tc.grants, "POST", "/api/plugins/install", tc.body); rec.Code != tc.status {
			t.Errorf("%s: status %d: %s", tc.name, rec.Code, rec.Body)
		}
	}

	// Updating and removing a project everywhere.
	p.agent.put("hub", "luckperms-1.0.jar", content("luckperms", "1.0"), false)
	p.agent.remove("hub", "luckperms-2.0.jar", false)
	rec = serve(admin, "POST", "/api/plugins/update", `{"project": "luckperms", `+both+`}`)
	if got := results(rec); !slices.Equal(got, []string{"lobby:", "hub: luckperms-2.0.jar"}) {
		t.Errorf("update %q", got)
	}
	rec = serve(admin, "POST", "/api/plugins/remove", `{"project": "vault", `+both+`}`)
	if got := results(rec); !slices.Equal(got, []string{"lobby: -vault-1.7.jar, restart", "hub: -vault-1.7.jar"}) {
		t.Errorf("remove %q", got)
	}
	for path, body := range map[string]string{"update": `{"project": "../x", ` + both + `}`, "remove": `{"project": "", ` + both + `}`} {
		if rec := serve(admin, "POST", "/api/plugins/"+path, body); rec.Code != http.StatusBadRequest {
			t.Errorf("%s of an invalid project: status %d", path, rec.Code)
		}
	}
	var everywhere Everywhere
	if rec := serve(admin, "GET", "/api/plugins/installed", ""); rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &everywhere) != nil ||
		len(everywhere.Projects) != 2 || everywhere.Projects[0].Project.Title != "Luckperms" || len(everywhere.Projects[0].Servers) != 2 ||
		len(everywhere.Unreachable) != 1 || everywhere.Unreachable[0].NodeID != "n2" {
		t.Errorf("installed: status %d: %s", rec.Code, rec.Body)
	}

	// The plugin files of a server.
	const base = "/api/nodes/n1/servers/lobby/plugins"
	for _, tc := range []struct {
		method, path, body string
		status             int
	}{
		{"PUT", base + "/custom.jar", "my own", http.StatusCreated},
		{"POST", base + "/custom.jar/disable", "", http.StatusNoContent},
		{"POST", base + "/custom.jar/enable", "", http.StatusNoContent},
		{"PUT", base + "/pins/luckperms", "", http.StatusNoContent},
		{"PUT", base + "/pins/x%20y", "", http.StatusBadRequest},
		{"PUT", base + "/channels/via", `{"channel": "beta"}`, http.StatusNoContent},
		{"PUT", base + "/channels/via", `{"channel": "nightly"}`, http.StatusBadRequest},
		{"PUT", "/api/nodes/n1/servers/nope/plugins/pins/luckperms", "", http.StatusNotFound},
		{"POST", base + "/nope.jar/enable", "", http.StatusNotFound},
	} {
		if rec := serve(admin, tc.method, tc.path, tc.body); rec.Code != tc.status {
			t.Errorf("%s %s: status %d: %s", tc.method, tc.path, rec.Code, rec.Body)
		}
	}
	var l Listing
	if rec := serve(admin, "GET", base, ""); rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &l) != nil || len(l.Plugins) != 3 {
		t.Fatalf("plugins of lobby: status %d: %s", rec.Code, rec.Body)
	}
	for _, plugin := range l.Plugins {
		switch {
		case plugin.FileName == "custom.jar" && (plugin.Size != 6 || plugin.Project != nil || plugin.Disabled),
			plugin.FileName == "luckperms-2.0.jar" && !plugin.Pinned,
			plugin.FileName == "via-4.0.jar" && (plugin.Tracks != "beta" || plugin.Update != "5.0-beta"):
			t.Errorf("plugin %+v", plugin)
		}
	}
	if rec := serve(admin, "DELETE", base+"/custom.jar", ""); rec.Code != http.StatusNoContent {
		t.Errorf("delete: status %d: %s", rec.Code, rec.Body)
	}
	if rec := serve(admin, "DELETE", base+"/custom.jar", ""); rec.Code != http.StatusNotFound {
		t.Errorf("delete again: status %d: %s", rec.Code, rec.Body)
	}

	// The catalogue.
	for path, want := range map[string]string{
		"/api/plugins/projects/via/versions?type=paper&version=1.21.4":            `"number":"5.0-beta"`,
		"/api/plugins/projects/via/changes?type=paper&version=1.21.4&from=via40":  `"changelog":"Changes in 5.0-beta"`,
		"/api/plugins/game-versions":                                              `["1.21.4"]`,
		"/api/plugins/projects/via/versions?type=paper&version=1.21%204":          `"error":"invalid Minecraft version"`,
		"/api/plugins/projects/via/changes?type=paper&version=1.21.4&from=..%2Fx": `"error":"invalid version"`,
	} {
		if rec := serve(admin, "GET", path, ""); !strings.Contains(rec.Body.String(), want) {
			t.Errorf("%s: status %d: %s", path, rec.Code, rec.Body)
		}
	}
}
