package policy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	"github.com/QwikByte/noryx/internal/master/access"
	"github.com/QwikByte/noryx/internal/master/network"
	"github.com/QwikByte/noryx/internal/master/plugin"
	"github.com/QwikByte/noryx/internal/master/schedule"
	"github.com/QwikByte/noryx/internal/master/server"
)

func TestCheck(t *testing.T) {
	got, err := Policies{}.Check(json.RawMessage(`{"action":"restart","warnings":[1,10,5,10],"command":"op me"}`))
	if want := `{"action":"restart","warnings":[10,5,1],"message":"","commands":[]}`; err != nil || string(got) != want {
		t.Fatalf("checked settings = %s, %v; want %s", got, err, want)
	}
	if lead := (Policies{}).Lead(got); lead != 10*time.Minute {
		t.Fatalf("lead = %v", lead)
	}
	for _, tc := range []struct{ in, want string }{
		// Only restarts go server by server.
		{`{"action":"stop","rolling":2,"message":"  Bye in {minutes} min "}`, `{"action":"stop","warnings":[],"message":"Bye in {minutes} min","commands":[]}`},
		{`{"action":"restart","rolling":2}`, `{"action":"restart","warnings":[],"message":"","commands":[],"rolling":2}`},
		// A policy saved with one command gets a list of them.
		{`{"action":"command","command":" say hi "}`, `{"action":"command","warnings":[],"message":"","commands":["say hi"]}`},
		{`{"action":"command","commands":["say hi","save-all"]}`, `{"action":"command","warnings":[],"message":"","commands":["say hi","save-all"]}`},
		// Conditions warn nobody, and starts have neither conditions nor backups.
		{`{"action":"restart","warnings":[5],"condition":"wait","wait":30}`, `{"action":"restart","warnings":[],"message":"","commands":[],"condition":"wait","wait":30}`},
		{`{"action":"image","condition":"empty","wait":30}`, `{"action":"image","warnings":[],"message":"","commands":[],"condition":"empty"}`},
		{`{"action":"start","condition":"wait","wait":5,"backup":{"selection":{"worlds":true}}}`, `{"action":"start","warnings":[],"message":"","commands":[]}`},
		// Backing up first is checked as a backup job, without datastores.
		{`{"action":"plugins","backup":{"selection":{"worlds":true},"datastores":["aaaaaaaaaaaaaaaaaaaaaaaaaa"],"keep":3}}`,
			`{"action":"plugins","warnings":[],"message":"","commands":[],"backup":{"selection":{"everything":false,"worlds":true,"plugins":false,"config":false,"paths":[],"exclude":[]},"datastores":[],"location":"","keep":3,"keepDays":0,"keepWeeks":0,"keepMonths":0}}`},
	} {
		if got, err := (Policies{}).Check(json.RawMessage(tc.in)); err != nil || string(got) != tc.want {
			t.Errorf("checked %s = %s, %v; want %s", tc.in, got, err, tc.want)
		}
	}
	for _, bad := range []string{
		`{"action":"explode"}`,
		`{"action":"stop","warnings":[61]}`,
		`{"action":"stop","warnings":[0]}`,
		`{"action":"restart","message":"two\nlines"}`,
		`{"action":"restart","rolling":51}`,
		`{"action":"restart","rolling":-1}`,
		`{"action":"command","commands":[]}`,
		`{"action":"command","commands":["say hi",""]}`,
		`{"action":"command","command":"say hi\nop attacker"}`,
		`{"action":"command","commands":["` + strings.Repeat("a", maxCommand+1) + `"]}`,
		`{"action":"command","commands":` + strings.Repeat(`["x"`, 1) + strings.Repeat(`,"x"`, maxCommands) + `]}`,
		`{"action":"restart","condition":"never"}`,
		`{"action":"restart","condition":"wait"}`,
		`{"action":"restart","condition":"wait","wait":361}`,
		`{"action":"restart","backup":{"selection":{}}}`,
		`{"action":"restart","backup":{"selection":{"worlds":true},"location":"../elsewhere"}}`,
	} {
		if _, err := (Policies{}).Check(json.RawMessage(bad)); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
	// Steps besides the action need the permissions they need by hand.
	for settings, want := range map[string][]access.Permission{
		`{"action":"restart"}`: nil,
		`{"action":"restart","backup":{"selection":{"worlds":true}}}`: {access.BackupsCreate},
		`{"action":"image"}`: {access.ServersSettings},
		`{"action":"plugins","backup":{"selection":{"worlds":true}}}`: {access.BackupsCreate, access.Plugins},
	} {
		if got := (Policies{}).Needs(json.RawMessage(settings)); !slices.Equal(got, want) {
			t.Errorf("%s needs %v, want %v", settings, got, want)
		}
	}
	if lead := (Policies{}).Lead(json.RawMessage(`{"action":"restart","warnings":[5],"condition":"empty"}`)); lead != 0 {
		t.Errorf("lead with a condition = %v", lead)
	}
}

// agent records the calls to the ServerService of a node, and the backups and updates of
// plugins of the fakes that share it.
type agent struct {
	mu    sync.Mutex
	calls []string
}

func (a *agent) Invoke(_ context.Context, method string, args, reply any, _ ...grpc.CallOption) error {
	call := method[strings.LastIndex(method, "/")+1:]
	if req, ok := args.(*noryxv1.SendCommandRequest); ok {
		call += " " + req.GetCommand()
	} else {
		call += " " + args.(interface{ GetId() string }).GetId()
	}
	if res, ok := reply.(*noryxv1.UpdateImageResponse); ok {
		res.Updated = true
	}
	a.record(call)
	return nil
}

func (a *agent) NewStream(context.Context, *grpc.StreamDesc, string, ...grpc.CallOption) (grpc.ClientStream, error) {
	return nil, errors.New("not supported")
}

func (a *agent) Conn(context.Context, string) (grpc.ClientConnInterface, error) { return a, nil }

func (a *agent) record(call string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.calls = append(a.calls, call)
}

func (a *agent) recorded() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Clone(a.calls)
}

// networks restart the servers of their networks server by server, which the agent records.
type networks struct {
	*agent
	list []network.Network
}

func (n networks) List(context.Context) ([]network.Network, error) { return n.list, nil }

func (n networks) RollingRestart(_ context.Context, nw network.Network, batch int, only ...network.Ref) error {
	ids := make([]string, len(only))
	for i, ref := range only {
		ids[i] = ref.ServerID
	}
	n.record(fmt.Sprintf("RollingRestart %s %d %s", nw.Name, batch, strings.Join(ids, ",")))
	return nil
}

// usage tells the players of servers by ID; nil means they can't be counted.
type usage struct {
	mu      sync.Mutex
	players map[string]*noryxv1.Players
}

func (u *usage) Latest(context.Context, string) (*noryxv1.GetStatsResponse, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	res := &noryxv1.GetStatsResponse{}
	for id, p := range u.players {
		res.Servers = append(res.Servers, &noryxv1.ServerStats{Id: id, Running: true, Players: p})
	}
	return res, nil
}

func (u *usage) set(id string, online uint32) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.players[id] = &noryxv1.Players{Online: online}
}

// backups back up servers, which the agent records; broken ones fail, empty ones have no data.
type fakeBackups struct{ *agent }

func (b fakeBackups) BackUp(_ context.Context, _, serverID string, t schedule.Task) (*noryxv1.Backup, error) {
	var s struct{ Selection struct{ Worlds bool } }
	if err := json.Unmarshal(t.Settings, &s); err != nil || !s.Selection.Worlds {
		return nil, fmt.Errorf("settings %s", t.Settings)
	}
	b.record("BackUp " + serverID)
	switch serverID {
	case "broken":
		return nil, errors.New("disk full")
	case "empty":
		return nil, nil
	}
	return &noryxv1.Backup{}, nil
}

// plugins update plugins, which the agent records.
type fakePlugins struct{ *agent }

func (p fakePlugins) Update(_ context.Context, servers []plugin.Ref, projects []string) []plugin.Result {
	p.record("UpdatePlugins " + servers[0].ServerID)
	return []plugin.Result{{Ref: servers[0], Installed: []plugin.Installed{{FileName: "LuckPerms-5.5.jar"}, {FileName: "ViaVersion-5.2.jar"}}, Pinned: []string{"Chunky"}, Restart: true}}
}

var (
	paper, velocity  = noryxv1.ServerType_SERVER_TYPE_PAPER, noryxv1.ServerType_SERVER_TYPE_VELOCITY
	running, stopped = noryxv1.ServerState_SERVER_STATE_RUNNING, noryxv1.ServerState_SERVER_STATE_STOPPED
)

func target(id string, typ noryxv1.ServerType, state noryxv1.ServerState) schedule.Server {
	return schedule.Server{Server: &noryxv1.Server{Id: id, Name: id, Type: typ, State: state}, NodeID: "n1", NodeName: "node-1"}
}

// policies returns policies whose fakes record their calls in a, with the networks.
func policies(a *agent, u *usage, list ...network.Network) Policies {
	if u == nil {
		u = &usage{players: map[string]*noryxv1.Players{}}
	}
	return New(a, networks{a, list}, u, fakeBackups{a}, fakePlugins{a}, config(server.DefaultWarnings()))
}

// config gives policies the warnings of the settings.
type config server.Warnings

func (c config) Warnings() server.Warnings { return server.Warnings(c) }

func listed(servers ...schedule.Server) schedule.Servers {
	return func(context.Context) ([]schedule.Server, error) { return servers, nil }
}

func TestRun(t *testing.T) {
	servers := func(context.Context) ([]schedule.Server, error) {
		return []schedule.Server{target("lobby", paper, running), target("proxy", velocity, running), target("old", paper, stopped)},
			errors.New("node-2: unreachable")
	}
	for _, tc := range []struct {
		settings string
		want     []string
	}{
		// The 5 minute warning is too late, the 1 minute warning is sent right away.
		{`{"action":"restart","warnings":[5,1],"message":"Restart in {minutes} min"}`,
			[]string{"SendCommand say Restart in 1 min", "RestartServer lobby", "RestartServer proxy"}},
		{`{"action":"stop","warnings":[1]}`, []string{"SendCommand say The server stops in 1 min.", "StopServer lobby", "StopServer proxy"}},
		{`{"action":"start"}`, []string{"StartServer old"}},
		{`{"action":"command","commands":["say bye","save-all"]}`, []string{"SendCommand say bye", "SendCommand save-all"}},
		// Images are updated on all servers, plugins on those that can have some.
		{`{"action":"image"}`, []string{"UpdateImage lobby", "UpdateImage old", "UpdateImage proxy"}},
		{`{"action":"plugins"}`, []string{"UpdatePlugins lobby", "UpdatePlugins old", "UpdatePlugins proxy"}},
	} {
		a := &agent{}
		task := schedule.Task{Settings: json.RawMessage(tc.settings)}
		err := policies(a, nil).Run(t.Context(), task, servers, time.Now().Add(50*time.Millisecond))
		calls := a.recorded()
		if strings.Contains(tc.settings, "warnings") {
			slices.Sort(calls[1:]) // servers are restarted at the same time, after the warnings
		} else if !strings.Contains(tc.settings, "commands") {
			slices.Sort(calls)
		}
		if err == nil || err.Error() != "node-2: unreachable" || !slices.Equal(calls, tc.want) {
			t.Errorf("%s: calls %q, %v; want %q", tc.settings, calls, err, tc.want)
		}
	}
}

// A policy without a warning of its own warns with the text of the settings when it runs, and
// the warning shows as they say then. One whose own warning doesn't fit them any more acts
// without warning, and the run tells why.
func TestRunWarnings(t *testing.T) {
	for _, tc := range []struct {
		settings string
		want     []string
		err      string
	}{
		{`{"action":"restart","warnings":[1]}`, []string{`SendCommand minecraft:title @a title {"text":"Neustart in 1 Minute"}`, "RestartServer lobby"}, ""},
		{`{"action":"restart","warnings":[1],"message":"` + strings.Repeat("<", 200) + `"}`, []string{"RestartServer lobby"}, "too long"},
	} {
		a := &agent{}
		p := policies(a, nil)
		p.config = config{Restart: "Neustart in {minutes} Minute", Stop: "Stopp", MaxMinutes: 10, Kind: "title"}
		err := p.Run(t.Context(), schedule.Task{Settings: json.RawMessage(tc.settings)}, listed(target("lobby", paper, running)), time.Now().Add(50*time.Millisecond))
		if tc.err == "" && err != nil || tc.err != "" && (err == nil || !strings.Contains(err.Error(), tc.err)) || !slices.Equal(a.recorded(), tc.want) {
			t.Errorf("%s: calls %q, %v; want %q", tc.settings, a.recorded(), err, tc.want)
		}
	}
}

// Server by server, the running game servers of each network restart with a rolling restart,
// and then its proxy. The other servers restart at once, also those of networks that don't
// run and the proxy of a network whose servers don't.
func TestRunServerByServer(t *testing.T) {
	ref := func(id string) network.Ref { return network.Ref{NodeID: "n1", ServerID: id} }
	main := network.Network{Name: "Main", Proxy: ref("proxy"), Backends: []network.Backend{
		{Ref: ref("lobby")}, {Ref: ref("game")}, {Ref: ref("old")}, {Ref: ref("starting")}, {Ref: ref("other")},
	}}
	idle := network.Network{Name: "Idle", Proxy: ref("idle-proxy"), Backends: []network.Backend{{Ref: ref("idle")}}}
	servers := listed( // other isn't a target
		target("proxy", velocity, running), target("lobby", paper, running), target("game", paper, running),
		target("old", paper, stopped), target("starting", paper, noryxv1.ServerState_SERVER_STATE_STARTING),
		target("solo", paper, running), target("idle-proxy", velocity, running), target("idle", paper, stopped),
	)
	a := &agent{}
	task := schedule.Task{Settings: json.RawMessage(`{"action":"restart","rolling":2}`)}
	if err := policies(a, nil, main, idle).Run(t.Context(), task, servers, time.Now()); err != nil {
		t.Fatal(err)
	}
	calls := a.recorded()
	if rolling, proxy := slices.Index(calls, "RollingRestart Main 2 lobby,game"), slices.Index(calls, "RestartServer proxy"); rolling < 0 || proxy < rolling {
		t.Errorf("calls %q: the proxy didn't restart after its servers", calls)
	}
	slices.Sort(calls)
	want := []string{"RestartServer idle-proxy", "RestartServer proxy", "RestartServer solo", "RestartServer starting", "RollingRestart Main 2 lobby,game"}
	if !slices.Equal(calls, want) {
		t.Errorf("calls %q, want %q", calls, want)
	}
}

// Servers with players, or whose players can't be counted, are left alone, which isn't a
// failure; waiting lets the players leave until the time is up.
func TestConditions(t *testing.T) {
	pollEvery = 10 * time.Millisecond
	servers := listed(target("lobby", paper, running), target("busy", paper, running), target("unknown", paper, running), target("leaving", paper, running))
	u := &usage{players: map[string]*noryxv1.Players{"lobby": {}, "busy": {Online: 3}, "unknown": nil, "leaving": {Online: 1}}}
	a := &agent{}
	task := schedule.Task{Settings: json.RawMessage(`{"action":"restart","condition":"empty"}`)}
	if err := policies(a, u).Run(t.Context(), task, servers, time.Now()); err != nil || !slices.Equal(a.recorded(), []string{"RestartServer lobby"}) {
		t.Fatalf("only if empty: calls %q, %v", a.recorded(), err)
	}

	// The players of leaving leave while the run waits; busy stays until the time is up.
	a = &agent{}
	task.Settings = json.RawMessage(`{"action":"restart","condition":"wait","wait":1}`)
	go func() {
		time.Sleep(100 * time.Millisecond)
		u.set("leaving", 0)
	}()
	started := time.Now()
	err := policies(a, u).Run(t.Context(), task, servers, started.Add(-time.Minute+400*time.Millisecond))
	calls := a.recorded()
	slices.Sort(calls)
	if took := time.Since(started); err != nil || !slices.Equal(calls, []string{"RestartServer leaving", "RestartServer lobby"}) || took < 350*time.Millisecond || took > 5*time.Second {
		t.Fatalf("waiting: calls %q, %v after %v", calls, err, took)
	}

	// Waiting ends with the run.
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	if err := policies(&agent{}, u).Run(ctx, task, listed(target("busy", paper, running)), time.Now()); err == nil || !strings.Contains(err.Error(), "deadline exceeded") {
		t.Fatalf("waiting after the run ended: %v", err)
	}
}

// Backing up first backs up each server before it restarts; one whose backup fails is left
// alone, one without data restarts anyway.
func TestBackUpFirst(t *testing.T) {
	a := &agent{}
	settings, err := Policies{}.Check(json.RawMessage(`{"action":"restart","warnings":[1],"backup":{"selection":{"worlds":true}}}`))
	if err != nil {
		t.Fatal(err)
	}
	task := schedule.Task{Settings: settings}
	servers := listed(target("lobby", paper, running), target("broken", paper, running), target("empty", paper, running), target("old", paper, stopped))
	err = policies(a, nil).Run(t.Context(), task, servers, time.Now().Add(50*time.Millisecond))
	calls := a.recorded()
	backedUp := slices.IndexFunc(calls, func(c string) bool { return strings.HasPrefix(c, "Restart") })
	if backedUp < 3 || !slices.ContainsFunc(calls[:backedUp], func(c string) bool { return c == "BackUp lobby" }) {
		t.Fatalf("calls %q: not backed up first", calls)
	}
	slices.Sort(calls)
	warning := "SendCommand say The server restarts in 1 min."
	want := []string{"BackUp broken", "BackUp empty", "BackUp lobby", "RestartServer empty", "RestartServer lobby", warning, warning, warning}
	if !slices.Equal(calls, want) || err == nil || !strings.Contains(err.Error(), "broken on node-1: disk full") {
		t.Fatalf("calls %q, %v; want %q", calls, err, want)
	}
}

// Server by server with a condition, the game servers of a network are backed up and restart
// whatever their players, as these move to other servers; the proxy, which has the players of
// the whole network, waits for them like the other servers.
func TestConditionsServerByServer(t *testing.T) {
	ref := func(id string) network.Ref { return network.Ref{NodeID: "n1", ServerID: id} }
	main := network.Network{Name: "Main", Proxy: ref("proxy"), Backends: []network.Backend{{Ref: ref("lobby")}, {Ref: ref("game")}}}
	u := &usage{players: map[string]*noryxv1.Players{"proxy": {Online: 2}, "lobby": {Online: 1}, "game": {Online: 1}, "solo": {}}}
	a := &agent{}
	task := schedule.Task{Settings: json.RawMessage(`{"action":"restart","rolling":1,"condition":"empty","backup":{"selection":{"worlds":true}}}`)}
	servers := listed(target("proxy", velocity, running), target("lobby", paper, running), target("game", paper, running), target("solo", paper, running))
	if err := policies(a, u, main).Run(t.Context(), task, servers, time.Now()); err != nil {
		t.Fatal(err)
	}
	calls := a.recorded()
	slices.Sort(calls)
	want := []string{"BackUp game", "BackUp lobby", "BackUp solo", "RestartServer solo", "RollingRestart Main 1 lobby,game"}
	if !slices.Equal(calls, want) {
		t.Fatalf("calls %q, want %q", calls, want)
	}
}

// Updates tell what they changed.
func TestChanges(t *testing.T) {
	r := &run{Policies: policies(&agent{}, nil)}
	if change, err := r.updatePlugins(t.Context(), target("lobby", paper, running)); err != nil ||
		change != "Updated LuckPerms-5.5.jar, ViaVersion-5.2.jar. Kept at their version: Chunky. It loads the new files once it restarts." {
		t.Fatalf("plugins: %q, %v", change, err)
	}
	if change, err := r.updateImage(t.Context(), target("lobby", paper, running)); err != nil || change != "It got a new image and restarted with it." {
		t.Fatalf("image: %q, %v", change, err)
	}
}
