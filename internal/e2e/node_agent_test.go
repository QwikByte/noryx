package e2e

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	"github.com/QwikByte/noryx/internal/agent/storage"
)

func (f *fakeRuntime) LimitLogs(fileMB, files uint32) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.logLimit = [2]uint32{fileMB, files}
}

func (f *fakeRuntime) limit() [2]uint32 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.logLimit
}

// The agent of a node gets the limit of the output of containers, the space kept free and the
// interval of measurements as they change, and again whenever it reports others. It checks
// their bounds itself, also against the master.
func TestNodeAgentSettings(t *testing.T) {
	t.Cleanup(func() { storage.KeepFree(storage.MinFree) })
	m := startMaster(t)
	a := m.startAgent(t, "node-1")
	srv := m.createServer(t, a, "Lobby", noryxv1.ServerType_SERVER_TYPE_PAPER, 25565)
	api := apiClient{t: t, url: m.panel(t).URL}
	path := "/api/nodes/" + a.node.ID
	type view struct {
		LogFileMB, LogFiles, KeepFreeGB, StatsIntervalSeconds uint32
		Warning                                               string
	}
	var got view
	api.do("GET", path, nil, http.StatusOK, &got)
	if got != (view{10, 3, 1, 5, ""}) || a.runtime.limit() != [2]uint32{10, 3} {
		t.Fatalf("new node = %+v, its runtime limits the output to %v", got, a.runtime.limit())
	}
	put := func(path string, settings map[string]any, status int) view {
		var v view
		api.do("PUT", path, settings, status, &v)
		return v
	}
	node := map[string]any{"name": "node-1", "address": a.node.Address, "defaultStorage": "default"}
	for _, bad := range []map[string]any{{"keepFreeGb": 0}, {"keepFreeGb": 51}, {"statsIntervalSeconds": 4}, {"logFiles": 0}, {"logFileMb": 101}} {
		for k, v := range node {
			bad[k] = v
		}
		put(path, bad, http.StatusBadRequest)
	}

	// An upload that leaves 10 GiB less than 50 GiB free passes the check of the space now, but not
	// once 50 GiB stay free. As it sends nothing, it then ends before its size and writes nothing.
	conn, err := m.nodes.Conn(t.Context(), a.node.ID)
	check(t, err)
	var st unix.Statfs_t
	check(t, unix.Statfs(a.runtime.dir, &st))
	size := max(int64(st.Bavail)*st.Bsize-40<<30, 0)
	upload := func() error {
		up, err := noryxv1.NewFileServiceClient(conn).WriteFile(t.Context())
		check(t, err)
		check(t, up.Send(&noryxv1.WriteFileRequest{Content: &noryxv1.WriteFileRequest_Header{Header: &noryxv1.WriteFileHeader{
			ServerId: srv.ServerID, Path: "upload.dat", Size: size, Overwrite: true,
		}}}))
		_, err = up.CloseAndRecv()
		return err
	}
	if err := upload(); err != nil && !strings.Contains(err.Error(), "so it wasn't written") {
		t.Fatalf("upload while 1 GiB stays free: %v", err)
	}
	got = put(path, map[string]any{"name": "node-1", "address": a.node.Address, "defaultStorage": "default",
		"logFileMb": 0, "logFiles": 0, "keepFreeGb": 50, "statsIntervalSeconds": 60}, http.StatusOK)
	if got != (view{0, 0, 50, 60, ""}) || a.runtime.limit() != [2]uint32{} {
		t.Fatalf("changed node = %+v, its runtime limits the output to %v", got, a.runtime.limit())
	}
	if err := upload(); status.Code(err) != codes.ResourceExhausted || !strings.Contains(err.Error(), "as 50 GB stays free") {
		t.Fatalf("upload while 50 GiB stay free: %v", err)
	}

	// Requests that leave them out keep them.
	if got = put(path, node, http.StatusOK); got.KeepFreeGB != 50 {
		t.Fatalf("node changed without the settings of its agent = %+v", got)
	}

	// Not even the master gets the agent to keep less than 1 GiB free.
	client := noryxv1.NewNodeServiceClient(conn)
	_, err = client.Configure(t.Context(), &noryxv1.ConfigureRequest{Settings: &noryxv1.NodeSettings{KeepFreeGb: 0, StatsIntervalSeconds: 5}})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("keep nothing free: %v", err)
	}

	// An agent that lost its settings, e.g. with its data, gets them again.
	_, err = client.Configure(t.Context(), &noryxv1.ConfigureRequest{Settings: noryxv1.DefaultNodeSettings()})
	check(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go m.nodes.WatchPresence(ctx, 50*time.Millisecond)
	for i := 0; ; i++ {
		info, err := client.GetInfo(t.Context(), &noryxv1.GetInfoRequest{})
		check(t, err)
		if info.GetSettings().GetKeepFreeGb() == 50 && a.runtime.limit() == [2]uint32{} {
			break
		}
		if i == 100 {
			t.Fatalf("the agent reports %v", info.GetSettings())
		}
		time.Sleep(50 * time.Millisecond)
	}
}
