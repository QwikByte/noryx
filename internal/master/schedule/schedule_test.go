package schedule

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	"github.com/QwikByte/noryx/internal/logging"
	"github.com/QwikByte/noryx/internal/master/access"
	"github.com/QwikByte/noryx/internal/master/database"
	"github.com/QwikByte/noryx/internal/master/network"
	"github.com/QwikByte/noryx/internal/master/node"
	"github.com/QwikByte/noryx/internal/master/tag"
)

func TestNext(t *testing.T) {
	berlin, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Fatal(err)
	}
	at := func(s string) time.Time {
		parsed, err := time.ParseInLocation(time.DateTime, s, berlin)
		if err != nil {
			t.Fatal(err)
		}
		return parsed
	}
	daily := Schedule{Times: []string{"04:00", "16:30"}, TimeZone: "Europe/Berlin"}
	weekend := Schedule{Days: []time.Weekday{time.Saturday, time.Sunday}, Times: []string{"02:30"}, TimeZone: "Europe/Berlin"}
	monthly := Schedule{MonthDays: []int{1, 15}, Times: []string{"04:00"}, TimeZone: "Europe/Berlin"}
	lastDays := Schedule{MonthDays: []int{29, 30, 31}, Times: []string{"04:00"}, TimeZone: "Europe/Berlin"}
	the29th := Schedule{MonthDays: []int{29}, Times: []string{"02:30"}, TimeZone: "Europe/Berlin"}
	dates := Schedule{Dates: []string{"2026-10-24", "2026-12-24"}, Times: []string{"18:00"}, TimeZone: "Europe/Berlin"}
	for _, tc := range []struct {
		schedule Schedule
		after    string
		want     string
	}{
		{daily, "2026-10-02 03:59:59", "2026-10-02 04:00:00"},
		{daily, "2026-10-02 04:00:00", "2026-10-02 16:30:00"},
		{daily, "2026-10-02 17:00:00", "2026-10-03 04:00:00"},
		{weekend, "2026-10-02 12:00:00", "2026-10-03 02:30:00"}, // Friday to Saturday
		{weekend, "2026-10-04 03:00:00", "2026-10-10 02:30:00"}, // Sunday to the next Saturday
		// 02:30 doesn't exist when the clocks go forward on 29 March 2026; it becomes 03:30.
		{weekend, "2026-03-28 03:00:00", "2026-03-29 03:30:00"},
		{the29th, "2026-03-28 12:00:00", "2026-03-29 03:30:00"},
		{monthly, "2026-10-02 12:00:00", "2026-10-15 04:00:00"},
		{monthly, "2026-10-15 04:00:00", "2026-11-01 04:00:00"},
		{monthly, "2026-12-15 05:00:00", "2027-01-01 04:00:00"},
		// Months without the 29th, 30th or 31st run on their last day, once.
		{lastDays, "2026-01-31 04:00:00", "2026-02-28 04:00:00"},
		{lastDays, "2026-02-28 04:00:00", "2026-03-29 04:00:00"},
		{lastDays, "2026-04-29 04:00:00", "2026-04-30 04:00:00"},
		{lastDays, "2026-04-30 04:00:00", "2026-05-29 04:00:00"},
		{lastDays, "2028-02-28 04:00:00", "2028-02-29 04:00:00"},
		{lastDays, "2028-02-29 04:00:00", "2028-03-29 04:00:00"},
		{dates, "2026-10-02 12:00:00", "2026-10-24 18:00:00"},
		{dates, "2026-10-24 18:00:00", "2026-12-24 18:00:00"},
	} {
		if got := tc.schedule.Next(at(tc.after)); !got.Equal(at(tc.want)) {
			t.Errorf("%v after %s = %s, want %s", tc.schedule, tc.after, got.In(berlin), tc.want)
		}
	}
	// The time zone of the schedule counts, not that of the time passed.
	if got := daily.Next(at("2026-10-02 12:00:00").UTC()); !got.Equal(at("2026-10-02 16:30:00")) {
		t.Errorf("next after a UTC time = %s", got)
	}
	// Dates are days in the time zone of the schedule: in Auckland, the 24th begins on the 23rd in UTC.
	auckland := Schedule{Dates: []string{"2026-10-24"}, Times: []string{"04:00"}, TimeZone: "Pacific/Auckland"}
	if got := auckland.Next(time.Date(2026, 10, 23, 10, 0, 0, 0, time.UTC)); !got.Equal(time.Date(2026, 10, 23, 15, 0, 0, 0, time.UTC)) {
		t.Errorf("next date in Auckland = %s", got.UTC())
	}
	// After the last date, there is no next time.
	if got := dates.Next(at("2026-12-24 18:00:00")); !got.IsZero() {
		t.Errorf("next after the last date = %s", got)
	}
	// 02:30 is there twice when the clocks go back on 25 October 2026, but runs once.
	once := Schedule{Times: []string{"02:30"}, TimeZone: "Europe/Berlin"}
	if first := once.Next(at("2026-10-25 00:00:00")); first.In(berlin).Day() != 25 || !once.Next(first).Equal(at("2026-10-26 02:30:00")) {
		t.Errorf("02:30 on 25 October: %s, then %s", first, once.Next(first))
	}
}

func TestNormalize(t *testing.T) {
	s := Schedule{Days: []time.Weekday{3, 1, 3}, Times: []string{"16:30", "04:00", "16:30"}, TimeZone: "UTC"}
	if msg := s.Normalize(); msg != "" || !slices.Equal(s.Days, []time.Weekday{1, 3}) || !slices.Equal(s.Times, []string{"04:00", "16:30"}) {
		t.Fatalf("normalized %+v: %q", s, msg)
	}
	every := Schedule{Days: []time.Weekday{0, 1, 2, 3, 4, 5, 6}, Times: []string{"04:00"}, TimeZone: "UTC"}
	if every.Normalize(); len(every.Days) != 0 {
		t.Fatalf("all weekdays = %v, want none, which means every day", every.Days)
	}
	monthly := Schedule{MonthDays: []int{31, 1, 31}, Times: []string{"04:00"}, TimeZone: "UTC"}
	dates := Schedule{Dates: []string{"2026-12-24", "2026-10-24", "2026-12-24"}, Times: []string{"04:00"}, TimeZone: "UTC"}
	if monthly.Normalize() != "" || !slices.Equal(monthly.MonthDays, []int{1, 31}) || dates.Normalize() != "" || !slices.Equal(dates.Dates, []string{"2026-10-24", "2026-12-24"}) {
		t.Fatalf("normalized %+v and %+v", monthly, dates)
	}
	tooMany := Schedule{Times: []string{"04:00"}, TimeZone: "UTC"}
	for day := range maxDates + 1 {
		tooMany.Dates = append(tooMany.Dates, time.Date(2027, 1, 1+day, 0, 0, 0, 0, time.UTC).Format(time.DateOnly))
	}
	for _, bad := range []Schedule{
		{Times: []string{"24:00"}, TimeZone: "UTC"},
		{Times: []string{"4:00"}, TimeZone: "UTC"},
		{Times: nil, TimeZone: "UTC"},
		{Times: []string{"04:00"}, TimeZone: "Mars/Olympus"},
		{Times: []string{"04:00"}},
		{Days: []time.Weekday{7}, Times: []string{"04:00"}, TimeZone: "UTC"},
		{MonthDays: []int{0}, Times: []string{"04:00"}, TimeZone: "UTC"},
		{MonthDays: []int{32}, Times: []string{"04:00"}, TimeZone: "UTC"},
		{Days: []time.Weekday{1}, MonthDays: []int{1}, Times: []string{"04:00"}, TimeZone: "UTC"},
		{MonthDays: []int{1}, Dates: []string{"2026-12-24"}, Times: []string{"04:00"}, TimeZone: "UTC"},
		{Dates: []string{"2026-02-30"}, Times: []string{"04:00"}, TimeZone: "UTC"},
		{Dates: []string{"2026-13-01"}, Times: []string{"04:00"}, TimeZone: "UTC"},
		{Dates: []string{"2026-1-5"}, Times: []string{"04:00"}, TimeZone: "UTC"},
		{Dates: []string{"1999-12-31"}, Times: []string{"04:00"}, TimeZone: "UTC"},
		{Dates: []string{"2026-12-24T04:00"}, Times: []string{"04:00"}, TimeZone: "UTC"},
		tooMany,
	} {
		if bad.Normalize() == "" {
			t.Errorf("accepted %+v", bad)
		}
	}
}

const (
	serverA   = "aaaaaaaaaaaaaaaaaaaaaaaaaa"
	serverB   = "bbbbbbbbbbbbbbbbbbbbbbbbbb"
	serverC   = "cccccccccccccccccccccccccc"
	proxyP    = "pppppppppppppppppppppppppp"
	networkID = "nnnnnnnnnnnnnnnnnnnnnnnnnn"
)

func TestTargets(t *testing.T) {
	got, err := CheckTargets([]Target{
		{NodeID: "n1", ServerID: serverA}, {Kind: KindServer, NodeID: "n1"}, {NodeID: "n2", ServerID: serverA, Value: "x"},
		{Kind: KindServer, NodeID: "n2", ServerID: serverA}, {Kind: KindTag, Value: " Lobby "}, {Kind: KindTag, Value: "lobby", NodeID: "n1"},
		{Kind: KindNetwork, Value: networkID, Role: RoleProxy}, {Kind: KindNetwork, Value: networkID},
	})
	want := []Target{
		{Kind: KindServer, NodeID: "n1"}, {Kind: KindServer, NodeID: "n2", ServerID: serverA}, {Kind: KindTag, Value: "lobby"},
		{Kind: KindNetwork, Value: networkID, Role: RoleProxy}, {Kind: KindNetwork, Value: networkID},
	}
	if err != nil || !slices.Equal(got, want) {
		t.Fatalf("targets = %v, %v; want %v", got, err, want)
	}
	for _, bad := range [][]Target{
		{{NodeID: ""}}, {{NodeID: "n1", ServerID: "../x"}}, {{Kind: KindTag, Value: "no spaces"}}, {{Kind: KindTag}},
		{{Kind: KindNetwork, Value: "../x"}}, {{Kind: KindNetwork, Value: networkID, Role: "players"}}, {{Kind: "planet"}},
		slices.Repeat([]Target{{NodeID: "n1"}}, maxTargets+1),
	} {
		if _, err := CheckTargets(bad); err == nil {
			t.Errorf("accepted %v", bad)
		}
	}
	// Tasks need targets, unless their kind does without, and can't be enabled once their dates passed.
	s := &Service{kinds: map[string]Kind{"fake": fakeKind{}}}
	daily := Schedule{Times: []string{"04:00"}, TimeZone: "UTC"}
	if _, err := s.build("fake", Input{Name: "x", Schedule: daily}, Author{}); err == nil {
		t.Error("a task without targets was accepted")
	}
	past := Schedule{Dates: []string{"2020-01-01"}, Times: []string{"04:00"}, TimeZone: "UTC"}
	if _, err := s.build("fake", Input{Name: "x", Enabled: true, Schedule: past, Targets: []Target{{NodeID: "n1"}}}, Author{}); err == nil {
		t.Error("an enabled task whose dates passed was accepted")
	}
	if _, err := s.build("fake", Input{Name: "x", Schedule: past, Targets: []Target{{NodeID: "n1"}}}, Author{}); err != nil {
		t.Errorf("a paused task whose dates passed: %v", err)
	}
}

func TestResolve(t *testing.T) {
	g := groups{
		tags: map[tag.Server][]string{{NodeID: "n1", ServerID: serverA}: {"lobby"}, {NodeID: "n2", ServerID: serverB}: {"games", "lobby"}},
		networks: []network.Network{{
			ID: networkID, Proxy: network.Ref{NodeID: "n1", ServerID: proxyP},
			Backends: []network.Backend{{Ref: network.Ref{NodeID: "n1", ServerID: serverA}}, {Ref: network.Ref{NodeID: "n2", ServerID: serverC}}},
		}},
	}
	run := &journal{}
	sel := g.resolve([]Target{{Kind: KindServer, NodeID: "n3"}, {Kind: KindServer, NodeID: "n1", ServerID: serverB}, {Kind: KindTag, Value: "lobby"}, {Kind: KindTag, Value: "gone"}}, run)
	want := selection{"n1": {serverA: false, serverB: true}, "n2": {serverB: false}, "n3": {"": false}}
	if !equal(sel, want) || !slices.Equal(run.notes, []string{"No server has the tag gone."}) {
		t.Fatalf("resolved %v with notes %q, want %v", sel, run.notes, want)
	}
	for role, want := range map[string]selection{
		"":          {"n1": {proxyP: false, serverA: false}, "n2": {serverC: false}},
		RoleServers: {"n1": {serverA: false}, "n2": {serverC: false}},
		RoleProxy:   {"n1": {proxyP: false}},
	} {
		if sel := g.resolve([]Target{{Kind: KindNetwork, Value: networkID, Role: role}}, nil); !equal(sel, want) {
			t.Errorf("network with role %q = %v, want %v", role, sel, want)
		}
	}
	if !sel.covers(tag.Server{NodeID: "n3", ServerID: serverC}) || sel.covers(tag.Server{NodeID: "n2", ServerID: serverC}) {
		t.Error("covers is wrong")
	}
}

func equal(a, b selection) bool {
	if len(a) != len(b) {
		return false
	}
	for node, ids := range a {
		if len(ids) != len(b[node]) {
			return false
		}
		for id, named := range ids {
			if other, ok := b[node][id]; !ok || other != named {
				return false
			}
		}
	}
	return true
}

// fakeKind records its runs, and touches their servers.
type fakeKind struct{ runs chan []string }

// fakeSettings are those of a fakeKind: the permissions it needs, whether it skips its
// servers, and whether it waits until the task is withdrawn.
type fakeSettings struct {
	Needs []access.Permission `json:"needs"`
	Skip  bool                `json:"skip"`
	Wait  bool                `json:"wait"`
}

func (fakeKind) Check(settings json.RawMessage) (json.RawMessage, error) { return settings, nil }
func (fakeKind) Lead(json.RawMessage) time.Duration                      { return 0 }
func (fakeKind) Category() slog.Attr                                     { return logging.System }

func (fakeKind) Needs(raw json.RawMessage) []access.Permission {
	var s fakeSettings
	_ = json.Unmarshal(raw, &s)
	return s.Needs
}

func (k fakeKind) Run(ctx context.Context, t Task, servers Servers, _ time.Time) error {
	var s fakeSettings
	_ = json.Unmarshal(t.Settings, &s)
	if s.Wait {
		k.runs <- []string{"waiting"}
		select {
		case <-t.Withdrawn():
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	list, err := servers(ctx)
	names := []string{}
	for _, srv := range list {
		names = append(names, srv.GetName())
		var skipped error
		if s.Skip {
			skipped = Skipped("Players were online.")
		}
		_ = srv.ReportChange(t, logging.System, "Touch server", "touched", skipped)
	}
	if !s.Wait {
		k.runs <- names
	}
	return err
}

// fakeNodes are nodes whose agents list the servers of each node.
type fakeNodes struct {
	mu      sync.Mutex
	servers map[string][]string // names by node; the names are the IDs too
}

func (n *fakeNodes) Get(_ context.Context, id string) (node.Node, error) {
	return node.Node{ID: id, Name: "node-" + id}, nil
}

func (n *fakeNodes) Conn(_ context.Context, id string) (grpc.ClientConnInterface, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.servers[id] == nil {
		return nil, errors.New("offline")
	}
	return agent(n.servers[id]), nil
}

func (n *fakeNodes) add(nodeID, serverID string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.servers[nodeID] = append(n.servers[nodeID], serverID)
}

// agent answers ListServers with its servers.
type agent []string

func (a agent) Invoke(_ context.Context, method string, _, reply any, _ ...grpc.CallOption) error {
	if method != noryxv1.ServerService_ListServers_FullMethodName {
		return errors.New("not supported")
	}
	res := &noryxv1.ListServersResponse{}
	for _, id := range a {
		res.Servers = append(res.Servers, &noryxv1.Server{Id: id, Name: id})
	}
	proto.Merge(reply.(*noryxv1.ListServersResponse), res)
	return nil
}

func (agent) NewStream(context.Context, *grpc.StreamDesc, string, ...grpc.CallOption) (grpc.ClientStream, error) {
	return nil, errors.New("not supported")
}

type fakeNetworks []network.Network

func (n fakeNetworks) List(context.Context) ([]network.Network, error) { return n, nil }

// openDB opens a database with the nodes n1 and n2 and a network.
func openDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := database.Open(filepath.Join(t.TempDir(), "master.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	for _, statement := range []string{
		`INSERT INTO nodes (id, name, address, created_at) VALUES ('n1', 'node-1', 'x:1', 0), ('n2', 'node-2', 'x:2', 0)`,
		`INSERT INTO networks (id, name, proxy_node_id, proxy_server_id, proxy_type, forwarding_secret, created_at) VALUES ('` + networkID + `', 'Main', 'n1', '` + proxyP + `', 'velocity', 's', 0)`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	return db
}

// wait waits for a run of the task and returns it once it is recorded.
func wait(t *testing.T, s *Service, id string, runs chan []string) ([]string, Task) {
	t.Helper()
	var servers []string
	select {
	case servers = <-runs:
	case <-time.After(5 * time.Second):
		t.Fatal("the task didn't run")
	}
	for {
		task, err := s.Get(t.Context(), "fake", id)
		if err != nil {
			t.Fatal(err)
		}
		if !task.Running && task.LastRun != nil {
			return servers, task
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestService(t *testing.T) {
	db := openDB(t)
	kind := fakeKind{make(chan []string, 1)}
	nodes := &fakeNodes{servers: map[string][]string{"n1": {serverA}}}
	s := NewService(db, nodes, tag.NewStore(db), fakeNetworks{}, access.NewService(db), map[string]Kind{"fake": kind}, func(string) bool { return false })
	if err := s.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	in := Input{
		Name: "Nightly", Enabled: true, Settings: json.RawMessage(`{}`),
		Schedule: Schedule{Times: []string{"04:00"}, TimeZone: "UTC"}, Targets: []Target{{NodeID: "n1"}},
	}
	task, err := s.Create(t.Context(), "fake", in, Author{})
	if err != nil {
		t.Fatal(err)
	}
	if task.NextRun == nil || task.NextRun.UTC().Format("15:04") != "04:00" || task.Targets[0] != (Target{Kind: KindServer, NodeID: "n1"}) {
		t.Fatalf("created %+v", task)
	}
	if _, err := s.Create(t.Context(), "fake", in, Author{}); err == nil {
		t.Fatal("created a second task with the same name")
	}
	for _, missing := range []Target{{NodeID: "missing"}, {Kind: KindNetwork, Value: serverA}} {
		if _, err := s.Create(t.Context(), "fake", Input{Name: "Other", Schedule: in.Schedule, Targets: []Target{missing}}, Author{}); err == nil {
			t.Fatalf("created a task for %v, which doesn't exist", missing)
		}
	}

	// A run whose task can't be read, e.g. as the master stops, is left out.
	stopped, stop := context.WithCancel(t.Context())
	stop()
	s.execute(stopped, task.ID, time.Now(), "", nil)
	select {
	case <-kind.runs:
		t.Fatal("a task ran that couldn't be read")
	default:
	}

	// A task that is due runs, and its run is recorded.
	s.mu.Lock()
	s.slots[task.ID].at = time.Now()
	s.mu.Unlock()
	s.plan(Task{}) // wakes the loop
	servers, task := wait(t, s, task.ID, kind.runs)
	if !slices.Equal(servers, []string{serverA}) || task.LastRun.Outcome != Succeeded || task.NextRun == nil || time.Until(*task.NextRun) < time.Minute {
		t.Fatalf("after the run of %q: %+v", servers, task)
	}

	// Tags and networks name the servers they have at each run, also those they got later.
	// Servers of nodes that are offline fail the run.
	in.Targets = []Target{{Kind: KindTag, Value: "lobby"}, {Kind: KindNetwork, Value: networkID, Role: RoleServers}}
	if task, err = s.Update(t.Context(), "fake", task.ID, in, Author{}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RunNow(t.Context(), "fake", task.ID, Author{Name: "alice"}); err != nil {
		t.Fatal(err)
	}
	if servers, task = wait(t, s, task.ID, kind.runs); len(servers) != 0 || task.LastRun.Note != "No server has the tag lobby." || task.LastRun.StartedBy != "alice" {
		t.Fatalf("run of %q: %+v", servers, task.LastRun)
	}
	tags := tag.NewStore(db)
	if err := tags.Change(t.Context(), []tag.Server{{NodeID: "n1", ServerID: serverA}, {NodeID: "n2", ServerID: serverB}}, []string{"lobby"}, nil); err != nil {
		t.Fatal(err)
	}
	nodes.add("n1", serverC)
	s.networks = fakeNetworks{{ID: networkID, Proxy: network.Ref{NodeID: "n1", ServerID: proxyP},
		Backends: []network.Backend{{Ref: network.Ref{NodeID: "n1", ServerID: serverC}}}}}
	if _, err := s.RunNow(t.Context(), "fake", task.ID, Author{}); err != nil {
		t.Fatal(err)
	}
	if servers, task = wait(t, s, task.ID, kind.runs); !slices.Equal(servers, []string{serverA, serverC}) || task.LastRun.Error != "node-n2: offline" {
		t.Fatalf("run of %q: %+v", servers, task.LastRun)
	}
	for srv, want := range map[string]bool{serverA: true, serverB: false, serverC: true, proxyP: false} {
		covering, err := s.Covering(t.Context(), "fake", tag.Server{NodeID: "n1", ServerID: srv})
		if err != nil || (len(covering) == 1) != want {
			t.Errorf("tasks covering %s: %v, %v; want it covered: %t", srv, covering, err, want)
		}
	}

	// The newest runs are kept, newest first.
	for i := range maxRuns + 5 {
		if err := s.record(t.Context(), task.ID, Run{StartedAt: time.Unix(int64(i), 0), Outcome: Failed, Error: "x"}); err != nil {
			t.Fatal(err)
		}
	}
	runs, err := s.Runs(t.Context(), "fake", task.ID)
	if err != nil || len(runs) != maxRuns || runs[0].StartedAt.Unix() != maxRuns+4 || runs[maxRuns-1].StartedAt.Unix() != 5 {
		t.Fatalf("%d runs, %v", len(runs), err)
	}
	if _, err := s.Runs(t.Context(), "other", task.ID); !errors.Is(err, errNotFound) {
		t.Fatalf("runs of a task of another kind: %v", err)
	}

	// A deleted network is no longer a target, and a task without targets says so when it runs.
	in.Targets = []Target{{Kind: KindNetwork, Value: networkID}}
	if _, err := s.Update(t.Context(), "fake", task.ID, in, Author{}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM networks`); err != nil {
		t.Fatal(err)
	}
	if task, err = s.Get(t.Context(), "fake", task.ID); err != nil || len(task.Targets) != 0 {
		t.Fatalf("targets after deleting the network: %+v, %v", task.Targets, err)
	}
	if _, err := s.Update(t.Context(), "fake", task.ID, in, Author{}); err == nil {
		t.Fatal("saved a task for a deleted network")
	}
	if _, err := s.RunNow(t.Context(), "fake", task.ID, Author{}); err != nil {
		t.Fatal(err)
	}
	if _, task = wait(t, s, task.ID, kind.runs); !strings.HasPrefix(task.LastRun.Note, "It has no targets left") {
		t.Fatalf("note = %q", task.LastRun.Note)
	}

	// Deleted servers are no longer targets, and disabled tasks aren't scheduled.
	in.Targets, in.Enabled = []Target{{NodeID: "n1", ServerID: serverA}}, false
	if task, err = s.Update(t.Context(), "fake", task.ID, in, Author{}); err != nil || task.NextRun != nil {
		t.Fatalf("disabled task: %+v, %v", task, err)
	}
	if err := s.Forget(t.Context(), "n1", serverA); err != nil {
		t.Fatal(err)
	}
	if task, err = s.Get(t.Context(), "fake", task.ID); err != nil || len(task.Targets) != 0 {
		t.Fatalf("targets after forgetting the server: %+v, %v", task.Targets, err)
	}
	if _, err := s.Get(t.Context(), "other", task.ID); !errors.Is(err, errNotFound) {
		t.Fatalf("a task of another kind: %v", err)
	}
}

// A task turns itself off after its last date: when it ran then, or when the master starts
// after the date passed.
func TestDates(t *testing.T) {
	db := openDB(t)
	kind := fakeKind{make(chan []string, 1)}
	s := NewService(db, &fakeNodes{servers: map[string][]string{"n1": {serverA}}}, tag.NewStore(db), fakeNetworks{}, access.NewService(db), map[string]Kind{"fake": kind}, func(string) bool { return false })
	passed := Schedule{Days: []time.Weekday{}, Dates: []string{"2026-01-01"}, Times: []string{"04:00"}, TimeZone: "UTC"}
	stored, err := json.Marshal(passed)
	if err != nil {
		t.Fatal(err)
	}
	insert := func(id, name string) {
		t.Helper()
		if _, err := db.Exec(`INSERT INTO tasks (id, kind, name, enabled, schedule, settings, created_at) VALUES (?, 'fake', ?, 1, ?, '{}', 0)`, id, name, stored); err != nil {
			t.Fatal(err)
		}
	}
	insert("t1", "Passed while the master was down")
	if err := s.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	insert("t2", "Due now")
	last, err := s.Get(t.Context(), "fake", "t2")
	if err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	s.slots["t2"] = &slot{task: last, at: time.Now()}
	s.mu.Unlock()
	s.plan(Task{}) // wakes the loop
	wait(t, s, "t2", kind.runs)
	for _, id := range []string{"t1", "t2"} {
		for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
			task, err := s.Get(t.Context(), "fake", id)
			if err != nil {
				t.Fatal(err)
			}
			if !task.Enabled && task.NextRun == nil {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s wasn't turned off", task.Name)
			}
		}
	}
	// A schedule that changed meanwhile keeps the task on.
	insert("t3", "Changed")
	if _, err := db.Exec(`UPDATE tasks SET schedule = '{"days":[],"times":["05:00"],"timeZone":"UTC"}' WHERE id = 't3'`); err != nil {
		t.Fatal(err)
	}
	s.finish(t.Context(), Task{ID: "t3", kind: "fake", Schedule: passed})
	if task, err := s.Get(t.Context(), "fake", "t3"); err != nil || !task.Enabled {
		t.Fatalf("changed task: %+v, %v", task, err)
	}
	// Upcoming runs are those of enabled tasks in order.
	s.plan(Task{ID: "t4", kind: "fake", Enabled: true, Schedule: Schedule{Times: []string{"04:00", "16:00"}, TimeZone: "UTC"}})
	s.plan(Task{ID: "t5", kind: "fake", Enabled: true, Schedule: Schedule{Times: []string{"10:00"}, TimeZone: "UTC"}})
	upcoming := s.Upcoming("fake", time.Now().Add(48*time.Hour))
	if len(upcoming) != 6 || !slices.IsSortedFunc(upcoming, func(a, b Upcoming) int { return a.At.Compare(b.At) }) || len(s.Upcoming("other", time.Now().Add(48*time.Hour))) != 0 {
		t.Fatalf("upcoming = %v", upcoming)
	}
}
