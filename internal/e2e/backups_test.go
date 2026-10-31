package e2e

import (
	"archive/zip"
	"bytes"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	"github.com/QwikByte/noryx/internal/agent/runtime"
	"github.com/QwikByte/noryx/internal/master/network"
	"github.com/QwikByte/noryx/internal/master/schedule"
)

type backupView struct {
	ID      string   `json:"id"`
	Label   string   `json:"label"`
	Paths   []string `json:"paths"`
	Exclude []string `json:"exclude"`
	JobID   string   `json:"jobId"`
	Kept    bool     `json:"kept"`
}

// serverData writes and reads the files of a server in a test.
type serverData struct {
	t   *testing.T
	dir string
}

func (f serverData) write(name, content string) {
	f.t.Helper()
	check(f.t, os.MkdirAll(filepath.Dir(filepath.Join(f.dir, name)), 0o750))
	check(f.t, os.WriteFile(filepath.Join(f.dir, name), []byte(content), 0o600))
}

func (f serverData) read(name string) string {
	content, _ := os.ReadFile(filepath.Join(f.dir, name))
	return string(content)
}

// Backups can leave out folders, change their label and be kept, and be restored in parts,
// after a backup of what the restore replaces.
func TestBackupOptions(t *testing.T) {
	m := startMaster(t)
	a := m.startAgent(t, "node-1")
	srv := m.createServer(t, a, "Lobby", noryxv1.ServerType_SERVER_TYPE_PAPER, 25565)
	api := apiClient{t: t, url: m.panel(t).URL}
	base := "/api/nodes/" + srv.NodeID + "/servers/" + srv.ServerID + "/backups"
	data := serverData{t, filepath.Join(a.runtime.dir, srv.ServerID)}
	data.write("world/level.dat", "level")
	data.write("world/region/r.0.0.mca", "chunks")
	data.write("plugins/dynmap/web/tiles/t.png", "tile")
	data.write("logs/latest.log", "log")

	// A backup leaves out what is chosen, which its folders don't list.
	api.do("POST", base, map[string]any{"selection": map[string]any{"everything": true, "exclude": []string{"/"}}}, http.StatusBadRequest, nil)
	var b backupView
	api.do("POST", base, map[string]any{"label": "Update", "selection": map[string]any{"everything": true, "exclude": []string{"plugins/dynmap/web/tiles", "logs"}}},
		http.StatusCreated, &b)
	if !slices.Equal(b.Exclude, []string{"logs", "plugins/dynmap/web/tiles"}) {
		t.Fatalf("backup = %+v", b)
	}
	var listing struct {
		Files []struct {
			Name      string `json:"name"`
			Directory bool   `json:"directory"`
		} `json:"files"`
	}
	api.do("GET", base+"/"+b.ID+"/files?path=plugins/dynmap/web", nil, http.StatusOK, &listing)
	if len(listing.Files) != 0 {
		t.Fatalf("listed %+v", listing.Files)
	}
	api.do("GET", base+"/"+b.ID+"/files", nil, http.StatusOK, &listing)
	if len(listing.Files) != 2 || listing.Files[0].Name != "plugins" || listing.Files[1].Name != "world" || !listing.Files[1].Directory {
		t.Fatalf("listed %+v", listing.Files)
	}
	api.do("GET", base+"/"+b.ID+"/files?path=logs", nil, http.StatusNotFound, nil)

	// Restoring a folder backs up what it replaces first.
	data.write("world/region/r.0.0.mca", "griefed")
	data.write("world/level.dat", "changed")
	var restored struct{ Snapshot *backupView }
	api.do("POST", base+"/"+b.ID+"/restore", map[string]any{"paths": []string{"world/region"}, "snapshotFirst": true}, http.StatusOK, &restored)
	if data.read("world/region/r.0.0.mca") != "chunks" || data.read("world/level.dat") != "changed" {
		t.Fatal("more or less than the folder was restored")
	}
	if s := restored.Snapshot; s == nil || s.Label != "Before restoring Update" || !slices.Equal(s.Paths, []string{"world/region"}) {
		t.Fatalf("snapshot = %+v", s)
	}
	api.do("POST", base+"/"+b.ID+"/restore", map[string]any{"paths": []string{"world_nether"}}, http.StatusNotFound, nil)
	api.do("POST", base+"/"+b.ID+"/restore", map[string]any{"paths": []string{"../world"}}, http.StatusBadRequest, nil)

	// A kept backup survives its job's pruning until it is no longer kept, which needs the
	// permission to delete backups.
	job := map[string]any{
		"name": "Monthly", "enabled": true,
		"schedule": map[string]any{"days": []int{}, "times": []string{"03:00"}, "timeZone": "Europe/Berlin"},
		"targets":  []map[string]string{{"nodeId": srv.NodeID}},
		"settings": map[string]any{"selection": map[string]any{"worlds": true}, "keep": 0, "keepMonths": 1001},
	}
	api.do("POST", "/api/backup-jobs", job, http.StatusBadRequest, nil)
	job["settings"] = map[string]any{"selection": map[string]any{"worlds": true}, "keep": 0, "keepMonths": 1}
	var task schedule.Task
	api.do("POST", "/api/backup-jobs", job, http.StatusCreated, &task)
	jobBackups := func() []backupView {
		var list []backupView
		api.do("GET", base, nil, http.StatusOK, &list)
		return slices.DeleteFunc(list, func(b backupView) bool { return b.JobID != task.ID })
	}
	run(t, api, "/api/backup-jobs/"+task.ID)
	kept := jobBackups()[0]
	api.do("PATCH", base+"/"+kept.ID, map[string]any{"kept": true}, http.StatusOK, &kept)
	creator := m.withGrants(t, map[string]any{"name": "Backups", "permissions": []string{"backups.create"}, "targets": []network.Ref{srv}})
	creator.do("PATCH", base+"/"+kept.ID, map[string]any{"label": "Before the update"}, http.StatusOK, &kept)
	creator.do("PATCH", base+"/"+kept.ID, map[string]any{"kept": false}, http.StatusForbidden, nil)
	if !kept.Kept || kept.Label != "Before the update" {
		t.Fatalf("backup = %+v", kept)
	}
	run(t, api, "/api/backup-jobs/"+task.ID)
	if list := jobBackups(); len(list) != 2 || list[1].ID != kept.ID {
		t.Fatalf("job's backups = %+v", list)
	}
	api.do("PATCH", base+"/"+kept.ID, map[string]any{"kept": false}, http.StatusOK, nil)
	run(t, api, "/api/backup-jobs/"+task.ID)
	if list := jobBackups(); len(list) != 1 || list[0].ID == kept.ID {
		t.Fatalf("job's backups = %+v", list)
	}
}

// A backup can be restored into another server, also on another node, which gets neither
// the secrets of the original nor its trust in a proxy, and keeps its own.
func TestRestoreIntoAnotherServer(t *testing.T) {
	m := startMaster(t)
	a1, a2 := m.startAgent(t, "node-1"), m.startAgent(t, "node-2")
	proxy := m.createServer(t, a1, "Proxy", noryxv1.ServerType_SERVER_TYPE_VELOCITY, 25577)
	lobby := m.createServer(t, a1, "Lobby", noryxv1.ServerType_SERVER_TYPE_PAPER, 25565)
	other := m.createServer(t, a2, "Copy", noryxv1.ServerType_SERVER_TYPE_PAPER, 25566)
	api := apiClient{t: t, url: m.panel(t).URL}
	api.do("POST", "/api/networks", map[string]any{"name": "Main", "proxy": proxy, "servers": []network.Ref{lobby}}, http.StatusCreated, nil)
	base := "/api/nodes/" + lobby.NodeID + "/servers/" + lobby.ServerID + "/backups"
	original := serverData{t, filepath.Join(a1.runtime.dir, lobby.ServerID)}
	original.write("world/level.dat", "level")
	original.write("server.properties", "motd=lobby\nrcon.password=lobby-secret\nonline-mode=false\n")
	original.write(".rcon-cli.env", "lobby")
	original.write("config/paper-global.yml", "proxies:\n  velocity:\n    enabled: true\n    online-mode: true\n    secret: network-secret\n")
	original.write("plugins/Sync/token.yml", "token: lobby")
	original.write("noryx-filesets.json", `{"marked":["plugins/Sync/token.yml"]}`)
	copied := serverData{t, filepath.Join(a2.runtime.dir, other.ServerID)}
	copied.write("server.properties", "motd=copy\nrcon.password=copy-secret\n")
	copied.write(".rcon-cli.env", "copy")
	copied.write("plugins/Sync/token.yml", "token: copy")
	var b backupView
	api.do("POST", base, map[string]any{"selection": map[string]any{"everything": true}}, http.StatusCreated, &b)

	// It needs the permission to restore backups there, into a server of the same kind.
	into := func(ref network.Ref) map[string]any {
		return map[string]any{"node": ref.NodeID, "server": ref.ServerID, "snapshotFirst": true}
	}
	viewer := m.withGrants(t, map[string]any{"name": "Viewers", "permissions": []string{"backups.view"}, "allServers": true})
	viewer.do("POST", base+"/"+b.ID+"/restore-into", into(other), http.StatusForbidden, nil)
	api.do("POST", base+"/"+b.ID+"/restore-into", into(proxy), http.StatusConflict, nil)
	api.do("POST", base+"/"+b.ID+"/restore-into", into(lobby), http.StatusBadRequest, nil)

	var restored struct{ Snapshot *backupView }
	api.do("POST", base+"/"+b.ID+"/restore-into", into(other), http.StatusOK, &restored)
	properties := copied.read("server.properties")
	if copied.read("world/level.dat") != "level" || !strings.Contains(properties, "motd=lobby") {
		t.Fatalf("the backup wasn't restored: server.properties = %q", properties)
	}
	if !strings.Contains(properties, "rcon.password=copy-secret") || !strings.Contains(properties, "online-mode=true") || copied.read(".rcon-cli.env") != "copy" {
		t.Fatalf("the copy didn't keep its secrets: server.properties = %q", properties)
	}
	if strings.Contains(copied.read("config/paper-global.yml"), "network-secret") || copied.read("plugins/Sync/token.yml") != "" {
		t.Fatal("the copy got secrets of the original")
	}
	var backups []backupView
	api.do("GET", "/api/nodes/"+other.NodeID+"/servers/"+other.ServerID+"/backups", nil, http.StatusOK, &backups)
	if len(backups) != 1 || restored.Snapshot == nil || backups[0].ID != restored.Snapshot.ID || backups[0].Label != "Before restoring Lobby" {
		t.Fatalf("backups of the copy = %+v, snapshot = %+v", backups, restored.Snapshot)
	}
	if original.read("server.properties") != "motd=lobby\nrcon.password=lobby-secret\nonline-mode=false\n" {
		t.Fatal("the original changed")
	}
}

func TestBackups(t *testing.T) {
	m := startMaster(t)
	a := m.startAgent(t, "node-1")
	srv := m.createServer(t, a, "Lobby", noryxv1.ServerType_SERVER_TYPE_PAPER, 25565)
	api := apiClient{t: t, url: m.panel(t).URL}
	base := "/api/nodes/" + srv.NodeID + "/servers/" + srv.ServerID
	data := filepath.Join(a.runtime.dir, srv.ServerID)
	write := func(name, content string) {
		check(t, os.MkdirAll(filepath.Dir(filepath.Join(data, name)), 0o750))
		check(t, os.WriteFile(filepath.Join(data, name), []byte(content), 0o600))
	}
	read := func(name string) string {
		content, _ := os.ReadFile(filepath.Join(data, name))
		return string(content)
	}
	write("world/level.dat", "level")
	write("world/region/r.0.0.mca", "chunks")
	write("server.properties", "motd=hi\nrcon.password=s3cret\n")
	write("plugins/LuckPerms.jar", "jar")

	// A running server saves its worlds before they are archived, and keeps running.
	api.do("POST", base+"/start", nil, http.StatusNoContent, nil)
	var b backupView
	api.do("POST", base+"/backups", map[string]any{"label": "Before the update", "selection": map[string]any{"worlds": true, "config": true}},
		http.StatusCreated, &b)
	if b.Label != "Before the update" || !slices.Equal(b.Paths, []string{"server.properties", "world"}) {
		t.Fatalf("backup = %+v", b)
	}
	if got, want := a.runtime.consoleCommands(), []string{"save-off", "save-all flush", "save-on"}; !slices.Equal(got, want) {
		t.Fatalf("console commands = %q, want %q", got, want)
	}
	archive := api.do("GET", base+"/backups/"+b.ID+"/download", nil, http.StatusOK, nil)
	zr, err := zip.NewReader(bytes.NewReader([]byte(archive)), int64(len(archive)))
	check(t, err)
	var names []string
	for _, f := range zr.File {
		names = append(names, f.Name)
	}
	if want := []string{"server.properties", "world/", "world/level.dat", "world/region/", "world/region/r.0.0.mca"}; !slices.Equal(names, want) {
		t.Fatalf("archive = %q, want %q", names, want)
	}
	// Downloads hide the secrets; the backup itself keeps them.
	properties, err := zr.Open("server.properties")
	check(t, err)
	if content, _ := io.ReadAll(properties); string(content) != "motd=hi\nrcon.password=<hidden>\n" {
		t.Fatalf("downloaded server.properties = %q", content)
	}

	// Restoring brings back the worlds and settings, but leaves the plugins.
	write("world/region/r.0.0.mca", "griefed")
	write("server.properties", "motd=changed\n")
	write("plugins/Other.jar", "jar")
	api.do("POST", base+"/backups/"+b.ID+"/restore", nil, http.StatusOK, nil)
	if read("world/region/r.0.0.mca") != "chunks" || read("server.properties") != "motd=hi\nrcon.password=s3cret\n" || read("plugins/Other.jar") != "jar" {
		t.Fatal("the backup was not restored as selected")
	}
	if servers, _ := a.runtime.List(t.Context()); servers[0].State != noryxv1.ServerState_SERVER_STATE_RUNNING {
		t.Fatal("the server was not started again after restoring")
	}

	// Invalid selections and locations are refused.
	for _, selection := range []map[string]any{{}, {"paths": []string{"../other"}}} {
		api.do("POST", base+"/backups", map[string]any{"selection": selection}, http.StatusBadRequest, nil)
	}
	api.do("POST", base+"/backups", map[string]any{"selection": map[string]any{"everything": true}, "location": "elsewhere"}, http.StatusBadRequest, nil)
	api.do("GET", base+"/backups/20260101-000000-aaaaaa/download", nil, http.StatusNotFound, nil)

	// A job backs up all servers of the node and keeps the newest backup only.
	job := map[string]any{
		"name": "Nightly", "enabled": true,
		"schedule": map[string]any{"days": []int{}, "times": []string{"03:00"}, "timeZone": "Europe/Berlin"},
		"targets":  []map[string]string{{"nodeId": srv.NodeID}},
		"settings": map[string]any{"selection": map[string]any{"everything": true}, "keep": 1},
	}
	var task schedule.Task
	api.do("POST", "/api/backup-jobs", job, http.StatusCreated, &task)
	for range 2 {
		run(t, api, "/api/backup-jobs/"+task.ID)
	}
	var backups []backupView
	api.do("GET", base+"/backups", nil, http.StatusOK, &backups)
	if len(backups) != 2 || backups[0].JobID != task.ID || backups[0].Label != "Nightly" || !slices.Equal(backups[0].Paths, []string{"."}) || backups[1].ID != b.ID {
		t.Fatalf("backups = %+v", backups)
	}
	api.do("DELETE", base+"/backups/"+b.ID, nil, http.StatusNoContent, nil)
	api.do("DELETE", base+"/backups/"+b.ID, nil, http.StatusNotFound, nil)

	// A job skips a server without the selected data, e.g. one that never started, and notes
	// it; backing it up by hand fails.
	empty := m.createServer(t, a, "Empty", noryxv1.ServerType_SERVER_TYPE_PAPER, 25566)
	job["name"], job["settings"] = "Worlds", map[string]any{"selection": map[string]any{"worlds": true}}
	api.do("POST", "/api/backup-jobs", job, http.StatusCreated, &task)
	run(t, api, "/api/backup-jobs/"+task.ID)
	api.do("GET", "/api/backup-jobs/"+task.ID, nil, http.StatusOK, &task)
	if want := "Empty on node-1: It has none of the selected data yet, e.g. as it never started."; task.LastRun.Note != want {
		t.Fatalf("note = %q, want %q", task.LastRun.Note, want)
	}
	api.do("POST", "/api/nodes/"+empty.NodeID+"/servers/"+empty.ServerID+"/backups", map[string]any{"selection": map[string]any{"worlds": true}},
		http.StatusConflict, nil)

	// A deleted server is no longer a target, and its backups are gone.
	policy := map[string]any{
		"name": "Broadcast", "enabled": true, "schedule": job["schedule"],
		"targets":  []map[string]string{{"nodeId": srv.NodeID, "serverId": srv.ServerID}},
		"settings": map[string]any{"action": "command", "commands": []string{"say Vote for us!"}},
	}
	api.do("POST", "/api/policies", policy, http.StatusCreated, &task)
	run(t, api, "/api/policies/"+task.ID)
	if got := a.runtime.consoleCommands(); got[len(got)-1] != "say Vote for us!" {
		t.Fatalf("console commands = %q", got)
	}
	api.do("DELETE", base, nil, http.StatusNoContent, nil)
	api.do("GET", "/api/policies/"+task.ID, nil, http.StatusOK, &task)
	if len(task.Targets) != 0 {
		t.Fatalf("targets after deleting the server = %+v", task.Targets)
	}
	if _, err := os.Stat(filepath.Join(a.dir, "backups", srv.ServerID)); !os.IsNotExist(err) {
		t.Fatalf("the backups of the deleted server remain: %v", err)
	}
}

// Restoring a backup of a proxy keeps the network's forwarding secret as it is, and the
// network is configured again; if that fails, the restore warns.
func TestRestoreInNetwork(t *testing.T) {
	m := startMaster(t)
	a1, a2 := m.startAgent(t, "node-1"), m.startAgent(t, "node-2")
	proxy := m.createServer(t, a1, "Proxy", noryxv1.ServerType_SERVER_TYPE_VELOCITY, 25577)
	lobby := m.createServer(t, a1, "Lobby", noryxv1.ServerType_SERVER_TYPE_PAPER, 25565)
	survival := m.createServer(t, a2, "Survival", noryxv1.ServerType_SERVER_TYPE_PAPER, 25566)
	api := apiClient{t: t, url: m.panel(t).URL}
	api.do("POST", "/api/networks", map[string]any{"name": "Main", "proxy": proxy, "servers": []network.Ref{lobby, survival}}, http.StatusCreated, nil)
	secret := a1.runtime.network(proxy.ServerID).ForwardingSecret

	// The backup has the secret of an earlier network.
	base := "/api/nodes/" + proxy.NodeID + "/servers/" + proxy.ServerID + "/backups"
	file := filepath.Join(a1.runtime.dir, proxy.ServerID, "forwarding.secret")
	check(t, os.WriteFile(file, []byte("old"), 0o600))
	var b backupView
	api.do("POST", base, map[string]any{"selection": map[string]any{"everything": true}}, http.StatusCreated, &b)
	check(t, os.WriteFile(file, []byte(secret), 0o600))
	a1.runtime.mu.Lock()
	delete(a1.runtime.networks, proxy.ServerID)
	a1.runtime.mu.Unlock()

	var restored struct{ Warning string }
	api.do("POST", base+"/"+b.ID+"/restore", nil, http.StatusOK, &restored)
	if got, _ := os.ReadFile(file); string(got) != secret || restored.Warning != "" {
		t.Fatalf("secret after restoring = %q, warning = %q", got, restored.Warning)
	}
	if got := a1.runtime.network(proxy.ServerID); got.Forwarding != runtime.ForwardingModern || len(got.Backends) != 2 {
		t.Fatalf("the network was not configured again: %+v", got)
	}

	a2.runtime.mu.Lock()
	a2.runtime.down = true
	a2.runtime.mu.Unlock()
	api.do("POST", base+"/"+b.ID+"/restore", nil, http.StatusOK, &restored)
	if !strings.Contains(restored.Warning, "survival could not be configured") {
		t.Fatalf("warning = %q", restored.Warning)
	}
}

func TestPolicies(t *testing.T) {
	m := startMaster(t)
	a := m.startAgent(t, "node-1")
	lobby := m.createServer(t, a, "Lobby", noryxv1.ServerType_SERVER_TYPE_PAPER, 25565)
	survival := m.createServer(t, a, "Survival", noryxv1.ServerType_SERVER_TYPE_PAPER, 25566)
	api := apiClient{t: t, url: m.panel(t).URL}
	api.do("POST", "/api/nodes/"+lobby.NodeID+"/servers/"+lobby.ServerID+"/start", nil, http.StatusNoContent, nil)
	policy := func(name string, settings map[string]any) map[string]any {
		return map[string]any{
			"name": name, "enabled": true, "settings": settings,
			"schedule": map[string]any{"days": []int{1, 3, 5}, "times": []string{"04:00"}, "timeZone": "UTC"},
			"targets":  []map[string]string{{"nodeId": lobby.NodeID}},
		}
	}

	// Operating hours: the node's stopped servers start, running ones stop.
	var task schedule.Task
	api.do("POST", "/api/policies", policy("Opening", map[string]any{"action": "start"}), http.StatusCreated, &task)
	if task.NextRun == nil || task.NextRun.UTC().Hour() != 4 || !slices.Contains([]time.Weekday{1, 3, 5}, task.NextRun.UTC().Weekday()) {
		t.Fatalf("next run = %v", task.NextRun)
	}
	run(t, api, "/api/policies/"+task.ID)
	if servers, _ := a.runtime.List(t.Context()); servers[1].ID != survival.ServerID || servers[1].State != noryxv1.ServerState_SERVER_STATE_RUNNING {
		t.Fatal("the stopped server was not started")
	}
	api.do("POST", "/api/policies", policy("Closing", map[string]any{"action": "stop"}), http.StatusCreated, &task)
	run(t, api, "/api/policies/"+task.ID)
	servers, _ := a.runtime.List(t.Context())
	for _, s := range servers {
		if s.State != noryxv1.ServerState_SERVER_STATE_STOPPED {
			t.Fatalf("%s still runs", s.Name)
		}
	}

	// Policies are validated, and backup jobs are separate from them.
	api.do("POST", "/api/policies", policy("Closing", map[string]any{"action": "stop"}), http.StatusConflict, nil)
	api.do("POST", "/api/policies", policy("Bad", map[string]any{"action": "explode"}), http.StatusBadRequest, nil)
	api.do("POST", "/api/policies", policy("Bad", map[string]any{"action": "command", "commands": []string{"say a\nop b"}}), http.StatusBadRequest, nil)
	bad := policy("Bad", map[string]any{"action": "start"})
	bad["targets"] = []map[string]string{{"nodeId": "unknown"}}
	api.do("POST", "/api/policies", bad, http.StatusBadRequest, nil)
	api.do("GET", "/api/backup-jobs/"+task.ID, nil, http.StatusNotFound, nil)
	var list []schedule.Task
	api.do("GET", "/api/policies", nil, http.StatusOK, &list)
	if len(list) != 2 || list[0].Name != "Closing" || list[1].LastRun == nil || list[1].LastRun.Error != "" {
		t.Fatalf("policies = %+v", list)
	}

	// A server's page lists the policies that cover it, also through its tags; each policy
	// keeps its runs, and the agenda lists the upcoming ones.
	api.do("POST", "/api/servers/tags", map[string]any{"servers": []network.Ref{survival}, "add": []string{"survival"}}, http.StatusNoContent, nil)
	api.do("POST", "/api/policies", map[string]any{
		"name": "Tagged", "enabled": true, "settings": map[string]any{"action": "start"},
		"schedule": map[string]any{"dates": []string{"2099-12-24"}, "times": []string{"04:00"}, "timeZone": "Europe/Berlin"},
		"targets":  []map[string]string{{"kind": "tag", "value": "survival"}},
	}, http.StatusCreated, &task)
	api.do("GET", "/api/policies?node="+survival.NodeID+"&server="+survival.ServerID, nil, http.StatusOK, &list)
	if len(list) != 3 {
		t.Fatalf("policies of %s = %+v", survival.ServerID, list)
	}
	run(t, api, "/api/policies/"+task.ID)
	var runs []schedule.Run
	api.do("GET", "/api/policies/"+task.ID+"/runs", nil, http.StatusOK, &runs)
	if len(runs) != 1 || runs[0].Outcome != schedule.Succeeded || runs[0].EndedAt.Before(runs[0].StartedAt) {
		t.Fatalf("runs = %+v", runs)
	}
	if servers, _ := a.runtime.List(t.Context()); servers[1].State != noryxv1.ServerState_SERVER_STATE_RUNNING {
		t.Fatal("the tagged server didn't start")
	}
	var upcoming []schedule.Upcoming
	api.do("GET", "/api/policies/upcoming", nil, http.StatusOK, &upcoming)
	if len(upcoming) != 6 || upcoming[0].At.UTC().Weekday()%2 != 1 || upcoming[0].At.UTC().Hour() != 4 {
		t.Fatalf("upcoming = %+v", upcoming)
	}
	api.do("DELETE", "/api/policies/"+task.ID, nil, http.StatusNoContent, nil)
}

// run runs a task now and waits until it is done.
func run(t *testing.T, api apiClient, path string) {
	t.Helper()
	var task schedule.Task
	api.do("POST", path+"/run", nil, http.StatusAccepted, &task)
	for deadline := time.Now().Add(10 * time.Second); task.Running; time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the task didn't finish")
		}
		api.do("GET", path, nil, http.StatusOK, &task)
	}
	if task.LastRun == nil || task.LastRun.Error != "" {
		t.Fatalf("run of %s: %+v", task.Name, task.LastRun)
	}
}
