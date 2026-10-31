package network

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	"github.com/QwikByte/noryx/internal/master/node"
)

// agents are the agents of the nodes, which list servers, restart them, measure their players
// and send commands to the proxy, and record the restarts and commands.
type agents struct {
	mu      sync.Mutex
	servers map[Ref]*noryxv1.Server
	players map[Ref][]string
	// failing servers don't restart, and stuck ones stop instead of running again.
	failing, stuck map[Ref]bool
	// cantSend makes the proxy refuse to send players.
	cantSend  bool
	restarted []Ref
	commands  []string
}

func newAgents(up ...Ref) *agents {
	a := &agents{servers: map[Ref]*noryxv1.Server{}, players: map[Ref][]string{}, failing: map[Ref]bool{}, stuck: map[Ref]bool{}}
	for _, ref := range up {
		a.servers[ref] = &noryxv1.Server{Id: ref.ServerID, State: running}
	}
	return a
}

func (a *agents) Get(context.Context, string) (node.Node, error) {
	return node.Node{}, errors.New("not needed")
}

func (a *agents) Conn(_ context.Context, nodeID string) (grpc.ClientConnInterface, error) {
	return agentConn{a, nodeID}, nil
}

type agentConn struct {
	*agents
	node string
}

func (c agentConn) Invoke(_ context.Context, method string, args, reply any, _ ...grpc.CallOption) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	switch method {
	case noryxv1.ServerService_ListServers_FullMethodName:
		res := reply.(*noryxv1.ListServersResponse)
		for ref, srv := range c.servers {
			if ref.NodeID == c.node {
				res.Servers = append(res.Servers, srv)
			}
		}
	case noryxv1.ServerService_RestartServer_FullMethodName:
		ref := Ref{c.node, args.(*noryxv1.RestartServerRequest).GetId()}
		if c.failing[ref] {
			return status.Error(codes.Internal, "the runtime failed")
		}
		c.restarted = append(c.restarted, ref)
		if c.stuck[ref] {
			c.servers[ref].State = noryxv1.ServerState_SERVER_STATE_STOPPED
		}
	case noryxv1.StatsService_GetStats_FullMethodName:
		res := reply.(*noryxv1.GetStatsResponse)
		for ref, names := range c.players {
			if ref.NodeID == c.node {
				res.Servers = append(res.Servers, &noryxv1.ServerStats{Id: ref.ServerID, Players: &noryxv1.Players{Names: names}})
			}
		}
	case noryxv1.ServerService_SendCommand_FullMethodName:
		if c.cantSend {
			return status.Error(codes.FailedPrecondition, "The proxy has no send command.")
		}
		c.commands = append(c.commands, c.node+"/"+args.(*noryxv1.SendCommandRequest).GetId()+": "+args.(*noryxv1.SendCommandRequest).GetCommand())
	default:
		return status.Error(codes.Unimplemented, method)
	}
	return nil
}

func (agentConn) NewStream(context.Context, *grpc.StreamDesc, string, ...grpc.CallOption) (grpc.ClientStream, error) {
	return nil, status.Error(codes.Unimplemented, "no streams")
}

func (a *agents) record() ([]Ref, []string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Clone(a.restarted), slices.Clone(a.commands)
}

// newService returns the service of networks whose nodes run agents.
func newService(a *agents) *Service { return NewService(nil, a, nil, nil, nil) }

var (
	proxy               = Ref{"n1", "proxy"}
	lobby, hub          = Ref{"n1", "lobby"}, Ref{"n2", "hub"}
	game1, game2, game3 = Ref{"n1", "game1"}, Ref{"n2", "game2"}, Ref{"n1", "game3"}
	stopped             = Ref{"n2", "stopped"}
	mainNetwork         = Network{
		Name: "Main", Proxy: proxy, ProxyType: "velocity",
		Backends: []Backend{
			{Ref: lobby, Name: "lobby"}, {Ref: game1, Name: "game1"}, {Ref: hub, Name: "hub"},
			{Ref: game2, Name: "game2"}, {Ref: stopped, Name: "stopped"}, {Ref: game3, Name: "game3"},
		},
		Try: []string{"lobby", "hub"},
	}
)

// A rolling restart restarts the running game servers a batch at a time, then those that
// players join one by one, the first last, and leaves the proxy and stopped servers alone.
func TestRollingRestartOrder(t *testing.T) {
	a := newAgents(proxy, lobby, hub, game1, game2, game3)
	a.servers[stopped] = &noryxv1.Server{Id: stopped.ServerID, State: noryxv1.ServerState_SERVER_STATE_STOPPED}
	if err := newService(a).RollingRestart(t.Context(), mainNetwork, 2); err != nil {
		t.Fatal(err)
	}
	restarted, commands := a.record()
	want := [][]Ref{{game1, game2}, {game3}, {hub}, {lobby}}
	for _, group := range want {
		if len(restarted) < len(group) || !sameRefs(restarted[:len(group)], group) {
			t.Fatalf("restarted %v, want the groups %v", a.restarted, want)
		}
		restarted = restarted[len(group):]
	}
	if len(restarted) != 0 || len(commands) != 0 {
		t.Fatalf("restarted %v more, sent %q without players", restarted, commands)
	}

	// Only the servers asked for restart; the others stay where players can go.
	a = newAgents(proxy, lobby, hub, game1, game2, game3)
	if err := newService(a).RollingRestart(t.Context(), mainNetwork, 1, game2, lobby); err != nil {
		t.Fatal(err)
	}
	if restarted, _ := a.record(); !slices.Equal(restarted, []Ref{game2, lobby}) {
		t.Fatalf("restarted %v", restarted)
	}
}

// Before servers restart, their players move to the first server players join that doesn't
// restart with them; names that a command would read as others, or as more, stay.
func TestRollingRestartMovesPlayers(t *testing.T) {
	t.Parallel()
	a := newAgents(proxy, lobby, hub, game1)
	a.players[lobby] = []string{"Steve", "all", "@a", "Alex;op Alex", ".Bedrock"}
	n := mainNetwork
	n.Backends = n.Backends[:3]
	if err := newService(a).RollingRestart(t.Context(), n, 1); err != nil {
		t.Fatal(err)
	}
	restarted, commands := a.record()
	if want := []string{"n1/proxy: send Steve hub", "n1/proxy: send .Bedrock hub"}; !slices.Equal(commands, want) {
		t.Errorf("commands %q, want %q", commands, want)
	}
	if !slices.Equal(restarted, []Ref{game1, hub, lobby}) {
		t.Errorf("restarted %v", restarted)
	}
}

func TestRollingRestartFails(t *testing.T) {
	ctx := t.Context()
	for _, tc := range []struct {
		name  string
		setUp func(a *agents)
		batch int
		want  string
	}{
		{"no batch", func(*agents) {}, 0, "from 1 to 50"},
		{"a large batch", func(*agents) {}, MaxBatch + 1, "from 1 to 50"},
		{"nothing runs", func(a *agents) { clear(a.servers) }, 1, "No server of the network runs"},
		{"a restart fails", func(a *agents) { a.failing[game2] = true }, 1, "game2: the runtime failed"},
		{"a server doesn't start", func(a *agents) { a.stuck[game1] = true }, 1, "game1 didn't start again"},
		{"players can't move", func(a *agents) { a.players[game1], a.cantSend = []string{"Steve"}, true }, 1, "players can't move"},
	} {
		a := newAgents(proxy, lobby, hub, game1, game2, game3)
		tc.setUp(a)
		err := newService(a).RollingRestart(ctx, mainNetwork, tc.batch)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want %q", tc.name, err, tc.want)
		}
	}
}

func TestMovePlayers(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	a := newAgents(proxy, lobby, game1)
	a.players[game1] = []string{"Steve"}
	n := mainNetwork
	if sent, err := newService(a).MovePlayers(ctx, n, game1); err != nil || sent != 1 {
		t.Fatalf("moved %d, %v", sent, err)
	}
	if _, commands := a.record(); !slices.Equal(commands, []string{"n1/proxy: send Steve lobby"}) {
		t.Errorf("commands %q", commands)
	}
	if restarted, _ := a.record(); len(restarted) != 0 {
		t.Errorf("restarted %v", restarted)
	}
	for _, tc := range []struct {
		name  string
		setUp func(a *agents)
		from  Ref
		want  string
	}{
		{"from the proxy", func(*agents) {}, proxy, ""},
		{"from another server", func(*agents) {}, Ref{"n3", "other"}, ""},
		{"from a stopped server", func(*agents) {}, game2, "The server doesn't run"},
		{"without a proxy", func(a *agents) { delete(a.servers, proxy) }, game1, "The proxy doesn't run"},
		{"to nowhere", func(a *agents) { delete(a.servers, lobby) }, game1, "nowhere to go"},
	} {
		a := newAgents(proxy, lobby, game1)
		tc.setUp(a)
		if _, err := newService(a).MovePlayers(ctx, n, tc.from); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want %q", tc.name, err, tc.want)
		}
	}
}

func sameRefs(a, b []Ref) bool {
	sort := func(refs []Ref) []Ref {
		refs = slices.Clone(refs)
		slices.SortFunc(refs, func(x, y Ref) int { return strings.Compare(x.NodeID+x.ServerID, y.NodeID+y.ServerID) })
		return refs
	}
	return slices.Equal(sort(a), sort(b))
}
