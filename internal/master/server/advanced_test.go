package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QwikByte/noryx/internal/master/operation"
)

// An agent that doesn't know the advanced settings yet leaves them out of its answer when a
// server is created or changed, which the panel then shows as a warning.
func TestWarnsOfOlderAgentsAdvanced(t *testing.T) {
	h := NewHandler(Deps{Nodes: fakeNodes{conn: olderAgent{}}, Networks: fakeNetworks{}, Operations: operation.New(time.Second), Moves: NewMoves()})
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/nodes/{node}/servers", h.create)
	mux.HandleFunc("PUT /api/nodes/{node}/servers/{id}", h.update)
	for _, req := range []struct{ method, path, body string }{
		{http.MethodPost, "/api/nodes/n1/servers", `"type": "paper", "acceptEula": true, `},
		{http.MethodPut, "/api/nodes/n1/servers/s1", ""},
	} {
		for settings, warns := range map[string]bool{
			``: false,
			`, "healthStart": 2, "crashLimit": 5, "stableAfter": 10, "unhealthyRestart": 0`: false,
			`, "healthStart": 10`:     true,
			`, "crashLimit": 2`:       true,
			`, "stableAfter": 30`:     true,
			`, "unhealthyRestart": 5`: true,
			`, "processLimit": 1024`:  false,
			`, "processLimit": 4096`:  true,
			`, "initMemoryMb": 512`:   true,
		} {
			body := `{` + req.body + `"name": "Lobby", "memoryMb": 1024, "port": 25565` + settings + `}`
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequest(req.method, req.path, strings.NewReader(body)))
			var res struct {
				advanced
				Warning string
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil || rec.Code >= 300 {
				t.Fatalf("%s %s: status %d: %s", req.method, body, rec.Code, rec.Body)
			}
			if res.advanced != (advanced{HealthStart: 2, CrashLimit: 5, StableAfter: 10, ProcessLimit: 1024}) || (res.Warning != "") != warns {
				t.Errorf("%s %s: %+v", req.method, body, res)
			}
		}
	}
}
