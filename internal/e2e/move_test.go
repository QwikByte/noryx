package e2e

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	"github.com/QwikByte/noryx/internal/agent/runtime"
	"github.com/QwikByte/noryx/internal/master/access"
	"github.com/QwikByte/noryx/internal/master/backup"
	"github.com/QwikByte/noryx/internal/master/network"
	masterserver "github.com/QwikByte/noryx/internal/master/server"
	"github.com/QwikByte/noryx/internal/master/tag"
)

func TestMoveServer(t *testing.T) {
	m := startMaster(t)
	a1, a2 := m.startAgent(t, "node-1"), m.startAgent(t, "node-2")
	lobby := m.createServer(t, a1, "Lobby", noryxv1.ServerType_SERVER_TYPE_PAPER, 25565)
	m.createServer(t, a2, "Survival", noryxv1.ServerType_SERVER_TYPE_PAPER, 25566)
	api := apiClient{t: t, url: m.panel(t).URL}
	path := func(r network.Ref) string { return "/api/nodes/" + r.NodeID + "/servers/" + r.ServerID }
	data := filepath.Join(a1.runtime.dir, lobby.ServerID)
	check(t, os.MkdirAll(filepath.Join(data, "world", "region"), 0o750))
	check(t, os.WriteFile(filepath.Join(data, "world", "region", "r.0.0.mca"), []byte("chunks"), 0o600))
	check(t, os.WriteFile(filepath.Join(data, "server.properties"), []byte("rcon.password=s3cret\n"), 0o600))
	api.do("POST", path(lobby)+"/backups", map[string]any{"label": "Before the move", "selection": map[string]any{"paths": []string{"world"}}}, http.StatusCreated, nil)
	var mods access.Group
	api.do("POST", "/api/groups", map[string]any{"name": "Lobby moderators", "permissions": []string{"servers.restart"}, "targets": []network.Ref{lobby}},
		http.StatusCreated, &mods)
	api.do("POST", path(lobby)+"/start", nil, http.StatusNoContent, nil)
	api.do("PUT", path(lobby)+"/plugins/channels/luckperms", map[string]string{"channel": "beta"}, http.StatusNoContent, nil)
	_, err := m.db.ExecContext(t.Context(), `
		INSERT INTO backup_copies (copy_node, location, server_id, server_name, proxy, node_id, node_name, backup_id, label, created_at, size, paths,
			exclude, kept, copied_at)
		VALUES (?, '', ?, 'Lobby', 0, ?, 'node-1', '20240101-000000-aaaaaa', '', 0, 1, '["."]', '[]', 0, 0)`, a2.node.ID, lobby.ServerID, a1.node.ID)
	check(t, err)

	// What would fail is refused before the server stops.
	api.do("POST", path(lobby)+"/move", map[string]any{"node": a1.node.ID}, http.StatusBadRequest, nil)
	api.do("POST", path(lobby)+"/move", map[string]any{"node": a2.node.ID, "port": 25566}, http.StatusConflict, nil)
	api.do("POST", path(lobby)+"/move", map[string]any{"node": a2.node.ID, "storage": "ssd"}, http.StatusBadRequest, nil)
	if state(t, a1, lobby.ServerID) != noryxv1.ServerState_SERVER_STATE_RUNNING {
		t.Fatal("the server stopped")
	}

	// The server moves with its data and backups, keeps its ID and runs again.
	api.do("POST", path(lobby)+"/move", map[string]any{"node": a2.node.ID, "backups": true}, http.StatusAccepted, nil)
	if mv := waitForMove(t, api, lobby.ServerID); mv.Phase != "done" || len(mv.Warnings) != 0 || mv.Backups != 1 || mv.Bytes == 0 {
		t.Fatalf("move = %+v", mv)
	}
	moved := network.Ref{NodeID: a2.node.ID, ServerID: lobby.ServerID}
	if state(t, a2, lobby.ServerID) != noryxv1.ServerState_SERVER_STATE_RUNNING || slices.ContainsFunc(must(a1.runtime.List(t.Context())), isServer(lobby.ServerID)) {
		t.Fatal("the server is not on the new node only, running")
	}
	if chunks, err := os.ReadFile(filepath.Join(a2.runtime.dir, lobby.ServerID, "world", "region", "r.0.0.mca")); string(chunks) != "chunks" {
		t.Fatalf("world = %q, %v", chunks, err)
	}
	// Unlike downloads, moves keep the secrets.
	if props, err := os.ReadFile(filepath.Join(a2.runtime.dir, lobby.ServerID, "server.properties")); string(props) != "rcon.password=s3cret\n" {
		t.Fatalf("server.properties = %q, %v", props, err)
	}
	var backups []struct{ Label string }
	api.do("GET", path(moved)+"/backups", nil, http.StatusOK, &backups)
	if len(backups) != 1 || backups[0].Label != "Before the move" {
		t.Fatalf("backups = %+v", backups)
	}
	api.do("GET", "/api/groups/"+mods.ID, nil, http.StatusOK, &mods)
	if len(mods.Targets) != 1 || mods.Targets[0].NodeID != a2.node.ID {
		t.Fatalf("group targets = %+v", mods.Targets)
	}
	var copies []backup.Copy // of its backups, which belong to the server on its node
	api.do("GET", path(moved)+"/copies", nil, http.StatusOK, &copies)
	if len(copies) != 1 || copies[0].NodeID != a2.node.ID || copies[0].NodeName != "node-2" {
		t.Fatalf("copies = %+v", copies)
	}
	var channelNode string // of the channel it takes a plugin in
	check(t, m.db.QueryRow(`SELECT node_id FROM plugin_channels WHERE server_id = ?`, lobby.ServerID).Scan(&channelNode))
	if channelNode != a2.node.ID {
		t.Fatalf("the channel of a plugin stayed on %s", channelNode)
	}

	// The settings and notes move with the server, also the version of a mod loader.
	created := must(noryxv1.NewServerServiceClient(must(m.nodes.Conn(t.Context(), a1.node.ID))).CreateServer(t.Context(), &noryxv1.CreateServerRequest{
		Name: "Modded", Type: noryxv1.ServerType_SERVER_TYPE_FABRIC, MemoryMb: 1024, Port: 25570, AcceptEula: true, LoaderVersion: "0.16.10",
		StopTimeoutSeconds: 600, TimeZone: "Asia/Tokyo",
		HealthStartMinutes: 15, CrashLimit: 3, StableAfterMinutes: 30, UnhealthyRestartMinutes: 5, ProcessLimit: 2048, InitMemoryMb: 512,
	}))
	api.do("PUT", "/api/nodes/"+a1.node.ID+"/servers/"+created.GetServer().GetId()+"/notes", map[string]any{"notes": "Modpack test"}, http.StatusNoContent, nil)
	api.do("POST", "/api/nodes/"+a1.node.ID+"/servers/"+created.GetServer().GetId()+"/move", map[string]any{"node": a2.node.ID}, http.StatusAccepted, nil)
	mv := waitForMove(t, api, created.GetServer().GetId())
	if spec := a2.runtime.spec(created.GetServer().GetId()); mv.Phase != "done" || spec.LoaderVersion != "0.16.10" || spec.StopTimeout != 600 || spec.TimeZone != "Asia/Tokyo" {
		t.Fatalf("move = %+v, spec = %+v", mv, spec)
	}
	if spec := a2.runtime.spec(created.GetServer().GetId()); spec.HealthStart != 15 || spec.CrashLimit != 3 || spec.StableAfter != 30 || spec.UnhealthyRestart != 5 ||
		spec.ProcessLimit != 2048 || spec.InitMemoryMB != 512 {
		t.Fatalf("advanced settings after the move: %+v", spec)
	}
	if notes := must(tag.NewStore(m.db).Notes(t.Context())); notes[tag.Server{NodeID: a2.node.ID, ServerID: created.GetServer().GetId()}] != "Modpack test" {
		t.Fatalf("notes = %q", notes)
	}

	// A Bedrock server keeps how many players may join, whose ports it takes.
	bedrock := must(noryxv1.NewServerServiceClient(must(m.nodes.Conn(t.Context(), a1.node.ID))).CreateServer(t.Context(), &noryxv1.CreateServerRequest{
		Name: "Pocket", Type: noryxv1.ServerType_SERVER_TYPE_BEDROCK, MemoryMb: 1024, Port: 25600, AcceptEula: true, MaxPlayers: 5,
	}))
	api.do("POST", "/api/nodes/"+a1.node.ID+"/servers/"+bedrock.GetServer().GetId()+"/move", map[string]any{"node": a2.node.ID}, http.StatusAccepted, nil)
	if mv, spec := waitForMove(t, api, bedrock.GetServer().GetId()), a2.runtime.spec(bedrock.GetServer().GetId()); mv.Phase != "done" || spec.MaxPlayers != 5 {
		t.Fatalf("move of a Bedrock server = %+v, spec = %+v", mv, spec)
	}

	// If the new node fails, the server stays where it was and runs again.
	a1.runtime.mu.Lock()
	a1.runtime.createErr = errors.New("no space left on device")
	a1.runtime.mu.Unlock()
	api.do("POST", path(moved)+"/move", map[string]any{"node": a1.node.ID}, http.StatusAccepted, nil)
	if mv := waitForMove(t, api, lobby.ServerID); mv.Phase != "failed" || mv.Error == "" {
		t.Fatalf("move = %+v", mv)
	}
	if state(t, a2, lobby.ServerID) != noryxv1.ServerState_SERVER_STATE_RUNNING || slices.ContainsFunc(must(a1.runtime.List(t.Context())), isServer(lobby.ServerID)) {
		t.Fatal("the failed move left the server elsewhere")
	}
}

// While a server moves, the terminal refuses commands that change it, like the API.
func TestTerminalWhileMoving(t *testing.T) {
	m := startMaster(t)
	a1, a2 := m.startAgent(t, "node-1"), m.startAgent(t, "node-2")
	lobby := m.createServer(t, a1, "Lobby", noryxv1.ServerType_SERVER_TYPE_PAPER, 25565)
	panel := m.panel(t)
	api := apiClient{t: t, url: panel.URL}
	hold := make(chan struct{})
	a2.runtime.mu.Lock()
	a2.runtime.hold = hold
	a2.runtime.mu.Unlock()

	api.do("POST", "/api/nodes/"+a1.node.ID+"/servers/"+lobby.ServerID+"/move", map[string]any{"node": a2.node.ID}, http.StatusAccepted, nil)
	for _, command := range []string{"server start %s", "server command %s say hi", "backup create %s", "backup restore %s b1"} {
		if _, err := runTerminal(t, http.DefaultClient, panel.URL, a1.node.ID, fmt.Sprintf(command, lobby.ServerID)); !strings.Contains(err, "moving to another node") {
			t.Errorf("%s while moving: error %q", command, err)
		}
	}
	if _, err := runTerminal(t, http.DefaultClient, panel.URL, a1.node.ID, "backup list "+lobby.ServerID); err != "" {
		t.Errorf("backup list while moving: %s", err)
	}
	close(hold)
	if mv := waitForMove(t, api, lobby.ServerID); mv.Phase != "done" {
		t.Fatalf("move = %+v", mv)
	}
	if _, err := runTerminal(t, http.DefaultClient, panel.URL, a2.node.ID, "server start "+lobby.ServerID); err != "" {
		t.Errorf("start after the move: %s", err)
	}
}

func waitForMove(t *testing.T, api apiClient, serverID string) masterserver.Move {
	t.Helper()
	for range 100 {
		var moves []masterserver.Move
		api.do("GET", "/api/moves", nil, http.StatusOK, &moves)
		if i := slices.IndexFunc(moves, func(mv masterserver.Move) bool { return mv.ServerID == serverID }); i >= 0 && moves[i].FinishedAt != nil {
			return moves[i]
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("the move didn't finish")
	return masterserver.Move{}
}

func state(t *testing.T, a agent, id string) noryxv1.ServerState {
	servers := must(a.runtime.List(t.Context()))
	if i := slices.IndexFunc(servers, isServer(id)); i >= 0 {
		return servers[i].State
	}
	return noryxv1.ServerState_SERVER_STATE_UNSPECIFIED
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

func isServer(id string) func(runtime.Server) bool {
	return func(s runtime.Server) bool { return s.ID == id }
}

// The archive of a moving server comes from another node, which may be compromised, so the
// new node checks it like an uploaded backup: an archive with a link, a path that leaves the
// folder, a zip bomb or an end that announces more entries than a backup may hold creates no
// server and writes nothing.
func TestImportHostileArchive(t *testing.T) {
	m := startMaster(t)
	a := m.startAgent(t, "node-1")
	conn, err := m.nodes.Conn(t.Context(), a.node.ID)
	check(t, err)
	client := noryxv1.NewServerServiceClient(conn)
	link := func(zw *zip.Writer) {
		h := &zip.FileHeader{Name: "world"}
		h.SetMode(fs.ModeSymlink | 0o777)
		w, err := zw.CreateHeader(h)
		check(t, err)
		_, err = io.WriteString(w, "/etc")
		check(t, err)
	}
	escape := func(zw *zip.Writer) {
		_, err := zw.Create("../outside")
		check(t, err)
	}
	bomb := func(zw *zip.Writer) {
		w, err := zw.Create("world/region/r.0.0.mca")
		check(t, err)
		_, err = w.Write(make([]byte, 100<<20)) // unpacks to 100 MB from about 100 KB
		check(t, err)
	}
	for _, c := range []struct {
		name    string
		archive []byte
		want    codes.Code
	}{
		{"a link", zipWith(t, link), codes.InvalidArgument},
		{"a path outside", zipWith(t, escape), codes.InvalidArgument},
		{"a zip bomb", zipWith(t, bomb), codes.InvalidArgument},
		{"a huge directory", announcing(64<<10, 1_000_001), codes.ResourceExhausted},
	} {
		id := runtime.NewID()
		stream, err := client.ImportServer(t.Context())
		check(t, err)
		check(t, stream.Send(&noryxv1.ImportServerRequest{Content: &noryxv1.ImportServerRequest_Header{Header: &noryxv1.Server{
			Id: id, Name: "Moved", Type: noryxv1.ServerType_SERVER_TYPE_PAPER, Version: "1.21.4", MemoryMb: 1024, Port: 25570,
		}}}))
		_ = stream.Send(&noryxv1.ImportServerRequest{Content: &noryxv1.ImportServerRequest_Data{Data: c.archive}}) // CloseAndRecv tells why it failed
		if _, err := stream.CloseAndRecv(); status.Code(err) != c.want {
			t.Errorf("%s: %v", c.name, err)
		}
		if slices.ContainsFunc(must(a.runtime.List(t.Context())), isServer(id)) {
			t.Errorf("%s: the server was created", c.name)
		}
		if _, err := os.Stat(filepath.Join(a.runtime.dir, id)); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("%s: its data remained: %v", c.name, err)
		}
	}
	if _, err := os.Lstat(filepath.Join(a.runtime.dir, "outside")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("wrote outside the server's data: %v", err)
	}
}

// zipWith returns a ZIP archive with the entries that add writes.
func zipWith(t *testing.T, add func(*zip.Writer)) []byte {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	add(zw)
	check(t, zw.Close())
	return buf.Bytes()
}

// announcing returns size bytes that end like a ZIP64 archive with entries entries.
func announcing(size int, entries uint64) []byte {
	le := binary.LittleEndian
	end := le.AppendUint32(nil, 0x06064b50) // the ZIP64 end
	end = le.AppendUint64(end, 44)
	end = append(end, 45, 0, 45, 0, 0, 0, 0, 0, 0, 0, 0, 0)
	end = le.AppendUint64(end, entries) // on this disk
	end = le.AppendUint64(end, entries) // and in all
	end = le.AppendUint64(end, 46)      // the size of the directory
	end = le.AppendUint64(end, uint64(size-56-20-22-46))
	end = le.AppendUint32(end, 0x07064b50) // the locator of the ZIP64 end
	end = le.AppendUint32(end, 0)
	end = le.AppendUint64(end, uint64(size-56-20-22))
	end = le.AppendUint32(end, 1)
	end = le.AppendUint32(end, 0x06054b50) // the end, which leaves the numbers to the ZIP64 end
	end = append(end, 0, 0, 0, 0, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0, 0)
	return append([]byte("PK\x03\x04"), append(make([]byte, size-4-len(end)), end...)...)
}
