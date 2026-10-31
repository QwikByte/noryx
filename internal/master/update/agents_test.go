package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	"github.com/QwikByte/noryx/internal/buildinfo"
	"github.com/QwikByte/noryx/internal/master/access"
	"github.com/QwikByte/noryx/internal/master/database"
	"github.com/QwikByte/noryx/internal/master/httpapi"
	"github.com/QwikByte/noryx/internal/master/node/nodetest"
)

// agent is an agent of a version, which installs the version the master asks for.
type agent struct {
	noryxv1.UnimplementedNodeServiceServer

	mu      sync.Mutex
	version string
	asked   []string // the versions it was asked to install
	refuse  error
}

func (a *agent) GetInfo(context.Context, *noryxv1.GetInfoRequest) (*noryxv1.GetInfoResponse, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return &noryxv1.GetInfoResponse{AgentVersion: a.version}, nil
}

func (a *agent) Update(_ context.Context, req *noryxv1.UpdateRequest) (*noryxv1.UpdateResponse, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.asked = append(a.asked, req.GetVersion())
	return &noryxv1.UpdateResponse{}, a.refuse
}

func (a *agent) set(fn func(a *agent)) {
	a.mu.Lock()
	defer a.mu.Unlock()
	fn(a)
}

func (a *agent) updates() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Clone(a.asked)
}

// updating is a master of version v1.3.0, of a package unless the unit is removed, with the
// releases of a fake GitHub. The agents of its nodes n1 and n2 are older, that of n3 is as new,
// n4 doesn't answer and n5 isn't enrolled yet.
type updating struct {
	*Service
	fakes  map[string]*agent      // the agents of the nodes, by node
	latest atomic.Pointer[string] // GitHub's answer for the latest release, or "status <code>"
}

// github sets GitHub's answer for the latest release.
func (u *updating) github(answer string) { u.latest.Store(&answer) }

func newUpdating(t *testing.T, follow bool) *updating {
	t.Helper()
	version := buildinfo.Version
	t.Cleanup(func() { buildinfo.Version = version })
	buildinfo.Version = "v1.3.0"
	db, err := database.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	nodes := nodetest.New(t, db)
	u := &updating{fakes: map[string]*agent{}}
	u.github(`{"tag_name": "v1.4.0", "body": "New: something"}`)
	for id, version := range map[string]string{"n1": "v1.2.0", "n2": "v1.3.0-rc.1", "n3": "v1.3.0"} {
		a := &agent{version: version}
		u.fakes[id] = a
		nodes.Add(t, id, func(s *grpc.Server) { noryxv1.RegisterNodeServiceServer(s, a) })
	}
	nodes.Add(t, "n4", nil)
	if _, err := db.Exec(`INSERT INTO nodes (id, name, address, created_at) VALUES ('n5', 'n5', '203.0.113.5:7443', 0)`); err != nil {
		t.Fatal(err)
	}
	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		answer, code := *u.latest.Load(), http.StatusOK
		if status, ok := strings.CutPrefix(answer, "status "); ok {
			code, _ = strconv.Atoi(status)
		}
		if r.URL.Path != "/releases/latest" {
			code = http.StatusNotFound
		}
		if code != http.StatusOK {
			http.Error(w, "GitHub doesn't know.", code)
			return
		}
		fmt.Fprint(w, answer)
	}))
	t.Cleanup(github.Close)
	dir := t.TempDir()
	unit := filepath.Join(dir, "noryx-master-update.path")
	if err := os.WriteFile(unit, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	u.Service = New(nodes.Service, config{follow: follow}, Options{DataDir: dir, API: github.URL + "/releases", Page: github.URL + "/page", Unit: unit})
	return u
}

// outdated returns the agents that the status lists as older, with their progress.
func (u *updating) outdated(t *testing.T) []string {
	t.Helper()
	st, err := u.Status(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var agents []string
	for _, a := range st.Agents {
		desc := a.NodeID + " " + a.Version
		if a.Update != nil {
			desc += " updating " + a.Update.Error
		}
		agents = append(agents, strings.TrimSpace(desc))
	}
	slices.Sort(agents)
	return agents
}

// statusOf is the HTTP status of an error of the API, or 0.
func statusOf(err error) int {
	var apiErr *httpapi.Error
	if errors.As(err, &apiErr) {
		return apiErr.Status
	}
	return 0
}

// The status tells the latest release once it is newer than the master, and why a check failed.
// A master of a package updates itself through systemd: it asks once there is a newer release,
// and tells when the update didn't start or finish in time.
func TestUpdateMaster(t *testing.T) {
	u := newUpdating(t, true)

	if err := u.UpdateMaster(t.Context()); statusOf(err) != http.StatusConflict || err.Error() != "There is no newer release." {
		t.Fatalf("UpdateMaster before a check = %v", err)
	}
	for releases, want := range map[string]string{
		"status 500":                   "GitHub answered 500 Internal Server Error",
		`{"tag_name": "nightly"}`:      `the latest release has the unexpected version "nightly"`,
		`{"tag_name": "v1.3.0"}`:       "",
		"status 404":                   "",
		`{"tag_name": 1.3}`:            "read the latest release:",
		`{"tag_name": "v1.2.9-beta1"}`: "",
	} {
		u.github(releases)
		u.Check(t.Context())
		st, err := u.Status(t.Context())
		if err != nil || !strings.HasPrefix(st.CheckError, want) || want == "" && st.CheckError != "" || st.Latest != nil || st.CheckedAt == nil {
			t.Errorf("status after GitHub answered %s: %+v, %v", releases, st, err)
		}
	}
	u.github(`{"tag_name": "v1.4.0", "body": "New: something", "published_at": "2026-10-30T12:00:00Z"}`)
	u.Check(t.Context())
	st, err := u.Status(t.Context())
	if err != nil || st.Version != "v1.3.0" || st.Latest == nil || st.Latest.Version != "v1.4.0" || st.Latest.Notes != "New: something" ||
		st.Latest.URL != buildinfo.Repository+"/releases/tag/v1.4.0" || !st.Updatable || !st.AgentsFollow || st.Prereleases || st.Master != nil {
		t.Fatalf("status with a newer release: %+v, %v", st, err)
	}

	if err := u.UpdateMaster(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, file := range []string{u.followUp, u.request} {
		if _, err := os.Stat(file); err != nil {
			t.Errorf("%s: %v", file, err)
		}
	}
	if st, err := u.Status(t.Context()); err != nil || st.Master == nil || st.Master.Error != "" {
		t.Fatalf("status while updating: %+v, %v", st, err)
	}
	u.master = time.Now().Add(-pending - time.Second)
	if st, err := u.Status(t.Context()); err != nil || !strings.HasPrefix(st.Master.Error, "The update didn't start.") {
		t.Errorf("status of an update that didn't start: %+v, %v", st.Master, err)
	}
	if err := os.Remove(u.request); err != nil { // systemd took it
		t.Fatal(err)
	}
	if st, err := u.Status(t.Context()); err != nil || !strings.HasPrefix(st.Master.Error, "The update didn't finish.") {
		t.Errorf("status of an update that didn't finish: %+v, %v", st.Master, err)
	}

	if err := os.Remove(u.unit); err != nil {
		t.Fatal(err)
	}
	if err := u.UpdateMaster(t.Context()); statusOf(err) != http.StatusConflict || !strings.Contains(err.Error(), "install.sh | sudo bash -s -- update") {
		t.Errorf("UpdateMaster without a package = %v", err)
	}
	if st, err := u.Status(t.Context()); err != nil || st.Updatable {
		t.Errorf("status without a package: %+v, %v", st, err)
	}
}

// The master updates the online agents that are older than itself to its version, each once at
// a time, and tells which failed and which took too long; agents that are as new are left out.
func TestUpdateAgents(t *testing.T) {
	u := newUpdating(t, false)

	n1, n2, n3 := u.fakes["n1"], u.fakes["n2"], u.fakes["n3"]
	if got := u.outdated(t); !slices.Equal(got, []string{"n1 v1.2.0", "n2 v1.3.0-rc.1"}) {
		t.Fatalf("outdated agents %q", got)
	}
	for id, want := range map[string]int{"n3": http.StatusConflict, "nope": http.StatusNotFound, "n4": 0} {
		if err := u.UpdateAgent(t.Context(), id); err == nil || statusOf(err) != want {
			t.Errorf("UpdateAgent(%s) = %v", id, err)
		}
	}

	n1.set(func(a *agent) {
		a.refuse = status.Error(codes.FailedPrecondition, "The agent wasn't installed from a package.")
	})
	if err := u.UpdateAgent(t.Context(), "n1"); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("UpdateAgent on a refusing agent = %v", err)
	}
	if got := u.outdated(t); !slices.Equal(got, []string{"n1 v1.2.0 updating The agent wasn't installed from a package.", "n2 v1.3.0-rc.1"}) {
		t.Fatalf("outdated agents %q", got)
	}
	n1.set(func(a *agent) { a.refuse = nil })
	if err := u.UpdateAgent(t.Context(), "n1"); err != nil { // a failed update may be tried again
		t.Fatal(err)
	}
	if err := u.UpdateAgent(t.Context(), "n1"); statusOf(err) != http.StatusConflict || err.Error() != "The agent of n1 is updating already." {
		t.Errorf("UpdateAgent while updating = %v", err)
	}
	if err := u.UpdateAgents(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(n1.updates(), []string{"v1.3.0", "v1.3.0"}) || !slices.Equal(n2.updates(), []string{"v1.3.0"}) || len(n3.updates()) != 0 {
		t.Fatalf("updates asked for: n1 %q, n2 %q, n3 %q", n1.updates(), n2.updates(), n3.updates())
	}

	// n1 took too long, n2 runs the new version now: its update is forgotten.
	u.mu.Lock()
	u.agents["n1"] = Progress{Since: time.Now().Add(-pending - time.Second)}
	u.mu.Unlock()
	n2.set(func(a *agent) { a.version = "v1.3.0" })
	if got := u.outdated(t); !slices.Equal(got, []string{"n1 v1.2.0 updating The update didn't finish. See on the node: journalctl -u noryx-agent-update"}) {
		t.Fatalf("outdated agents %q", got)
	}
	u.mu.Lock()
	if _, ok := u.agents["n2"]; ok {
		t.Error("the update of n2 is still remembered")
	}
	u.mu.Unlock()
	if err := u.UpdateAgent(t.Context(), "n1"); err != nil { // one that took too long may be tried again
		t.Fatal(err)
	}
}

// A master updated from the panel updates the agents once it runs, if the settings let them
// follow, and forgets that it should, so that it updates them only once.
func TestAgentsFollow(t *testing.T) {
	u := newUpdating(t, true)
	buildinfo.Version = "v1.3.0-4-gdeadbee" // a build that isn't a release, so that Run returns
	if err := os.WriteFile(u.followUp, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	u.Run(t.Context())
	if got := u.fakes["n1"].updates(); !slices.Equal(got, []string{"v1.3.0-4-gdeadbee"}) {
		t.Errorf("n1 was asked for %q", got)
	}
	if _, err := os.Stat(u.followUp); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("follow-up: %v", err)
	}
	u.Run(t.Context())
	if got := u.fakes["n1"].updates(); len(got) != 1 {
		t.Errorf("n1 was asked again: %q", got)
	}
}

// Only administrators update Noryx, and each answer is the status.
func TestHandler(t *testing.T) {
	u := newUpdating(t, false)

	mux := http.NewServeMux()
	NewHandler(u.Service).Register(access.NewMux(mux))
	serve := func(g access.Grants, method, path string) (int, Status) {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequestWithContext(access.WithGrants(t.Context(), g), method, path, nil))
		var st Status
		_ = json.Unmarshal(rec.Body.Bytes(), &st)
		return rec.Code, st
	}
	if code, st := serve(access.Admin(), "GET", "/api/update"); code != http.StatusOK || st.Latest != nil || len(st.Agents) != 2 {
		t.Errorf("status: %d, %+v", code, st)
	}
	if code, st := serve(access.Admin(), "POST", "/api/update/check"); code != http.StatusOK || st.Latest == nil || st.Latest.Version != "v1.4.0" {
		t.Errorf("check: %d, %+v", code, st)
	}
	if code, st := serve(access.Admin(), "POST", "/api/update/agents/n1"); code != http.StatusOK || len(st.Agents) != 2 || st.Agents[0].Update == nil {
		t.Errorf("update n1: %d, %+v", code, st)
	}
	if code, _ := serve(access.Admin(), "POST", "/api/update/agents/n3"); code != http.StatusConflict {
		t.Errorf("update n3: %d", code)
	}
	if code, st := serve(access.Admin(), "POST", "/api/update/agents"); code != http.StatusOK || len(u.fakes["n2"].updates()) != 1 || len(st.Agents) != 2 {
		t.Errorf("update the agents: %d, %+v", code, st)
	}
	if code, st := serve(access.Admin(), "POST", "/api/update/master"); code != http.StatusOK || st.Master == nil {
		t.Errorf("update the master: %d, %+v", code, st)
	}
	user := access.Admin().Only([]access.Permission{access.NodesView, access.SettingsEdit, access.UsersManage})
	for _, route := range []string{"GET /api/update", "POST /api/update/check", "POST /api/update/master", "POST /api/update/agents", "POST /api/update/agents/n1"} {
		method, path, _ := strings.Cut(route, " ")
		if code, _ := serve(user, method, path); code != http.StatusForbidden {
			t.Errorf("%s by a user who isn't an administrator: %d", route, code)
		}
	}
}
