package httpapi

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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
