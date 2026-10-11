package e2e

import (
	"net/http"
	"slices"
	"strings"
	"testing"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	"github.com/QwikByte/noryx/internal/master/network"
	"github.com/QwikByte/noryx/internal/master/settings"
	"github.com/QwikByte/noryx/internal/master/usage"
)

func TestUsage(t *testing.T) {
	m := startMaster(t)
	a := m.startAgent(t, "node-1")
	lobby := m.createServer(t, a, "Lobby", noryxv1.ServerType_SERVER_TYPE_PAPER, 25565)
	api := apiClient{t: t, url: m.panel(t).URL}
	api.do("POST", "/api/nodes/"+lobby.NodeID+"/servers/"+lobby.ServerID+"/start", nil, http.StatusNoContent, nil)

	var latest struct {
		Node *struct {
			CPUCount         uint32 `json:"cpuCount"`
			MemoryTotalBytes uint64 `json:"memoryTotalBytes"`
		} `json:"node"`
		Servers []struct {
			ID          string `json:"id"`
			Running     bool   `json:"running"`
			MemoryBytes uint64 `json:"memoryBytes"`
		} `json:"servers"`
	}
	api.do("GET", "/api/nodes/"+lobby.NodeID+"/usage", nil, http.StatusOK, &latest)
	if latest.Node == nil || latest.Node.MemoryTotalBytes == 0 || len(latest.Servers) != 1 ||
		latest.Servers[0].ID != lobby.ServerID || !latest.Servers[0].Running || latest.Servers[0].MemoryBytes != 512<<20 {
		t.Fatalf("latest = %+v", latest)
	}
	api.do("GET", "/api/nodes/"+lobby.NodeID+"/servers/"+lobby.ServerID+"/usage/history?range=week", nil, http.StatusOK, nil)
	api.do("GET", "/api/nodes/"+lobby.NodeID+"/usage/history?range=decade", nil, http.StatusBadRequest, nil)

	// Ranges beyond the week come from the hours, as far as the settings keep them.
	var ranges []string
	api.do("GET", "/api/usage/ranges", nil, http.StatusOK, &ranges)
	if !slices.Equal(ranges, []string{"day", "week", "month"}) {
		t.Fatalf("ranges = %v", ranges)
	}
	api.do("GET", "/api/nodes/"+lobby.NodeID+"/usage/history?range=month", nil, http.StatusOK, nil)
	api.do("GET", "/api/nodes/"+lobby.NodeID+"/usage/history?range=quarter", nil, http.StatusBadRequest, nil)
	var s struct{ Settings settings.Settings }
	api.do("GET", "/api/settings", nil, http.StatusOK, &s)
	s.Settings.Retention.UsageDays = 90
	api.do("PUT", "/api/settings", s.Settings, http.StatusOK, nil)
	api.do("GET", "/api/usage/ranges", nil, http.StatusOK, &ranges)
	if !slices.Equal(ranges, []string{"day", "week", "month", "quarter"}) {
		t.Fatalf("ranges kept for 90 days = %v", ranges)
	}
	api.do("GET", "/api/nodes/"+lobby.NodeID+"/servers/"+lobby.ServerID+"/usage/history?range=quarter", nil, http.StatusOK, nil)
	api.do("GET", "/api/nodes/"+lobby.NodeID+"/usage/history?range=year", nil, http.StatusBadRequest, nil)
}

// Servers and nodes have the thresholds of the settings unless they have their own, which only
// those who may change their settings or the node change.
func TestUsageThresholds(t *testing.T) {
	m := startMaster(t)
	a := m.startAgent(t, "node-1")
	lobby := m.createServer(t, a, "Lobby", noryxv1.ServerType_SERVER_TYPE_PAPER, 25565)
	api := apiClient{t: t, url: m.panel(t).URL}
	server := "/api/nodes/" + lobby.NodeID + "/servers/" + lobby.ServerID + "/usage/thresholds"
	node := "/api/nodes/" + lobby.NodeID + "/usage/thresholds"
	type view struct{ Own, Defaults usage.Thresholds }
	thresholds := func(c apiClient, method, path string, in any) (v view) {
		c.do(method, path, in, http.StatusOK, &v)
		return v
	}

	v := thresholds(api, "GET", server, nil)
	if len(v.Own) != 0 || v.Defaults[usage.CPU].Value != 90 || v.Defaults[usage.TPS].Value != 15 {
		t.Fatalf("thresholds of the lobby = %+v", v)
	}
	thresholds(api, "PUT", server, usage.Thresholds{usage.CPU: {Value: 95, Minutes: 15}})
	if v := thresholds(api, "GET", server, nil); v.Own[usage.CPU].Value != 95 || v.Own[usage.CPU].Minutes != 15 || len(v.Own) != 1 {
		t.Fatalf("own thresholds of the lobby = %+v", v.Own)
	}
	api.do("PUT", server, usage.Thresholds{usage.Storage: {Value: 90}}, http.StatusBadRequest, nil)
	api.do("PUT", "/api/nodes/"+lobby.NodeID+"/servers/"+strings.Repeat("a", 26)+"/usage/thresholds", usage.Thresholds{}, http.StatusNotFound, nil)
	if v := thresholds(api, "PUT", node, usage.Thresholds{usage.Storage: {Value: 80}}); len(v.Own) != 1 || v.Own[usage.Storage].Value != 80 || v.Defaults[usage.Storage].Value != 90 {
		t.Fatalf("thresholds of the node = %+v", v)
	}
	api.do("PUT", "/api/nodes/unknown/usage/thresholds", usage.Thresholds{}, http.StatusNotFound, nil)

	// The defaults are settings of the master.
	var s struct{ Settings settings.Settings }
	api.do("GET", "/api/settings", nil, http.StatusOK, &s)
	s.Settings.Thresholds.Servers[usage.TPS] = usage.Threshold{Value: 25, Minutes: 5}
	api.do("PUT", "/api/settings", s.Settings, http.StatusBadRequest, nil)
	s.Settings.Thresholds.Servers[usage.TPS] = usage.Threshold{Value: 18, Minutes: 5}
	api.do("PUT", "/api/settings", s.Settings, http.StatusOK, nil)
	if v := thresholds(api, "GET", server, nil); v.Defaults[usage.TPS].Value != 18 {
		t.Fatalf("defaults of servers = %+v", v.Defaults)
	}

	viewer := m.withGrants(t, map[string]any{"name": "Viewers", "permissions": []string{"servers.view", "nodes.view"}, "allServers": true})
	viewer.do("GET", server, nil, http.StatusOK, nil)
	viewer.do("GET", node, nil, http.StatusOK, nil)
	viewer.do("PUT", server, usage.Thresholds{}, http.StatusForbidden, nil)
	viewer.do("PUT", node, usage.Thresholds{}, http.StatusForbidden, nil)
	if body := viewer.do("GET", "/api/usage/warnings", nil, http.StatusOK, nil); body != "[]\n" {
		t.Errorf("warnings = %s", body)
	}
	editor := m.withGrants(t, map[string]any{"name": "Lobby", "permissions": []string{"servers.view", "servers.settings"}, "targets": []network.Ref{lobby}})
	if v := thresholds(editor, "PUT", server, usage.Thresholds{}); len(v.Own) != 0 {
		t.Fatalf("own thresholds of the lobby = %+v", v.Own)
	}
	editor.do("GET", node, nil, http.StatusForbidden, nil)
	editor.do("PUT", node, usage.Thresholds{}, http.StatusForbidden, nil)
}
