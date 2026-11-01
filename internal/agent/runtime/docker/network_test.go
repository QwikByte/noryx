package docker

import (
	"encoding/json"
	"maps"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"golang.org/x/sys/unix"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	"github.com/QwikByte/noryx/internal/agent/runtime"
)

// A proxy has a network of its own, which its backends keep when their containers are
// created again; other servers are in the shared network, where they can't reach each other.
func TestPlacement(t *testing.T) {
	in := func(names ...string) container.InspectResponse {
		c := container.InspectResponse{NetworkSettings: &container.NetworkSettings{Networks: map[string]*network.EndpointSettings{}}}
		for _, n := range names {
			c.NetworkSettings.Networks[n] = &network.EndpointSettings{}
		}
		return c
	}
	proxy := runtime.Spec{ID: "proxy", Type: noryxv1.ServerType_SERVER_TYPE_VELOCITY}
	backend := runtime.Spec{ID: "lobby", Type: noryxv1.ServerType_SERVER_TYPE_PAPER, BehindProxy: true}
	standalone := runtime.Spec{ID: "survival", Type: noryxv1.ServerType_SERVER_TYPE_PAPER}
	for _, tc := range []struct {
		name string
		c    container.InspectResponse
		spec runtime.Spec
		want string
	}{
		{"proxy", in(sharedNetwork), proxy, "noryx-proxy-proxy"},
		{"backend", in("noryx-proxy-proxy"), backend, "noryx-proxy-proxy"},
		{"backend that left its network", in("noryx-proxy-proxy"), standalone, sharedNetwork},
		{"standalone", in(sharedNetwork), standalone, sharedNetwork},
	} {
		if got := placement(tc.c, tc.spec); got != tc.want {
			t.Errorf("%s: placement = %s, want %s", tc.name, got, tc.want)
		}
	}
}

// On Podman, the bridges of the agent's networks have names that its tables match. A network
// with another, e.g. one created by hand, is created again while no container uses it, and
// refused while one does; Docker's networks stay as they are.
func TestPodmanNetworks(t *testing.T) {
	names := map[string]bool{}
	for _, n := range []string{sharedNetwork, proxyNetwork(runtime.NewID()), datastoreName(runtime.NewID()), portNetwork(runtime.NewID())} {
		b := bridge(n)
		if len(b) >= unix.IFNAMSIZ || !strings.HasPrefix(b, bridgePrefix) || names[b] {
			t.Errorf("bridge %q of %s", b, n)
		}
		names[b] = true
	}
	if bridge(sharedNetwork) != "noryx-servers" {
		t.Errorf("shared bridge %s", bridge(sharedNetwork))
	}

	bridges := map[string]string{"noryx-proxy-ok": bridge("noryx-proxy-ok"), "noryx-proxy-old": "podman3", "noryx-proxy-used": "podman4"}
	notFound := func(w http.ResponseWriter) {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]string{"message": "network not found"})
	}
	var created []network.CreateRequest
	var removed []string
	engine := func(podman bool, libpod string) *Docker {
		created, removed = nil, nil
		return fakeEngine(t, podman, libpod, map[string]http.HandlerFunc{
			"GET /v1.41/networks/{name}": func(w http.ResponseWriter, r *http.Request) { // Docker's API leaves the bridge out
				if bridges[r.PathValue("name")] == "" {
					notFound(w)
					return
				}
				_ = json.NewEncoder(w).Encode(network.Inspect{Network: network.Network{Name: r.PathValue("name")}})
			},
			"GET /v4.0.0/libpod/networks/{name}/json": func(w http.ResponseWriter, r *http.Request) {
				if b := bridges[r.PathValue("name")]; b != "" {
					_ = json.NewEncoder(w).Encode(map[string]string{"name": r.PathValue("name"), "network_interface": b})
					return
				}
				notFound(w)
			},
			"GET /v1.41/containers/json": func(w http.ResponseWriter, r *http.Request) {
				items := []container.Summary{}
				if strings.Contains(r.URL.Query().Get("filters"), "noryx-proxy-used") {
					items = append(items, container.Summary{ID: "c"})
				}
				_ = json.NewEncoder(w).Encode(items)
			},
			"DELETE /v1.41/networks/{name}": func(w http.ResponseWriter, r *http.Request) {
				removed = append(removed, r.PathValue("name"))
				w.WriteHeader(http.StatusNoContent)
			},
			"POST /v1.41/networks/create": func(w http.ResponseWriter, r *http.Request) {
				var req network.CreateRequest
				must(t, json.NewDecoder(r.Body).Decode(&req))
				created = append(created, req)
				_ = json.NewEncoder(w).Encode(network.CreateResponse{ID: "n"})
			},
		})
	}

	d := engine(true, "4.9.3")
	for _, name := range []string{"noryx-proxy-ok", "noryx-proxy-new", "noryx-proxy-old"} {
		must(t, d.ensureNetwork(t.Context(), name))
	}
	if !slices.Equal(removed, []string{"noryx-proxy-old"}) || len(created) != 2 {
		t.Fatalf("removed %v, created %v", removed, created)
	}
	for _, req := range created {
		if req.Options["com.docker.network.bridge.name"] != bridge(req.Name) || req.Labels[labelManaged] != "true" {
			t.Errorf("created %+v", req)
		}
	}
	if err := d.ensureNetwork(t.Context(), "noryx-proxy-used"); err == nil || !strings.Contains(err.Error(), "lacks the bridge") || len(removed) > 1 {
		t.Errorf("a network in use: %v, removed %v", err, removed)
	}
	// Docker's shared network doesn't let its containers communicate.
	d = engine(false, "")
	for _, name := range []string{"noryx-proxy-used", sharedNetwork} {
		must(t, d.ensureNetwork(t.Context(), name))
	}
	if len(removed) > 0 || len(created) != 1 || !maps.Equal(created[0].Options, map[string]string{"com.docker.network.bridge.enable_icc": "false"}) {
		t.Errorf("Docker: removed %v, created %+v", removed, created)
	}
}
