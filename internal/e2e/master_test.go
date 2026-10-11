package e2e

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QwikByte/noryx/internal/enrollment"
	"github.com/QwikByte/noryx/internal/master/access"
	masterapp "github.com/QwikByte/noryx/internal/master/app"
	"github.com/QwikByte/noryx/internal/master/auth"
	masterserver "github.com/QwikByte/noryx/internal/master/server"
	"github.com/QwikByte/noryx/internal/master/settings"
)

func TestMasterSettings(t *testing.T) {
	m := startMaster(t)
	api := apiClient{t: t, url: m.panel(t).URL}

	var got struct {
		Settings settings.Settings `json:"settings"`
		Master   settings.Master   `json:"master"`
	}
	api.do("GET", "/api/settings", nil, http.StatusOK, &got)
	if s := got.Settings; s.SessionHours != 12 || s.JoinTokenMinutes != 60 || *s.NodeDefaults.MemoryReserveMB != 1024 || s.LogDays != 30 || s.LogSizeMB != 2048 || got.Master.EnrollAddr != m.enrollAddr {
		t.Fatalf("default settings = %+v, master = %+v", s, got.Master)
	}

	valid := func(change func(map[string]any)) map[string]any {
		s := map[string]any{
			"enrollAddr": "panel.example.com:9443", "sessionHours": 24, "joinTokenMinutes": 30,
			"nodeDefaults": map[string]any{"portMin": 25565, "portMax": 25600, "memoryReserveMb": 2048}, "logDays": 90, "logSizeMb": 500,
		}
		change(s)
		return s
	}
	for name, change := range map[string]func(map[string]any){
		"address without port": func(s map[string]any) { s["enrollAddr"] = "panel.example.com" },
		"address with a path":  func(s map[string]any) { s["enrollAddr"] = "panel.example.com/x:9443" },
		"panel at a host name": func(s map[string]any) { s["panelAddr"] = "panel.example.com:8080" },
		"endless sessions":     func(s map[string]any) { s["sessionHours"] = 24 * 365 },
		"short join tokens":    func(s map[string]any) { s["joinTokenMinutes"] = 1 },
		"reversed port range":  func(s map[string]any) { s["nodeDefaults"] = map[string]any{"portMin": 30000, "portMax": 20000} },
		"logs kept forever":    func(s map[string]any) { s["logDays"] = 0 },
		"tiny log":             func(s map[string]any) { s["logSizeMb"] = 10 },
		"endless log":          func(s map[string]any) { s["logSizeMb"] = 1 << 20 },
	} {
		if body := api.do("PUT", "/api/settings", valid(change), http.StatusBadRequest, nil); body == "" {
			t.Errorf("%s: no error message", name)
		}
	}
	api.do("PUT", "/api/settings", valid(func(map[string]any) {}), http.StatusOK, &got)
	if size := m.settings.LogMaxSize(); size != 500<<20 {
		t.Fatalf("the log may take %d bytes, want 500 MiB", size)
	}

	// New nodes get the default limits and join tokens with the new address and validity.
	var created struct {
		Node struct {
			PortMin         *int `json:"portMin"`
			MemoryReserveMB *int `json:"memoryReserveMb"`
		} `json:"node"`
		JoinToken          string    `json:"joinToken"`
		JoinTokenExpiresAt time.Time `json:"joinTokenExpiresAt"`
		InstallCommand     string    `json:"installCommand"`
	}
	api.do("POST", "/api/nodes", map[string]string{"name": "node-1", "address": "127.0.0.1:7443"}, http.StatusCreated, &created)
	token, err := enrollment.ParseToken(created.JoinToken)
	check(t, err)
	if token.Master != "panel.example.com:9443" || *created.Node.PortMin != 25565 || *created.Node.MemoryReserveMB != 2048 {
		t.Fatalf("created node = %+v, token for %s", created.Node, token.Master)
	}
	if left := time.Until(created.JoinTokenExpiresAt); left < 29*time.Minute || left > 30*time.Minute {
		t.Fatalf("join token expires in %v, want 30 minutes", left)
	}
	if !strings.HasSuffix(created.InstallCommand, "/install.sh && sudo bash install.sh agent --join "+created.JoinToken) {
		t.Fatalf("install command = %q", created.InstallCommand)
	}

	// The settings are kept, and an empty address means the one from the command line.
	reloaded, err := settings.Load(t.Context(), m.db, settings.Master{EnrollAddr: m.enrollAddr}, m.cert)
	check(t, err)
	if !reflect.DeepEqual(reloaded.Get(), got.Settings) {
		t.Fatalf("reloaded settings = %+v, want %+v", reloaded.Get(), got.Settings)
	}
	api.do("PUT", "/api/settings", valid(func(s map[string]any) { s["enrollAddr"] = " " }), http.StatusOK, nil)
	if addr := m.settings.EnrollAddr(); addr != m.enrollAddr {
		t.Fatalf("enrollment address = %s, want %s", addr, m.enrollAddr)
	}

	// The panel creates servers with the settings of new servers, also for those who may not
	// see the settings.
	n := masterserver.NewServers{Type: "velocity", MemoryMB: 3000, ProxyMemoryMB: 768, StopTimeout: 120, TimeZone: "Europe/Berlin"}
	api.do("PUT", "/api/settings", map[string]any{"newServers": n}, http.StatusOK, nil)
	var newServers masterserver.NewServers
	if api.do("GET", "/api/servers/defaults", nil, http.StatusOK, &newServers); newServers != n {
		t.Fatalf("settings of new servers = %+v, want %+v", newServers, n)
	}
}

// Only administrators change the panel's address and HTTPS, which can open the panel to other
// networks, and restart the master.
func TestPanelAddrAndRestart(t *testing.T) {
	m := startMaster(t)
	svc := m.services(t)
	var restarts atomic.Int32
	svc.Restart = func() error { restarts.Add(1); return nil }
	srv := httptest.NewTLSServer(masterapp.Handler(svc))
	t.Cleanup(srv.Close)
	admin, err := svc.Users.CreateUser(t.Context(), "admin", "the-admins-password")
	check(t, err)
	check(t, svc.Access.MakeAdmin(t.Context(), admin.ID))
	root := browser(t, srv)
	root.do("POST", "/api/auth/login", map[string]string{"username": "admin", "password": "the-admins-password"}, http.StatusOK, nil)
	var group access.Group
	root.do("POST", "/api/groups", map[string]any{"name": "Settings", "permissions": []string{"settings.edit"}}, http.StatusCreated, &group)
	var invited struct {
		SetupLink auth.SetupLink `json:"setupLink"`
	}
	root.do("POST", "/api/users", map[string]any{"username": "editor", "groups": []string{group.ID}}, http.StatusCreated, &invited)
	editor := browser(t, srv)
	editor.do("POST", "/api/auth/setup", map[string]string{"token": invited.SetupLink.Token, "password": "the-editors-password"}, http.StatusOK, nil)

	addr := listen(t)
	free := addr.Addr().String()
	check(t, addr.Close())
	editor.do("PUT", "/api/settings", map[string]any{"sessionHours": 24}, http.StatusOK, nil)
	editor.do("PUT", "/api/settings", map[string]any{"panelAddr": free}, http.StatusForbidden, nil)
	editor.do("PUT", "/api/settings", map[string]any{"panelHttps": "self-signed"}, http.StatusForbidden, nil)
	editor.do("PUT", "/api/settings", map[string]any{"panelDomain": "panel.example.com"}, http.StatusForbidden, nil)
	editor.do("POST", "/api/master/restart", nil, http.StatusForbidden, nil)
	root.do("PUT", "/api/settings", map[string]any{"panelAddr": free, "panelHttps": "self-signed", "panelDomain": "panel.example.com"}, http.StatusOK, nil)
	editor.do("PUT", "/api/settings", map[string]any{"sessionHours": 12, "panelAddr": free, "panelHttps": "self-signed", "panelDomain": " panel.example.com"}, http.StatusOK, nil)
	if got := svc.Settings.Get(); got.PanelAddr != free || got.PanelHTTPS != "self-signed" || got.SessionHours != 12 || restarts.Load() != 0 {
		t.Fatalf("settings = %+v, restarts = %d", got, restarts.Load())
	}
	root.do("POST", "/api/master/restart", nil, http.StatusAccepted, nil)
	if restarts.Load() != 1 {
		t.Fatalf("restarts = %d", restarts.Load())
	}

	// A master that its service manager doesn't start again can't restart itself.
	apiClient{t: t, url: m.panel(t).URL}.do("POST", "/api/master/restart", nil, http.StatusConflict, nil)
}

// The sign-in page reads the panel's name and notice without a session, and authenticator
// apps get the name with new secrets.
func TestPanelName(t *testing.T) {
	m := startMaster(t)
	svc := m.services(t)
	srv := httptest.NewTLSServer(masterapp.Handler(svc))
	t.Cleanup(srv.Close)
	admin, err := svc.Users.CreateUser(t.Context(), "admin", "the-admins-password")
	check(t, err)
	check(t, svc.Access.MakeAdmin(t.Context(), admin.ID))
	visitor, root := browser(t, srv), browser(t, srv)
	var public struct{ Name, Notice string }
	visitor.do("GET", "/api/panel", nil, http.StatusOK, &public)
	if public.Name != "Noryx" || public.Notice != "" {
		t.Fatalf("public = %+v", public)
	}

	root.do("POST", "/api/auth/login", map[string]string{"username": "admin", "password": "the-admins-password"}, http.StatusOK, nil)
	root.do("PUT", "/api/settings", map[string]any{"panelName": "Noryx\r\nTest"}, http.StatusBadRequest, nil)
	root.do("PUT", "/api/settings", map[string]any{"panelName": "Noryx · Test", "signInNotice": "Ask Alex for access."}, http.StatusOK, nil)
	visitor.do("GET", "/api/panel", nil, http.StatusOK, &public)
	if public.Name != "Noryx · Test" || public.Notice != "Ask Alex for access." {
		t.Fatalf("public = %+v", public)
	}
	var setup auth.MFASetup
	root.do("POST", "/api/auth/mfa/setup", nil, http.StatusOK, &setup)
	if !strings.HasPrefix(setup.URI, "otpauth://totp/Noryx%20%C2%B7%20Test:admin?issuer=Noryx%20%C2%B7%20Test&") {
		t.Fatalf("uri = %s", setup.URI)
	}

	// It reads the language and look for users who haven't chosen them too.
	root.do("PUT", "/api/settings", map[string]any{"userDefaults": map[string]string{"language": "fr"}}, http.StatusBadRequest, nil)
	root.do("PUT", "/api/settings", map[string]any{"userDefaults": map[string]string{"accent": "pink"}}, http.StatusBadRequest, nil)
	root.do("PUT", "/api/settings", map[string]any{"userDefaults": map[string]string{"language": "de", "accent": "violet"}}, http.StatusOK, nil)
	var defaults struct{ Defaults settings.UserDefaults }
	visitor.do("GET", "/api/panel", nil, http.StatusOK, &defaults)
	if defaults.Defaults != (settings.UserDefaults{Language: "de", Accent: "violet"}) {
		t.Fatalf("defaults = %+v", defaults.Defaults)
	}
}

// Each user keeps the language they chose, and the session tells it to every browser.
func TestLanguage(t *testing.T) {
	m := startMaster(t)
	svc := m.services(t)
	srv := httptest.NewTLSServer(masterapp.Handler(svc))
	t.Cleanup(srv.Close)
	_, err := svc.Users.CreateUser(t.Context(), "admin", "the-admins-password")
	check(t, err)
	login := map[string]string{"username": "admin", "password": "the-admins-password"}
	first := browser(t, srv)
	first.do("POST", "/api/auth/login", login, http.StatusOK, nil)
	first.do("PUT", "/api/auth/language", map[string]string{"language": "../de"}, http.StatusBadRequest, nil)
	first.do("PUT", "/api/auth/language", map[string]string{"language": "de"}, http.StatusOK, nil)

	var user auth.User
	second := browser(t, srv)
	second.do("POST", "/api/auth/login", login, http.StatusOK, &user)
	if user.Language != "de" {
		t.Fatalf("signed in as %+v", user)
	}
	first.do("GET", "/api/auth/me", nil, http.StatusOK, &user)
	if user.Language != "de" {
		t.Fatalf("me = %+v", user)
	}
}
