package e2e

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	agentoverlay "github.com/QwikByte/noryx/internal/agent/overlay"
	"github.com/QwikByte/noryx/internal/master/datastore"
	"github.com/QwikByte/noryx/internal/master/network"
	"github.com/QwikByte/noryx/internal/master/schedule"
)

// A network gets a datastore on one node, which its servers there reach by name and those of
// another node over the private network, with databases whose passwords and tables only those
// who manage datastores see.
func TestDatastores(t *testing.T) {
	m := startMaster(t)
	a1, a2, a3 := m.startAgent(t, "node-1"), m.startAgent(t, "node-2"), m.startAgent(t, "node-3")
	proxy := m.createServer(t, a1, "Proxy", noryxv1.ServerType_SERVER_TYPE_VELOCITY, 25577)
	lobby := m.createServer(t, a1, "Lobby", noryxv1.ServerType_SERVER_TYPE_PAPER, 25565)
	game := m.createServer(t, a2, "Game", noryxv1.ServerType_SERVER_TYPE_PAPER, 25566)
	api := apiClient{t: t, url: m.panel(t).URL}
	for i, a := range []agent{a1, a2} {
		check(t, agentoverlay.Allow(a.dir))
		api.do("POST", "/api/nodes/"+a.node.ID+"/overlay", map[string]string{"endpoint": fmt.Sprintf("203.0.113.%d:51820", i+1)}, http.StatusOK, nil)
	}
	var n network.Network
	api.do("POST", "/api/networks", map[string]any{"name": "Main", "proxy": proxy, "servers": []network.Ref{lobby, game}}, http.StatusCreated, &n)
	base := "/api/networks/" + n.ID + "/datastores"

	for _, bad := range []map[string]any{
		{"name": "Main DB", "nodeId": a1.node.ID, "engine": "mariadb", "memoryMb": 512},
		{"name": "main", "nodeId": a1.node.ID, "engine": "mysql", "memoryMb": 512},
		{"name": "main", "nodeId": a1.node.ID, "engine": "mariadb", "memoryMb": 64},
		{"name": "main", "nodeId": a1.node.ID, "engine": "mariadb", "version": "10.6", "memoryMb": 512},
		{"name": "main", "nodeId": a1.node.ID, "engine": "postgres", "memoryMb": 512, "maxConnections": 257},
		{"name": "main", "nodeId": a1.node.ID, "engine": "mariadb", "memoryMb": 512, "bufferMb": 385},
	} {
		api.do("POST", base, bad, http.StatusBadRequest, nil)
	}
	var ds datastore.View
	api.do("POST", base, map[string]any{"name": "main", "nodeId": a1.node.ID, "engine": "mariadb", "version": "11.8", "memoryMb": 512}, http.StatusCreated, &ds)
	if ds.State != "running" || ds.Version != "11.8" || !slices.Equal(ds.Versions, []string{"11.8", "12.3"}) || ds.NodeName != "node-1" {
		t.Fatalf("datastore = %+v", ds)
	}
	api.do("POST", base, map[string]any{"name": "main", "nodeId": a2.node.ID, "engine": "postgres", "memoryMb": 512}, http.StatusConflict, nil)

	// The servers of node-1 join it, the proxy too; node-2 reaches its port in the private network.
	for _, ref := range []network.Ref{proxy, lobby} {
		if got := a1.runtime.network(ref.ServerID).Datastores; !slices.Equal(got, []string{ds.ID}) {
			t.Fatalf("%s joined %v", ref.ServerID, got)
		}
	}
	if got := a2.runtime.network(game.ServerID).Datastores; len(got) > 0 {
		t.Fatalf("game joined %v on another node", got)
	}
	published := a1.kernel.applied().Clients["db-"+ds.ID]
	list, _ := a1.runtime.ListDatastores(t.Context())
	if published.Address.String() != "10.213.0.2" || published.Port == 0 || list[0].Port != uint32(published.Port) || list[0].Overlay != "10.213.0.1" {
		t.Fatalf("published %+v, datastore %+v", published, list[0])
	}

	dbs := "/api/datastores/" + ds.ID + "/databases"
	api.do("POST", dbs, map[string]string{"name": "luckperms"}, http.StatusCreated, nil)
	api.do("POST", dbs, map[string]string{"name": "luckperms"}, http.StatusCreated, nil) // again is fine
	api.do("POST", dbs, map[string]string{"name": "mysql"}, http.StatusBadRequest, nil)
	api.do("DELETE", dbs+"/mysql", nil, http.StatusNotFound, nil)
	_, password := a1.runtime.Content(ds.ID, "luckperms")
	if !noryxv1.DatabasePassword.MatchString(password) {
		t.Fatalf("password %q", password)
	}

	// What it uses, now for those who may see datastores, and its history.
	var latest struct {
		Datastores []struct {
			ID          string  `json:"id"`
			Running     bool    `json:"running"`
			MemoryBytes uint64  `json:"memoryBytes"`
			Connections *uint32 `json:"connections"`
		} `json:"datastores"`
	}
	api.do("GET", "/api/nodes/"+a1.node.ID+"/usage", nil, http.StatusOK, &latest)
	if u := latest.Datastores; len(u) != 1 || u[0].ID != ds.ID || !u[0].Running || u[0].MemoryBytes != 256<<20 || u[0].Connections == nil || *u[0].Connections != 1 {
		t.Fatalf("latest usage %+v", latest)
	}
	history := "/api/datastores/" + ds.ID + "/usage/history"
	api.do("GET", history+"?range=week", nil, http.StatusOK, nil)
	api.do("GET", history+"?range=year", nil, http.StatusBadRequest, nil)
	api.do("GET", "/api/datastores/unknown/usage/history", nil, http.StatusNotFound, nil)
	serverViewer := m.withGrants(t, map[string]any{"name": "Server viewers", "permissions": []string{"servers.view", "nodes.view"}, "allServers": true})
	serverViewer.do("GET", history, nil, http.StatusForbidden, nil)
	if body := serverViewer.do("GET", "/api/nodes/"+a1.node.ID+"/usage", nil, http.StatusOK, nil); strings.Contains(body, ds.ID) {
		t.Errorf("a server viewer sees the datastore's usage: %s", body)
	}

	// Those who manage datastores see the password, which lists never show, to enter it into
	// the configuration of plugins, at the addresses where the servers reach the datastore.
	var shown struct {
		Password string `json:"password"`
	}
	api.do("GET", dbs+"/luckperms/password", nil, http.StatusOK, &shown)
	body := api.do("GET", base, nil, http.StatusOK, nil)
	var listed []datastore.View
	api.do("GET", base, nil, http.StatusOK, &listed)
	endpoints := []datastore.Endpoint{{Host: "noryx-db-" + ds.ID, Port: 3306}, {Host: "10.213.0.1", Port: uint32(published.Port), Remote: true}}
	if shown.Password != password || strings.Contains(body, password) || len(listed) != 1 || !slices.Equal(listed[0].Endpoints, endpoints) {
		t.Fatalf("password %q, listed %s", shown.Password, body)
	}
	viewer := m.withGrants(t, map[string]any{"name": "Database viewers", "permissions": []string{"datastores.view"}})
	viewer.do("GET", base, nil, http.StatusOK, nil)
	viewer.do("GET", dbs+"/luckperms/password", nil, http.StatusForbidden, nil)
	viewer.do("GET", dbs+"/luckperms/tables", nil, http.StatusForbidden, nil)

	// A new password, which the plugins need then.
	api.do("POST", dbs+"/luckperms/rotate", nil, http.StatusNoContent, nil)
	_, rotated := a1.runtime.Content(ds.ID, "luckperms")
	api.do("GET", dbs+"/luckperms/password", nil, http.StatusOK, &shown)
	if rotated == password || shown.Password != rotated {
		t.Fatalf("rotated %q, shown %q", rotated, shown.Password)
	}

	// The tables of a database and their rows.
	check(t, a1.runtime.Write(ds.ID, "luckperms", "groups v1"))
	var tables []datastore.Table
	api.do("GET", dbs+"/luckperms/tables", nil, http.StatusOK, &tables)
	var page datastore.Page
	api.do("GET", dbs+"/luckperms/tables/content?offset=0", nil, http.StatusOK, &page)
	if len(tables) != 1 || tables[0].Name != "content" || len(page.Columns) != 1 || len(page.Rows) != 1 || page.Rows[0][0].Text != "groups v1" || page.More {
		t.Fatalf("tables %+v, page %+v", tables, page)
	}
	// Sorted by a column, and filtered by its value or by text it contains.
	for query, rows := range map[string]int{
		"?filter=content&value=groups%20v1":          1,
		"?filter=content&value=GROUPS&contains=true": 1,
		"?filter=content&value=groups":               0,
		"?sort=content&descending=true":              1,
	} {
		api.do("GET", dbs+"/luckperms/tables/content"+query, nil, http.StatusOK, &page)
		if len(page.Rows) != rows {
			t.Errorf("%s: %+v", query, page)
		}
	}
	for _, query := range []string{"?sort=missing", "?filter=missing&value=x", "?filter=content&value=%00", "?filter=content&value=" + strings.Repeat("x", 1025)} {
		api.do("GET", dbs+"/luckperms/tables/content"+query, nil, http.StatusBadRequest, nil)
	}
	api.do("GET", dbs+"/luckperms/tables/missing", nil, http.StatusNotFound, nil)
	api.do("GET", dbs+"/luckperms/tables/a%20b", nil, http.StatusBadRequest, nil)
	api.do("GET", dbs+"/other/tables", nil, http.StatusNotFound, nil)

	// The log of its container, without passwords, for those who manage datastores, in the
	// panel and the terminal.
	a1.runtime.Log(ds.ID, "LOG:  ready to accept connections", "STATEMENT:  ALTER ROLE luckperms PASSWORD '"+rotated+"';")
	logs := "/api/datastores/" + ds.ID + "/logs"
	hidden := "STATEMENT:  ALTER ROLE luckperms PASSWORD '<hidden>';"
	for query, want := range map[string]string{
		"":                  "id: 1000000001\ndata: LOG:  ready to accept connections\n\nid: 1000000002\ndata: " + hidden + "\n\nevent: end\ndata:\n\n",
		"?after=1000000001": "id: 1000000002\ndata: " + hidden + "\n\nevent: end\ndata:\n\n",
	} {
		if body := api.do("GET", logs+query, nil, http.StatusOK, nil); body != want {
			t.Errorf("log%s = %q, want %q", query, body, want)
		}
	}
	viewer.do("GET", logs, nil, http.StatusForbidden, nil)
	api.do("GET", "/api/datastores/unknown/logs", nil, http.StatusNotFound, nil)
	for command, want := range map[string]string{"datastore logs -n 1 " + ds.ID: hidden + "\n", "datastore logs -n -1 " + ds.ID: "--lines can't be negative"} {
		if out, errMsg := runTerminal(t, http.DefaultClient, m.panel(t).URL, a1.node.ID, command); out+errMsg != want {
			t.Errorf("%s: output %q, error %q; want %q", command, out, errMsg, want)
		}
	}

	// Dumps by hand and by a backup job, restored and downloaded.
	backups := "/api/datastores/" + ds.ID + "/backups"
	var dump datastore.Dump
	api.do("POST", backups, map[string]any{"label": "before"}, http.StatusCreated, &dump)
	if !slices.Equal(dump.Databases, []string{"luckperms"}) || dump.Label != "before" {
		t.Fatalf("dump = %+v", dump)
	}
	check(t, a1.runtime.Write(ds.ID, "luckperms", "groups v2"))
	check(t, a1.runtime.setState(lobby.ServerID, noryxv1.ServerState_SERVER_STATE_RUNNING))
	api.do("POST", backups+"/"+dump.ID+"/restore", map[string]any{}, http.StatusNoContent, nil)
	if content, pw := a1.runtime.Content(ds.ID, "luckperms"); content != "groups v1" || pw != rotated {
		t.Fatalf("restored %q with password %q", content, pw)
	}
	if got := serverState(t, api, lobby); got != "running" {
		t.Errorf("lobby is %s after the restore", got)
	}
	archive := api.do("GET", backups+"/"+dump.ID+"/download", nil, http.StatusOK, nil)
	zr, err := zip.NewReader(strings.NewReader(archive), int64(len(archive)))
	check(t, err)
	f, err := zr.Open("luckperms.sql")
	check(t, err)
	if data, _ := io.ReadAll(f); string(data) != "groups v1" {
		t.Errorf("the download has %q", data)
	}
	job := map[string]any{
		"name": "Databases", "enabled": true, "targets": []map[string]string{},
		"schedule": map[string]any{"days": []int{}, "times": []string{"03:00"}, "timeZone": "Europe/Berlin"},
		"settings": map[string]any{"selection": map[string]any{}, "datastores": []string{ds.ID}, "keep": 1},
	}
	var task schedule.Task
	api.do("POST", "/api/backup-jobs", job, http.StatusCreated, &task)
	run(t, api, "/api/backup-jobs/"+task.ID)
	var dumps []datastore.Dump
	api.do("GET", backups, nil, http.StatusOK, &dumps)
	if len(dumps) != 2 || dumps[0].JobID != task.ID || dumps[0].Label != "Databases" {
		t.Fatalf("dumps = %+v", dumps)
	}

	// Dumps made elsewhere, the SQL of a database or a ZIP archive with a <database>.sql for
	// each, are kept like those made here and restored the same way.
	var uploaded datastore.Dump
	api.do("POST", backups+"/upload?database=luckperms&label=moved", []byte("groups v3"), http.StatusCreated, &uploaded)
	if !slices.Equal(uploaded.Databases, []string{"luckperms"}) || uploaded.Label != "moved" {
		t.Fatalf("uploaded %+v", uploaded)
	}
	api.do("POST", backups+"/"+uploaded.ID+"/restore", map[string]any{}, http.StatusNoContent, nil)
	if content, _ := a1.runtime.Content(ds.ID, "luckperms"); content != "groups v3" {
		t.Fatalf("after loading the SQL: %q", content)
	}
	zipped := func(files ...string) []byte {
		var buf bytes.Buffer
		zw := zip.NewWriter(&buf)
		for i := 0; i < len(files); i += 2 {
			f, err := zw.Create(files[i])
			check(t, err)
			_, err = io.WriteString(f, files[i+1])
			check(t, err)
		}
		check(t, zw.Close())
		return buf.Bytes()
	}
	api.do("POST", backups+"/upload", zipped("luckperms.sql", "groups v4", "__MACOSX/._luckperms.sql", "junk"), http.StatusCreated, &uploaded)
	api.do("POST", backups+"/"+uploaded.ID+"/restore", map[string]any{}, http.StatusNoContent, nil)
	if content, _ := a1.runtime.Content(ds.ID, "luckperms"); content != "groups v4" || !slices.Equal(uploaded.Databases, []string{"luckperms"}) {
		t.Fatalf("after loading %+v: %q", uploaded, content)
	}
	for query, body := range map[string][]byte{
		"":                    []byte("groups v5"),
		"?label=":             zipped("../luckperms.sql", "groups v5"),
		"?label=other":        zipped("luckperms.sql", "groups v5", "plugin.jar", ""),
		"?database=Luckperms": []byte("groups v5"),
	} {
		status := http.StatusBadRequest
		if strings.Contains(query, "database") {
			status = http.StatusNotFound
		}
		api.do("POST", backups+"/upload"+query, body, status, nil)
	}
	viewer.do("POST", backups+"/upload?database=luckperms", []byte("groups v5"), http.StatusForbidden, nil)
	api.do("GET", backups, nil, http.StatusOK, &dumps)
	if len(dumps) != 4 {
		t.Errorf("dumps after the uploads: %+v", dumps)
	}

	// An upgrade keeps the data of the previous version until it is removed.
	api.do("PATCH", "/api/datastores/"+ds.ID, map[string]any{"version": "12.3"}, http.StatusOK, &ds)
	if ds.Version != "12.3" || ds.Previous != "11.8" {
		t.Fatalf("after the upgrade: %+v", ds)
	}
	var changed datastore.View
	api.do("PATCH", "/api/datastores/"+ds.ID, map[string]any{"removePrevious": true, "memoryMb": 1024}, http.StatusOK, &changed)
	if changed.Previous != "" || changed.MemoryMB != 1024 {
		t.Fatalf("after removing the previous version: %+v", changed)
	}

	// The connections and buffers of its engine, within bounds that follow its memory, which
	// it keeps through other changes.
	for _, bad := range []map[string]any{{"maxConnections": 9}, {"maxConnections": 1001}, {"bufferMb": 769}, {"memoryMb": 512, "bufferMb": 768}} {
		api.do("PATCH", "/api/datastores/"+ds.ID, bad, http.StatusBadRequest, nil)
	}
	api.do("PATCH", "/api/datastores/"+ds.ID, map[string]any{"maxConnections": 400, "bufferMb": 768}, http.StatusOK, &changed)
	api.do("PATCH", "/api/datastores/"+ds.ID, map[string]any{"memoryMb": 2048}, http.StatusOK, &changed)
	list, _ = a1.runtime.ListDatastores(t.Context())
	if changed.MaxConnections != 400 || changed.BufferMB != 768 || list[0].MaxConnections != 400 || list[0].BufferMB != 768 || list[0].MemoryMB != 2048 {
		t.Fatalf("after tuning: %+v, on the node %+v", changed, list[0])
	}

	// A datastore of a node that joins the private network is published for the nodes of the
	// servers once the network is applied, and no longer once the node leaves.
	a4 := m.startAgent(t, "node-4")
	var stats datastore.View
	api.do("POST", base, map[string]any{"name": "stats", "nodeId": a4.node.ID, "engine": "postgres", "memoryMb": 512, "maxConnections": 256}, http.StatusCreated, &stats)
	if list, _ := a4.runtime.ListDatastores(t.Context()); stats.MaxConnections != 256 || list[0].MaxConnections != 256 || list[0].BufferMB != 0 {
		t.Fatalf("created %+v, on the node %+v", stats, list[0])
	}
	check(t, agentoverlay.Allow(a4.dir))
	api.do("POST", "/api/nodes/"+a4.node.ID+"/overlay", map[string]string{"endpoint": "203.0.113.4:51820"}, http.StatusOK, nil)
	api.do("POST", "/api/networks/"+n.ID+"/apply", nil, http.StatusOK, nil)
	if c := a4.kernel.applied().Clients["db-"+stats.ID]; c.Port == 0 || c.Address.String() != "10.213.0.1" || len(c.Others) != 1 || c.Others[0].String() != "10.213.0.2" {
		t.Fatalf("node-4 publishes stats for %+v", c)
	}
	api.do("DELETE", "/api/nodes/"+a4.node.ID+"/overlay", nil, http.StatusNoContent, nil)
	if list, _ := a4.runtime.ListDatastores(t.Context()); len(list) != 1 || list[0].Port != 0 {
		t.Fatalf("after leaving, node-4 publishes %+v", list)
	}
	api.do("DELETE", "/api/datastores/"+stats.ID, nil, http.StatusNoContent, nil)

	// A move to a node that can't reach the datastore needs a confirmation, and the network
	// stays while it has datastores.
	body = api.do("POST", "/api/nodes/"+game.NodeID+"/servers/"+game.ServerID+"/move", map[string]any{"node": a3.node.ID}, http.StatusConflict, nil)
	if !strings.Contains(body, network.ErrUnreachable) {
		t.Errorf("move: %s", body)
	}
	api.do("DELETE", "/api/networks/"+n.ID, nil, http.StatusConflict, nil)

	// Deleting a datastore removes it with its dumps and closes its port.
	api.do("DELETE", "/api/datastores/"+ds.ID, nil, http.StatusNoContent, nil)
	if list, _ := a1.runtime.ListDatastores(t.Context()); len(list) > 0 {
		t.Fatalf("datastores left: %+v", list)
	}
	if _, ok := a1.kernel.applied().Clients["db-"+ds.ID]; ok {
		t.Error("the port stayed open")
	}
	api.do("GET", backups, nil, http.StatusNotFound, nil)
	api.do("DELETE", "/api/networks/"+n.ID, nil, http.StatusOK, nil)
}
