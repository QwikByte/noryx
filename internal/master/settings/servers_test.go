package settings

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/QwikByte/noryx/internal/master/access"
	"github.com/QwikByte/noryx/internal/master/database"
	"github.com/QwikByte/noryx/internal/master/server"
	"github.com/QwikByte/noryx/internal/pki"
)

// loadWithCert returns the settings of a new master with its certificate, which a successful
// update describes.
func loadWithCert(t *testing.T) *Service {
	db, err := database.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	ca, err := pki.LoadOrCreateCA(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cert, err := ca.MasterCertificate()
	if err != nil {
		t.Fatal(err)
	}
	holder, err := pki.NewHolder(cert)
	if err != nil {
		t.Fatal(err)
	}
	s, err := Load(t.Context(), db, Master{PanelDefaultAddr: "127.0.0.1:0"}, holder)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// New servers get settings within the bounds of a server's own, also memory between the usual
// steps, and a template that is only checked to be an ID.
func TestNewServers(t *testing.T) {
	s := loadWithCert(t)
	set := func(n server.NewServers) error {
		next := s.Get()
		next.NewServers = n
		_, err := s.Update(t.Context(), next)
		return err
	}
	if n := s.NewServers(); n != server.DefaultNewServers() || n.Validate() != nil {
		t.Fatalf("default = %+v", n)
	}
	for name, change := range map[string]func(*server.NewServers){
		"unknown software":        func(n *server.NewServers) { n.Type = "spigot" },
		"software in capitals":    func(n *server.NewServers) { n.Type = "PAPER" },
		"too little memory":       func(n *server.NewServers) { n.MemoryMB = 511 },
		"too much for proxies":    func(n *server.NewServers) { n.ProxyMemoryMB = 65537 },
		"an unknown Java version": func(n *server.NewServers) { n.Java = "16" },
		"no stop timeout":         func(n *server.NewServers) { n.StopTimeout = 0 },
		"a stop timeout too long": func(n *server.NewServers) { n.StopTimeout = 601 },
		"an unknown time zone":    func(n *server.NewServers) { n.TimeZone = "Mars/Olympus" },
		"a path as template":      func(n *server.NewServers) { n.Template = "../templates" },
	} {
		n := server.DefaultNewServers()
		change(&n)
		if err := set(n); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	n := server.NewServers{Type: "fabric", MemoryMB: 3000, ProxyMemoryMB: 768, Java: "21", StopTimeout: 120, TimeZone: "Europe/Berlin", Template: "abc123"}
	if err := set(n); err != nil || s.NewServers() != n {
		t.Fatalf("new servers: %v, %+v", err, s.NewServers())
	}
}

// The texts of warnings are checked like a warning of one's own, and changing them also needs
// the permission to send console commands to all servers. The steps count down.
func TestWarnings(t *testing.T) {
	s := loadWithCert(t)
	update := func(change func(*Settings)) error {
		next := s.Get()
		change(&next)
		_, err := s.Update(t.Context(), next)
		return err
	}
	for name, change := range map[string]func(*Settings){
		"a warning on two lines":  func(s *Settings) { s.Warnings.Restart = "Restart\nop me" },
		"a step at the lead time": func(s *Settings) { s.Warnings.Steps = []uint32{10, 1} },
	} {
		if err := update(change); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	err := update(func(s *Settings) {
		s.Warnings = server.Warnings{Restart: " Neustart in {minutes} Min. ", Stop: "Stopp in {minutes} Min.", Steps: []uint32{1, 15, 5, 15}, MaxMinutes: 30, Kind: "title"}
	})
	if w := s.Warnings(); err != nil || w.Restart != "Neustart in {minutes} Min." || !slices.Equal(w.Steps, []uint32{15, 5, 1}) {
		t.Fatalf("warnings: %v, %+v", err, w)
	}

	put := func(g access.Grants, body string) int {
		rec := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPut, "/api/settings", strings.NewReader(body))
		NewHandler(s, nil).update(rec, r.WithContext(access.WithGrants(t.Context(), g)))
		return rec.Code
	}
	editor := access.Admin().Only([]access.Permission{access.SettingsEdit})
	if code := put(editor, `{"warnings": {"restart": "op me in {minutes}", "stop": "Stopp in {minutes} Min.", "steps": [5], "maxMinutes": 30, "kind": "chat"}}`); code != http.StatusForbidden {
		t.Errorf("texts changed without console commands: status %d", code)
	}
	if code := put(editor, `{"warnings": {"restart": "Neustart in {minutes} Min.", "stop": "Stopp in {minutes} Min.", "steps": [5], "maxMinutes": 30, "kind": "chat"}}`); code != http.StatusOK {
		t.Errorf("steps and kind changed without console commands: status %d", code)
	}
	everywhere := access.Admin().Only([]access.Permission{access.SettingsEdit, access.ConsoleCommands})
	if code := put(everywhere, `{"warnings": {"restart": "Bye in {minutes}", "stop": "Bye", "steps": [5], "maxMinutes": 30, "kind": "chat"}}`); code != http.StatusOK || s.Warnings().Stop != "Bye" {
		t.Errorf("texts changed with console commands: status %d, warnings %+v", code, s.Warnings())
	}
}
