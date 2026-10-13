package docker

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"slices"
	"sync"
	"testing"

	"github.com/moby/moby/api/types/container"
)

// fakeNames is a fake runtime that knows containers by their names, with what each holds, and
// renames and removes them like Docker.
type fakeNames struct {
	mu         sync.Mutex
	containers map[string]string
	noRemove   bool // removing a container that doesn't run fails
}

func (e *fakeNames) docker(t *testing.T) *Docker {
	notFound := func(w http.ResponseWriter) {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]string{"message": "No such container"})
	}
	return fakeEngine(t, false, "", map[string]http.HandlerFunc{
		"GET /v1.41/containers/{name}/json": func(w http.ResponseWriter, r *http.Request) {
			e.mu.Lock()
			defer e.mu.Unlock()
			if _, ok := e.containers[r.PathValue("name")]; !ok {
				notFound(w)
				return
			}
			_ = json.NewEncoder(w).Encode(container.InspectResponse{Name: "/" + r.PathValue("name")})
		},
		"GET /v1.41/containers/json": func(w http.ResponseWriter, _ *http.Request) {
			e.mu.Lock()
			defer e.mu.Unlock()
			var list []container.Summary
			for name := range e.containers {
				list = append(list, container.Summary{Names: []string{"/" + name}})
			}
			_ = json.NewEncoder(w).Encode(list)
		},
		"POST /v1.41/containers/{name}/rename": func(w http.ResponseWriter, r *http.Request) {
			e.mu.Lock()
			defer e.mu.Unlock()
			from, to := r.PathValue("name"), r.URL.Query().Get("name")
			held, ok := e.containers[from]
			switch _, taken := e.containers[to]; {
			case !ok:
				notFound(w)
			case taken:
				w.WriteHeader(http.StatusConflict)
				_ = json.NewEncoder(w).Encode(map[string]string{"message": "name in use"})
			default:
				delete(e.containers, from)
				e.containers[to] = held
				w.WriteHeader(http.StatusNoContent)
			}
		},
		"DELETE /v1.41/containers/{name}": func(w http.ResponseWriter, r *http.Request) {
			e.mu.Lock()
			defer e.mu.Unlock()
			name := r.PathValue("name")
			switch _, ok := e.containers[name]; {
			case !ok:
				notFound(w)
			case e.noRemove && r.URL.Query().Get("force") != "1":
				w.WriteHeader(http.StatusInternalServerError)
				_ = json.NewEncoder(w).Encode(map[string]string{"message": "can't remove"})
			default:
				delete(e.containers, name)
				w.WriteHeader(http.StatusNoContent)
			}
		},
	})
}

func (e *fakeNames) state() map[string]string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return maps.Clone(e.containers)
}

// A replacement that fails after any step leaves the container as it was, started again if it ran;
// one that succeeds leaves the new one, also if the old one can't be removed, which goes later.
func TestReplace(t *testing.T) {
	captureLog(t)
	errStep := errors.New("step failed")
	for _, tc := range []struct {
		name     string
		failAt   string
		noRemove bool
		want     map[string]string
		started  []string
	}{
		{"stop fails", "stop", false, map[string]string{"noryx-s1": "old"}, nil},
		{"create fails", "create", false, map[string]string{"noryx-s1": "old"}, []string{"old"}},
		{"start fails", "start", false, map[string]string{"noryx-s1": "old"}, []string{"new", "old"}},
		{"done", "", false, map[string]string{"noryx-s1": "new"}, []string{"new"}},
		{"old stays", "", true, map[string]string{"noryx-s1": "new", "noryx-s1-old": "old"}, []string{"new"}},
	} {
		e := &fakeNames{containers: map[string]string{"noryx-s1": "old"}, noRemove: tc.noRemove}
		d := e.docker(t)
		var started []string
		step := func(name string, do func()) func(context.Context) error {
			return func(context.Context) error {
				if name == tc.failAt && (name != "start" || len(started) == 0) {
					if name == "start" {
						started = append(started, e.state()["noryx-s1"])
					}
					return errStep
				}
				do()
				return nil
			}
		}
		err := d.replace(t.Context(), "noryx-s1", true,
			step("stop", func() {}),
			step("create", func() {
				e.mu.Lock()
				e.containers["noryx-s1"] = "new"
				e.mu.Unlock()
			}),
			step("start", func() { started = append(started, e.state()["noryx-s1"]) }))
		if (err != nil) != (tc.failAt != "") || tc.failAt != "" && !errors.Is(err, errStep) {
			t.Errorf("%s: err %v", tc.name, err)
		}
		if got := e.state(); !maps.Equal(got, tc.want) {
			t.Errorf("%s: containers %v, want %v", tc.name, got, tc.want)
		}
		if !slices.Equal(started, tc.started) {
			t.Errorf("%s: started %v, want %v", tc.name, started, tc.started)
		}
	}
}

// What an agent that stopped while it replaced containers left is put right: an old container
// without a new one gets its name back, and one next to a new one goes, also before the next
// replacement.
func TestReconcile(t *testing.T) {
	captureLog(t)
	e := &fakeNames{containers: map[string]string{
		"noryx-s1-old": "old", "noryx-s2": "new", "noryx-s2-old": "old", "noryx-db-d1-old": "old", "other-old": "foreign",
	}}
	d := e.docker(t)
	if err := d.reconcileAll(t.Context()); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"noryx-s1": "old", "noryx-s2": "new", "noryx-db-d1": "old", "other-old": "foreign"}
	if got := e.state(); !maps.Equal(got, want) {
		t.Fatalf("containers %v, want %v", got, want)
	}

	// Without that, the old container of an earlier replacement would be in the way of the next one.
	e.mu.Lock()
	e.containers["noryx-s1-old"] = "older"
	e.mu.Unlock()
	noop := func(context.Context) error { return nil }
	create := func(context.Context) error {
		e.mu.Lock()
		defer e.mu.Unlock()
		e.containers["noryx-s1"] = "new"
		return nil
	}
	if err := d.replace(t.Context(), "noryx-s1", false, noop, create, noop); err != nil {
		t.Fatal(err)
	}
	if got := e.state(); !maps.Equal(got, map[string]string{"noryx-s1": "new", "noryx-s2": "new", "noryx-db-d1": "old", "other-old": "foreign"}) {
		t.Fatalf("containers after the next replacement: %v", got)
	}
}
