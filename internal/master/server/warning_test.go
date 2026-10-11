package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"google.golang.org/grpc"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	"github.com/QwikByte/noryx/internal/master/access"
	"github.com/QwikByte/noryx/internal/master/auth"
	"github.com/QwikByte/noryx/internal/master/operation"
)

// A stop or restart with a warning warns the players of the running game servers, again 5
// minutes and 1 minute before, and only then stops or restarts. Until then, it can be
// cancelled, which stops nothing.
func TestWarning(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		agent := &recorder{servers: []*noryxv1.Server{
			{Id: "s1", Type: noryxv1.ServerType_SERVER_TYPE_PAPER, State: noryxv1.ServerState_SERVER_STATE_RUNNING},
			{Id: "p1", Type: noryxv1.ServerType_SERVER_TYPE_VELOCITY, State: noryxv1.ServerState_SERVER_STATE_RUNNING},
		}}
		ops := operation.New(0)
		conf := &config{warnings: DefaultWarnings()}
		h := NewHandler(fakeNodes{conn: agent}, fakeNetworks{}, nil, nil, nil, ops, NewMoves(), nil, conf)
		mux := http.NewServeMux()
		mux.HandleFunc("POST /api/nodes/{node}/servers/{id}/restart", h.power("restart"))
		mux.HandleFunc("POST /api/servers/actions", h.bulk)
		admin := access.WithGrants(t.Context(), access.Admin())
		post := func(ctx context.Context, path, body string, status int) operation.Operation {
			t.Helper()
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequestWithContext(ctx, http.MethodPost, path, strings.NewReader(body)))
			var op operation.Operation
			if rec.Code != status || status == http.StatusAccepted && json.Unmarshal(rec.Body.Bytes(), &op) != nil {
				t.Fatalf("%s %s: status %d: %s", path, body, rec.Code, rec.Body)
			}
			return op
		}

		op := post(admin, "/api/nodes/n1/servers/s1/restart", `{"warning": {"minutes": 10}}`, http.StatusAccepted)
		if !slices.Equal(op.Steps, []string{"warn", "restart"}) || !op.Cancellable {
			t.Fatalf("operation = %+v", op)
		}
		for _, wait := range []time.Duration{0, 5 * time.Minute, 4 * time.Minute, time.Minute} {
			time.Sleep(wait)
			synctest.Wait()
		}
		want := []string{"ListServers", "SendCommand s1 say The server restarts in 10 min.", "SendCommand s1 say The server restarts in 5 min.",
			"SendCommand s1 say The server restarts in 1 min.", "RestartServer s1"}
		if got := agent.taken(); !slices.Equal(got, want) {
			t.Fatalf("calls %q, want %q", got, want)
		}

		// Proxies get no warning.
		op = post(admin, "/api/servers/actions", `{"action": "stop", "servers": [{"nodeId": "n1", "serverId": "s1"}, {"nodeId": "n1", "serverId": "p1"}],
			"warning": {"minutes": 2, "message": "Bye in {minutes} min"}}`, http.StatusAccepted)
		synctest.Wait()
		if _, err := ops.Cancel(admin, op.ID, auth.User{}, access.Admin()); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		if got := agent.taken(); !slices.Equal(got, []string{"ListServers", "SendCommand s1 say Bye in 2 min"}) {
			t.Fatalf("calls %q", got)
		}
		if ops := ops.List(auth.User{}, access.Admin()); !ops[0].Cancelled || ops[0].FinishedAt == nil || ops[0].Steps[ops[0].Step] != "warn" {
			t.Fatalf("cancelled operation = %+v", ops[0])
		}

		// Warnings take 1 to 10 minutes, only before stops and restarts, and a message of one's
		// own needs the permission to send console commands.
		post(admin, "/api/nodes/n1/servers/s1/restart", `{"warning": {"minutes": 11}}`, http.StatusBadRequest)
		post(admin, "/api/nodes/n1/servers/s1/restart", `{"warning": {"minutes": 1, "message": "two\nlines"}}`, http.StatusBadRequest)
		post(admin, "/api/servers/actions", `{"action": "start", "servers": [{"nodeId": "n1", "serverId": "s1"}], "warning": {"minutes": 1}}`, http.StatusBadRequest)
		post(t.Context(), "/api/nodes/n1/servers/s1/restart", `{"warning": {"minutes": 1, "message": "op me"}}`, http.StatusForbidden)

		// The settings decide the texts, the steps, the longest lead time and how warnings show,
		// as they say when a warning is asked for. A text becomes JSON only with an encoder.
		conf.warnings = Warnings{Restart: `Neustart in {minutes} Minuten, "bald"`, Stop: "Stopp", Steps: []uint32{30, 3}, MaxMinutes: 30, Kind: "title"}
		post(admin, "/api/nodes/n1/servers/s1/restart", `{"warning": {"minutes": 31}}`, http.StatusBadRequest)
		post(admin, "/api/nodes/n1/servers/s1/restart", `{"warning": {"minutes": 1, "message": "`+strings.Repeat("<", maxWarningMessage)+`"}}`, http.StatusBadRequest)
		post(admin, "/api/nodes/n1/servers/s1/restart", `{"warning": {"minutes": 20}}`, http.StatusAccepted)
		conf.warnings = DefaultWarnings()
		for _, wait := range []time.Duration{0, 17 * time.Minute, 3 * time.Minute} {
			time.Sleep(wait)
			synctest.Wait()
		}
		want = []string{"ListServers", `SendCommand s1 minecraft:title @a title {"text":"Neustart in 20 Minuten, \"bald\""}`,
			`SendCommand s1 minecraft:title @a title {"text":"Neustart in 3 Minuten, \"bald\""}`, "RestartServer s1"}
		if got := agent.taken(); !slices.Equal(got, want) {
			t.Fatalf("calls %q, want %q", got, want)
		}
	})
}

// config is the Config of tests, whose warnings they change.
type config struct{ warnings Warnings }

func (c *config) NewServers() NewServers { return DefaultNewServers() }
func (c *config) Warnings() Warnings     { return c.warnings }

// The settings of warnings have texts like a warning of one's own, a few steps below the
// longest lead time of up to an hour, and show in the chat, as a title or above the hotbar.
func TestValidateWarnings(t *testing.T) {
	if err := DefaultWarnings().Validate(); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*Warnings){
		"no text":                 func(w *Warnings) { w.Stop = "" },
		"two lines":               func(w *Warnings) { w.Restart = "two\nlines" },
		"a text too long":         func(w *Warnings) { w.Restart = strings.Repeat("x", maxWarningMessage+1) },
		"a title too long":        func(w *Warnings) { w.Restart, w.Kind = strings.Repeat("&", maxWarningMessage), "title" },
		"another kind":            func(w *Warnings) { w.Kind = "bossbar" },
		"no lead time":            func(w *Warnings) { w.MaxMinutes, w.Steps = 0, nil },
		"more than an hour":       func(w *Warnings) { w.MaxMinutes = 61 },
		"a step at the lead time": func(w *Warnings) { w.Steps = []uint32{10} },
		"a step of 0 minutes":     func(w *Warnings) { w.Steps = []uint32{0} },
		"more than a handful":     func(w *Warnings) { w.MaxMinutes, w.Steps = 60, []uint32{30, 20, 10, 5, 2, 1} },
	} {
		w := DefaultWarnings()
		change(&w)
		if err := w.Validate(); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	w := Warnings{Restart: "Neustart in {minutes} Minuten", Stop: "Stopp in {minutes} Minuten", Steps: []uint32{30, 10, 5, 2, 1}, MaxMinutes: 60, Kind: "actionbar"}
	if err := w.Validate(); err != nil {
		t.Fatal(err)
	}
	if got, err := w.Commands(w.Restart, 5); err != nil || !slices.Equal(got, []string{`minecraft:title @a actionbar {"text":"Neustart in 5 Minuten"}`}) {
		t.Fatalf("commands %q, %v", got, err)
	}
}

// recorder is an agent with servers that records the calls to it.
type recorder struct {
	fakeConn
	servers []*noryxv1.Server
	mu      sync.Mutex
	calls   []string
}

func (a *recorder) Invoke(_ context.Context, method string, args, reply any, _ ...grpc.CallOption) error {
	call := method[strings.LastIndex(method, "/")+1:]
	switch req := args.(type) {
	case *noryxv1.SendCommandRequest:
		call += " " + req.GetId() + " " + req.GetCommand()
	case interface{ GetId() string }:
		call += " " + req.GetId()
	}
	if reply, ok := reply.(*noryxv1.ListServersResponse); ok {
		reply.Servers = a.servers
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.calls = append(a.calls, call)
	return nil
}

// taken returns the calls so far and forgets them.
func (a *recorder) taken() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	calls := a.calls
	a.calls = nil
	return calls
}
