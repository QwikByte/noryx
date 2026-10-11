package settings

import (
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/QwikByte/noryx/internal/master/access"
	"github.com/QwikByte/noryx/internal/master/database"
	"github.com/QwikByte/noryx/internal/master/https"
)

func TestPanelAddr(t *testing.T) {
	db, err := database.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	load := func() *Service {
		s, err := Load(t.Context(), db, Master{PanelDefaultAddr: "127.0.0.1:0"}, nil)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	listen := func(s *Service) {
		ln, err := s.ListenPanel()
		if err != nil {
			t.Fatal(err)
		}
		ln.Close()
	}
	set := func(s *Service, addr string) error {
		next := s.Get()
		next.PanelAddr = addr
		_, err := s.Update(t.Context(), next)
		return err
	}
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	_, busyPort, _ := net.SplitHostPort(busy.Addr().String())
	free := freeAddr(t)

	// Without a setting, the panel listens at the address from the command line.
	s := load()
	listen(s)
	if s.master.PanelAddr != "127.0.0.1:0" || s.master.PanelAddrError != "" {
		t.Fatalf("master = %+v", s.master)
	}
	for _, bad := range []string{"localhost:8080", "127.0.0.1", "127.0.0.1:0", "127.0.0.1:70000", busy.Addr().String()} {
		if err := set(s, bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if err := set(s, busy.Addr().String()); !strings.Contains(err.Error(), "address already in use") {
		t.Errorf("busy port: %v", err)
	}
	if err := set(s, " "+free+" "); err != nil || s.Get().PanelAddr != free {
		t.Fatalf("set %s: %v, settings = %+v", free, err, s.Get())
	}

	// After a restart, the panel listens at the address from the settings.
	s = load()
	listen(s)
	if s.master.PanelAddr != free || s.master.PanelAddrError != "" {
		t.Fatalf("master = %+v", s.master)
	}

	// The port the panel listens at now is only free after a restart.
	s.master.PanelAddr = busy.Addr().String()
	if err := set(s, "0.0.0.0:"+busyPort); err != nil {
		t.Errorf("other interfaces at the current port: %v", err)
	}

	// If another program took the port in the meantime, the panel falls back.
	if err := set(s, free); err != nil {
		t.Fatal(err)
	}
	taken, err := net.Listen("tcp", free)
	if err != nil {
		t.Fatal(err)
	}
	defer taken.Close()
	if err := set(s, free); err != nil {
		t.Errorf("unchanged address checked again: %v", err)
	}
	s = load()
	listen(s)
	if s.master.PanelAddr != "127.0.0.1:0" || !strings.Contains(s.master.PanelAddrError, "address already in use") {
		t.Fatalf("master = %+v", s.master)
	}
}

func freeAddr(t *testing.T) string {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().String()
}

// HTTPS of the settings applies with the next start, unless the command line gives a certificate.
func TestPanelHTTPS(t *testing.T) {
	db, err := database.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s, err := Load(t.Context(), db, Master{PanelDefaultAddr: "0.0.0.0:443", EnrollAddr: "203.0.113.7:9443"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	update := func(change func(*Settings)) error {
		next := s.Get()
		change(&next)
		_, err := s.Update(t.Context(), next)
		return err
	}
	for name, change := range map[string]func(*Settings){
		"unknown certificate":            func(s *Settings) { s.PanelHTTPS = "acme" },
		"Let's Encrypt without a domain": func(s *Settings) { s.PanelHTTPS = https.LetsEncrypt },
		"an IP address as domain":        func(s *Settings) { s.PanelHTTPS, s.PanelDomain = https.LetsEncrypt, "203.0.113.7" },
		"a domain without dot":           func(s *Settings) { s.PanelDomain = "panel" },
		"Let's Encrypt on localhost": func(s *Settings) {
			s.PanelHTTPS, s.PanelDomain, s.PanelAddr = https.LetsEncrypt, "panel.example.com", "127.0.0.1:"+freePort(t)
		},
		"Let's Encrypt on port 80": func(s *Settings) {
			s.PanelHTTPS, s.PanelDomain, s.PanelAddr = https.LetsEncrypt, "panel.example.com", "0.0.0.0:80"
		},
	} {
		if err := update(change); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if err := update(func(s *Settings) { s.PanelHTTPS, s.PanelDomain = https.LetsEncrypt, " Panel.Example.com. " }); err != nil || s.Get().PanelDomain != "panel.example.com" {
		t.Fatalf("Let's Encrypt for panel.example.com: %v, settings = %+v", err, s.Get())
	}
	if err := update(func(s *Settings) { s.PanelHTTPS = https.SelfSigned }); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	srv, err := s.PanelHTTPS(dir)
	if err != nil || srv == nil {
		t.Fatalf("certificates: %v", err)
	}
	m, cert := s.master, s.panelCert.Certificate()
	if m.PanelHTTPS != https.SelfSigned || m.PanelDomain != "panel.example.com" ||
		!slices.Contains(cert.Names, "panel.example.com") || !slices.Contains(cert.Names, "203.0.113.7") {
		t.Fatalf("master = %+v, certificate = %+v", m, cert)
	}

	files, err := Load(t.Context(), db, Master{PanelHTTPS: https.Files}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if srv, err := files.PanelHTTPS(dir); srv != nil || err != nil || files.master.PanelHTTPS != https.Files {
		t.Errorf("with a certificate of the command line: %v, %v", srv, err)
	}
}

func freePort(t *testing.T) string {
	_, port, _ := net.SplitHostPort(freeAddr(t))
	return port
}

// A refused change leaves the settings as they are, also the limits of new nodes, which the
// request is decoded into.
func TestRefusedUpdate(t *testing.T) {
	db, err := database.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s, err := Load(t.Context(), db, Master{PanelDefaultAddr: "127.0.0.1:0"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	body := `{"nodeDefaults": {"portMin": 30000, "portMax": 70000, "memoryReserveMb": 2048}}`
	r := httptest.NewRequest(http.MethodPut, "/api/settings", strings.NewReader(body))
	rec := httptest.NewRecorder()
	NewHandler(s, nil).update(rec, r.WithContext(access.WithGrants(t.Context(), access.Admin())))
	if l := s.NodeDefaults(); rec.Code != http.StatusBadRequest || l.PortMin != nil || *l.MemoryReserveMB != 1024 {
		t.Fatalf("status %d, limits of new nodes %+v", rec.Code, l)
	}
}

// Two-factor authentication is required of all users or of groups, which are checked,
// sorted and named once. A refused change leaves them as they are.
func TestRequireMFA(t *testing.T) {
	db, err := database.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s, err := Load(t.Context(), db, Master{PanelDefaultAddr: "127.0.0.1:0"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if r := s.Get().RequireMFA; r.All || r.Groups == nil || len(r.Groups) != 0 {
		t.Fatalf("default = %+v", r)
	}
	set := func(r MFARequirement) error {
		next := s.Get()
		next.RequireMFA = r
		_, err := s.Update(t.Context(), next)
		return err
	}
	many := make([]string, maxMFAGroups+1)
	for i := range many {
		many[i] = "group" + strconv.Itoa(i)
	}
	for _, bad := range [][]string{{"../admins"}, {""}, many} {
		if err := set(MFARequirement{Groups: bad}); err == nil {
			t.Errorf("groups %v accepted", bad)
		}
	}
	if err := set(MFARequirement{Groups: []string{"mods", "administrators", "mods"}}); err != nil {
		t.Fatal(err)
	}
	if r := s.Get().RequireMFA; r.All || !slices.Equal(r.Groups, []string{"administrators", "mods"}) {
		t.Fatalf("requirement = %+v", r)
	}

	body := `{"requireMfa": {"all": false, "groups": ["other"]}, "logDays": 0}`
	r := httptest.NewRequest(http.MethodPut, "/api/settings", strings.NewReader(body))
	rec := httptest.NewRecorder()
	NewHandler(s, nil).update(rec, r.WithContext(access.WithGrants(t.Context(), access.Admin())))
	if r := s.Get().RequireMFA; rec.Code != http.StatusBadRequest || !slices.Equal(r.Groups, []string{"administrators", "mods"}) {
		t.Fatalf("status %d, requirement %+v", rec.Code, r)
	}

	if err := set(MFARequirement{All: true, Groups: []string{"mods"}}); err != nil {
		t.Fatal(err)
	}
	if s, err = Load(t.Context(), db, Master{}, nil); err != nil {
		t.Fatal(err)
	}
	if r := s.Get().RequireMFA; !r.All || len(r.Groups) != 0 {
		t.Fatalf("requirement for all = %+v", r)
	}
}

// The name of the panel and the notice of the sign-in page are plain text, which the sign-in
// page reads without a session.
func TestPanelNameAndNotice(t *testing.T) {
	db, err := database.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s, err := Load(t.Context(), db, Master{PanelDefaultAddr: "127.0.0.1:0"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	set := func(name, notice string) error {
		next := s.Get()
		next.PanelName, next.SignInNotice = name, notice
		_, err := s.Update(t.Context(), next)
		return err
	}
	for _, bad := range [][2]string{
		{"Noryx\nTest", ""}, {"Noryx\x1b[31m", ""}, {"Noryx Test", ""}, {strings.Repeat("n", maxPanelName+1), ""},
		{"", "Ask\x00Alex"}, {"", "Ask\tAlex"}, {"", "Ask Alex"}, {"", strings.Repeat("n", maxSignInNotice+1)},
	} {
		if err := set(bad[0], bad[1]); err == nil {
			t.Errorf("name %q and notice %q accepted", bad[0], bad[1])
		}
	}
	if s.PanelName() != "Noryx" {
		t.Fatalf("name = %q", s.PanelName())
	}
	if err := set(strings.Repeat("·", maxPanelName), ""); err != nil {
		t.Errorf("%d characters: %v", maxPanelName, err)
	}
	if err := set(" Noryx · Test ", " Ask Alex\r\nfor access. "); err != nil || s.PanelName() != "Noryx · Test" || s.Get().SignInNotice != "Ask Alex\nfor access." {
		t.Fatalf("set: %v, settings = %+v", err, s.Get())
	}

	mux := http.NewServeMux()
	NewHandler(s, nil).RegisterPublic(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/panel", nil))
	if body := strings.TrimSpace(rec.Body.String()); rec.Code != http.StatusOK ||
		body != `{"name":"Noryx · Test","notice":"Ask Alex\nfor access.","defaults":{"language":"","theme":"","accent":"","density":"","clock":""}}` {
		t.Fatalf("status %d: %s", rec.Code, body)
	}
}

// The defaults of users are values that users can choose themselves, and the groups of
// invitations are named once.
func TestUserDefaults(t *testing.T) {
	db, err := database.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s, err := Load(t.Context(), db, Master{PanelDefaultAddr: "127.0.0.1:0"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := s.Get(); got.UserDefaults != (UserDefaults{}) || got.InviteGroups == nil || len(got.InviteGroups) != 0 {
		t.Fatalf("defaults = %+v, invite groups %v", got.UserDefaults, got.InviteGroups)
	}
	set := func(d UserDefaults, groups []string) error {
		next := s.Get()
		next.UserDefaults, next.InviteGroups = d, groups
		_, err := s.Update(t.Context(), next)
		return err
	}
	for _, bad := range []UserDefaults{{Language: "fr"}, {Language: "../de"}, {Theme: "pink"}, {Accent: "red"}, {Density: "tight"}, {Clock: "13h"}} {
		if err := set(bad, nil); err == nil {
			t.Errorf("%+v accepted", bad)
		}
	}
	many := make([]string, maxInviteGroups+1)
	for i := range many {
		many[i] = "group" + strconv.Itoa(i)
	}
	for _, bad := range [][]string{{"../admins"}, {""}, many} {
		if err := set(UserDefaults{}, bad); err == nil {
			t.Errorf("invite groups %v accepted", bad)
		}
	}
	want := UserDefaults{Language: "de", Theme: "dark", Accent: "blue", Density: "compact", Clock: "24h"}
	if err := set(want, []string{"mods", "administrators", "mods"}); err != nil {
		t.Fatal(err)
	}
	if got := s.Get(); got.UserDefaults != want || !slices.Equal(got.InviteGroups, []string{"administrators", "mods"}) {
		t.Fatalf("defaults = %+v, invite groups %v", got.UserDefaults, got.InviteGroups)
	}
	if groups := s.InviteGroups(); !slices.Equal(groups, []string{"administrators", "mods"}) {
		t.Fatalf("invite groups = %v", groups)
	}
}
