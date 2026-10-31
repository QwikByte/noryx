package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestCanceledRequestsAreNotLogged(t *testing.T) {
	var logged strings.Builder
	defer slog.SetDefault(slog.Default())
	slog.SetDefault(slog.New(slog.NewTextHandler(&logged, nil)))

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	gone := httptest.NewRequestWithContext(ctx, http.MethodGet, "/api/servers/s1/logs", nil)
	for _, err := range []error{status.Error(codes.Canceled, "context canceled"), fmt.Errorf("query: %w", context.Canceled)} {
		w := httptest.NewRecorder()
		if WriteError(w, gone, err); w.Code != statusClientClosed {
			t.Errorf("%v: status %d", err, w.Code)
		}
	}
	if msg := Message(fmt.Errorf("query: %w", context.Canceled)); msg != "canceled" {
		t.Errorf("Message = %q", msg)
	}
	if logged.Len() != 0 {
		t.Fatalf("logged %q", logged.String())
	}

	// The connection to an agent closed while the client still waits.
	w := httptest.NewRecorder()
	WriteError(w, httptest.NewRequest(http.MethodGet, "/api/servers/s1/logs", nil), status.Error(codes.Canceled, "grpc: the client connection is closing"))
	if w.Code != http.StatusInternalServerError || !strings.Contains(logged.String(), "level=ERROR") {
		t.Fatalf("status %d, logged %q", w.Code, logged.String())
	}
}

// Errors tell the field of the request they are about, also within objects, next to their message
// and code.
func TestErrorsNameTheirField(t *testing.T) {
	for _, tc := range []struct {
		err  error
		body string
	}{
		{Errorf(http.StatusBadRequest, "Choose a server."), `{"error":"Choose a server."}`},
		{Confirm("move", "Move it?"), `{"error":"Move it?","code":"move"}`},
		{FieldErrorf(http.StatusConflict, "port", "Port %d is used.", 25565), `{"error":"Port 25565 is used.","field":"port"}`},
		{Within("newServers", FieldErrorf(http.StatusBadRequest, "memoryMb", "Too much.")), `{"error":"Too much.","field":"newServers.memoryMb"}`},
		{Within("thresholds", fmt.Errorf("check: %w", Errorf(http.StatusBadRequest, "Too high."))), `{"error":"Too high.","field":"thresholds"}`},
		{Within("name", status.Error(codes.InvalidArgument, "Invalid.")), `{"error":"Invalid."}`},
	} {
		w := httptest.NewRecorder()
		WriteError(w, httptest.NewRequest(http.MethodPost, "/api/servers", nil), tc.err)
		if w.Code >= 500 || strings.TrimSpace(w.Body.String()) != tc.body {
			t.Errorf("%v: status %d, body %s", tc.err, w.Code, w.Body)
		}
	}
}

// publicErr is an error whose text users may read, like the answer of a storage.
type publicErr string

func (e publicErr) Error() string { return string(e) }
func (publicErr) Public()         {}

// Users learn the messages of API errors, of agents and public errors after the texts that wrap
// them, never the "rpc error" of a status nor what isn't meant for them, which is logged.
func TestWrappedErrors(t *testing.T) {
	refused := FieldErrorf(http.StatusConflict, "port", "Port 25565 is used.")
	agent := status.Error(codes.InvalidArgument, "the name is taken")
	internal := errors.New("database is locked")
	defer slog.SetDefault(slog.Default())
	for _, tc := range []struct {
		name   string
		err    error
		status int
		body   string
		logged bool
	}{
		{"API error", refused, 409, `{"error":"Port 25565 is used.","field":"port"}`, false},
		{"wrapped API error", fmt.Errorf("Survival: %w", refused), 409, `{"error":"Survival: Port 25565 is used.","field":"port"}`, false},
		{"twice wrapped", fmt.Errorf("Network: %w", fmt.Errorf("Survival: %w", refused)), 409, `{"error":"Network: Survival: Port 25565 is used.","field":"port"}`, false},
		{"status of an agent", agent, 400, `{"error":"the name is taken"}`, false},
		{"wrapped status", fmt.Errorf("lobby: %w", agent), 400, `{"error":"lobby: the name is taken"}`, false},
		{"status joined with nil", errors.Join(agent, nil), 400, `{"error":"the name is taken"}`, false},
		{"status of another code", status.Error(codes.DataLoss, "the disk failed"), 502, `{"error":"the disk failed"}`, false},
		{"public error", fmt.Errorf("the node of the copies: %w", publicErr("the storage answered 403 Forbidden: AccessDenied")), 502,
			`{"error":"the node of the copies: the storage answered 403 Forbidden: AccessDenied"}`, false},
		{"join with a hidden part", errors.Join(agent, fmt.Errorf("forget it: %w", internal)), 400, `{"error":"the name is taken"}`, true},
		{"join of shown parts", errors.Join(refused, agent), 409, `{"error":"Port 25565 is used.\nthe name is taken","field":"port"}`, false},
		{"internal error", fmt.Errorf("save: %w", internal), 500, `{"error":"internal error"}`, true},
		{"wrapper without the text of its error", wrapper{refused}, 409, `{"error":"Port 25565 is used.","field":"port"}`, false},
	} {
		var logged strings.Builder
		slog.SetDefault(slog.New(slog.NewTextHandler(&logged, nil)))
		w := httptest.NewRecorder()
		WriteError(w, httptest.NewRequest(http.MethodPost, "/api/servers", nil), tc.err)
		if body := strings.TrimSpace(w.Body.String()); w.Code != tc.status || body != tc.body || (logged.Len() > 0) != tc.logged {
			t.Errorf("%s: %d %s, logged %q", tc.name, w.Code, body, logged.String())
		}
		var message map[string]string
		_ = json.Unmarshal(w.Body.Bytes(), &message)
		if got := Message(tc.err); got != message["error"] {
			t.Errorf("%s: Message = %q, want %q", tc.name, got, message["error"])
		}
	}
}

// wrapper wraps an error without its text.
type wrapper struct{ err error }

func (w wrapper) Error() string { return "something else" }
func (w wrapper) Unwrap() error { return w.err }

// A client gets bodyTime to send the body of a JSON request; one that sends it slowly is cut
// off, while a request whose body arrived in time keeps running after the deadline.
func TestBodyTime(t *testing.T) {
	defer func(d time.Duration) { bodyTime = d }(bodyTime)
	bodyTime = 200 * time.Millisecond
	handled := make(chan error, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var v map[string]any
		err := ReadJSON(w, r, &v)
		if err == nil {
			time.Sleep(2 * bodyTime) // handling outlasts the deadline
			err = r.Context().Err()
		}
		handled <- err
	}))
	defer srv.Close()

	res, err := http.Post(srv.URL, "application/json", strings.NewReader(`{"a": 1}`))
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if err := <-handled; err != nil {
		t.Fatalf("a body sent in time: %v", err)
	}

	conn, err := net.Dial("tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	fmt.Fprint(conn, "POST / HTTP/1.1\r\nHost: x\r\nContent-Type: application/json\r\nContent-Length: 100\r\n\r\n{")
	select {
	case err := <-handled:
		if err == nil {
			t.Fatal("a body that never ended was read")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a slow body held the request")
	}
}
