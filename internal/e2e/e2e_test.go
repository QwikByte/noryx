// Package e2e tests master and agent together over real mutually authenticated connections.
package e2e

import (
	"bytes"
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	"github.com/QwikByte/noryx/internal/agent/app"
	"github.com/QwikByte/noryx/internal/agent/datadir"
	"github.com/QwikByte/noryx/internal/agent/enroll"
	agentlogs "github.com/QwikByte/noryx/internal/agent/logs"
	agentnode "github.com/QwikByte/noryx/internal/agent/node"
	agentoverlay "github.com/QwikByte/noryx/internal/agent/overlay"
	"github.com/QwikByte/noryx/internal/agent/progress"
	"github.com/QwikByte/noryx/internal/agent/runtime"
	"github.com/QwikByte/noryx/internal/agent/runtime/runtimetest"
	"github.com/QwikByte/noryx/internal/agent/storage"
	"github.com/QwikByte/noryx/internal/master/access"
	masterapp "github.com/QwikByte/noryx/internal/master/app"
	"github.com/QwikByte/noryx/internal/master/auth"
	"github.com/QwikByte/noryx/internal/master/backup"
	"github.com/QwikByte/noryx/internal/master/database"
	"github.com/QwikByte/noryx/internal/master/datastore"
	"github.com/QwikByte/noryx/internal/master/fileset"
	"github.com/QwikByte/noryx/internal/master/geysermc"
	"github.com/QwikByte/noryx/internal/master/hangar"
	"github.com/QwikByte/noryx/internal/master/logs"
	"github.com/QwikByte/noryx/internal/master/modpack"
	"github.com/QwikByte/noryx/internal/master/modrinth"
	"github.com/QwikByte/noryx/internal/master/mojang"
	"github.com/QwikByte/noryx/internal/master/network"
	"github.com/QwikByte/noryx/internal/master/node"
	"github.com/QwikByte/noryx/internal/master/notify"
	"github.com/QwikByte/noryx/internal/master/operation"
	"github.com/QwikByte/noryx/internal/master/overlay"
	"github.com/QwikByte/noryx/internal/master/player"
	"github.com/QwikByte/noryx/internal/master/plugin"
	"github.com/QwikByte/noryx/internal/master/policy"
	"github.com/QwikByte/noryx/internal/master/preference"
	"github.com/QwikByte/noryx/internal/master/s3/s3test"
	"github.com/QwikByte/noryx/internal/master/schedule"
	"github.com/QwikByte/noryx/internal/master/server"
	"github.com/QwikByte/noryx/internal/master/settings"
	"github.com/QwikByte/noryx/internal/master/tag"
	"github.com/QwikByte/noryx/internal/master/template"
	"github.com/QwikByte/noryx/internal/master/update"
	"github.com/QwikByte/noryx/internal/master/usage"
	"github.com/QwikByte/noryx/internal/master/workflow"
	"github.com/QwikByte/noryx/internal/pki"
)

func TestEnrollAndControlNode(t *testing.T) {
	ctx := t.Context()
	m := startMaster(t)
	a := m.startAgent(t, "node-1")
	if err := enroll.Run(ctx, a.token.String(), t.TempDir()); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("reused join token: got %v, want PermissionDenied", err)
	}

	// The master controls the node.
	conn, err := m.nodes.Conn(ctx, a.node.ID)
	check(t, err)
	info, err := noryxv1.NewNodeServiceClient(conn).GetInfo(ctx, &noryxv1.GetInfoRequest{})
	check(t, err)
	// Older masters read the runtime and its version from one field.
	if name, version := info.RuntimeOf(); info.GetRuntime() != "docker fake" || name != noryxv1.RuntimeDocker || version != "fake" {
		t.Fatalf("runtime = %q, %q %q", info.GetRuntime(), name, version)
	}

	servers := noryxv1.NewServerServiceClient(conn)
	lobby := &noryxv1.CreateServerRequest{Name: "Lobby", Type: noryxv1.ServerType_SERVER_TYPE_PAPER, MemoryMb: 2048, Port: 25565, AcceptEula: true}
	created, err := servers.CreateServer(ctx, lobby)
	check(t, err)
	_, err = servers.StartServer(ctx, &noryxv1.StartServerRequest{Id: created.GetServer().GetId()})
	check(t, err)
	list, err := servers.ListServers(ctx, &noryxv1.ListServersRequest{})
	check(t, err)
	if len(list.GetServers()) != 1 || list.GetServers()[0].GetState() != noryxv1.ServerState_SERVER_STATE_RUNNING {
		t.Fatalf("servers = %v, want one running server", list.GetServers())
	}

	// The console arrives without colour codes, and commands return their output.
	id := created.GetServer().GetId()
	logs, err := servers.StreamLogs(ctx, &noryxv1.StreamLogsRequest{Id: id, Tail: 10})
	check(t, err)
	var lines []string
	for {
		res, err := logs.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		check(t, err)
		lines = append(lines, res.GetLine())
	}
	if want := []string{"[INFO]: Starting", "[INFO]: Done"}; !slices.Equal(lines, want) {
		t.Fatalf("log lines = %q, want %q", lines, want)
	}
	out, err := servers.SendCommand(ctx, &noryxv1.SendCommandRequest{Id: id, Command: "/say hi"})
	check(t, err)
	if out.GetOutput() != "ran say hi" || out.GetFormatted() != "§6ran say hi" {
		t.Fatalf("command output = %q, formatted %q", out.GetOutput(), out.GetFormatted())
	}

	// The master relays the console to the browser as Server-Sent Events, whose IDs let a
	// browser that connects again continue after the last line it got. Colours are kept as
	// Minecraft's codes for panels that ask for them.
	panel := m.panel(t)
	console := "/api/nodes/" + a.node.ID + "/servers/" + id + "/logs"
	for query, want := range map[string]string{
		"":                  "id: 1000000001\ndata: [INFO]: Starting\n\nid: 1000000002\ndata: [INFO]: Done\n\nevent: end\ndata:\n\n",
		"?after=1000000001": "id: 1000000002\ndata: [INFO]: Done\n\nevent: end\ndata:\n\n",
		"?colors=true":      "id: 1000000001\ndata: §2[INFO]: Starting\n\nid: 1000000002\ndata: [INFO]: Done\n\nevent: end\ndata:\n\n",
	} {
		res, err := http.Get(panel.URL + console + query)
		check(t, err)
		body, err := io.ReadAll(res.Body)
		res.Body.Close()
		check(t, err)
		if string(body) != want {
			t.Fatalf("event stream%s = %q, want %q", query, body, want)
		}
	}

	// Earlier output ends before the line that the console shows first.
	var earlier struct {
		Lines []struct{ ID, Text string } `json:"lines"`
	}
	api := apiClient{t: t, url: panel.URL}
	api.do("GET", console+"/earlier?before=1000000002", nil, http.StatusOK, &earlier)
	if len(earlier.Lines) != 1 || earlier.Lines[0].ID != "1000000001" || earlier.Lines[0].Text != "§2[INFO]: Starting" {
		t.Fatalf("earlier output = %+v", earlier.Lines)
	}
	api.do("GET", console+"/earlier?before=1000000001", nil, http.StatusOK, &earlier)
	if len(earlier.Lines) != 0 {
		t.Fatalf("output before the first line = %+v", earlier.Lines)
	}

	// Invalid requests are rejected by the agent.
	for _, tc := range []struct {
		name string
		want codes.Code
		call func() error
	}{
		{"missing EULA", codes.InvalidArgument, func() error {
			_, err := servers.CreateServer(ctx, &noryxv1.CreateServerRequest{Name: "Survival", Type: lobby.Type, MemoryMb: 2048, Port: 25566})
			return err
		}},
		{"port in use", codes.AlreadyExists, func() error {
			_, err := servers.CreateServer(ctx, lobby)
			return err
		}},
		{"command with several lines", codes.InvalidArgument, func() error {
			_, err := servers.SendCommand(ctx, &noryxv1.SendCommandRequest{Id: id, Command: "say hi\nop attacker"})
			return err
		}},
		{"path traversal", codes.InvalidArgument, func() error {
			_, err := servers.DeleteServer(ctx, &noryxv1.DeleteServerRequest{Id: "../../etc"})
			return err
		}},
	} {
		if got := status.Code(tc.call()); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}

	// The master renews the node certificate: the agent stores it and presents it to
	// new connections without a restart.
	enrolled := a.identity.Get().Leaf
	renewed, err := m.nodes.RenewCertificate(ctx, a.node.ID)
	check(t, err)
	_, presented, err := m.nodes.Status(ctx, a.node.ID)
	check(t, err)
	stored, err := pki.LoadKeyPair(a.dir, enroll.NodeCert)
	check(t, err)
	if renewed.Equal(enrolled) || !presented.Equal(renewed) || !stored.Leaf.Equal(renewed) {
		t.Fatal("the renewed certificate is not in use")
	}
	if n, err := m.nodes.Get(ctx, a.node.ID); err != nil || n.CertificateExpiresAt == nil || !n.CertificateExpiresAt.Equal(renewed.NotAfter) {
		t.Fatalf("stored expiry = %v, %v, want %v", n.CertificateExpiresAt, err, renewed.NotAfter)
	}

	// A certificate for a key the agent did not create is rejected.
	nodeClient := noryxv1.NewNodeServiceClient(conn)
	_, err = nodeClient.CreateCSR(ctx, &noryxv1.CreateCSRRequest{})
	check(t, err)
	foreignCSR, err := pki.NewCSR(pki.NewKey())
	check(t, err)
	foreign, err := m.ca.SignNodeCSR(foreignCSR, a.node.ID)
	check(t, err)
	if _, err := nodeClient.InstallCertificate(ctx, &noryxv1.InstallCertificateRequest{CertificateDer: foreign}); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("foreign certificate: got %v, want FailedPrecondition", err)
	}
}

// master is a master with its enrollment endpoint.
type master struct {
	db         *sql.DB
	ca         *pki.CA
	cert       *pki.Holder
	settings   *settings.Service
	nodes      *node.Service
	logs       *logs.Store
	enrollAddr string
	modrinth   *fakeModrinth
	hangar     *fakeHangar
	geysermc   *fakeGeyserMC
	s3         *s3test.Server // S3-compatible storage for copies of backups
	mojang     *fakeMojang
	update     update.Options
	// quick is how long requests wait for their operations; tests get the result right away.
	quick time.Duration
}

func startMaster(t *testing.T) *master {
	dir := t.TempDir()
	db, err := database.Open(filepath.Join(dir, "master.db"))
	check(t, err)
	t.Cleanup(func() { db.Close() })
	ca, err := pki.LoadOrCreateCA(filepath.Join(dir, "pki"))
	check(t, err)
	cert, err := ca.MasterCertificate()
	check(t, err)
	masterCert, err := pki.NewHolder(cert)
	check(t, err)
	ln := listen(t)
	conf, err := settings.Load(t.Context(), db, settings.Master{Version: "test", EnrollAddr: ln.Addr().String()}, masterCert)
	check(t, err)
	nodes := node.NewService(db, ca, masterCert, conf)
	t.Cleanup(nodes.Close)
	logStore := logs.NewStore(db, logs.NewNames(nodes), conf)
	logStore.Start(t.Context())
	t.Cleanup(logStore.Close)
	enrollServer := grpc.NewServer(grpc.Creds(credentials.NewTLS(pki.MasterServerTLS(masterCert))))
	noryxv1.RegisterEnrollmentServiceServer(enrollServer, nodes)
	serve(t, enrollServer, ln)
	return &master{
		db: db, ca: ca, cert: masterCert, settings: conf, nodes: nodes, logs: logStore,
		enrollAddr: ln.Addr().String(), modrinth: startModrinth(t), hangar: startHangar(t), geysermc: startGeyserMC(t), s3: s3test.New(t), mojang: startMojang(t), update: update.Options{DataDir: dir}, quick: time.Minute,
	}
}

// services are those of a master's panel.
func (m *master) services(t *testing.T) masterapp.Services {
	nodes := m.nodes
	modrinthClient := modrinth.New(m.modrinth.URL+"/v2", m.modrinth.URL+"/cdn/")
	geyser := geysermc.New(m.geysermc.URL+"/v2", m.geysermc.URL+"/v2", 0)
	plugins := plugin.NewService(m.db, nodes, modrinthClient, hangar.New(m.hangar.URL+"/api/v1", m.hangar.URL+"/cdn/"), geyser)
	moves := server.NewMoves()
	overlays := overlay.NewService(m.db, nodes)
	datastores := datastore.NewStore(m.db, nodes, overlays)
	networks := network.NewService(m.db, nodes, plugins, overlays, datastores)
	tags := tag.NewStore(m.db)
	accessService := access.NewService(m.db)
	sightings := player.NewSightings(m.db, m.settings)
	usageStore := usage.NewStore(m.db, nodes, m.settings, sightings)
	copies := backup.NewCopies(m.db, nodes, m.s3.Client().Transport)
	jobs := backup.NewJobs(nodes, datastores, copies)
	tasks := schedule.NewService(m.db, nodes, tags, networks, accessService, map[string]schedule.Kind{
		backup.TaskKind: jobs, policy.TaskKind: policy.New(nodes, networks, usageStore, jobs, plugins, m.settings),
	}, moves.Busy)
	check(t, tasks.Start(t.Context()))
	fileSets := fileset.NewService(m.db, nodes, networks, tags, datastores, moves)
	notifications := notify.New(m.db, m.logs, m.settings, notify.Options{})
	workflows := workflow.NewService(m.db, workflow.Deps{
		Nodes: nodes, Targets: tasks, Networks: networks, Players: player.NewService(nodes, networks, geyser), Usage: usageStore,
		Backups: jobs, Plugins: plugins, Notify: notifications, Logs: m.logs, Names: logs.NewNames(nodes), Access: accessService, Config: m.settings,
	})
	check(t, workflows.Start(t.Context()))
	return masterapp.Services{
		Users: auth.NewService(m.db), Access: accessService, Settings: m.settings, Nodes: nodes, Overlay: overlays,
		Networks: networks, Plugins: plugins, GeyserMC: geyser, Mojang: mojang.New(m.mojang.URL, m.mojang.URL, m.mojang.URL+"/texture/"), Modpacks: modpack.NewService(m.db, nodes, modrinthClient), Templates: template.NewService(m.db, plugins), Tasks: tasks, Copies: copies,
		FileSets: fileSets, Datastores: datastore.NewService(datastores, nodes, networks), Logs: m.logs, Updates: update.New(nodes, m.settings, m.update),
		Usage: usageStore, Sightings: sightings, Tags: tags, Preferences: preference.NewStore(m.db), Operations: operation.New(m.quick), Moves: moves,
		Notify: notifications, Workflows: workflows,
	}
}

// panel serves the REST API of the master for an administrator, without signing in.
func (m *master) panel(t *testing.T) *httptest.Server {
	api := masterapp.API(m.services(t))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		api.ServeHTTP(w, r.WithContext(access.WithGrants(r.Context(), access.Admin())))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// agent is an enrolled agent that serves a fake runtime.
type agent struct {
	node     node.Node
	token    node.JoinToken
	dir      string
	identity *agentnode.Identity
	runtime  *fakeRuntime
	kernel   *fakeKernel
	log      *agentlogs.Buffer
}

// startAgent registers a node and enrolls its agent with a join token. Options of its gRPC
// server can change its answers, e.g. to those of an older agent.
func (m *master) startAgent(t *testing.T, name string, opts ...grpc.ServerOption) agent {
	ln := listen(t)
	n, token, err := m.nodes.Create(t.Context(), name, ln.Addr().String())
	check(t, err)
	a := agent{node: n, token: token, dir: t.TempDir(), runtime: &fakeRuntime{Datastores: &runtimetest.Datastores{}, dir: t.TempDir()}, kernel: &fakeKernel{}, log: agentlogs.NewBuffer()}
	check(t, enroll.Run(t.Context(), token.String(), a.dir))
	a.identity, err = agentnode.LoadIdentity(a.dir)
	check(t, err)
	creds := credentials.NewTLS(pki.AgentServerTLS(a.identity.Holder, a.identity.CA))
	opts = append(opts, grpc.Creds(creds))
	serve(t, app.NewGRPCServer(a.runtime, a.identity, storage.New(a.dir), agentoverlay.NewService(a.dir, a.kernel), a.log, opts...), ln)
	return a
}

// createServer creates a server on the agent of a node.
func (m *master) createServer(t *testing.T, a agent, name string, typ noryxv1.ServerType, port uint32) network.Ref {
	conn, err := m.nodes.Conn(t.Context(), a.node.ID)
	check(t, err)
	res, err := noryxv1.NewServerServiceClient(conn).CreateServer(t.Context(), &noryxv1.CreateServerRequest{
		Name: name, Type: typ, MemoryMb: 1024, Port: port, AcceptEula: true,
	})
	check(t, err)
	return network.Ref{NodeID: a.node.ID, ServerID: res.GetServer().GetId()}
}

// browser is a client of a panel served over HTTPS, for its secure session cookie, that
// keeps its own cookies like the browser of a user.
func browser(t *testing.T, srv *httptest.Server) apiClient {
	client := *srv.Client()
	jar, err := cookiejar.New(nil)
	check(t, err)
	client.Jar = jar
	return apiClient{t: t, url: srv.URL, client: &client}
}

type apiClient struct {
	t      *testing.T
	url    string
	client *http.Client // http.DefaultClient if nil
}

// do sends a request with an optional body, JSON unless it is []byte, checks the status and decodes the
// response into out unless it is nil. It returns the response body.
func (c apiClient) do(method, path string, in any, wantStatus int, out any) string {
	c.t.Helper()
	var body io.Reader
	switch in := in.(type) {
	case nil:
	case []byte: // sent as is, e.g. an uploaded file
		body = bytes.NewReader(in)
	default:
		data, err := json.Marshal(in)
		check(c.t, err)
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(c.t.Context(), method, c.url+path, body)
	check(c.t, err)
	res, err := cmp.Or(c.client, http.DefaultClient).Do(req)
	check(c.t, err)
	defer res.Body.Close()
	data, err := io.ReadAll(res.Body)
	check(c.t, err)
	if res.StatusCode != wantStatus {
		c.t.Fatalf("%s %s: status %d, want %d: %s", method, path, res.StatusCode, wantStatus, data)
	}
	if out != nil {
		check(c.t, json.Unmarshal(data, out))
	}
	return string(data)
}

func check(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func listen(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	check(t, err)
	return ln
}

func serve(t *testing.T, s *grpc.Server, ln net.Listener) {
	go func() { _ = s.Serve(ln) }()
	t.Cleanup(s.Stop)
}

// fakeRuntime keeps servers and their network configuration, and datastores, in memory and
// the data of the servers in a directory.
type fakeRuntime struct {
	*runtimetest.Datastores
	dir      string
	mu       sync.Mutex
	servers  []runtime.Server
	networks map[string]runtime.Network
	commands []string
	sent     map[string][]string // the console commands of each server
	// online are the players of servers that their consoles list.
	online map[string][]string
	// on, if set, acts on console commands and on restarts ("restart"), e.g. like a plugin.
	on       func(id, what string)
	restarts []string
	reloads  []string // proxies that reloaded their configuration
	// createErr makes creating servers fail, e.g. on a node that is full.
	createErr error
	// hold, if set, holds up creating servers until it is closed, e.g. to act while a server
	// moves to this node.
	hold chan struct{}
	// down makes the runtime unreachable, like Docker while it isn't running.
	down bool
	// podman makes it tell that it is Podman.
	podman bool
}

var errDown = errors.New("failed to connect to the docker API")

func (f *fakeRuntime) Name() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.podman {
		return noryxv1.RuntimePodman
	}
	return noryxv1.RuntimeDocker
}

func (f *fakeRuntime) Info(context.Context) (runtime.Info, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.down {
		return runtime.Info{}, errDown
	}
	return runtime.Info{Version: "fake", CPUs: 4, MemoryBytes: 16 << 30}, nil
}

func (f *fakeRuntime) List(context.Context) ([]runtime.Server, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.down {
		return nil, errDown
	}
	return append([]runtime.Server(nil), f.servers...), nil
}

func (f *fakeRuntime) Create(ctx context.Context, spec runtime.Spec) error {
	f.mu.Lock()
	hold := f.hold
	f.mu.Unlock()
	// Like Docker, it downloads the image first, which the hold holds up halfway.
	progress.Step(ctx, "image", 0)
	if hold != nil {
		progress.Set(ctx, 50, 100)
		select {
		case <-hold:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	progress.Step(ctx, "container", 0)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.createErr != nil {
		return f.createErr
	}
	f.servers = append(f.servers, runtime.Server{Spec: spec, State: noryxv1.ServerState_SERVER_STATE_STOPPED})
	return os.Mkdir(filepath.Join(f.dir, spec.ID), 0o750)
}

func (f *fakeRuntime) Data(_ context.Context, id string) (*datadir.Dir, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !slices.ContainsFunc(f.servers, func(s runtime.Server) bool { return s.ID == id }) {
		return nil, runtime.ErrNotFound
	}
	return datadir.Open(filepath.Join(f.dir, id))
}

func (f *fakeRuntime) Start(_ context.Context, id string) error {
	return f.setState(id, noryxv1.ServerState_SERVER_STATE_RUNNING)
}

func (f *fakeRuntime) Stop(_ context.Context, id string) error {
	return f.setState(id, noryxv1.ServerState_SERVER_STATE_STOPPED)
}

func (f *fakeRuntime) Update(_ context.Context, spec runtime.Spec) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.servers {
		if f.servers[i].ID == spec.ID {
			f.servers[i].Spec = spec
			return nil
		}
	}
	return runtime.ErrNotFound
}

// UpdateImage finds a newer image for every server.
func (f *fakeRuntime) UpdateImage(_ context.Context, id string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !slices.ContainsFunc(f.servers, func(s runtime.Server) bool { return s.ID == id }) {
		return false, runtime.ErrNotFound
	}
	return true, nil
}

func (f *fakeRuntime) Restart(_ context.Context, id string) error {
	f.mu.Lock()
	f.restarts = append(f.restarts, id)
	if f.on != nil {
		f.on(id, "restart")
	}
	f.mu.Unlock()
	return f.setState(id, noryxv1.ServerState_SERVER_STATE_RUNNING)
}

func (f *fakeRuntime) Remove(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	i := slices.IndexFunc(f.servers, func(s runtime.Server) bool { return s.ID == id })
	if i < 0 {
		return runtime.ErrNotFound
	}
	f.servers = slices.Delete(f.servers, i, i+1)
	return os.RemoveAll(filepath.Join(f.dir, id))
}

func (f *fakeRuntime) Logs(_ context.Context, _ string, _ int, after time.Time) iter.Seq2[runtime.LogLine, error] {
	return func(yield func(runtime.LogLine, error) bool) {
		for i, text := range []string{"\x1b[32m[INFO]: Starting\x1b[m", "[INFO]: Done\r"} {
			line := runtime.LogLine{Time: time.Unix(1, int64(i+1)), Text: text}
			if line.Time.After(after) && !yield(line, nil) {
				return
			}
		}
	}
}

func (f *fakeRuntime) SendCommand(_ context.Context, id, command string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.commands = append(f.commands, command)
	if f.sent == nil {
		f.sent = map[string][]string{}
	}
	f.sent[id] = append(f.sent[id], command)
	if f.on != nil {
		f.on(id, command)
	}
	if names, ok := f.online[id]; ok && command == "minecraft:list" {
		return fmt.Sprintf("There are %d of a max of 20 players online: %s", len(names), strings.Join(names, ", ")), nil
	}
	return "§6ran " + command, nil
}

// commandsTo returns the console commands a server got.
func (f *fakeRuntime) commandsTo(id string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.sent[id])
}

// restarted returns the servers that restarted, in order.
func (f *fakeRuntime) restarted() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.restarts)
}

func (f *fakeRuntime) Usage(_ context.Context, id string) (runtime.Usage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	i := slices.IndexFunc(f.servers, func(s runtime.Server) bool { return s.ID == id })
	if i < 0 || f.servers[i].State == noryxv1.ServerState_SERVER_STATE_STOPPED {
		return runtime.Usage{}, runtime.ErrNotRunning
	}
	return runtime.Usage{MemoryBytes: 512 << 20, MemoryLimit: 1 << 30}, nil
}

func (f *fakeRuntime) consoleCommands() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.commands)
}

func (f *fakeRuntime) Duplicate(ctx context.Context, from string, spec runtime.Spec) error {
	if err := datadir.Copy(ctx, filepath.Join(f.dir, from), filepath.Join(f.dir, spec.ID)); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.servers = append(f.servers, runtime.Server{Spec: spec, State: noryxv1.ServerState_SERVER_STATE_STOPPED})
	return nil
}

func (f *fakeRuntime) spec(id string) runtime.Spec {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, s := range f.servers {
		if s.ID == id {
			return s.Spec
		}
	}
	return runtime.Spec{}
}

// Configure keeps the network of a server and where it publishes its port; a running proxy
// whose Bedrock port changes restarts, like a container created again.
func (f *fakeRuntime) Configure(_ context.Context, id string, network runtime.Network) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.networks == nil {
		f.networks = map[string]runtime.Network{}
	}
	f.networks[id] = network
	i := slices.IndexFunc(f.servers, func(s runtime.Server) bool { return s.ID == id })
	if i >= 0 {
		f.servers[i].Overlay = network.Overlay
	}
	if i < 0 || f.servers[i].BedrockPort == network.BedrockPort && f.servers[i].BedrockPlayers == network.BedrockPlayers {
		return false, nil
	}
	f.servers[i].BedrockPort, f.servers[i].BedrockPlayers = network.BedrockPort, network.BedrockPlayers
	running := f.servers[i].State == noryxv1.ServerState_SERVER_STATE_RUNNING
	if running {
		f.restarts = append(f.restarts, id)
	}
	return running, nil
}

func (f *fakeRuntime) Reload(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reloads = append(f.reloads, id)
	return nil
}

func (f *fakeRuntime) reloaded() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.reloads)
}

func (f *fakeRuntime) network(id string) runtime.Network {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.networks[id]
}

func (f *fakeRuntime) setState(id string, state noryxv1.ServerState) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.servers {
		if f.servers[i].ID == id {
			f.servers[i].State = state
			return nil
		}
	}
	return runtime.ErrNotFound
}
