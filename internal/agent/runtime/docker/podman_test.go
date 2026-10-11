package docker

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/events"
	"github.com/moby/moby/api/types/system"
	"github.com/moby/moby/client"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	"github.com/QwikByte/noryx/internal/agent/rcon"
	"github.com/QwikByte/noryx/internal/agent/runtime"
	"github.com/QwikByte/noryx/internal/agent/storage"
)

// live returns the runtime of the node for tests that run containers, which need root and
// the images: Docker with NORYX_DOCKER_TEST set, Podman as root with NORYX_DOCKER_TEST=podman.
func live(t *testing.T) *Docker {
	t.Helper()
	choice := os.Getenv("NORYX_DOCKER_TEST")
	if choice == "" {
		t.Skip("set NORYX_DOCKER_TEST to run containers in Docker, or to podman for Podman")
	}
	d, err := New(storage.New(t.TempDir()), Options{Podman: choice == noryxv1.RuntimePodman})
	must(t, err)
	t.Cleanup(func() { _ = d.Close() })
	return d
}

// fakeEngine returns a runtime connected to an API that answers like Docker, or like Podman of
// the version libpod as root, with the handlers of routes.
func fakeEngine(t *testing.T, podmanAgent bool, libpod string, routes map[string]http.HandlerFunc) *Docker {
	t.Helper()
	mux := http.NewServeMux()
	if routes[podmanInfo] == nil {
		mux.HandleFunc(podmanInfo, reply(map[string]any{"host": map[string]any{"security": map[string]bool{"rootless": false}}}))
	}
	for pattern, handler := range routes {
		mux.HandleFunc(pattern, handler)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Api-Version", "1.41")
		if libpod != "" {
			w.Header().Set("Libpod-Api-Version", libpod)
		}
		if r.URL.Path == "/_ping" {
			return
		}
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	d := &Docker{consoles: rcon.NewConsoles()}
	if podmanAgent {
		d.podman = &podman{}
	}
	var err error
	d.cli, err = client.New(client.WithHost("tcp://"+strings.TrimPrefix(srv.URL, "http://")), client.WithResponseHook(d.check))
	must(t, err)
	return d
}

func reply(v any) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) { _ = json.NewEncoder(w).Encode(v) }
}

// podmanInfo is the route of Podman's own information.
const podmanInfo = "GET /v4.0.0/libpod/info"

// The agent refuses a runtime other than the one it is set to use, and a Podman too old or
// rootless, as it relies on what each one does differently, e.g. to keep servers apart.
func TestRuntimeMismatch(t *testing.T) {
	for _, tc := range []struct {
		name     string
		podman   bool
		libpod   string
		rootless bool
		err      string // empty if accepted
	}{
		{"Docker", false, "", false, ""},
		{"Podman at Docker's socket", false, "5.4.2", false, "Podman 5.4.2 answers at the socket of Docker"},
		{"Docker at Podman's socket", true, "", false, "Docker answers at the socket of Podman"},
		{"Podman too old", true, "4.3.1", false, "Podman 4.3.1 is too old, the agent needs 4.9.0 or newer"},
		{"rootless Podman", true, "5.4.2", true, "Podman runs rootless"},
		{"Podman", true, "5.4.2", false, ""},
	} {
		asked := 0
		d := fakeEngine(t, tc.podman, tc.libpod, map[string]http.HandlerFunc{
			"GET /v1.41/info": reply(system.Info{ServerVersion: "1.2.3"}),
			podmanInfo: func(w http.ResponseWriter, r *http.Request) {
				asked++
				reply(map[string]any{"host": map[string]any{"security": map[string]bool{"rootless": tc.rootless}}})(w, r)
			},
		})
		got, err := d.Info(t.Context())
		switch {
		case tc.err == "" && (err != nil || got.Version != "1.2.3"):
			t.Errorf("%s: %+v, %v", tc.name, got, err)
		case tc.err == "" && tc.podman:
			// Podman is asked once whether it runs as root.
			if _, err := d.Info(t.Context()); err != nil || asked != 1 {
				t.Errorf("%s: asked %d times, %v", tc.name, asked, err)
			}
		case tc.err != "" && (!errors.Is(err, runtime.ErrWrongRuntime) || !strings.Contains(err.Error(), tc.err)):
			t.Errorf("%s: error %v, want %q", tc.name, err, tc.err)
		case tc.err != "":
			// Other calls fail too, with the reason.
			if _, err := d.cli.ContainerList(t.Context(), client.ContainerListOptions{}); err == nil || !strings.Contains(err.Error(), tc.err) {
				t.Errorf("%s: list: %v", tc.name, err)
			}
		}
	}
}

// Podman leaves the health checks of the images of itzg out, and before 5.8 starts only
// containers whose restart policy is always at boot; Docker's containers stay as they are, with
// the health check that game servers get on both.
func TestAdapt(t *testing.T) {
	server := func() client.ContainerCreateOptions {
		opts, err := containerOptions(runtime.Spec{ID: "s", Type: noryxv1.ServerType_SERVER_TYPE_PAPER, Port: 25565, Java: "21"}, "/data", sharedNetwork)
		must(t, err)
		return opts
	}
	docker := &Docker{}
	opts := server()
	docker.adapt(&opts)
	if hc := opts.Config.Healthcheck; hc == nil || hc.Test[1] != "mc-health" || opts.HostConfig.RestartPolicy.Name != container.RestartPolicyUnlessStopped || opts.HostConfig.Binds[0] != "/data:/data" {
		t.Errorf("Docker: health check %v, restart policy %s, mounts %v", opts.Config.Healthcheck, opts.HostConfig.RestartPolicy.Name, opts.HostConfig.Binds)
	}
	for version, policy := range map[string]container.RestartPolicyMode{
		"":       container.RestartPolicyAlways, // not known yet
		"v5.4.2": container.RestartPolicyAlways,
		"v5.8.0": container.RestartPolicyUnlessStopped,
		"v6.1.3": container.RestartPolicyUnlessStopped,
	} {
		d := &Docker{podman: &podman{}}
		if version != "" {
			d.podman.version.Store(&version)
		}
		opts := server()
		d.adapt(&opts)
		if hc := opts.Config.Healthcheck; hc == nil || hc.Test[1] != "mc-health" || opts.HostConfig.RestartPolicy.Name != policy || opts.HostConfig.Binds[0] != "/data:/data:Z" {
			t.Errorf("Podman %s: health check %v, restart policy %s, want %s", version, hc, opts.HostConfig.RestartPolicy.Name, policy)
		}
	}
	d := &Docker{podman: &podman{}}
	proxy, err := containerOptions(runtime.Spec{ID: "p", Type: noryxv1.ServerType_SERVER_TYPE_VELOCITY, Port: 25577, RestartPolicy: noryxv1.RestartPolicy_RESTART_POLICY_NEVER}, "/data", "noryx-proxy-p")
	must(t, err)
	d.adapt(&proxy)
	if hc := proxy.Config.Healthcheck; hc == nil || hc.Test[1] != "/usr/bin/health.sh" || proxy.HostConfig.RestartPolicy.Name != container.RestartPolicyDisabled {
		t.Errorf("Podman, proxy: health check %v, restart policy %s", hc, proxy.HostConfig.RestartPolicy.Name)
	}
	ds, err := datastoreOptions(runtime.DatastoreSpec{ID: "d", Engine: noryxv1.DatastoreEngine_DATASTORE_ENGINE_MARIADB, Version: "11.8"}, "/srv/d")
	must(t, err)
	d.adapt(&ds)
	// SELinux labels the mounts for the container alone.
	if want := []string{"/srv/d/data-11.8:/var/lib/mysql:Z", "/srv/d/superuser:/run/noryx/superuser:ro,Z"}; ds.Config.Healthcheck.Test[1] != "healthcheck.sh" || !slices.Equal(ds.HostConfig.Binds, want) {
		t.Errorf("Podman, datastore: health check %v, mounts %v", ds.Config.Healthcheck, ds.HostConfig.Binds)
	}
}

// Podman runs only images named with their registry, unless it is set up otherwise.
func TestImagesNameTheirRegistry(t *testing.T) {
	refs := []string{serverImage, proxyImage}
	for _, e := range engines {
		refs = append(refs, e.image)
	}
	for _, ref := range refs {
		if !strings.HasPrefix(ref, "docker.io/") {
			t.Errorf("%s doesn't name its registry", ref)
		}
	}
}

// Podman leaves the health of containers out of its list, so the agent asks for those that
// start or fail.
func TestListHealthOnPodman(t *testing.T) {
	summary := func(id string) container.Summary {
		return container.Summary{ID: id, Names: []string{"/noryx-" + id}, State: container.StateRunning, Status: "Up 2 minutes",
			Labels: map[string]string{labelManaged: "true", labelSpec: `{"id":"` + id + `"}`}}
	}
	d := fakeEngine(t, true, "5.4.2", map[string]http.HandlerFunc{
		"GET /v1.41/containers/json": func(w http.ResponseWriter, r *http.Request) {
			items := []container.Summary{summary("a"), summary("b"), summary("c")}
			switch filters := r.URL.Query().Get("filters"); {
			case strings.Contains(filters, `"health":{"starting":true}`):
				items = items[1:2]
			case strings.Contains(filters, `"health":{"unhealthy":true}`):
				items = items[2:]
			}
			_ = json.NewEncoder(w).Encode(items)
		},
		"GET /v1.41/containers/{id}/json": reply(container.InspectResponse{State: &container.State{Running: true}}),
	})
	servers, err := d.List(t.Context())
	if err != nil || len(servers) != 3 {
		t.Fatalf("%v, %v", servers, err)
	}
	want := []struct {
		state     noryxv1.ServerState
		unhealthy bool
	}{{noryxv1.ServerState_SERVER_STATE_RUNNING, false}, {noryxv1.ServerState_SERVER_STATE_STARTING, false}, {noryxv1.ServerState_SERVER_STATE_RUNNING, true}}
	for i, s := range servers {
		if s.State != want[i].state || s.Unhealthy != want[i].unhealthy {
			t.Errorf("%s: %v, unhealthy %v", s.ID, s.State, s.Unhealthy)
		}
	}
}

// Podman tells a crash by "restart" after "died", without how long the server ran, and the
// status of its health only on inspection.
func TestPodmanEvents(t *testing.T) {
	var mu sync.Mutex
	stopped := 0
	d := fakeEngine(t, true, "5.4.2", map[string]http.HandlerFunc{
		"GET /v1.41/containers/{id}/json": func(w http.ResponseWriter, r *http.Request) {
			labels := map[string]string{labelManaged: "true", labelSpec: `{"id":"s1"}`}
			health := &container.Health{Status: container.Unhealthy}
			_ = json.NewEncoder(w).Encode(container.InspectResponse{Config: &container.Config{Labels: labels}, State: &container.State{Running: true, Health: health}})
		},
		"POST /v1.41/containers/{id}/stop": func(http.ResponseWriter, *http.Request) {
			mu.Lock()
			stopped++
			mu.Unlock()
		},
	})
	logged := captureLog(t)
	w := watched{inRow: map[string]int{}, unhealthy: map[string]bool{}, lives: map[string]*life{}}
	labels := map[string]string{labelSpec: `{"id":"s1"}`, "exitCode": "1"}
	at := time.Unix(1_800_000_000, 0)
	event := func(action events.Action, after time.Duration) {
		at = at.Add(after)
		d.podmanEvent(t.Context(), events.Message{Action: action, Actor: events.Actor{ID: "c1", Attributes: labels}, TimeNano: at.UnixNano()}, w)
	}

	// A restart through the API, and a start by the agent, are no crashes.
	event(events.ActionStart, 0)
	event(events.ActionRestart, time.Hour)
	event(events.ActionDie, time.Second)
	event(events.ActionStart, time.Second)
	if len(w.inRow) != 0 {
		t.Fatalf("crashes in a row after a restart: %v", w.inRow)
	}
	// Crashes soon after the start make a row, which a start by the agent ends.
	for range 2 {
		event(events.ActionDie, 3*time.Second)
		event(events.ActionRestart, 0)
		event(events.ActionStart, time.Second)
	}
	if w.inRow["c1"] != 2 {
		t.Fatalf("crashes in a row: %d, want 2", w.inRow["c1"])
	}
	event(events.ActionDie, time.Minute)
	event(events.ActionStart, time.Second)
	if len(w.inRow) != 0 {
		t.Fatalf("crashes in a row after a start by the agent: %v", w.inRow)
	}
	// One that ran long enough doesn't continue a row; maxCrashes in a row stop the server.
	event(events.ActionDie, stableAfter)
	event(events.ActionRestart, 0)
	event(events.ActionStart, time.Second)
	for range maxCrashes - 1 {
		event(events.ActionDie, 3*time.Second)
		event(events.ActionRestart, 0)
		event(events.ActionStart, time.Second)
	}
	event(events.ActionHealthStatus, time.Second)

	entries := logged()
	if len(entries) != maxCrashes+3 {
		t.Fatalf("logged %d entries, want %d: %v", len(entries), maxCrashes+3, entries)
	}
	if e := entries[2]; e["crashes"] != float64(1) || e["exit_code"] != "1" {
		t.Errorf("crash after a stable run: %v", e)
	}
	if e := entries[len(entries)-2]; !strings.HasPrefix(e["msg"].(string), "Stop a server") || e["crashes"] != float64(maxCrashes) {
		t.Errorf("last crash in a row: %v", e)
	}
	if e := entries[len(entries)-1]; !strings.Contains(e["msg"].(string), "unhealthy") || e["server"] != "s1" {
		t.Errorf("health: %v", e)
	}
	time.Sleep(100 * time.Millisecond) // the stop runs in the background
	mu.Lock()
	defer mu.Unlock()
	if stopped != 1 {
		t.Errorf("stopped %d times, want 1", stopped)
	}
}

// Podman before 5.8 tells the time of log lines in whole seconds.
func TestNewer(t *testing.T) {
	after := time.Date(2026, 10, 8, 12, 0, 5, 300_000_000, time.UTC)
	for _, tc := range []struct {
		line time.Time
		want bool
	}{
		{after, false},
		{after.Add(time.Nanosecond), true},
		{after.Add(-time.Millisecond), false},
		{time.Date(2026, 10, 8, 12, 0, 5, 0, time.UTC), true}, // whole seconds
		{time.Date(2026, 10, 8, 12, 0, 4, 0, time.UTC), false},
	} {
		if got := newer(tc.line, after); got != tc.want {
			t.Errorf("newer(%s) = %v, want %v", tc.line.Format(time.RFC3339Nano), got, tc.want)
		}
	}
}
