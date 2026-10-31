package docker

import (
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"sync"
	"testing"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

// engineState is the containers of a fake engine by name, with a failure set up for a step.
type engineState struct {
	mu         sync.Mutex
	containers map[string]*fakeContainer
	ids        int
	fail       string // the step that fails: stop, rename, create, start or remove
}

type fakeContainer struct {
	id      int
	running bool
}

// snapshot returns the containers by name with their IDs, the running ones marked with a +.
func (e *engineState) snapshot() map[string]int {
	e.mu.Lock()
	defer e.mu.Unlock()
	m := map[string]int{}
	for name, c := range e.containers {
		m[name] = c.id
		if c.running {
			m[name] = -c.id
		}
	}
	return m
}

// statefulEngine returns a runtime connected to an engine that keeps containers by name.
func statefulEngine(t *testing.T, e *engineState) *Docker {
	t.Helper()
	// step runs a step on the container of the request, or answers that it failed or is missing.
	step := func(name string, fn func(w http.ResponseWriter, r *http.Request, c *fakeContainer)) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			e.mu.Lock()
			defer e.mu.Unlock()
			c := e.containers[r.PathValue("name")]
			switch {
			case c == nil:
				w.WriteHeader(http.StatusNotFound)
				_ = json.NewEncoder(w).Encode(map[string]string{"message": "no such container"})
			case e.fail == name:
				w.WriteHeader(http.StatusInternalServerError)
				_ = json.NewEncoder(w).Encode(map[string]string{"message": name + " failed"})
			default:
				fn(w, r, c)
			}
		}
	}
	return fakeEngine(t, false, "", map[string]http.HandlerFunc{
		"GET /v1.41/containers/{name}/json": step("inspect", func(w http.ResponseWriter, _ *http.Request, c *fakeContainer) {
			_ = json.NewEncoder(w).Encode(container.InspectResponse{State: &container.State{Running: c.running}})
		}),
		"GET /v1.41/containers/json": func(w http.ResponseWriter, _ *http.Request) {
			e.mu.Lock()
			defer e.mu.Unlock()
			items := []container.Summary{}
			for name := range e.containers {
				items = append(items, container.Summary{Names: []string{"/" + name}})
			}
			_ = json.NewEncoder(w).Encode(items)
		},
		"POST /v1.41/containers/{name}/stop": step("stop", func(w http.ResponseWriter, _ *http.Request, c *fakeContainer) {
			c.running = false
			w.WriteHeader(http.StatusNoContent)
		}),
		"POST /v1.41/containers/{name}/start": step("start", func(w http.ResponseWriter, _ *http.Request, c *fakeContainer) {
			c.running = true
			w.WriteHeader(http.StatusNoContent)
		}),
		"POST /v1.41/containers/{name}/rename": step("rename", func(w http.ResponseWriter, r *http.Request, c *fakeContainer) {
			to := r.URL.Query().Get("name")
			if e.containers[to] != nil {
				w.WriteHeader(http.StatusConflict)
				_ = json.NewEncoder(w).Encode(map[string]string{"message": "name in use"})
				return
			}
			delete(e.containers, r.PathValue("name"))
			e.containers[to] = c
			w.WriteHeader(http.StatusNoContent)
		}),
		"DELETE /v1.41/containers/{name}": step("remove", func(w http.ResponseWriter, r *http.Request, c *fakeContainer) {
			if c.running && r.URL.Query().Get("force") == "" {
				w.WriteHeader(http.StatusConflict)
				return
			}
			delete(e.containers, r.PathValue("name"))
			w.WriteHeader(http.StatusNoContent)
		}),
		"POST /v1.41/containers/create": func(w http.ResponseWriter, r *http.Request) {
			e.mu.Lock()
			defer e.mu.Unlock()
			name := r.URL.Query().Get("name")
			if e.containers[name] != nil {
				w.WriteHeader(http.StatusConflict)
				return
			}
			// A failed create may leave a half-made container behind.
			e.ids++
			e.containers[name] = &fakeContainer{id: e.ids}
			if e.fail == "create" {
				w.WriteHeader(http.StatusInternalServerError)
				_ = json.NewEncoder(w).Encode(map[string]string{"message": "create failed"})
				return
			}
			_ = json.NewEncoder(w).Encode(container.CreateResponse{ID: name})
		},
	})
}

// testReplacement replaces the container name in the fake engine.
func testReplacement(d *Docker, name string, running bool) replacement {
	return replacement{
		name: name, running: running,
		stop: func(ctx context.Context) error {
			_, err := d.cli.ContainerStop(ctx, name, client.ContainerStopOptions{})
			return err
		},
		create: func(ctx context.Context) error {
			_, err := d.cli.ContainerCreate(ctx, client.ContainerCreateOptions{Name: name, Config: &container.Config{Image: "i"}})
			return err
		},
		start: func(ctx context.Context) error {
			_, err := d.cli.ContainerStart(ctx, name, client.ContainerStartOptions{})
			return err
		},
	}
}

// A failure at any step of a replacement leaves the container as it was, running if it ran;
// one after the new container runs leaves the old one to the next replacement, which tidies up.
func TestReplaceFailures(t *testing.T) {
	const name = "noryx-a"
	for _, tc := range []struct {
		fail    string
		running bool
		err     bool
		want    map[string]int
	}{
		{"", true, false, map[string]int{name: -2}},
		{"", false, false, map[string]int{name: 2}},
		{"stop", true, true, map[string]int{name: -1}},
		{"rename", true, true, map[string]int{name: -1}},
		{"rename", false, true, map[string]int{name: 1}},
		{"create", true, true, map[string]int{name: -1}},
		{"create", false, true, map[string]int{name: 1}},
		{"start", true, true, map[string]int{name: 1}}, // the old one can't start either
		{"remove", true, false, map[string]int{name: -2, name + oldSuffix: 1}},
	} {
		e := &engineState{containers: map[string]*fakeContainer{name: {id: 1, running: tc.running}}, ids: 1, fail: tc.fail}
		d := statefulEngine(t, e)
		// The replacement goes on although the call ended, e.g. with its timeout.
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		err := d.replace(ctx, testReplacement(d, name, tc.running))
		if (err != nil) != tc.err || !maps.Equal(e.snapshot(), tc.want) {
			t.Errorf("%s failing, running %v: %v, containers %v, want %v", tc.fail, tc.running, err, e.snapshot(), tc.want)
		}
		if _, busy := d.replacing.Load(name); busy {
			t.Errorf("%s failing: still marked as being replaced", tc.fail)
		}
	}

	// The next replacement removes what the last one couldn't.
	e := &engineState{containers: map[string]*fakeContainer{name: {id: 2}, name + oldSuffix: {id: 1}}, ids: 2}
	d := statefulEngine(t, e)
	if err := d.replace(t.Context(), testReplacement(d, name, false)); err != nil || !maps.Equal(e.snapshot(), map[string]int{name: 3}) {
		t.Errorf("after a stale old container: %v, containers %v", err, e.snapshot())
	}
}

// When the agent or the runtime stopped amid a replacement, the old container comes back as
// long as the new one doesn't exist, and is removed otherwise; containers being replaced right
// now stay as they are.
func TestReconcile(t *testing.T) {
	e := &engineState{containers: map[string]*fakeContainer{
		"noryx-a-old": {id: 1},                                    // renamed, not created yet
		"noryx-b-old": {id: 2}, "noryx-b": {id: 3, running: true}, // created, the old one not removed
		"noryx-c":     {id: 4}, // nothing to do
		"noryx-d-old": {id: 5}, // being replaced
	}}
	d := statefulEngine(t, e)
	d.claim("noryx-d")
	if err := d.reconcileAll(t.Context()); err != nil {
		t.Fatal(err)
	}
	want := map[string]int{"noryx-a": 1, "noryx-b": -3, "noryx-c": 4, "noryx-d-old": 5}
	if !maps.Equal(e.snapshot(), want) {
		t.Errorf("containers %v, want %v", e.snapshot(), want)
	}
	// A replacement of a container that is being replaced already is refused.
	err := d.replace(t.Context(), testReplacement(d, "noryx-d", false))
	if err == nil || !maps.Equal(e.snapshot(), want) {
		t.Errorf("replacing twice: %v, containers %v", err, e.snapshot())
	}
}
