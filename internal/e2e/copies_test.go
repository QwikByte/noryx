package e2e

import (
	"archive/zip"
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	"github.com/QwikByte/noryx/internal/agent/runtime"
	masterapp "github.com/QwikByte/noryx/internal/master/app"
	"github.com/QwikByte/noryx/internal/master/backup"
	"github.com/QwikByte/noryx/internal/master/network"
	"github.com/QwikByte/noryx/internal/master/schedule"
)

// unpacked returns the files of a ZIP archive.
func unpacked(t *testing.T, data []byte) map[string]string {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	check(t, err)
	files := map[string]string{}
	for _, f := range zr.File {
		r, err := f.Open()
		check(t, err)
		content, err := io.ReadAll(r)
		check(t, err)
		files[f.Name] = string(content)
	}
	return files
}

// nextSecond waits for the next second, so that backups made one after the other have times
// that tell which is newer.
func nextSecond() { time.Sleep(time.Until(time.Now().Truncate(time.Second).Add(time.Second))) }

// A backup job copies its backups to S3-compatible storage and to another node, without the
// secrets of the server, and keeps as many copies as backups. A server can be restored from
// its copies, also once it is gone, into another server, which keeps its own secrets.
func TestBackupCopies(t *testing.T) {
	m := startMaster(t)
	a1, a2, a3 := m.startAgent(t, "node-1"), m.startAgent(t, "node-2"), m.startAgent(t, "node-3")
	lobby := m.createServer(t, a1, "Lobby", noryxv1.ServerType_SERVER_TYPE_PAPER, 25565)
	other := m.createServer(t, a3, "Other", noryxv1.ServerType_SERVER_TYPE_PAPER, 25566)
	proxy := m.createServer(t, a3, "Proxy", noryxv1.ServerType_SERVER_TYPE_VELOCITY, 25577)
	data := serverData{t, filepath.Join(a1.runtime.dir, lobby.ServerID)}
	data.write("world/level.dat", "level")
	data.write("server.properties", "motd=lobby\nrcon.password=lobby-secret\n")
	data.write(".rcon-cli.env", "lobby")
	data.write("plugins/Sync/token.yml", "token: lobby")
	data.write("noryx-filesets.json", `{"marked":["plugins/Sync/token.yml"]}`)
	otherData := serverData{t, filepath.Join(a3.runtime.dir, other.ServerID)}
	otherData.write("server.properties", "motd=other\nrcon.password=other-secret\n")
	otherData.write("plugins/Sync/token.yml", "token: other")

	svc := m.services(t)
	srv := httptest.NewTLSServer(masterapp.Handler(svc))
	t.Cleanup(srv.Close)
	admin, err := svc.Users.CreateUser(t.Context(), "admin", "the-admins-password")
	check(t, err)
	check(t, svc.Access.MakeAdmin(t.Context(), admin.ID))
	api := browser(t, srv)
	api.do("POST", "/api/auth/login", map[string]string{"username": "admin", "password": "the-admins-password"}, http.StatusOK, nil)

	// A storage is only saved once the master could store an object there, over HTTPS, and
	// its secret key is never returned.
	storage := map[string]any{
		"name": "Offsite", "endpoint": m.s3.Endpoint, "region": "us-east-1", "bucket": m.s3.Bucket, "prefix": "noryx/", "accessKey": "key",
		"secretKey": "the-secret-key", "pathStyle": true, "encrypt": true,
	}
	api.do("POST", "/api/backup-storages", map[string]any{"name": "Web", "endpoint": "https://" + m.s3.Endpoint, "region": "us-east-1", "bucket": "b", "accessKey": "k", "secretKey": "s"},
		http.StatusBadRequest, nil)
	m.s3.Fail(func(*http.Request) int { return http.StatusForbidden })
	api.do("POST", "/api/backup-storages", storage, http.StatusBadRequest, nil)
	m.s3.Fail(nil)
	var st backup.Storage
	if body := api.do("POST", "/api/backup-storages", storage, http.StatusCreated, &st); strings.Contains(body, "the-secret-key") || st.Prefix != "noryx" {
		t.Fatalf("storage = %s", body)
	}
	if body := api.do("GET", "/api/backup-storages", nil, http.StatusOK, nil); strings.Contains(body, "the-secret-key") {
		t.Fatalf("storages = %s", body)
	}
	storage["name"], storage["secretKey"] = "Offsite S3", "" // keeps the secret key
	api.do("PUT", "/api/backup-storages/"+st.ID, storage, http.StatusOK, &st)
	if len(m.s3.Keys()) != 0 {
		t.Fatalf("objects = %v", m.s3.Keys())
	}

	// Copying needs the permission to see the backups of all servers.
	job := func(name string, copyTo map[string]string) map[string]any {
		return map[string]any{
			"name": name, "enabled": true,
			"schedule": map[string]any{"times": []string{"03:00"}, "timeZone": "UTC"},
			"targets":  []map[string]string{{"nodeId": lobby.NodeID, "serverId": lobby.ServerID}},
			"settings": map[string]any{"selection": map[string]any{"everything": true}, "keep": 2, "copy": copyTo},
		}
	}
	manager := m.withGrants(t, map[string]any{"name": "Jobs", "permissions": []string{"backupjobs.manage"}})
	manager.do("POST", "/api/backup-jobs", job("Denied", map[string]string{"storage": st.ID}), http.StatusForbidden, nil)
	manager.do("POST", "/api/backup-storages", storage, http.StatusForbidden, nil)
	api.do("POST", "/api/backup-jobs", job("Nowhere", map[string]string{"storage": "aaaaaaaaaaaaaaaaaaaaaaaaaa"}), http.StatusBadRequest, nil)
	api.do("POST", "/api/policies", map[string]any{
		"name": "Restart", "enabled": true, "schedule": map[string]any{"times": []string{"04:00"}, "timeZone": "UTC"},
		"targets":  []map[string]string{{"nodeId": lobby.NodeID}},
		"settings": map[string]any{"action": "restart", "backup": map[string]any{"selection": map[string]any{"worlds": true}, "copy": map[string]string{"storage": st.ID}}},
	}, http.StatusBadRequest, nil)

	// Each run copies the new backup and deletes the copies the job no longer keeps.
	var toS3, toNode schedule.Task
	api.do("POST", "/api/backup-jobs", job("Offsite", map[string]string{"storage": st.ID}), http.StatusCreated, &toS3)
	for range 3 {
		nextSecond()
		steps := runSteps(t, api, "/api/backup-jobs/"+toS3.ID)
		if names := stepNames(steps); !slices.Equal(names, []string{"Back up server succeeded", "Copy backups succeeded"}) || !strings.Contains(steps[1].Change, "copied 1 to Offsite S3") {
			t.Fatalf("steps = %+v", steps)
		}
	}
	var backups []backupView
	api.do("GET", "/api/nodes/"+lobby.NodeID+"/servers/"+lobby.ServerID+"/backups", nil, http.StatusOK, &backups)
	keys := m.s3.Keys()
	if len(backups) != 2 || !slices.Equal(keys, []string{
		"noryx/" + lobby.ServerID + "/" + backups[1].ID + ".zip", "noryx/" + lobby.ServerID + "/" + backups[0].ID + ".zip",
	}) {
		t.Fatalf("backups = %+v, objects = %v", backups, keys)
	}

	// Copies leave out the secrets of the server, like downloads, and are encrypted by the storage.
	object := m.s3.Objects()[keys[0]]
	files := unpacked(t, object.Data)
	if object.Header.Get("X-Amz-Server-Side-Encryption") != "AES256" || files["world/level.dat"] != "level" ||
		files["server.properties"] != "motd=lobby\nrcon.password=<hidden>\n" {
		t.Fatalf("copy with %v holds %v", object.Header, files)
	}
	for _, secret := range []string{".rcon-cli.env", "plugins/Sync/token.yml", "noryx-filesets.json"} {
		if _, ok := files[secret]; ok {
			t.Fatalf("the copy holds %s", secret)
		}
	}

	// Another node keeps copies apart from the backups of its own servers.
	api.do("POST", "/api/backup-jobs", job("Other node", map[string]string{"node": a2.node.ID}), http.StatusCreated, &toNode)
	nextSecond()
	steps := runSteps(t, api, "/api/backup-jobs/"+toNode.ID)
	if len(steps) != 2 || steps[1].Change != "copied 1 to node-2" {
		t.Fatalf("steps = %+v", steps)
	}
	var copies []backup.Copy
	base := "/api/nodes/" + lobby.NodeID + "/servers/" + lobby.ServerID + "/copies"
	api.do("GET", base, nil, http.StatusOK, &copies)
	if len(copies) != 3 || copies[0].Where != "node-2" || copies[0].CopyNodeID != a2.node.ID || copies[1].Where != "Offsite S3" || copies[0].ServerName != "Lobby" {
		t.Fatalf("copies = %+v", copies)
	}
	onNode2 := filepath.Join(a2.dir, "backups", "copies", lobby.ServerID, copies[0].BackupID+".zip")
	if _, err := os.Stat(onNode2); err != nil {
		t.Fatal(err)
	}
	// Copies to the server's own node would protect nothing.
	api.do("PUT", "/api/backup-jobs/"+toNode.ID, job("Other node", map[string]string{"node": a1.node.ID}), http.StatusOK, nil)
	if steps := runSteps(t, api, "/api/backup-jobs/"+toNode.ID); len(steps) != 2 || steps[1].Outcome != schedule.OutcomeSkipped {
		t.Fatalf("steps = %+v", steps)
	}

	// Restoring a copy into its server keeps the server's secrets.
	data.write("world/level.dat", "griefed")
	data.write("server.properties", "motd=changed\nrcon.password=lobby-secret\n")
	viewer := m.withGrants(t, map[string]any{"name": "Viewers", "permissions": []string{"backups.view"}, "allServers": true})
	viewer.do("POST", base+"/"+strconv.FormatInt(copies[1].ID, 10)+"/restore", nil, http.StatusForbidden, nil)
	api.do("POST", base+"/"+strconv.FormatInt(copies[1].ID, 10)+"/restore", map[string]any{"snapshotFirst": true}, http.StatusOK, nil)
	if data.read("world/level.dat") != "level" || data.read("server.properties") != "motd=lobby\nrcon.password=lobby-secret\n" ||
		data.read(".rcon-cli.env") != "lobby" || data.read("plugins/Sync/token.yml") != "token: lobby" {
		t.Fatalf("restored server.properties = %q", data.read("server.properties"))
	}
	// The copy of another server's backup isn't one of this server's.
	api.do("POST", "/api/nodes/"+other.NodeID+"/servers/"+other.ServerID+"/copies/"+strconv.FormatInt(copies[1].ID, 10)+"/restore", nil, http.StatusNotFound, nil)

	// Once the server is gone, its copies are restored into another one, of the same kind.
	api.do("DELETE", "/api/nodes/"+lobby.NodeID+"/servers/"+lobby.ServerID, nil, http.StatusNoContent, nil)
	api.do("GET", "/api/backup-copies", nil, http.StatusOK, &copies)
	if len(copies) != 3 {
		t.Fatalf("copies = %+v", copies)
	}
	into := func(ref network.Ref) map[string]string {
		return map[string]string{"node": ref.NodeID, "server": ref.ServerID}
	}
	api.do("POST", "/api/backup-copies/"+strconv.FormatInt(copies[0].ID, 10)+"/restore", into(proxy), http.StatusConflict, nil)
	api.do("POST", "/api/backup-copies/"+strconv.FormatInt(copies[0].ID, 10)+"/restore", into(other), http.StatusOK, nil)
	if otherData.read("world/level.dat") != "level" || otherData.read("server.properties") != "motd=lobby\nrcon.password=other-secret\n" ||
		otherData.read("plugins/Sync/token.yml") != "" {
		t.Fatalf("restored server.properties = %q", otherData.read("server.properties"))
	}
	var otherBackups []backupView
	api.do("GET", "/api/nodes/"+other.NodeID+"/servers/"+other.ServerID+"/backups", nil, http.StatusOK, &otherBackups)
	if len(otherBackups) != 0 {
		t.Fatalf("the copy stayed with the server: %+v", otherBackups)
	}

	// Deleting a copy deletes it where it is kept; a storage that a job copies to stays.
	api.do("DELETE", "/api/backup-copies/"+strconv.FormatInt(copies[0].ID, 10), nil, http.StatusNoContent, nil)
	if _, err := os.Stat(onNode2); !os.IsNotExist(err) {
		t.Fatalf("the copy is still on the node: %v", err)
	}
	api.do("DELETE", "/api/backup-copies/"+strconv.FormatInt(copies[1].ID, 10), nil, http.StatusNoContent, nil)
	if keys := m.s3.Keys(); len(keys) != 1 {
		t.Fatalf("objects = %v", keys)
	}
	api.do("DELETE", "/api/backup-storages/"+st.ID, nil, http.StatusConflict, nil)
	api.do("DELETE", "/api/backup-jobs/"+toS3.ID, nil, http.StatusNoContent, nil)
	api.do("DELETE", "/api/backup-storages/"+st.ID, nil, http.StatusNoContent, nil)
	api.do("GET", "/api/backup-copies", nil, http.StatusOK, &copies)
	if len(copies) != 0 {
		t.Fatalf("copies of a deleted storage = %+v", copies)
	}
}

// A node tells the IDs of its servers itself. A compromised node that claims the ID of a server
// of another node gets copies of its own, which neither replace nor delete those of the server,
// even with backups from the future.
func TestCopiesOfClaimedServers(t *testing.T) {
	m := startMaster(t)
	a1, a2, a3 := m.startAgent(t, "node-1"), m.startAgent(t, "node-2"), m.startAgent(t, "node-3")
	lobby := m.createServer(t, a1, "Lobby", noryxv1.ServerType_SERVER_TYPE_PAPER, 25565)
	serverData{t, filepath.Join(a1.runtime.dir, lobby.ServerID)}.write("world/level.dat", "level")
	svc := m.services(t)
	srv := httptest.NewTLSServer(masterapp.Handler(svc))
	t.Cleanup(srv.Close)
	admin, err := svc.Users.CreateUser(t.Context(), "admin", "the-admins-password")
	check(t, err)
	check(t, svc.Access.MakeAdmin(t.Context(), admin.ID))
	api := browser(t, srv)
	api.do("POST", "/api/auth/login", map[string]string{"username": "admin", "password": "the-admins-password"}, http.StatusOK, nil)
	var job schedule.Task
	api.do("POST", "/api/backup-jobs", map[string]any{
		"name": "Offsite", "enabled": true, "schedule": map[string]any{"times": []string{"03:00"}, "timeZone": "UTC"},
		"targets":  []map[string]string{{"nodeId": a1.node.ID}, {"nodeId": a3.node.ID}},
		"settings": map[string]any{"selection": map[string]any{"everything": true}, "keep": 2, "copy": map[string]string{"node": a2.node.ID}},
	}, http.StatusCreated, &job)
	runSteps(t, api, "/api/backup-jobs/"+job.ID)

	a3.runtime.mu.Lock()
	a3.runtime.servers = append(a3.runtime.servers, runtime.Server{Spec: runtime.Spec{ID: lobby.ServerID, Name: "Lobby", Type: noryxv1.ServerType_SERVER_TYPE_PAPER}})
	a3.runtime.mu.Unlock()
	serverData{t, filepath.Join(a3.runtime.dir, lobby.ServerID)}.write("world/level.dat", "planted")
	future := time.Now().Add(48 * time.Hour)
	id, planted := noryxv1.NewBackupID(future), serverData{t, filepath.Join(a3.dir, "backups", lobby.ServerID)}
	planted.write(id+".zip", string(zipOf(t, map[string]string{"world/level.dat": "planted"})))
	planted.write(id+".json", `{"created":"`+future.Format(time.RFC3339)+`","paths":["."],"jobId":"`+job.ID+`"}`)
	nextSecond()
	runSteps(t, api, "/api/backup-jobs/"+job.ID)

	copiesOf := func(ref network.Ref) []backup.Copy {
		var copies []backup.Copy
		api.do("GET", "/api/nodes/"+ref.NodeID+"/servers/"+ref.ServerID+"/copies", nil, http.StatusOK, &copies)
		return copies
	}
	ours, theirs := copiesOf(lobby), copiesOf(network.Ref{NodeID: a3.node.ID, ServerID: lobby.ServerID})
	if len(ours) != 2 || len(theirs) != 2 || ours[0].NodeID != a1.node.ID || theirs[0].NodeID != a3.node.ID || theirs[0].BackupID != id {
		t.Fatalf("copies of the lobby = %+v, of node-3 = %+v", ours, theirs)
	}
	// The copies of the server of another node aren't the lobby's.
	path := "/api/nodes/" + lobby.NodeID + "/servers/" + lobby.ServerID + "/copies/" + strconv.FormatInt(theirs[0].ID, 10)
	api.do("POST", path+"/restore", nil, http.StatusNotFound, nil)
	api.do("DELETE", path, nil, http.StatusNotFound, nil)
}
