package docker

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"math"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/events"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	"github.com/QwikByte/noryx/internal/agent/runtime"
)

func TestCrashState(t *testing.T) {
	health := func(s container.HealthStatus) *container.HealthSummary { return &container.HealthSummary{Status: s} }
	for _, tc := range []struct {
		name      string
		c         container.Summary
		state     noryxv1.ServerState
		unhealthy bool
		inspect   bool
	}{
		{"running", container.Summary{State: container.StateRunning, Status: "Up 2 hours (healthy)", Health: health(container.Healthy)}, noryxv1.ServerState_SERVER_STATE_RUNNING, false, false},
		{"running without a health check", container.Summary{State: container.StateRunning, Status: "Up 2 hours"}, noryxv1.ServerState_SERVER_STATE_RUNNING, false, false},
		// It runs, so restarts, stops and the console work as usual.
		{"unhealthy", container.Summary{State: container.StateRunning, Status: "Up 2 hours (unhealthy)", Health: health(container.Unhealthy)}, noryxv1.ServerState_SERVER_STATE_RUNNING, true, false},
		// Starting again after a crash, which only inspecting tells.
		{"starting", container.Summary{State: container.StateRunning, Health: health(container.Starting)}, noryxv1.ServerState_SERVER_STATE_STARTING, false, true},
		{"restarting", container.Summary{State: container.StateRestarting, Status: "Restarting (1) 5 seconds ago", Health: health(container.Unhealthy)}, noryxv1.ServerState_SERVER_STATE_CRASHING, false, true},
		{"never started", container.Summary{State: container.StateCreated, Status: "Created"}, noryxv1.ServerState_SERVER_STATE_STOPPED, false, false},
		{"stopped cleanly", container.Summary{State: container.StateExited, Status: "Exited (0) 3 minutes ago"}, noryxv1.ServerState_SERVER_STATE_STOPPED, false, false},
		{"stopped after a crash", container.Summary{State: container.StateExited, Status: "Exited (1) 3 minutes ago"}, noryxv1.ServerState_SERVER_STATE_STOPPED, false, true},
	} {
		if got := state(tc.c); got != tc.state {
			t.Errorf("%s: state %v, want %v", tc.name, got, tc.state)
		}
		if got := unhealthy(tc.c); got != tc.unhealthy {
			t.Errorf("%s: unhealthy %v, want %v", tc.name, got, tc.unhealthy)
		}
		if got := mayHaveCrashed(tc.c); got != tc.inspect {
			t.Errorf("%s: may have crashed %v, want %v", tc.name, got, tc.inspect)
		}
	}
}

// Docker starts a container again right away after its first crash, before the agent inspects
// it, and later after a delay.
func TestRestarts(t *testing.T) {
	for _, tc := range []struct {
		name  string
		state container.State
		want  bool
	}{
		{"running again", container.State{Running: true}, true},
		{"waiting to start again", container.State{Running: true, Restarting: true}, true},
		{"stopped", container.State{Status: container.StateExited, ExitCode: 143}, false},
	} {
		if got := restarts(&tc.state); got != tc.want {
			t.Errorf("%s: restarts = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// Every crash is logged once, the last of maxCrashes in a row as the server is stopped.
func TestNoteCrash(t *testing.T) {
	maxCrashes, stableAfter := noryxv1.DefaultCrashLimit, noryxv1.DefaultStableAfter // as the server keeps the defaults
	logged := captureLog(t)
	inRow := map[string]int{}
	crash := func(ran string, restarts int) bool {
		return noteCrash(inRow, events.Actor{ID: "c1", Attributes: map[string]string{"execDuration": ran, "exitCode": "1"}}, runtime.Spec{ID: "s1"}, restarts)
	}
	for i := 1; i < maxCrashes; i++ {
		if crash("3", i) {
			t.Fatalf("crash %d stops the server", i)
		}
	}
	if !crash("3", maxCrashes) {
		t.Fatal("the last crash in a row doesn't stop the server")
	}
	// One that ran long enough starts a new row.
	if crash(strconv.Itoa(int(stableAfter.Seconds())), maxCrashes+1) || inRow["c1"] != 1 {
		t.Fatalf("crash after a stable run: %d in a row", inRow["c1"])
	}
	entries := logged()
	if len(entries) != maxCrashes+1 {
		t.Fatalf("logged %d entries, want %d: %v", len(entries), maxCrashes+1, entries)
	}
	for i, e := range entries {
		want := min(i+1, maxCrashes)
		if i == maxCrashes {
			want = 1
		}
		if e["level"] != "WARN" || e["server"] != "s1" || e["exit_code"] != "1" || e["crashes"] != float64(want) {
			t.Errorf("entry %d = %v, want crash %d of s1", i, e, want)
		}
	}
	if !strings.HasPrefix(entries[maxCrashes-1]["msg"].(string), "Stop a server") {
		t.Errorf("last crash in a row: %v", entries[maxCrashes-1])
	}
}

// A server's own crash limit and time after which a run is stable apply.
func TestCrashLimitOfServer(t *testing.T) {
	captureLog(t)
	inRow := map[string]int{}
	spec := runtime.Spec{ID: "s1", CrashLimit: 2, StableAfter: 30}
	crash := func(ran time.Duration) bool {
		actor := events.Actor{ID: "c1", Attributes: map[string]string{"execDuration": strconv.Itoa(int(ran.Seconds()))}}
		return noteCrash(inRow, actor, spec, math.MaxInt)
	}
	if crash(20*time.Minute) || !crash(20*time.Minute) {
		t.Fatal("the second crash in a row within 30 minutes doesn't stop the server")
	}
	if crash(30*time.Minute) || inRow["c1"] != 1 {
		t.Fatalf("crash after a stable run: %d in a row", inRow["c1"])
	}
}

// A server that stays unhealthy for its minutes is restarted, which counts as a crash and keeps
// its row, also as the runtime counts restarts anew; that its crash limit is stopped instead.
func TestRestartHanging(t *testing.T) {
	var mu sync.Mutex
	status, stops := container.Healthy, 0
	labels := map[string]string{labelManaged: "true", labelSpec: `{"id":"s1","crashLimit":3,"stableAfter":5,"unhealthyRestart":2}`}
	d := fakeEngine(t, false, "", map[string]http.HandlerFunc{
		"GET /v1.41/containers/json": func(w http.ResponseWriter, _ *http.Request) {
			mu.Lock()
			defer mu.Unlock()
			c := container.Summary{ID: "c1", State: container.StateRunning, Health: &container.HealthSummary{Status: status}, Labels: labels}
			_ = json.NewEncoder(w).Encode([]container.Summary{c, {ID: "c2", State: container.StateRunning, Labels: map[string]string{labelSpec: `{"id":"s2"}`}}})
		},
		"GET /v1.41/containers/{id}/json": reply(container.InspectResponse{
			ID: "c1", Config: &container.Config{Labels: labels}, State: &container.State{Status: container.StateExited},
		}),
		"POST /v1.41/containers/{id}/stop": func(http.ResponseWriter, *http.Request) {
			mu.Lock()
			defer mu.Unlock()
			stops++
		},
	})
	set := func(s container.HealthStatus) {
		mu.Lock()
		defer mu.Unlock()
		status = s
	}
	stopped := func() int {
		time.Sleep(100 * time.Millisecond) // the restart runs in the background
		mu.Lock()
		defer mu.Unlock()
		return stops
	}
	logged := captureLog(t)
	w := watched{inRow: map[string]int{}, checks: map[string]*checks{}, restarting: map[string]bool{}, carried: map[string]int{}}
	// It fails for a while after it passed for 4 minutes, less than its 5 to be stable.
	d.restartHanging(t.Context(), w)
	w.checks["c1"].passes = time.Now().Add(-4 * time.Minute)
	set(container.Unhealthy)
	d.restartHanging(t.Context(), w)
	if w.checks["c1"].fails.IsZero() || len(w.checks) != 1 || stopped() != 0 {
		t.Fatalf("checks %v, stopped %d times", w.checks, stops)
	}
	w.checks["c1"].fails = time.Now().Add(-2 * time.Minute)
	w.inRow["c1"] = 1 // crashed before
	d.restartHanging(t.Context(), w)
	if !w.restarting["c1"] || w.inRow["c1"] != 2 || w.carried["c1"] != 2 || stopped() != 1 {
		t.Fatalf("restart: restarting %v, in a row %v, carried %v, stopped %d times", w.restarting, w.inRow, w.carried, stops)
	}
	// Its stop by the agent keeps the row, and a crash after the start continues it, while Docker
	// counts one restart.
	d.crashed(t.Context(), events.Actor{ID: "c1", Attributes: labels}, w)
	if len(w.restarting) != 0 || w.inRow["c1"] != 2 {
		t.Fatalf("after the stop: restarting %v, in a row %v", w.restarting, w.inRow)
	}
	spec, _ := specOf(labels)
	if !noteCrash(w.inRow, events.Actor{ID: "c1", Attributes: map[string]string{"execDuration": "200"}}, spec, 1+w.carried["c1"]) {
		t.Fatal("the third crash in a row doesn't stop the server")
	}
	// The fake runtime doesn't start it again, which is logged in between.
	entries := slices.DeleteFunc(logged(), func(e map[string]any) bool { return e["crashes"] == nil })
	if len(entries) != 2 || !strings.HasPrefix(entries[0]["msg"].(string), "Restart a server") || entries[0]["crashes"] != float64(2) ||
		!strings.HasPrefix(entries[1]["msg"].(string), "Stop a server that crashed 3 times") {
		t.Fatalf("logged %v", entries)
	}
	// One that was healthy for long enough starts a row; the crash limit stops it instead.
	w.inRow["c1"] = 2
	w.checks["c1"] = &checks{passes: time.Now().Add(-time.Hour), fails: time.Now().Add(-3 * time.Minute)}
	d.restartHanging(t.Context(), w)
	if w.inRow["c1"] != 1 || stopped() != 2 {
		t.Fatalf("after a stable run: %d in a row", w.inRow["c1"])
	}
	w.restarting = map[string]bool{}
	w.inRow["c1"] = 2
	w.checks["c1"] = &checks{fails: time.Now().Add(-3 * time.Minute)}
	d.restartHanging(t.Context(), w)
	if e := logged()[len(logged())-1]; !strings.HasPrefix(e["msg"].(string), "Stop a server whose health check") || w.restarting["c1"] || stopped() != 3 {
		t.Fatalf("crash limit: %v, restarting %v", e, w.restarting)
	}
}

// A failing health check is logged once, and passing again only after it failed.
func TestHealth(t *testing.T) {
	logged := captureLog(t)
	unhealthy := map[string]bool{}
	labels := map[string]string{labelSpec: `{"ID":"s1"}`}
	for _, action := range []events.Action{
		events.ActionHealthStatusHealthy, // after the start
		events.ActionHealthStatusUnhealthy,
		events.ActionHealthStatusUnhealthy,
		events.ActionHealthStatusHealthy,
	} {
		health(events.Message{Action: action, Actor: events.Actor{ID: "c1", Attributes: labels}}, unhealthy)
	}
	entries := logged()
	if len(entries) != 2 || entries[0]["level"] != "WARN" || entries[1]["level"] != "INFO" || entries[0]["server"] != "s1" {
		t.Fatalf("logged %v, want a warning and the recovery", entries)
	}
}

// captureLog sends the default logger's entries to a buffer, whose entries the returned
// function decodes.
func captureLog(t *testing.T) func() []map[string]any {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return func() []map[string]any {
		var entries []map[string]any
		for line := range strings.Lines(buf.String()) {
			var e map[string]any
			if err := json.Unmarshal([]byte(line), &e); err != nil {
				t.Fatal(err)
			}
			entries = append(entries, e)
		}
		return entries
	}
}

// The restart of a server that hangs leaves it stopped if someone stops it meanwhile, e.g. an
// operator who stops it while the agent waits for it to stop.
func TestRestartUnlessStopped(t *testing.T) {
	var mu sync.Mutex
	var d *Docker
	stopping, starts := false, 0
	labels := map[string]string{labelManaged: "true", labelSpec: `{"id":"s1"}`}
	d = fakeEngine(t, false, "", map[string]http.HandlerFunc{
		"GET /v1.41/containers/{id}/json": reply(container.InspectResponse{
			ID: "c1", Config: &container.Config{Labels: labels}, State: &container.State{Status: container.StateRunning},
		}),
		"POST /v1.41/containers/{id}/stop": func(http.ResponseWriter, *http.Request) {
			mu.Lock()
			concurrent := stopping
			mu.Unlock()
			if concurrent {
				d.stopsOf("s1").Add(1) // the operator's stop, while the agent's waits
			}
		},
		"POST /v1.41/containers/{id}/start": func(http.ResponseWriter, *http.Request) {
			mu.Lock()
			defer mu.Unlock()
			starts++
		},
	})
	if err := d.restartUnlessStopped(t.Context(), "s1"); err != nil || starts != 1 {
		t.Fatalf("restart: %v, started %d times", err, starts)
	}
	mu.Lock()
	stopping = true
	mu.Unlock()
	if err := d.restartUnlessStopped(t.Context(), "s1"); err != nil || starts != 1 {
		t.Fatalf("restart with a stop meanwhile: %v, started %d times", err, starts)
	}
}

// When the events of the runtime stop, e.g. as it restarts, the agent connects again and asks
// for those after the last one it saw, so that a crash in the gap is counted.
func TestWatchReplaysGap(t *testing.T) {
	captureLog(t)
	var mu sync.Mutex
	var since []string
	actor := events.Actor{ID: "c1", Attributes: map[string]string{labelManaged: "true", labelSpec: `{"id":"s1"}`, "exitCode": "1"}}
	at := time.Unix(1_800_000_000, 5)
	d := fakeEngine(t, false, "", map[string]http.HandlerFunc{
		"GET /v1.41/events": func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			since = append(since, r.URL.Query().Get("since"))
			first := len(since) == 1
			mu.Unlock()
			if first { // an event, then the runtime goes away
				_ = json.NewEncoder(w).Encode(events.Message{Action: events.ActionHealthStatusUnhealthy, Actor: actor, TimeNano: at.UnixNano()})
			} else { // the crash meanwhile
				_ = json.NewEncoder(w).Encode(events.Message{Action: events.ActionDie, Actor: actor, TimeNano: at.Add(time.Second).UnixNano()})
			}
		},
		"GET /v1.41/containers/{id}/json": reply(container.InspectResponse{RestartCount: 1, State: &container.State{Running: true}}),
		"GET /v1.41/containers/json":      reply([]container.Summary{}),
	})
	w := watched{inRow: map[string]int{}, unhealthy: map[string]bool{}, restarting: map[string]bool{}, carried: map[string]int{}}
	var from time.Time
	for range 2 {
		if err := d.watch(t.Context(), w, &from); err == nil {
			t.Fatal("the events didn't end")
		}
	}
	if !slices.Equal(since, []string{"", "1800000000.000001005"}) {
		t.Errorf("since = %q", since)
	}
	if w.inRow["c1"] != 1 {
		t.Errorf("crashes in a row: %d, want 1", w.inRow["c1"])
	}
}
