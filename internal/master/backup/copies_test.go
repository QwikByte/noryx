package backup

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	"github.com/QwikByte/noryx/internal/master/access"
	"github.com/QwikByte/noryx/internal/master/database"
	"github.com/QwikByte/noryx/internal/master/httpapi"
	"github.com/QwikByte/noryx/internal/master/node/nodetest"
	"github.com/QwikByte/noryx/internal/master/operation"
	"github.com/QwikByte/noryx/internal/master/s3/s3test"
	"github.com/QwikByte/noryx/internal/master/schedule"
)

// The copies a job keeps: all that are marked to keep, and of the others those its retention
// keeps, newest first.
func TestKeeps(t *testing.T) {
	copy := func(day int, id string, kept bool) Copy {
		return Copy{BackupID: id, CreatedAt: time.Date(2026, 10, day, 4, 0, 0, 0, time.UTC), Kept: kept}
	}
	copies := []Copy{copy(28, "c", false), copy(31, "a", false), copy(30, "kept", true), copy(31, "b", false), copy(29, "d", false), copy(1, "old", true)}
	for _, tc := range []struct {
		name      string
		retention *noryxv1.BackupRetention
		want      []bool
	}{
		{"all", &noryxv1.BackupRetention{}, []bool{true, true, true, true, true, true}},
		{"the newest", &noryxv1.BackupRetention{Last: 2}, []bool{true, true, true, false, false, true}},
		{"days", &noryxv1.BackupRetention{Days: 2}, []bool{true, false, true, true, false, true}},
		{"the newest one", &noryxv1.BackupRetention{Last: 1}, []bool{true, false, true, false, false, true}},
	} {
		keep := keeps(copies, tc.retention)
		// keeps sorts the copies newest first, those of the same time by their backups' IDs.
		var order []string
		for _, cp := range copies {
			order = append(order, cp.BackupID)
		}
		if !slices.Equal(order, []string{"b", "a", "kept", "d", "c", "old"}) || !slices.Equal(keep, tc.want) {
			t.Errorf("%s: kept %v of %q, want %v", tc.name, keep, order, tc.want)
		}
	}
}

// agent is the agent of a node: its servers, the backups of its server s1, the backups it
// imports and the copies of backups of other nodes that it keeps.
type agent struct {
	noryxv1.UnimplementedBackupServiceServer
	noryxv1.UnimplementedServerServiceServer
	name string

	mu       sync.Mutex
	servers  []*noryxv1.Server
	backups  []*noryxv1.Backup // of s1, the newest first
	broken   map[string]bool   // backups whose download fails after the first chunk
	copies   map[string]*noryxv1.Backup
	imported []*noryxv1.ImportBackupHeader // of backups and copies
	archives map[string][]byte             // of the backups it imported and the copies it keeps, by ID
	restored []string                      // server/backup
	deleted  []string                      // imported backups, as server/backup
}

// do runs fn while nothing else uses the agent.
func (a *agent) do(fn func()) {
	a.mu.Lock()
	defer a.mu.Unlock()
	fn()
}

// archive is the archive of a backup of the node, with the secrets of the server hidden.
func (a *agent) archive(id string) []byte { return []byte(a.name + "'s archive of " + id) }

// backupOf returns a backup of the job t1 made on a day of October 2026 on the node.
func (a *agent) backupOf(id string, day int) *noryxv1.Backup {
	return &noryxv1.Backup{
		Id: id, JobId: "t1", Label: "Nightly", CreatedUnix: time.Date(2026, 10, day, 4, 0, 0, 0, time.UTC).Unix(), Size: int64(len(a.archive(id))),
		Paths: []string{"world"}, Exclude: []string{},
	}
}

func (a *agent) ListServers(context.Context, *noryxv1.ListServersRequest) (*noryxv1.ListServersResponse, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return &noryxv1.ListServersResponse{Servers: a.servers}, nil
}

func (a *agent) ListBackups(_ context.Context, req *noryxv1.ListBackupsRequest) (*noryxv1.ListBackupsResponse, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if req.GetServerId() != "s1" {
		return &noryxv1.ListBackupsResponse{}, nil
	}
	return &noryxv1.ListBackupsResponse{Backups: a.backups}, nil
}

func (a *agent) DownloadBackup(req *noryxv1.DownloadBackupRequest, stream grpc.ServerStreamingServer[noryxv1.DownloadBackupResponse]) error {
	a.mu.Lock()
	found := slices.ContainsFunc(a.backups, func(b *noryxv1.Backup) bool { return b.GetId() == req.GetBackupId() })
	broken := a.broken[req.GetBackupId()]
	a.mu.Unlock()
	switch {
	case !req.GetHideSecrets():
		return status.Error(codes.PermissionDenied, "the secrets of the server would leave the node")
	case !found || req.GetServerId() != "s1":
		return status.Error(codes.NotFound, "backup not found")
	}
	return sendChunks(a.archive(req.GetBackupId()), broken, func(data []byte) error {
		return stream.Send(&noryxv1.DownloadBackupResponse{Data: data})
	})
}

func (a *agent) DownloadCopy(req *noryxv1.DownloadCopyRequest, stream grpc.ServerStreamingServer[noryxv1.DownloadCopyResponse]) error {
	a.mu.Lock()
	data, ok := a.archives[req.GetBackupId()]
	ok = ok && a.copies[req.GetBackupId()] != nil
	a.mu.Unlock()
	if !ok {
		return status.Error(codes.NotFound, "copy not found")
	}
	return sendChunks(data, false, func(data []byte) error { return stream.Send(&noryxv1.DownloadCopyResponse{Data: data}) })
}

// sendChunks sends data in small chunks, or only the first one and then an error if broken.
func sendChunks(data []byte, broken bool, send func([]byte) error) error {
	for chunk := range slices.Chunk(data, 4) {
		if err := send(chunk); err != nil {
			return err
		}
		if broken {
			return status.Error(codes.DataLoss, "the disk failed")
		}
	}
	return nil
}

// receive returns the data of the messages that recv receives until the stream ends.
func receive[T interface{ GetData() []byte }](recv func() (T, error)) ([]byte, error) {
	var data []byte
	for {
		msg, err := recv()
		if errors.Is(err, io.EOF) {
			return data, nil
		}
		if err != nil {
			return nil, err
		}
		data = append(data, msg.GetData()...)
	}
}

// keep keeps what an import received, as untrusted: the copy of a backup, or a backup imported
// into a server.
func (a *agent) keep(h *noryxv1.ImportBackupHeader, data []byte, isCopy bool) (*noryxv1.Backup, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, ok := a.copies[h.GetBackup().GetId()]; ok && isCopy {
		return nil, status.Error(codes.AlreadyExists, "the copy exists")
	}
	b := proto.Clone(h.GetBackup()).(*noryxv1.Backup)
	b.Size, b.Untrusted = int64(len(data)), true
	a.imported, a.archives[b.GetId()] = append(a.imported, h), data
	if isCopy {
		a.copies[b.GetId()] = b
	}
	return b, nil
}

func (a *agent) ImportCopy(stream grpc.ClientStreamingServer[noryxv1.ImportCopyRequest, noryxv1.ImportCopyResponse]) error {
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	data, err := receive(stream.Recv)
	if err != nil {
		return err
	}
	b, err := a.keep(first.GetHeader(), data, true)
	if err != nil {
		return err
	}
	return stream.SendAndClose(&noryxv1.ImportCopyResponse{Backup: b})
}

func (a *agent) ImportBackup(stream grpc.ClientStreamingServer[noryxv1.ImportBackupRequest, noryxv1.ImportBackupResponse]) error {
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	data, err := receive(stream.Recv)
	if err != nil {
		return err
	}
	b, err := a.keep(first.GetHeader(), data, false)
	if err != nil {
		return err
	}
	return stream.SendAndClose(&noryxv1.ImportBackupResponse{Backup: b})
}

func (a *agent) DeleteCopy(_ context.Context, req *noryxv1.DeleteCopyRequest) (*noryxv1.DeleteCopyResponse, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.copies[req.GetBackupId()] == nil {
		return nil, status.Error(codes.NotFound, "copy not found")
	}
	delete(a.copies, req.GetBackupId())
	delete(a.archives, req.GetBackupId())
	return &noryxv1.DeleteCopyResponse{}, nil
}

func (a *agent) RestoreBackup(_ context.Context, req *noryxv1.RestoreBackupRequest) (*noryxv1.RestoreBackupResponse, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	backup := req.GetServerId() + "/" + req.GetBackupId()
	if _, ok := a.archives[req.GetBackupId()]; !ok || slices.Contains(a.deleted, backup) {
		return nil, status.Error(codes.NotFound, "backup not found")
	}
	a.restored = append(a.restored, backup)
	return &noryxv1.RestoreBackupResponse{}, nil
}

func (a *agent) DeleteBackup(_ context.Context, req *noryxv1.DeleteBackupRequest) (*noryxv1.DeleteBackupResponse, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.deleted = append(a.deleted, req.GetServerId()+"/"+req.GetBackupId())
	return &noryxv1.DeleteBackupResponse{}, nil
}

// lastImport returns the last backup that the agent imported, with its archive, the restores so
// far and whether it deleted the backup again.
func (a *agent) lastImport() (h *noryxv1.ImportBackupHeader, data string, restored []string, deleted bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	h = a.imported[len(a.imported)-1]
	id := h.GetBackup().GetId()
	return h, string(a.archives[id]), slices.Clone(a.restored), slices.Contains(a.deleted, h.GetServerId()+"/"+id)
}

// copying is a master with the storage Offsite of a fake bucket and the nodes n1 and n2, whose
// agents keep copies. The job t1 backs up the server s1 of n1, lobby.
type copying struct {
	*Copies
	nodes   *nodetest.Nodes
	bucket  *s3test.Server
	offsite string // the ID of the storage Offsite
	agents  map[string]*agent
	job     schedule.Task
	lobby   schedule.Server
}

func newCopying(t *testing.T) *copying {
	t.Helper()
	db, err := database.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	c := &copying{nodes: nodetest.New(t, db), bucket: s3test.New(t), agents: map[string]*agent{}}
	c.Copies = NewCopies(db, c.nodes, c.bucket.Client().Transport)
	paper, velocity := noryxv1.ServerType_SERVER_TYPE_PAPER, noryxv1.ServerType_SERVER_TYPE_VELOCITY
	for _, id := range []string{"n1", "n2"} {
		a := &agent{
			name: id, broken: map[string]bool{}, copies: map[string]*noryxv1.Backup{}, archives: map[string][]byte{},
			servers: []*noryxv1.Server{{Id: "s1", Name: "Lobby", Type: paper}, {Id: "s2", Name: "Hub", Type: paper}, {Id: "proxy", Name: "Proxy", Type: velocity}},
		}
		c.agents[id] = a
		c.nodes.Add(t, id, func(s *grpc.Server) {
			noryxv1.RegisterBackupServiceServer(s, a)
			noryxv1.RegisterServerServiceServer(s, a)
		})
	}
	st, err := c.saveStorage(t.Context(), "", storageInput{StorageSettings{
		Name: "Offsite", Endpoint: c.bucket.Endpoint, Region: "us-east-1", Bucket: c.bucket.Bucket, Prefix: "noryx", AccessKey: "key", PathStyle: true,
	}, "s3cr3t"})
	if err != nil {
		t.Fatal(err)
	}
	c.offsite = st.ID
	for _, id := range []string{"t1", "t2"} {
		if _, err := db.Exec(`INSERT INTO tasks (id, kind, name, enabled, schedule, settings, created_at) VALUES (?, ?, ?, 1, '{}', '{}', 0)`, id, TaskKind, id); err != nil {
			t.Fatal(err)
		}
	}
	c.job = schedule.Task{ID: "t1", Name: "Nightly", Schedule: schedule.Schedule{TimeZone: "UTC"}}
	c.lobby = schedule.Server{Server: c.agents["n1"].servers[0], NodeID: "n1", NodeName: "n1"}
	return c
}

// sync syncs the copies of a server for the job t1 with the settings and checks what it changed.
func (c *copying) sync(t *testing.T, s JobSettings, srv schedule.Server, change string) {
	t.Helper()
	if got, err := c.Sync(t.Context(), c.job, s, srv); err != nil || got != change {
		t.Fatalf("Sync = %q, %v, want %q", got, err, change)
	}
}

// copied returns the copies by their backups' IDs and nodes, e.g. "b1@n1", sorted. Those in
// the storage must be in the bucket with the archive of their backup, and nothing else.
func (c *copying) copied(t *testing.T) []string {
	t.Helper()
	list, err := c.list(t.Context(), filter{})
	if err != nil {
		t.Fatal(err)
	}
	objects, inStorage := c.bucket.Objects(), 0
	var ids []string
	for _, cp := range list {
		ids = append(ids, cp.BackupID+"@"+cp.NodeID)
		if cp.StorageID == "" {
			continue
		}
		inStorage++
		key := "noryx/" + cp.ServerID + "/" + cp.BackupID + ".zip"
		if want := c.agents[cp.NodeID].archive(cp.BackupID); string(objects[key].Data) != string(want) {
			t.Errorf("%s holds %q, want %q", key, objects[key].Data, want)
		}
	}
	if len(objects) != inStorage {
		t.Errorf("the bucket holds %q for the copies %q", c.bucket.Keys(), ids)
	}
	slices.Sort(ids)
	return ids
}

// Sync copies the backups of the job that have no copy yet, with the secrets of the server
// hidden, and deletes the copies that the job no longer keeps: those of backups that are gone
// from the node, beyond the retention among the copies. Copies marked to keep stay, like their
// backups.
func TestSyncToStorage(t *testing.T) {
	c := newCopying(t)
	n1 := c.agents["n1"]
	byHand := n1.backupOf("by-hand", 3)
	byHand.JobId = ""
	n1.do(func() {
		n1.backups = []*noryxv1.Backup{n1.backupOf("b3", 3), byHand, n1.backupOf("b2", 2), n1.backupOf("b1", 1)}
	})
	s := JobSettings{Keep: 2, Copy: &CopyTo{Storage: c.offsite}}
	c.sync(t, s, c.lobby, "copied 3 to Offsite")
	if got := c.copied(t); !slices.Equal(got, []string{"b1@n1", "b2@n1", "b3@n1"}) {
		t.Fatalf("copies %q", got)
	}
	list, err := c.list(t.Context(), filter{})
	if cp := list[0]; err != nil || cp.BackupID != "b3" || cp.JobID != "t1" || cp.ServerName != "Lobby" || cp.Where != "Offsite" || cp.StorageID != c.offsite ||
		cp.Size != int64(len(n1.archive("b3"))) || cp.Label != "Nightly" || !slices.Equal(cp.Paths, []string{"world"}) || cp.Proxy || cp.Kept {
		t.Fatalf("copy %+v, %v", list[0], err)
	}
	c.sync(t, s, c.lobby, "") // nothing new

	// The node deleted the oldest backup and made a new one.
	n1.do(func() {
		n1.backups = []*noryxv1.Backup{n1.backupOf("b4", 4), n1.backupOf("b3", 3), n1.backupOf("b2", 2)}
	})
	c.sync(t, s, c.lobby, "copied 1 to Offsite, deleted 1 it no longer keeps")
	if got := c.copied(t); !slices.Equal(got, []string{"b2@n1", "b3@n1", "b4@n1"}) {
		t.Fatalf("copies after the node's retention: %q", got)
	}

	// A backup kept on the node keeps its copy, also once the backup is gone.
	n1.do(func() { n1.backups[2].Kept = true })
	c.sync(t, s, c.lobby, "")
	n1.do(func() { n1.backups = []*noryxv1.Backup{n1.backupOf("b5", 5), n1.backupOf("b4", 4)} })
	c.sync(t, s, c.lobby, "copied 1 to Offsite, deleted 1 it no longer keeps")
	if got := c.copied(t); !slices.Equal(got, []string{"b2@n1", "b4@n1", "b5@n1"}) {
		t.Fatalf("copies with a kept one: %q", got)
	}
}

// A backup that can't be copied, as the agent fails while sending it, leaves no part of it
// behind, doesn't hold up the others and is copied in the next run. A copy that can't be
// deleted stays until it can.
func TestSyncFailures(t *testing.T) {
	c := newCopying(t)
	n1 := c.agents["n1"]
	n1.do(func() {
		n1.backups = []*noryxv1.Backup{n1.backupOf("b3", 3), n1.backupOf("b2", 2), n1.backupOf("b1", 1)}
		n1.broken["b2"] = true
	})
	s := JobSettings{Keep: 2, Copy: &CopyTo{Storage: c.offsite}}
	change, err := c.Sync(t.Context(), c.job, s, c.lobby)
	if change != "copied 2 to Offsite" || err == nil || err.Error() != "can't copy the backup of 2026-10-02 04:00:00: the disk failed" {
		t.Fatalf("Sync with a broken backup = %q, %v", change, err)
	}
	if got := c.copied(t); !slices.Equal(got, []string{"b1@n1", "b3@n1"}) || c.bucket.Uploads() != 0 {
		t.Fatalf("copies %q, %d uploads in progress", got, c.bucket.Uploads())
	}
	n1.do(func() { n1.broken = map[string]bool{} })
	c.sync(t, s, c.lobby, "copied 1 to Offsite")

	// The storage refuses to delete.
	n1.do(func() { n1.backups = []*noryxv1.Backup{n1.backupOf("b4", 4), n1.backupOf("b3", 3)} })
	c.bucket.Fail(func(r *http.Request) int {
		if r.Method == http.MethodDelete {
			return http.StatusForbidden
		}
		return 0
	})
	change, err = c.Sync(t.Context(), c.job, s, c.lobby)
	if change != "copied 1 to Offsite" || err == nil || !strings.Contains(err.Error(), "can't delete the copy of 2026-10-02 04:00:00") ||
		!strings.Contains(err.Error(), "can't delete the copy of 2026-10-01 04:00:00") {
		t.Fatalf("Sync with a storage that doesn't delete = %q, %v", change, err)
	}
	if got := c.copied(t); len(got) != 4 {
		t.Fatalf("copies after failed deletes: %q", got)
	}
	c.bucket.Fail(nil)
	c.sync(t, s, c.lobby, "deleted 2 it no longer keeps")
	if got := c.copied(t); !slices.Equal(got, []string{"b3@n1", "b4@n1"}) {
		t.Fatalf("copies %q", got)
	}

	// A server of a node that is gone: nothing changes.
	gone := c.lobby
	gone.NodeID = "n9"
	if change, err := c.Sync(t.Context(), c.job, s, gone); change != "" || err == nil {
		t.Fatalf("Sync on a node that is gone = %q, %v", change, err)
	}
}

// Copies on another node: the master relays each backup to its agent, which keeps it in the
// storage location of the job, replaces a copy that the agent kept but the master didn't
// record, and deletes those the job no longer keeps. The node of the server can't keep its
// copies.
func TestSyncToNode(t *testing.T) {
	c := newCopying(t)
	n1, n2 := c.agents["n1"], c.agents["n2"]
	n1.do(func() { n1.backups = []*noryxv1.Backup{n1.backupOf("b2", 2), n1.backupOf("b1", 1)} })
	n2.do(func() { n2.copies["b1"], n2.archives["b1"] = &noryxv1.Backup{Id: "b1"}, []byte("partial") }) // copied before, but not recorded
	s := JobSettings{Keep: 1, Copy: &CopyTo{Node: "n2", Location: "offsite"}}
	c.sync(t, s, c.lobby, "copied 2 to n2")
	n2.do(func() {
		if len(n2.copies) != 2 || string(n2.archives["b1"]) != string(n1.archive("b1")) || string(n2.archives["b2"]) != string(n1.archive("b2")) {
			t.Fatalf("n2 keeps %q", n2.archives)
		}
		h := n2.imported[0]
		if b := h.GetBackup(); h.GetServerId() != "s1" || b.GetId() != "b2" || b.GetJobId() != "t1" || b.GetLocation() != "offsite" || b.GetLabel() != "Nightly" ||
			b.GetSize() != hidden(int64(len(n1.archive("b2")))) {
			t.Fatalf("header of the copy %v", h)
		}
	})
	list, err := c.list(t.Context(), filter{copyNode: "n2"})
	if err != nil || len(list) != 2 || list[0].Where != "n2" || list[0].Location != "offsite" || list[0].Size != int64(len(n1.archive("b2"))) || list[0].StorageID != "" {
		t.Fatalf("copies on n2 %+v, %v", list, err)
	}

	n1.do(func() { n1.backups = []*noryxv1.Backup{n1.backupOf("b3", 3), n1.backupOf("b2", 2)} })
	c.sync(t, s, c.lobby, "copied 1 to n2, deleted 1 it no longer keeps")
	n2.do(func() {
		if _, ok := n2.copies["b1"]; ok || len(n2.copies) != 2 {
			t.Fatalf("n2 keeps %q", n2.archives)
		}
	})
	// A copy that its node lost already counts as deleted.
	n2.do(func() { delete(n2.copies, "b2") })
	n1.do(func() { n1.backups = n1.backups[:1] })
	c.sync(t, s, c.lobby, "deleted 1 it no longer keeps")
	if got := c.copied(t); !slices.Equal(got, []string{"b3@n1"}) {
		t.Fatalf("copies %q", got)
	}

	s.Copy = &CopyTo{Node: "n1"}
	var skipped schedule.Skipped
	if _, err := c.Sync(t.Context(), c.job, s, c.lobby); !errors.As(err, &skipped) {
		t.Fatalf("Sync to the node of the server = %v", err)
	}
}

// Nodes tell the IDs of their servers themselves. A node that claims the ID of a server of
// another node, and of its backups, neither replaces nor deletes the copies of that server;
// nor does another job of the server.
func TestSyncKeepsCopiesOfOthers(t *testing.T) {
	c := newCopying(t)
	n1, n2 := c.agents["n1"], c.agents["n2"]
	n1.do(func() { n1.backups = []*noryxv1.Backup{n1.backupOf("b2", 2), n1.backupOf("b1", 1)} })
	s := JobSettings{Keep: 1, Copy: &CopyTo{Storage: c.offsite}}
	c.sync(t, s, c.lobby, "copied 2 to Offsite")

	n2.do(func() { n2.backups = []*noryxv1.Backup{n2.backupOf("b9", 9), n2.backupOf("b2", 2)} })
	impostor := schedule.Server{Server: n2.servers[0], NodeID: "n2", NodeName: "n2"}
	change, err := c.Sync(t.Context(), c.job, s, impostor)
	if change != "copied 1 to Offsite" || err == nil || !strings.Contains(err.Error(), "Offsite keeps a copy of another node or job with its ID") {
		t.Fatalf("Sync of the impostor = %q, %v", change, err)
	}
	other := c.job
	other.ID = "t2"
	n1.do(func() { n1.backups[0].JobId = "t2" })
	if change, err := c.Sync(t.Context(), other, s, c.lobby); change != "" || err == nil {
		t.Fatalf("Sync of another job = %q, %v", change, err)
	}
	if got := c.copied(t); !slices.Equal(got, []string{"b1@n1", "b2@n1", "b9@n2"}) {
		t.Fatalf("copies %q", got)
	}
}

// reapplied records the servers whose networks were configured again.
type reapplied struct {
	mu      sync.Mutex
	servers []string
}

func (r *reapplied) Reapply(_ context.Context, serverID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.servers = append(r.servers, serverID)
	return nil
}

// A copy is restored into a server of the same kind on its current node, the one it is of or
// another, like a backup of another server: the master relays it into the backups of the
// server, at most as large as it was, restores it, deletes it again and configures the
// server's network again.
func TestRestoreCopy(t *testing.T) {
	c := newCopying(t)
	n1, n2 := c.agents["n1"], c.agents["n2"]
	n1.do(func() { n1.backups = []*noryxv1.Backup{n1.backupOf("b1", 1)} })
	c.sync(t, JobSettings{Copy: &CopyTo{Storage: c.offsite}}, c.lobby, "copied 1 to Offsite")
	c.sync(t, JobSettings{Copy: &CopyTo{Node: "n2"}}, c.lobby, "copied 1 to n2")
	list, err := c.list(t.Context(), filter{})
	if err != nil || len(list) != 2 {
		t.Fatal(list, err)
	}
	inStorage, onNode := strconv.FormatInt(list[0].ID, 10), strconv.FormatInt(list[1].ID, 10)
	if list[0].StorageID == "" {
		inStorage, onNode = onNode, inStorage
	}
	networks := &reapplied{}
	moving := func(id string) error {
		if id == "s8" {
			return httpapi.Errorf(http.StatusConflict, "The server is moving.")
		}
		return nil
	}
	h := NewHandler(c.nodes, networks, operation.New(time.Minute), moving, c.Copies)
	mux := http.NewServeMux()
	h.Register(access.NewMux(mux))
	serve := func(g access.Grants, method, path, body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequestWithContext(access.WithGrants(t.Context(), g), method, path, strings.NewReader(body)))
		return rec
	}
	archive := string(n1.archive("b1"))

	if rec := serve(access.Admin(), "POST", "/api/backup-copies/"+inStorage+"/restore", `{"node": "n1", "server": "s2"}`); rec.Code != http.StatusOK {
		t.Fatalf("restore into s2: status %d: %s", rec.Code, rec.Body)
	}
	header, data, restored, deleted := n1.lastImport()
	b := header.GetBackup()
	if header.GetServerId() != "s2" || b.GetLabel() != "Nightly" || b.GetSize() != int64(len(archive)) || data != archive ||
		b.GetCreatedUnix() != n1.backupOf("b1", 1).GetCreatedUnix() || !strings.HasPrefix(b.GetId(), "20261001-040000-") || b.GetJobId() != "" ||
		!slices.Equal(restored, []string{"s2/" + b.GetId()}) || !deleted {
		t.Fatalf("imported %v with %q, restored %q, deleted again: %t", header, data, restored, deleted)
	}
	// From the node that keeps it into the server it is of, by the path of the server.
	if rec := serve(access.Admin(), "POST", "/api/nodes/n1/servers/s1/copies/"+onNode+"/restore", ""); rec.Code != http.StatusOK {
		t.Fatalf("restore from n2: status %d: %s", rec.Code, rec.Body)
	}
	if header, data, restored, deleted := n1.lastImport(); header.GetServerId() != "s1" || data != archive || len(restored) != 2 || !deleted {
		t.Fatalf("imported %v with %q, restored %q, deleted again: %t", header, data, restored, deleted)
	}
	networks.mu.Lock()
	if !slices.Equal(networks.servers, []string{"s2", "s1"}) {
		t.Errorf("networks configured again for %q", networks.servers)
	}
	networks.mu.Unlock()

	// The storage can't make the copy larger than it was.
	st, err := c.storage(t.Context(), c.offsite)
	if err != nil {
		t.Fatal(err)
	}
	client, err := st.client(c.transport)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Put(t.Context(), st.key("s1", "b1"), strings.NewReader(archive+" and a plugin"), 0); err != nil {
		t.Fatal(err)
	}
	if rec := serve(access.Admin(), "POST", "/api/backup-copies/"+inStorage+"/restore", `{"node": "n1", "server": "s1"}`); rec.Code != http.StatusOK {
		t.Fatalf("restore of a larger copy: status %d: %s", rec.Code, rec.Body)
	}
	if _, data, _, _ := n1.lastImport(); data != archive {
		t.Fatalf("imported %q", data)
	}

	for _, tc := range []struct {
		name, path, body string
		grants           access.Grants
		status           int
		message          string
	}{
		{"a proxy", "/api/backup-copies/" + inStorage, `{"node": "n1", "server": "proxy"}`, access.Admin(), http.StatusConflict, "Restore backups of proxies into proxies"},
		{"without the permission to restore", "/api/backup-copies/" + inStorage, `{"node": "n1", "server": "s2"}`,
			access.Admin().Only([]access.Permission{access.BackupsView}), http.StatusForbidden, "permission"},
		{"through another server", "/api/nodes/n1/servers/s2/copies/" + inStorage, "", access.Admin(), http.StatusNotFound, "Copy not found."},
		{"through another node", "/api/nodes/n2/servers/s1/copies/" + inStorage, "", access.Admin(), http.StatusNotFound, "Copy not found."},
		{"an unknown copy", "/api/backup-copies/42", `{"node": "n1", "server": "s2"}`, access.Admin(), http.StatusNotFound, "Copy not found."},
		{"no server", "/api/backup-copies/" + inStorage, `{"node": "n1"}`, access.Admin(), http.StatusBadRequest, "Choose a server."},
		{"an unknown server", "/api/backup-copies/" + inStorage, `{"node": "n1", "server": "s9"}`, access.Admin(), http.StatusNotFound, "Server not found."},
		{"a moving server", "/api/backup-copies/" + inStorage, `{"node": "n1", "server": "s8"}`, access.Admin(), http.StatusConflict, "The server is moving."},
	} {
		rec := serve(tc.grants, "POST", tc.path+"/restore", tc.body)
		var res struct{ Error string }
		if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil || rec.Code != tc.status || !strings.Contains(res.Error, tc.message) {
			t.Errorf("%s: status %d: %s", tc.name, rec.Code, rec.Body)
		}
	}
	if err := client.Delete(t.Context(), st.key("s1", "b1")); err != nil {
		t.Fatal(err)
	}
	if rec := serve(access.Admin(), "POST", "/api/backup-copies/"+inStorage+"/restore", `{"node": "n1", "server": "s2"}`); rec.Code != http.StatusNotFound ||
		!strings.Contains(rec.Body.String(), "The copy is no longer in Offsite.") {
		t.Fatalf("restore of a copy gone from the storage: status %d: %s", rec.Code, rec.Body)
	}
	if _, _, restored, _ := n1.lastImport(); len(restored) != 3 {
		t.Fatalf("restored %q", restored)
	}

	// The copies of a server, and deleting them where they are kept.
	var copies []Copy
	if rec := serve(access.Admin(), "GET", "/api/nodes/n1/servers/s1/copies", ""); rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &copies) != nil || len(copies) != 2 {
		t.Fatalf("copies of s1: status %d: %s", rec.Code, rec.Body)
	}
	if rec := serve(access.Admin(), "GET", "/api/nodes/n1/servers/s2/copies", ""); rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Fatalf("copies of s2: status %d: %s", rec.Code, rec.Body)
	}
	if rec := serve(access.Admin(), "DELETE", "/api/nodes/n1/servers/s1/copies/"+onNode, ""); rec.Code != http.StatusNoContent {
		t.Fatalf("delete the copy on n2: status %d: %s", rec.Code, rec.Body)
	}
	n2.do(func() {
		if len(n2.copies) != 0 {
			t.Errorf("n2 keeps %q", n2.archives)
		}
	})
	if rec := serve(access.Admin(), "DELETE", "/api/backup-copies/"+inStorage, ""); rec.Code != http.StatusNoContent {
		t.Fatalf("delete the copy in the storage: status %d: %s", rec.Code, rec.Body)
	}
	if got := c.copied(t); len(got) != 0 {
		t.Fatalf("copies after deleting them: %q", got)
	}
}
