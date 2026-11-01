package network

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	"github.com/QwikByte/noryx/internal/master/database"
	"github.com/QwikByte/noryx/internal/master/httpapi"
	"github.com/QwikByte/noryx/internal/master/node/nodetest"
	"github.com/QwikByte/noryx/internal/master/overlay"
	"github.com/QwikByte/noryx/internal/master/plugin"
)

// nodeAgent is the agent of a node with servers, which writes what it is asked to do into the
// journal of its master.
type nodeAgent struct {
	noryxv1.UnimplementedServerServiceServer
	noryxv1.UnimplementedNodeServiceServer
	node    string
	journal func(format string, args ...any)

	mu      sync.Mutex
	servers map[string]*noryxv1.Server
	// requests are the last configurations of the servers.
	requests map[string]*noryxv1.ConfigureNetworkRequest
	// refuse makes configuring servers fail, and down makes all calls fail.
	refuse map[string]error
	down   bool
}

func (a *nodeAgent) answers() error {
	if a.down {
		return status.Error(codes.Unavailable, "connection refused")
	}
	return nil
}

func (a *nodeAgent) GetInfo(context.Context, *noryxv1.GetInfoRequest) (*noryxv1.GetInfoResponse, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return &noryxv1.GetInfoResponse{}, a.answers()
}

func (a *nodeAgent) ListServers(context.Context, *noryxv1.ListServersRequest) (*noryxv1.ListServersResponse, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	res := &noryxv1.ListServersResponse{}
	for _, srv := range a.servers {
		res.Servers = append(res.Servers, proto.Clone(srv).(*noryxv1.Server))
	}
	return res, a.answers()
}

func (a *nodeAgent) ConfigureNetwork(_ context.Context, req *noryxv1.ConfigureNetworkRequest) (*noryxv1.ConfigureNetworkResponse, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	srv := a.servers[req.GetId()]
	switch {
	case a.down:
		return nil, a.answers()
	case srv == nil:
		return nil, status.Error(codes.NotFound, "server not found")
	case a.refuse[req.GetId()] != nil:
		return nil, a.refuse[req.GetId()]
	}
	a.journal("configure %s/%s %s", a.node, req.GetId(), strings.ToLower(strings.TrimPrefix(req.GetForwarding().String(), "FORWARDING_")))
	a.requests[req.GetId()] = req
	srv.BedrockPort = req.GetBedrockPort()
	return &noryxv1.ConfigureNetworkResponse{}, nil
}

func (a *nodeAgent) RestartServer(_ context.Context, req *noryxv1.RestartServerRequest) (*noryxv1.RestartServerResponse, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.journal("restart %s/%s", a.node, req.GetId())
	return &noryxv1.RestartServerResponse{}, a.answers()
}

// request returns the last configuration of a server.
func (a *nodeAgent) request(id string) *noryxv1.ConfigureNetworkRequest {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.requests[id]
}

// set changes the agent while nothing else uses it.
func (a *nodeAgent) set(fn func(a *nodeAgent)) {
	a.mu.Lock()
	defer a.mu.Unlock()
	fn(a)
}

// applying is a master whose nodes n1 and n2 run agents, which, like the plugins and mods of
// the networks and the publishing of their datastores, write what they do into one journal.
// The master is its own overlay, of members, and its own datastores, of placed.
type applying struct {
	*Service
	agents map[string]*nodeAgent

	mu      sync.Mutex
	log     []string
	members members
	placed  map[string][]string
	fail    map[string]error // makes ensure, provide, uninstall or publish fail
	changed bool             // Provide changes a file
}

var (
	survival = Ref{"n2", "survival"}
	bedrock  = Ref{"n1", "bedrock"}
	proxy2   = Ref{"n2", "proxy2"}
)

func newApplying(t *testing.T) *applying {
	t.Helper()
	db, err := database.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	nodes := nodetest.New(t, db)
	f := &applying{agents: map[string]*nodeAgent{}, fail: map[string]error{}}
	servers := map[Ref]*noryxv1.Server{
		proxy:    {Name: "Proxy", Type: noryxv1.ServerType_SERVER_TYPE_VELOCITY, Port: 25577},
		lobby:    {Name: "Lobby", Type: noryxv1.ServerType_SERVER_TYPE_PAPER, Port: 25565},
		bedrock:  {Name: "Bedrock", Type: noryxv1.ServerType_SERVER_TYPE_BEDROCK, Port: 19132, MaxPlayers: 2}, // and 19133
		hub:      {Name: "Hub", Type: noryxv1.ServerType_SERVER_TYPE_PAPER, Port: 25570},
		survival: {Name: "Survival", Type: noryxv1.ServerType_SERVER_TYPE_FABRIC, Port: 25566},
		proxy2:   {Name: "Proxy 2", Type: noryxv1.ServerType_SERVER_TYPE_VELOCITY, Port: 25578},
	}
	for _, id := range []string{"n1", "n2"} {
		a := &nodeAgent{node: id, journal: f.journal, servers: map[string]*noryxv1.Server{}, requests: map[string]*noryxv1.ConfigureNetworkRequest{}, refuse: map[string]error{}}
		for ref, srv := range servers {
			if ref.NodeID == id {
				srv.Id, srv.State = ref.ServerID, running
				a.servers[ref.ServerID] = srv
			}
		}
		f.agents[id] = a
		nodes.Add(t, id, func(s *grpc.Server) {
			noryxv1.RegisterServerServiceServer(s, a)
			noryxv1.RegisterNodeServiceServer(s, a)
		})
	}
	f.Service = NewService(db, nodes, f, f, f)
	return f
}

func (f *applying) journal(format string, args ...any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.log = append(f.log, fmt.Sprintf(format, args...))
}

// done returns what was done since it was called last.
func (f *applying) done() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	log := f.log
	f.log = nil
	return log
}

func (f *applying) set(fn func(f *applying)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(f)
}

// failure journals what was done and returns its error, if it fails.
func (f *applying) failure(what string, ref plugin.Ref, args ...string) error {
	f.journal("%s %s/%s %s", what, ref.NodeID, ref.ServerID, strings.Join(args, " "))
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.fail[what]
}

func (f *applying) Ensure(_ context.Context, ref plugin.Ref, project string) error {
	return f.failure("ensure", ref, project)
}

func (f *applying) Provide(_ context.Context, ref plugin.Ref, projects []string) (bool, error) {
	err := f.failure("provide", ref, projects...)
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.changed, err
}

func (f *applying) Uninstall(_ context.Context, ref plugin.Ref, project string) error {
	return f.failure("uninstall", ref, project)
}

func (f *applying) ByNode(context.Context) (map[string]overlay.Member, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.members, nil
}

func (f *applying) Placed(context.Context, string) (map[string][]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.placed, nil
}

func (f *applying) Publish(_ context.Context, _ string, nodes []string, m map[string]overlay.Member) error {
	f.journal("publish %s for %d members", strings.Join(nodes, " "), len(m))
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.fail["publish"]
}

// create creates the network Main of the proxy with lobby and the Fabric server survival.
func (f *applying) create(t *testing.T) Network {
	t.Helper()
	n, err := f.Create(t.Context(), Draft{Name: "Main", Proxy: proxy, Servers: []Ref{lobby, survival}})
	if err != nil {
		t.Fatal(err)
	}
	f.done()
	return n
}

// configured is the journal of configuring the network of create in full.
var configured = []string{"configure n1/lobby modern", "ensure n2/survival 8dI2tmqs", "configure n2/survival modern", "configure n1/proxy modern"}

// check checks the journal, and that the network is saved with backends and the error of
// applying it.
func (f *applying) check(t *testing.T, id string, journal []string, backends []string, applyError string) {
	t.Helper()
	if got := f.done(); !slices.Equal(got, journal) {
		t.Errorf("journal %q, want %q", got, journal)
	}
	n, err := f.Get(t.Context(), id)
	var names []string
	for _, b := range n.Backends {
		names = append(names, b.Name+"@"+b.NodeID)
	}
	if err != nil || !slices.Equal(names, backends) || n.ApplyError != applyError {
		t.Errorf("network with %q, apply error %q, %v; want %q, %q", names, n.ApplyError, err, backends, applyError)
	}
}

// statusOf is the HTTP status of an error of the API, or 0.
func statusOf(err error) int {
	var apiErr *httpapi.Error
	if errors.As(err, &apiErr) {
		return apiErr.Status
	}
	return 0
}

// unreachable is the explanation of a node whose agent doesn't answer.
func unreachable(nodeID string) string {
	return nodeID + " can't be reached. Check that its agent is running and that the master can connect to it."
}

// A new network configures its game servers first, after the forwarding mods they need, so
// that they accept the proxy before it sends players to them, and then the proxy, which
// reaches the servers of its node by their IDs and the others at the host of their node's
// agent. All of them share the forwarding secret of the network.
func TestCreate(t *testing.T) {
	f := newApplying(t)
	n, err := f.Create(t.Context(), Draft{Name: "Main", Proxy: proxy, Servers: []Ref{lobby, survival}})
	if err != nil {
		t.Fatal(err)
	}
	f.check(t, n.ID, configured, []string{"lobby@n1", "survival@n2"}, "")
	l, s, p := f.agents["n1"].request("lobby"), f.agents["n2"].request("survival"), f.agents["n1"].request("proxy")
	if secret := p.GetForwardingSecret(); len(secret) < 26 || l.GetForwardingSecret() != secret || s.GetForwardingSecret() != secret || secret != n.secret {
		t.Errorf("forwarding secrets %q, %q and %q", l.GetForwardingSecret(), s.GetForwardingSecret(), secret)
	}
	if !l.GetProxyOnNode() || s.GetProxyOnNode() || l.GetBedrockPlayers() || len(l.GetBackends()) != 0 || s.GetOverlayClient() != "" {
		t.Errorf("configuration of the servers %v, %v", l, s)
	}
	n2, err := f.nodes.Get(t.Context(), "n2")
	host, _, _ := net.SplitHostPort(n2.Address)
	if b := p.GetBackends(); err != nil || len(b) != 2 || b[0].GetName() != "lobby" || b[0].GetServerId() != "lobby" || b[1].GetName() != "survival" ||
		b[1].GetAddress() != net.JoinHostPort(host, "25566") || !slices.Equal(p.GetTry(), []string{"lobby"}) {
		t.Errorf("configuration of the proxy %v", p)
	}

	for _, tc := range []struct {
		name   string
		draft  Draft
		status int
		want   string
	}{
		{"a taken name", Draft{Name: "MAIN", Proxy: proxy2, Servers: []Ref{hub}}, http.StatusConflict, `A network named "MAIN" already exists.`},
		{"a taken proxy", Draft{Name: "Other", Proxy: proxy, Servers: []Ref{hub}}, http.StatusConflict, "This proxy already serves a network."},
		{"a taken server", Draft{Name: "Other", Proxy: proxy2, Servers: []Ref{hub, lobby}}, http.StatusConflict, "A server already belongs to another network."},
		{"a game server as proxy", Draft{Name: "Other", Proxy: hub, Servers: []Ref{proxy2}}, http.StatusBadRequest, "Choose a Velocity or BungeeCord proxy."},
		{"a proxy as server", Draft{Name: "Other", Proxy: proxy2, Servers: []Ref{proxy}}, http.StatusBadRequest, "Only Paper and its forks"},
		{"a Bedrock server", Draft{Name: "Other", Proxy: proxy2, Servers: []Ref{bedrock}}, http.StatusBadRequest, "Bedrock servers can't be part of a network."},
		{"Fabric with legacy forwarding", Draft{Name: "Other", Proxy: proxy2, Forwarding: Legacy, Servers: []Ref{survival}}, http.StatusBadRequest,
			"Fabric and Quilt servers only support Velocity's modern forwarding."},
		{"exposed servers", Draft{Name: "Other", Proxy: proxy2, Forwarding: Legacy, Servers: []Ref{lobby}}, http.StatusConflict, "With legacy forwarding"},
		{"an unknown server", Draft{Name: "Other", Proxy: proxy2, Servers: []Ref{{"n2", "nope"}}}, http.StatusNotFound, "Server not found."},
		{"no servers", Draft{Name: "Other", Proxy: proxy2}, http.StatusBadRequest, "A network needs at least one server."},
	} {
		if _, err := f.Create(t.Context(), tc.draft); statusOf(err) != tc.status || !strings.Contains(httpapi.Message(err), tc.want) {
			t.Errorf("%s: %v", tc.name, err)
		}
	}
	if networks, err := f.List(t.Context()); err != nil || len(networks) != 1 {
		t.Errorf("networks %+v, %v", networks, err)
	}
	if got := f.done(); len(got) != 0 {
		t.Errorf("refused networks did %q", got)
	}
}

// An agent that fails while a network is applied leaves the rest as it was: the change is
// saved, the proxy keeps its configuration, and the network tells why until it is applied
// again. A node that doesn't answer is named.
func TestApplyFails(t *testing.T) {
	f := newApplying(t)
	f.agents["n2"].set(func(a *nodeAgent) { a.refuse["survival"] = status.Error(codes.Internal, "the runtime failed") })
	n, err := f.Create(t.Context(), Draft{Name: "Main", Proxy: proxy, Servers: []Ref{lobby, survival}})
	failed := func(what, why string) string {
		return "The change was saved, but " + what + " could not be configured: " + why + " Apply the network again once all nodes are online."
	}
	want := failed("survival", "the runtime failed.")
	if statusOf(err) != http.StatusBadGateway || httpapi.Message(err) != want {
		t.Fatalf("Create = %v", err)
	}
	f.check(t, n.ID, []string{"configure n1/lobby modern", "ensure n2/survival 8dI2tmqs"}, []string{"lobby@n1", "survival@n2"}, want)

	f.agents["n2"].set(func(a *nodeAgent) { a.refuse, a.down = map[string]error{}, true })
	if _, err := f.Apply(t.Context(), n.ID); httpapi.Message(err) != failed("survival", unreachable("n2")) {
		t.Fatalf("Apply with n2 offline = %v", err)
	}
	f.check(t, n.ID, []string{"configure n1/lobby modern"}, []string{"lobby@n1", "survival@n2"}, failed("survival", unreachable("n2")))

	f.agents["n2"].set(func(a *nodeAgent) { a.down = false })
	f.set(func(f *applying) {
		f.fail["ensure"] = httpapi.Errorf(http.StatusBadGateway, "Modrinth can't be reached.")
	})
	want = failed("survival", "its forwarding mod could not be installed: Modrinth can't be reached.")
	if _, err := f.Apply(t.Context(), n.ID); httpapi.Message(err) != want {
		t.Fatalf("Apply without the forwarding mod = %v", err)
	}
	f.check(t, n.ID, []string{"configure n1/lobby modern", "ensure n2/survival 8dI2tmqs"}, []string{"lobby@n1", "survival@n2"}, want)

	f.set(func(f *applying) { f.fail = map[string]error{} })
	f.agents["n1"].set(func(a *nodeAgent) {
		a.refuse["proxy"] = status.Error(codes.FailedPrecondition, "The proxy's configuration is broken.")
	})
	want = failed("the proxy", "The proxy's configuration is broken.")
	if _, err := f.Apply(t.Context(), n.ID); httpapi.Message(err) != want {
		t.Fatalf("Apply with a broken proxy = %v", err)
	}
	f.check(t, n.ID, configured[:3], []string{"lobby@n1", "survival@n2"}, want)

	f.agents["n1"].set(func(a *nodeAgent) { a.refuse = map[string]error{} })
	if _, err := f.Apply(t.Context(), n.ID); err != nil {
		t.Fatal(err)
	}
	f.check(t, n.ID, configured, []string{"lobby@n1", "survival@n2"}, "")
}

// A change makes the servers that leave standalone first, without their forwarding mods, and
// then configures the others and the proxy. If one can't leave, nothing changes. Servers that
// join, or all if the forwarding changes, must support the forwarding; with legacy forwarding,
// the proxy reaches the servers of other nodes over the private network of the nodes.
func TestUpdate(t *testing.T) {
	f := newApplying(t)
	n := f.create(t)
	change := Change{Name: "Main", Forwarding: Modern, Backends: []Backend{{Ref: lobby, Name: "lobby"}, {Ref: hub, Name: "hub"}}, Try: []string{"hub", "lobby"}}
	f.agents["n2"].set(func(a *nodeAgent) { a.refuse["survival"] = status.Error(codes.ResourceExhausted, "The disk is full.") })
	if _, err := f.Update(t.Context(), n.ID, change); httpapi.Message(err) != "survival could not be made standalone again: The disk is full." {
		t.Fatalf("Update while survival can't leave = %v", err)
	}
	f.check(t, n.ID, []string{"uninstall n2/survival 8dI2tmqs"}, []string{"lobby@n1", "survival@n2"}, "")

	f.agents["n2"].set(func(a *nodeAgent) { a.refuse = map[string]error{} })
	if _, err := f.Update(t.Context(), n.ID, change); err != nil {
		t.Fatal(err)
	}
	f.check(t, n.ID, []string{"uninstall n2/survival 8dI2tmqs", "configure n2/survival none", "configure n1/lobby modern", "configure n2/hub modern", "configure n1/proxy modern"},
		[]string{"lobby@n1", "hub@n2"}, "")
	if p := f.agents["n1"].request("proxy"); !slices.Equal(p.GetTry(), []string{"hub", "lobby"}) {
		t.Errorf("proxy tries %q", p.GetTry())
	}

	refused := change
	refused.Backends = append(slices.Clone(change.Backends), Backend{Ref: survival, Name: "survival"})
	refused.Forwarding, refused.Firewalled = Legacy, true
	if _, err := f.Update(t.Context(), n.ID, refused); !errors.Is(err, errFabricMode) || !strings.HasPrefix(err.Error(), "survival: ") {
		t.Errorf("Update to legacy forwarding with a Fabric server = %v", err)
	}
	change.Forwarding = Legacy
	if _, err := f.Update(t.Context(), n.ID, change); !errors.Is(err, errExposed) {
		t.Errorf("Update to exposed legacy forwarding = %v", err)
	}
	if got := f.done(); len(got) != 0 {
		t.Errorf("refused changes did %q", got)
	}

	f.set(func(f *applying) {
		f.members = members{"n1": {NodeID: "n1", Address: "10.213.0.1", PublicKey: "key-1"}, "n2": {NodeID: "n2", Address: "10.213.0.2", PublicKey: "key-2"}}
	})
	if _, err := f.Update(t.Context(), n.ID, change); err != nil {
		t.Fatal(err)
	}
	f.check(t, n.ID, []string{"configure n1/lobby legacy", "configure n2/hub legacy", "configure n1/proxy legacy"}, []string{"lobby@n1", "hub@n2"}, "")
	h, p := f.agents["n2"].request("hub"), f.agents["n1"].request("proxy")
	if h.GetForwardingSecret() != "" || h.GetOverlayClient() != "10.213.0.1" || h.GetOverlayClientKey() != "key-1" || p.GetBackends()[1].GetAddress() != "10.213.0.2:25570" {
		t.Errorf("configuration over the private network: hub %v, proxy %v", h, p)
	}
}

// Bedrock players join through Geyser and Floodgate on the proxy, at a port that no server of
// the proxy's node uses. The proxy gets the newest of both whenever the network is applied, and
// restarts if they changed; it loses them once the port is gone. The agent of the proxy's node
// must know Bedrock ports.
func TestBedrock(t *testing.T) {
	f := newApplying(t)
	n := f.create(t)
	change := Change{Name: "Main", Forwarding: Modern, Backends: n.Backends, Try: n.Try, BedrockPort: 19133}
	if _, err := f.Update(t.Context(), n.ID, change); statusOf(err) != http.StatusConflict || httpapi.Message(err) != `Port 19133 is already used by "Bedrock" on n1. Choose another Bedrock port.` {
		t.Fatalf("Update to a used port = %v", err)
	}
	change.BedrockPort = 19134
	f.set(func(f *applying) { f.changed = true })
	if _, err := f.Update(t.Context(), n.ID, change); err != nil {
		t.Fatal(err)
	}
	provided := "provide n1/proxy " + strings.Join(bedrockPlugins, " ")
	f.check(t, n.ID, slices.Concat(configured[:3], []string{provided, "configure n1/proxy modern", "restart n1/proxy"}), []string{"lobby@n1", "survival@n2"}, "")
	if l, p := f.agents["n1"].request("lobby"), f.agents["n1"].request("proxy"); !l.GetBedrockPlayers() || p.GetBedrockPlayers() || p.GetBedrockPort() != 19134 {
		t.Errorf("configuration with Bedrock players: lobby %v, proxy %v", l, p)
	}
	f.set(func(f *applying) { f.changed = false })
	if _, err := f.Apply(t.Context(), n.ID); err != nil {
		t.Fatal(err)
	}
	f.check(t, n.ID, slices.Concat(configured[:3], []string{provided, "configure n1/proxy modern"}), []string{"lobby@n1", "survival@n2"}, "")

	change.BedrockPort = 0
	if _, err := f.Update(t.Context(), n.ID, change); err != nil {
		t.Fatal(err)
	}
	f.check(t, n.ID, slices.Concat([]string{"uninstall n1/proxy " + bedrockPlugins[0], "uninstall n1/proxy " + bedrockPlugins[1]}, configured), []string{"lobby@n1", "survival@n2"}, "")
	if l := f.agents["n1"].request("lobby"); l.GetBedrockPlayers() {
		t.Errorf("lobby still lets Bedrock players in: %v", l)
	}
}

// Deleting a network makes its servers standalone again and takes the proxy out of it. A
// network with datastores stays, and so does one whose server of another node can't leave. If
// the proxy's node doesn't answer, it and its servers are skipped, which the warning tells.
func TestDelete(t *testing.T) {
	f := newApplying(t)
	n := f.create(t)
	f.set(func(f *applying) { f.placed = map[string][]string{"n2": {"d1"}} })
	if _, err := f.Delete(t.Context(), n.ID); statusOf(err) != http.StatusConflict || !strings.Contains(httpapi.Message(err), "Delete its datastores first.") {
		t.Fatalf("Delete with datastores = %v", err)
	}
	f.set(func(f *applying) { f.placed = nil })
	f.agents["n2"].set(func(a *nodeAgent) { a.refuse["survival"] = status.Error(codes.ResourceExhausted, "The disk is full.") })
	if _, err := f.Delete(t.Context(), n.ID); statusOf(err) != http.StatusBadGateway ||
		httpapi.Message(err) != "The network was not deleted, because survival could not be made standalone again: The disk is full." {
		t.Fatalf("Delete while survival can't leave = %v", err)
	}
	f.check(t, n.ID, []string{"configure n1/lobby none", "uninstall n2/survival 8dI2tmqs"}, []string{"lobby@n1", "survival@n2"}, "")

	f.agents["n2"].set(func(a *nodeAgent) { a.refuse = map[string]error{} })
	f.agents["n1"].set(func(a *nodeAgent) { a.down = true })
	warning, err := f.Delete(t.Context(), n.ID)
	if err != nil || !strings.HasPrefix(warning, "n1 can't be reached, so the proxy and the network's servers on it keep their settings") {
		t.Fatalf("Delete with the proxy's node offline = %q, %v", warning, err)
	}
	if got := f.done(); !slices.Equal(got, []string{"uninstall n2/survival 8dI2tmqs", "configure n2/survival none"}) {
		t.Errorf("journal %q", got)
	}
	if _, err := f.Get(t.Context(), n.ID); !errors.Is(err, errNotFound) {
		t.Fatalf("deleted network: %v", err)
	}

	f.agents["n1"].set(func(a *nodeAgent) { a.down = false })
	n = f.create(t)
	if warning, err := f.Delete(t.Context(), n.ID); warning != "" || err != nil {
		t.Fatalf("Delete = %q, %v", warning, err)
	}
	if got := f.done(); !slices.Equal(got, []string{"configure n1/lobby none", "uninstall n2/survival 8dI2tmqs", "configure n2/survival none", "configure n1/proxy none"}) {
		t.Errorf("journal %q", got)
	}
}

// The servers of a network reach its datastores on their own node by name, and those of other
// nodes in the private network, where the master publishes the datastores for the nodes of the
// network once its servers are configured.
func TestApplyDatastores(t *testing.T) {
	f := newApplying(t)
	n := f.create(t)
	f.set(func(f *applying) {
		f.placed = map[string][]string{"n2": {"d1", "d2"}}
		f.members = members{"n1": {NodeID: "n1", Address: "10.213.0.1", PublicKey: "key-1"}, "n2": {NodeID: "n2", Address: "10.213.0.2", PublicKey: "key-2"}}
	})
	if err := f.Configure(t.Context(), n.ID); err != nil {
		t.Fatal(err)
	}
	f.check(t, n.ID, append(slices.Clone(configured), "publish n1 n1 n2 for 2 members"), []string{"lobby@n1", "survival@n2"}, "")
	if l, s := f.agents["n1"].request("lobby"), f.agents["n2"].request("survival"); len(l.GetDatastores()) != 0 || !slices.Equal(s.GetDatastores(), []string{"d1", "d2"}) {
		t.Errorf("datastores of lobby %q, of survival %q", l.GetDatastores(), s.GetDatastores())
	}
	f.set(func(f *applying) {
		f.fail["publish"] = httpapi.Errorf(http.StatusConflict, "n2 has no free port for the datastore main.")
	})
	want := "The change was saved, but the datastores could not be configured: n2 has no free port for the datastore main. Apply the network again once all nodes are online."
	if err := f.Configure(t.Context(), n.ID); httpapi.Message(err) != want {
		t.Fatalf("Configure = %v", err)
	}
	f.check(t, n.ID, append(slices.Clone(configured), "publish n1 n1 n2 for 2 members"), []string{"lobby@n1", "survival@n2"}, want)
}

// A server that moves to another node takes its place in its network along, which is applied
// again; so is the network of a server whose port changed, and those that reach servers of a
// node from another node, e.g. as its address changed. Servers of a network can't be removed.
func TestReapply(t *testing.T) {
	f := newApplying(t)
	n := f.create(t)
	if err := f.ReapplyNode(t.Context(), "n2"); err != nil {
		t.Fatal(err)
	}
	f.check(t, n.ID, configured, []string{"lobby@n1", "survival@n2"}, "")
	if err := f.ReapplyNode(t.Context(), "n1"); err != nil { // the proxy's node
		t.Fatal(err)
	}
	if err := f.Reapply(t.Context(), "hub"); err != nil { // of no network
		t.Fatal(err)
	}
	f.check(t, n.ID, nil, []string{"lobby@n1", "survival@n2"}, "")
	if err := f.Reapply(t.Context(), "lobby"); err != nil {
		t.Fatal(err)
	}
	f.check(t, n.ID, configured, []string{"lobby@n1", "survival@n2"}, "")

	f.agents["n1"].set(func(a *nodeAgent) {
		a.servers["survival"] = &noryxv1.Server{Id: "survival", Name: "Survival", Type: noryxv1.ServerType_SERVER_TYPE_FABRIC, Port: 25566, State: running}
	})
	if err := f.CheckMove(t.Context(), "survival", "n2", "n1", false); err != nil {
		t.Fatal(err)
	}
	if err := f.Move(t.Context(), "survival", "n2", "n1"); err != nil {
		t.Fatal(err)
	}
	f.check(t, n.ID, []string{"configure n1/lobby modern", "ensure n1/survival 8dI2tmqs", "configure n1/survival modern", "configure n1/proxy modern"},
		[]string{"lobby@n1", "survival@n1"}, "")
	if b := f.agents["n1"].request("proxy").GetBackends()[1]; b.GetServerId() != "survival" {
		t.Errorf("the proxy reaches survival at %v", b)
	}
	if err := f.Move(t.Context(), "hub", "n2", "n1"); err != nil || len(f.done()) != 0 {
		t.Errorf("Move of a server of no network = %v", err)
	}

	if err := f.CheckRemovable(t.Context(), "n1", "lobby"); statusOf(err) != http.StatusConflict {
		t.Errorf("CheckRemovable of a server of the network = %v", err)
	}
	if err := f.CheckRemovable(t.Context(), "n2", "hub"); err != nil {
		t.Errorf("CheckRemovable of another server = %v", err)
	}
}
