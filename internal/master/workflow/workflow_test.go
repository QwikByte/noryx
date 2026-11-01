package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	"github.com/QwikByte/noryx/internal/master/access"
	"github.com/QwikByte/noryx/internal/master/database"
	"github.com/QwikByte/noryx/internal/master/logs"
	"github.com/QwikByte/noryx/internal/master/schedule"
)

// step returns a step with settings.
func step(id, kind string, with any, inner ...Step) Step {
	raw, _ := json.Marshal(with)
	return Step{ID: id, Kind: kind, With: raw, Steps: inner}
}

func TestCheck(t *testing.T) {
	d := Draft{Name: " Nightly ", Definition: Definition{
		Triggers: []Trigger{
			{Kind: OnSchedule, Schedule: &schedule.Schedule{Times: []string{"16:00", "04:00"}, TimeZone: "UTC"}, Every: 5},
			{Kind: OnEvent, Level: "warn", Categories: []string{"servers", "servers"}},
		},
		Steps: []Step{
			step("restart", "restart", map[string]any{"targets": []map[string]string{{"nodeId": "n1"}}, "warnings": []int{1, 10, 5}, "message": "Bye in {minutes}"}),
			{ID: "check", Kind: "if", With: json.RawMessage(`{"condition":{"match":"all","rules":[{"left":"{{steps.restart.failed}}","op":"eq","right":"0"}]}}`),
				Steps: []Step{step("note", "log", map[string]any{"message": "Restarted {{steps.restart.count}} servers"})}, Branches: [][]Step{{}}},
		},
	}}
	needs, err := d.check()
	if err != nil {
		t.Fatal(err)
	}
	want := []access.Permission{access.ConsoleCommands, access.LogsView, access.ServersRestart}
	if !slices.Equal(needs, want) {
		t.Errorf("needs %v, want %v", needs, want)
	}
	var restart powerSettings
	if err := json.Unmarshal(d.Steps[0].With, &restart); err != nil || !slices.Equal(restart.Warnings, []uint32{10, 5, 1}) {
		t.Errorf("restart = %+v, %v", restart, err)
	}
	if d.Name != "Nightly" || d.Triggers[0].Every != 0 || !slices.Equal(d.Triggers[0].Schedule.Times, []string{"04:00", "16:00"}) ||
		!slices.Equal(d.Triggers[1].Categories, []string{"servers"}) || d.Steps[1].Branches != nil || d.Overlap != Skip || d.TimeZone != "UTC" {
		t.Errorf("normalized %+v", d)
	}

	valid := func() Draft {
		return Draft{Name: "x", Definition: Definition{Steps: []Step{step("a", "log", map[string]any{"message": "hi"})}}}
	}
	for name, change := range map[string]func(*Draft){
		"no steps":         func(d *Draft) { d.Steps = nil },
		"unknown kind":     func(d *Draft) { d.Steps[0].Kind = "explode" },
		"bad ID":           func(d *Draft) { d.Steps[0].ID = "Bad ID" },
		"same IDs":         func(d *Draft) { d.Steps = append(d.Steps, d.Steps[0]) },
		"bad template":     func(d *Draft) { d.Steps[0].With = json.RawMessage(`{"message":"{{oops"}`) },
		"unknown filter":   func(d *Draft) { d.Steps[0].With = json.RawMessage(`{"message":"{{a | explode}}"}`) },
		"one branch":       func(d *Draft) { d.Steps[0] = Step{ID: "p", Kind: "parallel", Branches: [][]Step{{}}} },
		"bad trigger":      func(d *Draft) { d.Triggers = []Trigger{{Kind: "sometimes"}} },
		"long interval":    func(d *Draft) { d.Triggers = []Trigger{{Kind: OnInterval, Every: maxEvery + 1}} },
		"bad player":       func(d *Draft) { d.Triggers = []Trigger{{Kind: OnServer, On: "joined", Players: []string{"@a"}}} },
		"bad measure":      func(d *Draft) { d.Triggers = []Trigger{{Kind: OnMetric, Measure: "luck"}} },
		"bad overlap":      func(d *Draft) { d.Overlap = "sometimes" },
		"bad time zone":    func(d *Draft) { d.TimeZone = "Mars/Olympus" },
		"two inputs alike": func(d *Draft) { d.Params = []Param{{Name: "a", Type: "text"}, {Name: "a", Type: "text"}} },
		"bad default":      func(d *Draft) { d.Params = []Param{{Name: "a", Type: "number", Default: "many"}} },
		"bad URL":          func(d *Draft) { d.Steps[0] = step("h", "http", map[string]any{"url": "http://example.com"}) },
		"host header": func(d *Draft) {
			d.Steps[0] = step("h", "http", map[string]any{"url": "https://example.com", "headers": []Header{{Name: "Host", Value: "x"}}})
		},
		"too deep": func(d *Draft) {
			s := d.Steps[0]
			for i := range maxDepth {
				s = Step{ID: "t" + string(rune('a'+i)), Kind: "try", Steps: []Step{s}}
			}
			d.Steps[0] = s
		},
	} {
		d := valid()
		change(&d)
		if _, err := d.check(); err == nil {
			t.Errorf("%s: passed", name)
		}
	}
}

// testRun returns a run of steps that needs no service.
func testRun(steps ...Step) *run {
	return &run{
		svc: &Service{}, wf: Workflow{ID: "wf", Name: "Test", Definition: Definition{Steps: steps}}, loc: time.UTC,
		data: map[string]any{"inputs": map[string]any{"limit": 3.0}}, vars: map[string]any{}, outputs: map[string]any{},
	}
}

func cond(left, op, right string) Condition {
	return Condition{Match: "all", Rules: []Rule{{Left: left, Op: op, Right: right}}}
}

func TestControlFlow(t *testing.T) {
	steps := []Step{
		{ID: "loop", Kind: "repeat", With: mustJSON(repeatSettings{Until: cond("{{vars.n}}", "ge", "{{inputs.limit}}"), Max: 10}),
			Steps: []Step{step("inc", "set", setSettings{Name: "n", Value: "1", Mode: "add"})}},
		{ID: "each", Kind: "foreach", With: mustJSON(foreachSettings{Items: "a, b, c, d", Concurrency: 3}),
			Steps: []Step{step("collect", "set", setSettings{Name: "seen", Value: "{{item | upper}}{{index}}", Mode: "append"})}},
		{ID: "pick", Kind: "switch", With: mustJSON(switchSettings{Value: "{{vars.n}}"}),
			Cases: []Case{{Value: "2", Steps: []Step{step("two", "set", setSettings{Name: "case", Value: "two"})}},
				{Value: "3", Steps: []Step{step("three", "set", setSettings{Name: "case", Value: "three"})}}},
			Else: []Step{step("other", "set", setSettings{Name: "case", Value: "other"})}},
		{ID: "both", Kind: "parallel", Branches: [][]Step{
			{step("left", "set", setSettings{Name: "left", Value: "L"})},
			{step("right", "set", setSettings{Name: "right", Value: "R"})},
		}},
		{ID: "attempt", Kind: "try",
			Steps: []Step{step("broken", "set", setSettings{Name: "n", Value: "lots", Mode: "add"}), step("skipped", "set", setSettings{Name: "never", Value: "x"})},
			Else:  []Step{step("caught", "set", setSettings{Name: "caught", Value: "{{error.step}}"})}},
		{ID: "lenient", Kind: "set", Continue: true, With: mustJSON(setSettings{Name: "n", Value: "{{vars.seen | add:1}}", Mode: "set"})},
		step("after", "set", setSettings{Name: "error", Value: "{{steps.lenient.outcome}}"}),
		{ID: "off", Kind: "set", Disabled: true, With: mustJSON(setSettings{Name: "off", Value: "x"})},
		step("end", "terminate", terminateSettings{Result: "{{vars.n}}"}),
		step("unreached", "set", setSettings{Name: "unreached", Value: "x"}),
	}
	r := testRun(steps...)
	err := (&scope{run: r}).steps(t.Context(), steps)
	if st := (*stop)(nil); !errors.As(err, &st) || st.failed {
		t.Fatalf("ended with %v", err)
	}
	seen, _ := r.vars["seen"].([]any)
	slices.SortFunc(seen, func(a, b any) int { return strings.Compare(a.(string), b.(string)) })
	want := map[string]any{"n": 3.0, "case": "three", "left": "L", "right": "R", "caught": "broken", "error": Failed, "seen": []any{"A0", "B1", "C2", "D3"}}
	if !reflect.DeepEqual(r.vars, want) || r.result != 3.0 {
		t.Fatalf("vars = %v, result = %v", r.vars, r.result)
	}
	var ids []string
	for _, rec := range r.records {
		ids = append(ids, rec.ID)
	}
	for _, id := range []string{"broken", "caught", "lenient"} {
		if !slices.Contains(ids, id) {
			t.Errorf("%s wasn't recorded: %v", id, ids)
		}
	}
	for _, id := range []string{"skipped", "off", "unreached", "two", "other"} {
		if slices.Contains(ids, id) {
			t.Errorf("%s ran: %v", id, ids)
		}
	}

	// A loop that never ends runs out of steps.
	forever := []Step{{ID: "loop", Kind: "repeat", With: mustJSON(repeatSettings{Until: cond("1", "eq", "2"), Max: maxRepeat}),
		Steps: []Step{{ID: "inner", Kind: "repeat", With: mustJSON(repeatSettings{Until: cond("1", "eq", "2"), Max: maxRepeat}),
			Steps: []Step{step("tick", "set", setSettings{Name: "n", Value: "1", Mode: "add"})}}}}}
	r = testRun(forever...)
	if err := (&scope{run: r}).steps(t.Context(), forever); err == nil || !strings.Contains(err.Error(), "more than") {
		t.Fatalf("ended with %v", err)
	}
	if len(r.records) != maxRecords || r.dropped == 0 {
		t.Fatalf("%d records, %d dropped", len(r.records), r.dropped)
	}
}

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

func TestServerEvents(t *testing.T) {
	w := watch{seen: map[string]map[string]*observed{}}
	stats := func(id string, names ...string) *noryxv1.ServerStats {
		return &noryxv1.ServerStats{Id: id, Running: true, Players: &noryxv1.Players{Online: uint32(len(names)), Names: names}}
	}
	if events := w.compare("n", []*noryxv1.ServerStats{stats("a", "Alex")}); len(events) != 0 {
		t.Fatalf("the first measurement told %v", events)
	}
	events := w.compare("n", []*noryxv1.ServerStats{stats("a", "Steve"), stats("b")})
	want := []serverEvent{{"started", "b", ""}, {"joined", "a", "Steve"}, {"left", "a", "Alex"}}
	if !sameEvents(events, want) {
		t.Fatalf("events = %v, want %v", events, want)
	}
	// A list that misses players tells nothing, and the players of a server that stopped left.
	partial := stats("a", "Steve")
	partial.Players.Online = 5
	if events := w.compare("n", []*noryxv1.ServerStats{partial, stats("b")}); len(events) != 0 {
		t.Fatalf("a partial list told %v", events)
	}
	events = w.compare("n", []*noryxv1.ServerStats{stats("b")})
	if want := []serverEvent{{"left", "a", "Steve"}, {"stopped", "a", ""}}; !sameEvents(events, want) {
		t.Fatalf("events = %v, want %v", events, want)
	}
}

func sameEvents(a, b []serverEvent) bool {
	key := func(e serverEvent) string { return e.on + e.serverID + e.player }
	sa, sb := make([]string, len(a)), make([]string, len(b))
	for i := range a {
		sa[i] = key(a[i])
	}
	for i := range b {
		sb[i] = key(b[i])
	}
	slices.Sort(sa)
	slices.Sort(sb)
	return slices.Equal(sa, sb)
}

func TestNext(t *testing.T) {
	at := time.Date(2026, 10, 8, 12, 7, 30, 0, time.UTC)
	if got := next(Trigger{Kind: OnInterval, Every: 15}, at); !got.Equal(time.Date(2026, 10, 8, 12, 15, 0, 0, time.UTC)) {
		t.Errorf("interval = %v", got)
	}
	s := &schedule.Schedule{Times: []string{"04:00"}, TimeZone: "UTC"}
	if got := next(Trigger{Kind: OnSchedule, Schedule: s}, at); !got.Equal(time.Date(2026, 10, 9, 4, 0, 0, 0, time.UTC)) {
		t.Errorf("schedule = %v", got)
	}
	if got := untilTime(at, "12:00"); got != 23*time.Hour+52*time.Minute+30*time.Second {
		t.Errorf("until 12:00 = %v", got)
	}
}

// fakeLogs is a log without entries.
type fakeLogs struct{}

func (fakeLogs) List(context.Context, logs.Filter, bool, int) ([]logs.Entry, error) { return nil, nil }
func (fakeLogs) Changed() <-chan struct{}                                           { return make(chan struct{}) }

func testService(t *testing.T) *Service {
	t.Helper()
	db, err := database.Open(filepath.Join(t.TempDir(), "master.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	s := NewService(db, Deps{Logs: fakeLogs{}})
	if err := s.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	return s
}

// While a workflow runs, a trigger skips, queues or runs in parallel, as the workflow says.
func TestOverlap(t *testing.T) {
	s := testService(t)
	for _, c := range []struct {
		overlap   string
		runs, max int
	}{{Skip, 1, 1}, {Queue, 3, 1}, {Parallel, 3, 3}} {
		wf, err := s.Create(t.Context(), Draft{Name: c.overlap, Enabled: true, Definition: Definition{
			Overlap:  c.overlap,
			Triggers: []Trigger{{Kind: OnWebhook}},
			Steps:    []Step{step("pause", "wait", waitSettings{For: "0.2", Unit: "seconds"})},
		}}, Author{})
		if err != nil {
			t.Fatal(err)
		}
		most := 0
		for range 3 {
			s.fire(wf.ID, OnWebhook, nil)
			time.Sleep(10 * time.Millisecond)
			most = max(most, s.running(wf.ID))
		}
		for deadline := time.Now().Add(5 * time.Second); s.running(wf.ID) > 0; time.Sleep(10 * time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatalf("%s: still running", c.overlap)
			}
		}
		runs, err := s.Runs(t.Context(), wf.ID)
		if err != nil || len(runs) != c.runs || most != c.max {
			t.Errorf("%s: %d runs, at most %d at once, %v", c.overlap, len(runs), most, err)
		}
	}
}

func (s *Service) running(id string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.active[id].running
}

// Runs of a workflow are cancelled, and calls of other workflows pass inputs and return results.
func TestCallsAndCancel(t *testing.T) {
	s := testService(t)
	child, err := s.Create(t.Context(), Draft{Name: "Child", Definition: Definition{
		Params: []Param{{Name: "n", Type: "number", Required: true}},
		Steps:  []Step{step("double", "terminate", terminateSettings{Result: "{{inputs.n | mul:2}}"})},
	}}, Author{})
	if err != nil {
		t.Fatal(err)
	}
	parent, err := s.Create(t.Context(), Draft{Name: "Parent", Definition: Definition{
		Steps: []Step{
			step("call", "call", callSettings{Workflow: child.ID, Inputs: map[string]string{"n": "21"}, Wait: true}),
			step("keep", "set", setSettings{Name: "answer", Value: "{{steps.call.result}}"}),
			step("pause", "wait", waitSettings{For: "1", Unit: "hours"}),
		},
	}}, Author{})
	if err != nil {
		t.Fatal(err)
	}
	run, err := s.RunNow(t.Context(), parent.ID, nil, Author{Name: "admin"})
	if err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		got, err := s.Run(t.Context(), parent.ID, run.ID)
		if err == nil && len(got.Steps) == 3 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("run = %+v, %v", got, err)
		}
	}
	if err := s.Cancel(parent.ID, run.ID); err != nil {
		t.Fatal(err)
	}
	var got Run
	for deadline := time.Now().Add(5 * time.Second); got.Outcome != Cancelled; time.Sleep(10 * time.Millisecond) {
		if got, err = s.Run(t.Context(), parent.ID, run.ID); err != nil || time.Now().After(deadline) {
			t.Fatalf("run = %+v, %v", got, err)
		}
	}
	var out struct{ Value float64 }
	if err := json.Unmarshal(got.Steps[1].Output, &out); err != nil || out.Value != 42 {
		t.Fatalf("the child returned %s", got.Steps[1].Output)
	}
	children, err := s.Runs(t.Context(), child.ID)
	if err != nil || len(children) != 1 || children[0].Trigger != ByWorkflow || children[0].StartedBy != "Parent" {
		t.Fatalf("runs of the child = %+v, %v", children, err)
	}
}

// A workflow needs what the workflows it runs need, also through others, so that nobody runs
// a workflow through another one that they couldn't run themselves.
func TestCallsNeedWhatTheyRun(t *testing.T) {
	s := testService(t)
	create := func(name string, by Author, steps ...Step) (Workflow, error) {
		return s.Create(t.Context(), Draft{Name: name, Definition: Definition{Steps: steps}}, by)
	}
	restart, err := create("Restart", admin, step("restart", "restart", map[string]any{"targets": []map[string]string{{"nodeId": "n1"}}}))
	if err != nil {
		t.Fatal(err)
	}
	middle, err := create("Middle", admin, step("call", "call", callSettings{Workflow: restart.ID}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := create("Outer", Author{}, step("call", "call", callSettings{Workflow: middle.ID})); err == nil {
		t.Error("someone who may not restart servers saved a workflow that restarts them through another one")
	}
	outer, err := create("Outer", admin, step("call", "call", callSettings{Workflow: middle.ID}))
	if err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	needs := s.active[outer.ID].needs
	s.mu.Unlock()
	if !slices.Contains(needs, access.ServersRestart) {
		t.Errorf("the workflow that restarts servers through another one needs %v", needs)
	}
}
