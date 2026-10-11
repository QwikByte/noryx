package e2e

import (
	"maps"
	"net/http"
	"testing"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	"github.com/QwikByte/noryx/internal/master/template"
)

// The agent applies the advanced settings of a server it checked, copies keep them, and templates
// bring them, checked like the agent checks them.
func TestAdvancedSettings(t *testing.T) {
	m := startMaster(t)
	a := m.startAgent(t, "node-1")
	api := apiClient{t: t, url: m.panel(t).URL}
	lobby := m.createServer(t, a, "Lobby", noryxv1.ServerType_SERVER_TYPE_PAPER, 25565)
	path := "/api/nodes/" + lobby.NodeID + "/servers/" + lobby.ServerID
	type listed struct {
		ID          string
		HealthStart uint32
	}
	get := func() listed {
		var servers []listed
		api.do("GET", "/api/nodes/"+lobby.NodeID+"/servers", nil, http.StatusOK, &servers)
		return servers[0]
	}
	if s := get(); s.HealthStart != 2 {
		t.Fatalf("new server = %+v", s)
	}

	settings := map[string]any{"name": "Lobby", "memoryMb": 1024, "port": 25565, "healthStart": 15}
	api.do("PUT", path, settings, http.StatusOK, nil)
	if s, spec := get(), a.runtime.spec(lobby.ServerID); s.HealthStart != 15 || spec.HealthStart != 15 {
		t.Fatalf("server = %+v, spec = %+v", s, spec)
	}
	for key, value := range map[string]any{"healthStart": 31} {
		bad := maps.Clone(settings)
		bad[key] = value
		api.do("PUT", path, bad, http.StatusBadRequest, nil)
	}

	var copied listed
	api.do("POST", path+"/duplicate", map[string]any{"name": "Lobby 2", "port": 25566}, http.StatusCreated, &copied)
	if copied.HealthStart != 15 || a.runtime.spec(copied.ID).HealthStart != 15 {
		t.Fatalf("copy = %+v", copied)
	}

	// Templates keep them for new servers; proxies keep the start period of their image.
	in := map[string]any{"name": "Modded", "type": "neoforge", "memoryMb": 8192, "healthStart": 20}
	var tmpl template.Template
	api.do("POST", "/api/templates", in, http.StatusCreated, &tmpl)
	if tmpl.HealthStart != 20 {
		t.Fatalf("template = %+v", tmpl.Settings)
	}
	api.do("POST", "/api/templates", map[string]any{"name": "Proxy", "type": "velocity", "memoryMb": 512, "healthStart": 20}, http.StatusCreated, &tmpl)
	if tmpl.HealthStart != 2 {
		t.Fatalf("proxy template = %+v", tmpl.Settings)
	}
	for key, value := range map[string]any{"healthStart": 1} {
		bad := maps.Clone(in)
		bad["name"], bad[key] = "Bad "+key, value
		api.do("POST", "/api/templates", bad, http.StatusBadRequest, nil)
	}
}
