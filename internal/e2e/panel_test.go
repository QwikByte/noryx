package e2e

import (
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	masterapp "github.com/QwikByte/noryx/internal/master/app"
)

// TestPanelInBrowser runs the smoke test of the panel in web/e2e in a browser: it signs in, opens the overview and
// the console of a server of a fake node. It needs the panel built into the master, Node.js and Chromium, so it only
// runs with NORYX_PANEL_TEST set, after `npm ci && npm run build && npx playwright install chromium` in web:
//
//	NORYX_PANEL_TEST=1 go test -tags ui -run TestPanelInBrowser ./internal/e2e
func TestPanelInBrowser(t *testing.T) {
	if os.Getenv("NORYX_PANEL_TEST") == "" {
		t.Skip("NORYX_PANEL_TEST is not set")
	}
	m := startMaster(t)
	svc := m.services(t)
	srv := httptest.NewUnstartedServer(masterapp.Handler(svc))
	// Chromium turns down the test's certificate before it is told to accept it, which the server would log.
	srv.Config.ErrorLog = log.New(io.Discard, "", 0)
	srv.StartTLS()
	t.Cleanup(srv.Close)
	res, err := srv.Client().Get(srv.URL + "/login")
	check(t, err)
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatal("The master has no panel: build it with npm run build in web, and test with -tags ui.")
	}

	const password = "the-admins-password"
	admin, err := svc.Users.CreateUser(t.Context(), "admin", password)
	check(t, err)
	check(t, svc.Access.MakeAdmin(t.Context(), admin.ID))
	a := m.startAgent(t, "node-1")
	lobby := m.createServer(t, a, "lobby", noryxv1.ServerType_SERVER_TYPE_PAPER, 25565)
	check(t, a.runtime.Start(t.Context(), lobby.ServerID))

	cmd := exec.CommandContext(t.Context(), "npx", "playwright", "test")
	cmd.Dir = filepath.Join("..", "..", "web")
	cmd.Env = append(os.Environ(), "NORYX_PANEL_URL="+srv.URL,
		"NORYX_PANEL_USERNAME=admin", "NORYX_PANEL_PASSWORD="+password, "NORYX_PANEL_SERVER=lobby")
	out, err := cmd.CombinedOutput()
	t.Logf("playwright test:\n%s", out)
	check(t, err)
}
