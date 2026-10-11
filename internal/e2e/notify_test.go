package e2e

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	"github.com/QwikByte/noryx/internal/logging"
	masterapp "github.com/QwikByte/noryx/internal/master/app"
	"github.com/QwikByte/noryx/internal/master/notify"
)

// webhook is a webhook at localhost that records what it gets.
type webhook struct {
	mu       sync.Mutex
	payloads []notify.Payload
	paths    []string
}

func (h *webhook) ServeHTTP(_ http.ResponseWriter, r *http.Request) {
	var p notify.Payload
	_ = json.NewDecoder(r.Body).Decode(&p)
	h.mu.Lock()
	defer h.mu.Unlock()
	h.payloads, h.paths = append(h.payloads, p), append(h.paths, r.URL.Path)
}

func (h *webhook) got() ([]notify.Payload, []string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.payloads), slices.Clone(h.paths)
}

// startWebhook serves a webhook over HTTPS at localhost with a certificate of the master's CA.
func (m *master) startWebhook(t *testing.T) (*webhook, string, *x509.CertPool) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	check(t, err)
	der, err := m.ca.Issue(key.Public(), "localhost", x509.ExtKeyUsageServerAuth)
	check(t, err)
	h := &webhook{}
	srv := httptest.NewUnstartedServer(h)
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	roots := x509.NewCertPool()
	roots.AddCert(m.ca.Cert)
	return h, strings.Replace(srv.URL, "127.0.0.1", "localhost", 1), roots
}

func TestNotifications(t *testing.T) {
	m := startMaster(t)
	prev := slog.Default()
	slog.SetDefault(slog.New(m.logs.Handler(slog.LevelInfo)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	a := m.startAgent(t, "node-1")
	go m.logs.Collect(t.Context(), m.nodes)
	lobby := m.createServer(t, a, "Lobby", noryxv1.ServerType_SERVER_TYPE_PAPER, 25565)
	hook, url, roots := m.startWebhook(t)

	svc := m.services(t)
	// Tests allow the webhook at localhost, which the master refuses otherwise.
	svc.Notify = notify.New(m.db, m.logs, m.settings, notify.Options{Allow: func(ip netip.Addr) bool { return ip.IsLoopback() }, Roots: roots, Wait: 100 * time.Millisecond})
	go svc.Notify.Run(t.Context())
	srv := httptest.NewTLSServer(masterapp.Handler(svc))
	t.Cleanup(srv.Close)
	admin, err := svc.Users.CreateUser(t.Context(), "admin", "the-admins-password")
	check(t, err)
	check(t, svc.Access.MakeAdmin(t.Context(), admin.ID))
	root := browser(t, srv)
	root.do("POST", "/api/auth/login", map[string]string{"username": "admin", "password": "the-admins-password"}, http.StatusOK, nil)

	// Channels connect to public addresses over HTTPS only.
	secret := url + "/hooks/1/the-secret-token"
	for _, target := range []string{"http://example.com/hook", "https://192.168.1.10/api/webhooks/1/x", "https://[::ffff:127.0.0.1]/x"} {
		root.do("POST", "/api/notifications/channels", map[string]string{"name": "Refused", "kind": "discord", "url": target}, http.StatusBadRequest, nil)
	}
	var ch notify.Channel
	root.do("POST", "/api/notifications/channels", map[string]string{"name": "Ops", "kind": "webhook", "url": secret}, http.StatusCreated, &ch)
	root.do("POST", "/api/notifications/channels/"+ch.ID+"/test", nil, http.StatusNoContent, nil)
	if got, paths := hook.got(); len(got) != 1 || !got[0].Test || paths[0] != "/hooks/1/the-secret-token" {
		t.Fatalf("test payloads = %+v", got)
	}
	root.do("POST", "/api/notifications/rules", map[string]any{
		"channelId": ch.ID, "enabled": true, "level": "warn", "categories": []string{"servers", "nodes"},
	}, http.StatusCreated, nil)

	// A crash on a node reaches the channel, with the names of its server and node.
	agentLog := slog.New(a.log.Handler(slog.LevelInfo))
	agentLog.Info("Start server", logging.Servers, logging.KeyServer, lobby.ServerID)
	agentLog.Warn("A server crashed and starts again; its console and crash reports tell why", logging.Servers, logging.KeyServer, lobby.ServerID)
	var crash *notify.Payload
	for deadline := time.Now().Add(10 * time.Second); crash == nil && time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		payloads, _ := hook.got()
		for _, p := range payloads {
			if len(p.Entries) > 0 && strings.HasPrefix(p.Entries[0].Message, "A server crashed") {
				crash = &p
			}
		}
	}
	if crash == nil || len(crash.Entries) != 1 || crash.Entries[0].ServerName != "Lobby" || crash.Entries[0].NodeName != "node-1" ||
		crash.Entries[0].Source != "agent" {
		t.Fatalf("crash = %+v", crash)
	}

	// The URL is never shown, nor logged.
	for _, path := range []string{"/api/notifications", "/api/logs/export?format=jsonl"} {
		if body := root.do("GET", path, nil, http.StatusOK, nil); strings.Contains(body, "the-secret-token") || !strings.Contains(body, "Ops") {
			t.Errorf("%s shows the secret: %s", path, body)
		}
	}

	// Rules send entries about every server, so they need the permission to see all of them.
	invite := func(name string, all bool) apiClient {
		var group struct{ ID string }
		root.do("POST", "/api/groups", map[string]any{
			"name": name, "permissions": []string{"notifications.manage", "logs.view"}, "allServers": all, "targets": []any{lobby},
		}, http.StatusCreated, &group)
		var invited struct {
			SetupLink struct{ Token string } `json:"setupLink"`
		}
		root.do("POST", "/api/users", map[string]any{"username": name, "groups": []string{group.ID}}, http.StatusCreated, &invited)
		user := browser(t, srv)
		user.do("POST", "/api/auth/setup", map[string]string{"token": invited.SetupLink.Token, "password": "a-good-password"}, http.StatusOK, nil)
		return user
	}
	invite("lobby-only", false).do("GET", "/api/notifications", nil, http.StatusForbidden, nil)
	invite("everywhere", true).do("GET", "/api/notifications", nil, http.StatusOK, nil)
}
