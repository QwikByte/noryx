package node_test

import (
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	"github.com/QwikByte/noryx/internal/master/access"
	"github.com/QwikByte/noryx/internal/master/database"
	"github.com/QwikByte/noryx/internal/master/httpapi"
	"github.com/QwikByte/noryx/internal/master/node"
	"github.com/QwikByte/noryx/internal/master/node/nodetest"
	"github.com/QwikByte/noryx/internal/master/operation"
	"github.com/QwikByte/noryx/internal/master/tag"
	"github.com/QwikByte/noryx/internal/pki"
)

// agent is the NodeService of an agent: its machine, the settings it applies, and its
// certificate, which it renews when the master asks.
type agent struct {
	noryxv1.UnimplementedNodeServiceServer
	holder *pki.Holder

	mu       sync.Mutex
	settings *noryxv1.NodeSettings
	key      ed25519.PrivateKey
}

func (a *agent) GetInfo(context.Context, *noryxv1.GetInfoRequest) (*noryxv1.GetInfoResponse, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return &noryxv1.GetInfoResponse{
		AgentVersion: "v1.4.0", Hostname: "node-1", Os: "Debian GNU/Linux 13", CpuCount: 8, MemoryBytes: 16 << 30,
		RuntimeName: "podman", RuntimeVersion: "5.6.0", Settings: a.settings,
		Storage:     []*noryxv1.StorageLocation{{Name: "default", Path: "/var/lib/noryx", FreeBytes: 1 << 30, TotalBytes: 2 << 30}},
		ServerTypes: []noryxv1.ServerType{noryxv1.ServerType_SERVER_TYPE_VELOCITY, noryxv1.ServerType_SERVER_TYPE_PAPER},
	}, nil
}

func (a *agent) Configure(_ context.Context, req *noryxv1.ConfigureRequest) (*noryxv1.ConfigureResponse, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.settings = req.GetSettings()
	return &noryxv1.ConfigureResponse{}, nil
}

func (a *agent) CreateCSR(context.Context, *noryxv1.CreateCSRRequest) (*noryxv1.CreateCSRResponse, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.key = pki.NewKey()
	csr, err := pki.NewCSR(a.key)
	return &noryxv1.CreateCSRResponse{CsrDer: csr}, err
}

func (a *agent) InstallCertificate(_ context.Context, req *noryxv1.InstallCertificateRequest) (*noryxv1.InstallCertificateResponse, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return &noryxv1.InstallCertificateResponse{}, a.holder.Set(tls.Certificate{Certificate: [][]byte{req.GetCertificateDer()}, PrivateKey: a.key})
}

// world is what the handler of nodes affects besides them: the networks of their servers, which
// it applies again and takes the servers of removed nodes out of, the file sets of those
// servers, the private network of the nodes and what else refers to nodes.
type world struct {
	mu         sync.Mutex
	reapplied  []string
	reapplyErr error
	removed    []string
	left       []tag.Server
	tidied     int
	reconciled chan struct{}
}

func (w *world) ReapplyNode(_ context.Context, nodeID string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.reapplied = append(w.reapplied, nodeID)
	return w.reapplyErr
}

func (w *world) CheckRemoveNode(_ context.Context, nodeID string, release bool) error {
	if nodeID == "n1" && !release {
		return httpapi.Confirm("network-servers", "Servers of n1 are part of networks.")
	}
	return nil
}

func (w *world) RemoveNode(_ context.Context, nodeID string, release bool, remove func() error) ([]tag.Server, error) {
	w.mu.Lock()
	w.removed = append(w.removed, nodeID)
	w.mu.Unlock()
	if !release {
		return nil, remove()
	}
	return []tag.Server{{NodeID: "n2", ServerID: "lobby"}}, remove()
}

func (w *world) Left(_ context.Context, servers []tag.Server) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.left = append(w.left, servers...)
}

func (w *world) Reconcile(context.Context) { w.reconciled <- struct{}{} }

func (w *world) tidy(context.Context) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.tidied++
}

// reconciles tells whether the private network was configured again since it was asked last.
func (w *world) reconciles() bool {
	select {
	case <-w.reconciled:
		return true
	case <-time.After(time.Second):
		return false
	}
}

// view is a node as the API shows it.
type view struct {
	ID, Name, Address, Status, Warning string
	CertificateExpiresAt               *time.Time
	LogFileMB                          uint32 `json:"logFileMb"`
	Info                               *struct {
		AgentVersion, Hostname, OS, RuntimeName, RuntimeVersion string
		ServerTypes                                             []string
		Storage                                                 []struct{ Name, Path string }
	}
}

// The API of nodes: users see the nodes on which they may see something, the details only with
// the permission to see the node, and whether they are online. Changes of the address apply
// the networks and the private network again, new settings reach the agent, removing waits for
// the networks, and certificates are renewed by the agents' own keys.
func TestHandler(t *testing.T) {
	db, err := database.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	nodes := nodetest.New(t, db)
	a := &agent{}
	a.holder = nodes.Add(t, "n1", func(s *grpc.Server) { noryxv1.RegisterNodeServiceServer(s, a) })
	nodes.Add(t, "n2", nil)
	w := &world{reconciled: make(chan struct{}, 10)}
	mux := http.NewServeMux()
	node.NewHandler(nodes.Service, w, w, operation.New(time.Minute), w, w.tidy).Register(access.NewMux(mux))
	serve := func(g access.Grants, method, path, body string, v any) int {
		t.Helper()
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequestWithContext(access.WithGrants(t.Context(), g), method, path, strings.NewReader(body)))
		if v != nil && rec.Code < 300 {
			if err := json.Unmarshal(rec.Body.Bytes(), v); err != nil {
				t.Fatalf("%s %s: %v: %s", method, path, err, rec.Body)
			}
		}
		if rec.Code >= 300 && v != nil {
			t.Logf("%s %s: %s", method, path, rec.Body)
		}
		return rec.Code
	}
	admin := access.Admin()

	var created struct {
		JoinToken, InstallCommand string
		Node                      view
	}
	if code := serve(admin, "POST", "/api/nodes", `{"name": "n3", "address": "203.0.113.3:7443"}`, &created); code != http.StatusCreated ||
		created.JoinToken == "" || !strings.Contains(created.InstallCommand, "--join "+created.JoinToken) || created.Node.Status != "pending" {
		t.Fatalf("create: status %d, %+v", code, created)
	}
	for body, want := range map[string]int{
		`{"name": "N3", "address": "203.0.113.4:7443"}`: http.StatusConflict,
		`{"name": "", "address": "203.0.113.4:7443"}`:   http.StatusBadRequest,
		`{"name": "n4", "address": "203.0.113.4"}`:      http.StatusBadRequest,
	} {
		if code := serve(admin, "POST", "/api/nodes", body, nil); code != want {
			t.Errorf("create %s: status %d", body, code)
		}
	}

	var list []view
	if code := serve(admin, "GET", "/api/nodes", "", &list); code != http.StatusOK || len(list) != 3 {
		t.Fatalf("list: status %d, %+v", code, list)
	}
	n1, n2, n3 := list[0], list[1], list[2]
	if n1.Status != "online" || n1.Address == "" || n1.Info == nil || n1.Info.AgentVersion != "v1.4.0" || n1.Info.RuntimeName != "podman" ||
		n1.Info.RuntimeVersion != "5.6.0" || !slices.Equal(n1.Info.ServerTypes, []string{"paper", "velocity"}) || n1.Info.Storage[0].Path != "/var/lib/noryx" {
		t.Errorf("n1 %+v, info %+v", n1, n1.Info)
	}
	if n2.Status != "offline" || n2.Info != nil || n3.Status != "pending" || n3.ID != created.Node.ID {
		t.Errorf("n2 %+v, n3 %+v", n2, n3)
	}

	// Who may only see servers sees no details of the node, besides its runtime.
	var concealed view
	if code := serve(admin.Only([]access.Permission{access.ServersView}), "GET", "/api/nodes/n1", "", &concealed); code != http.StatusOK ||
		concealed.Address != "" || concealed.CertificateExpiresAt != nil || concealed.Info.AgentVersion != "" || concealed.Info.Hostname != "" ||
		concealed.Info.OS != "" || concealed.Info.RuntimeVersion != "" || concealed.Info.Storage[0].Path != "" || concealed.Info.RuntimeName != "podman" {
		t.Errorf("concealed n1: status %d, %+v, info %+v", code, concealed, concealed.Info)
	}
	if code := serve(admin.Only([]access.Permission{access.NetworksView}), "GET", "/api/nodes", "", &list); code != http.StatusOK || len(list) != 0 {
		t.Errorf("nodes of a user who sees none: status %d, %+v", code, list)
	}
	if code := serve(admin.Only([]access.Permission{access.NetworksView}), "GET", "/api/nodes/n1", "", nil); code != http.StatusForbidden {
		t.Errorf("a node the user doesn't see: status %d", code)
	}
	if code := serve(admin, "GET", "/api/nodes/nope", "", nil); code != http.StatusNotFound {
		t.Errorf("an unknown node: status %d", code)
	}

	// A new name changes nothing else; a new host applies the networks and the private network
	// again, and new settings of the agent reach it.
	current, err := nodes.Get(t.Context(), "n1")
	if err != nil {
		t.Fatal(err)
	}
	var updated view
	if code := serve(admin, "PUT", "/api/nodes/n1", `{"name": "node-1", "address": "`+current.Address+`"}`, &updated); code != http.StatusOK ||
		updated.Name != "node-1" || updated.Warning != "" || updated.LogFileMB != 10 || updated.CertificateExpiresAt == nil || w.reconciles() {
		t.Fatalf("rename: status %d, %+v", code, updated)
	}
	_, port, _ := net.SplitHostPort(current.Address)
	w.reapplyErr = httpapi.Errorf(http.StatusBadGateway, "Main: n2 can't be reached.")
	body := `{"name": "node-1", "address": "localhost:` + port + `", "logFileMb": 20, "logFiles": 3, "keepFreeGb": 1, "statsIntervalSeconds": 5}`
	if code := serve(admin, "PUT", "/api/nodes/n1", body, &updated); code != http.StatusOK || updated.Status != "online" || updated.Warning != "Main: n2 can't be reached." ||
		!w.reconciles() {
		t.Fatalf("new address: status %d, %+v", code, updated)
	}
	a.mu.Lock()
	if a.settings.GetLogFileMb() != 20 {
		t.Errorf("the agent applies %v", a.settings)
	}
	a.mu.Unlock()
	offline, err := nodes.Get(t.Context(), "n2")
	if err != nil {
		t.Fatal(err)
	}
	body = `{"name": "n2", "address": "` + offline.Address + `", "logFileMb": 20, "logFiles": 3, "keepFreeGb": 1, "statsIntervalSeconds": 5}`
	if code := serve(admin, "PUT", "/api/nodes/n2", body, &updated); code != http.StatusOK || updated.Status != "offline" ||
		updated.Warning != "The agent of the node gets its new settings once the master reaches it again." {
		t.Errorf("settings for an offline agent: status %d, %+v", code, updated)
	}
	w.mu.Lock()
	if !slices.Equal(w.reapplied, []string{"n1"}) {
		t.Errorf("networks reapplied for %q", w.reapplied)
	}
	w.mu.Unlock()

	// A new join token, and a certificate that the agent's new key gets.
	var token struct{ JoinToken string }
	if code := serve(admin, "POST", "/api/nodes/"+n3.ID+"/join-token", "", &token); code != http.StatusOK || token.JoinToken == "" || token.JoinToken == created.JoinToken {
		t.Errorf("new join token: status %d, %+v", code, token)
	}
	if code := serve(admin, "POST", "/api/nodes/nope/join-token", "", nil); code != http.StatusNotFound {
		t.Errorf("join token of an unknown node: status %d", code)
	}
	_, before, err := nodes.Status(t.Context(), "n1")
	if err != nil {
		t.Fatal(err)
	}
	var renewed struct{ CertificateExpiresAt time.Time }
	if code := serve(admin, "POST", "/api/nodes/n1/certificate", "", &renewed); code != http.StatusOK || renewed.CertificateExpiresAt.Before(before.NotAfter) {
		t.Fatalf("renew: status %d, %+v", code, renewed)
	}
	a.mu.Lock()
	key := a.key.Public()
	a.mu.Unlock()
	if _, after, err := nodes.Status(t.Context(), "n1"); err != nil || after.SerialNumber.Cmp(before.SerialNumber) == 0 || !after.PublicKey.(ed25519.PublicKey).Equal(key) {
		t.Errorf("the master still sees the old certificate: %v", err)
	}
	if code := serve(admin, "POST", "/api/nodes/n2/certificate", "", nil); code != http.StatusBadGateway {
		t.Errorf("renew on an offline node: status %d", code)
	}

	// A node with servers of networks stays unless they may leave them.
	if code := serve(admin, "DELETE", "/api/nodes/n1", "", nil); code != http.StatusConflict {
		t.Fatalf("delete without releasing: status %d", code)
	}
	if code := serve(admin.Only([]access.Permission{access.NodesDelete}), "DELETE", "/api/nodes/n1?release=true", "", nil); code != http.StatusForbidden {
		t.Fatalf("delete without managing networks: status %d", code)
	}
	if code := serve(admin, "DELETE", "/api/nodes/n1?release=true", "", nil); code != http.StatusNoContent || !w.reconciles() {
		t.Fatalf("delete: status %d", code)
	}
	if code := serve(admin, "GET", "/api/nodes/n1", "", nil); code != http.StatusNotFound {
		t.Errorf("a deleted node: status %d", code)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if !slices.Equal(w.removed, []string{"n1"}) || !slices.Equal(w.left, []tag.Server{{NodeID: "n2", ServerID: "lobby"}}) || w.tidied != 1 {
		t.Errorf("removed %q, servers left %v, tidied %d times", w.removed, w.left, w.tidied)
	}
}
