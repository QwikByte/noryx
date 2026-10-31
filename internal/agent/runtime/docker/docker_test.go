package docker

import (
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"

	"github.com/moby/moby/api/types/network"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	"github.com/QwikByte/noryx/internal/agent/runtime"
)

func TestEveryServerTypeHasAnImage(t *testing.T) {
	for value, name := range noryxv1.ServerType_name {
		if typ := noryxv1.ServerType(value); typ != noryxv1.ServerType_SERVER_TYPE_UNSPECIFIED && images[typ].ref == "" {
			t.Errorf("%s has no image", name)
		}
	}
}

func TestEveryModdedTypeHasALoaderVariable(t *testing.T) {
	for value, name := range noryxv1.ServerType_name {
		if typ := noryxv1.ServerType(value); typ.Modded() != (loaderVariables[typ] != "") {
			t.Errorf("%s: loader variable %q", name, loaderVariables[typ])
		}
	}
}

// Proxies and Bedrock servers run as the user of their image, which owns their data, without
// capabilities, so that they can write to it on every start; Java game servers start as root
// to hand their data to the server's user.
func TestContainerUser(t *testing.T) {
	for value, name := range noryxv1.ServerType_name {
		typ := noryxv1.ServerType(value)
		if typ == noryxv1.ServerType_SERVER_TYPE_UNSPECIFIED {
			continue
		}
		opts, err := containerOptions(runtime.Spec{ID: "server", Type: typ, Port: 25565}, "/data", sharedNetwork)
		if err != nil {
			t.Fatal(err)
		}
		user, caps := "", serverCapabilities
		if typ.Proxy() || typ.Bedrock() {
			user, caps = imageUser, nil
		}
		if opts.Config.User != user || !slices.Equal(opts.HostConfig.CapAdd, caps) || !slices.Equal(opts.HostConfig.CapDrop, []string{"ALL"}) {
			t.Errorf("%s: user %q, capabilities +%v -%v", name, opts.Config.User, opts.HostConfig.CapAdd, opts.HostConfig.CapDrop)
		}
	}
}

// A backend reached over the private network of the nodes publishes its port only at the
// node's address there; one whose proxy runs on the node doesn't publish it at all.
func TestPublishedPort(t *testing.T) {
	for _, tc := range []struct {
		spec runtime.Spec
		want string // host IP, or "none"
	}{
		{runtime.Spec{}, ""},
		{runtime.Spec{BehindProxy: true, Overlay: "10.213.0.3"}, "10.213.0.3"},
		{runtime.Spec{BehindProxy: true, ProxyOnNode: true}, "none"},
	} {
		tc.spec.ID, tc.spec.Type, tc.spec.Port = "server", noryxv1.ServerType_SERVER_TYPE_PAPER, 25566
		opts, err := containerOptions(tc.spec, "/data", sharedNetwork)
		if err != nil {
			t.Fatal(err)
		}
		got := "none"
		for _, bindings := range opts.HostConfig.PortBindings {
			for _, b := range bindings {
				got = ""
				if b.HostIP.IsValid() {
					got = b.HostIP.String()
				}
				if b.HostPort != "25566" {
					t.Errorf("%+v: host port %s", tc.spec, b.HostPort)
				}
			}
		}
		if got != tc.want {
			t.Errorf("%+v: host IP %q, want %q", tc.spec, got, tc.want)
		}
	}
}

// Extra ports are published at the same port of the container on all addresses of the node, also
// for a backend whose own port only its proxy reaches.
func TestPublishedExtraPorts(t *testing.T) {
	for _, spec := range []runtime.Spec{
		{},
		{BehindProxy: true, Overlay: "10.213.0.3"},
		{BehindProxy: true, ProxyOnNode: true},
	} {
		spec.ID, spec.Type, spec.Port = "server", noryxv1.ServerType_SERVER_TYPE_PAPER, 25566
		spec.ExtraPorts = []runtime.ExtraPort{{Port: 8100, Protocol: "tcp", Label: "Map"}, {Port: 24454, Protocol: "udp", Label: "Voice chat"}}
		opts, err := containerOptions(spec, "/data", sharedNetwork)
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"8100/tcp", "24454/udp"} {
			port := network.MustParsePort(want)
			bindings := opts.HostConfig.PortBindings[port]
			_, exposed := opts.Config.ExposedPorts[port]
			if !exposed || len(bindings) != 1 || bindings[0].HostIP.IsValid() || bindings[0].HostPort != port.Port() {
				t.Errorf("%+v: %s exposed %v, published at %+v", spec, want, exposed, bindings)
			}
		}
		if n := len(opts.HostConfig.PortBindings); n != 2 && spec.ProxyOnNode || n != 3 && !spec.ProxyOnNode {
			t.Errorf("%+v: %d published ports", spec, n)
		}
	}
}

// The time zone is the only variable of the container besides those the agent sets from
// checked settings, and the stop timeout also applies when Docker stops the container itself.
func TestStopTimeoutAndTimeZone(t *testing.T) {
	for _, tc := range []struct {
		spec    runtime.Spec
		tz      string
		seconds int
	}{
		{runtime.Spec{}, "", 60},
		{runtime.Spec{StopTimeout: 300, TimeZone: "Europe/Berlin"}, "TZ=Europe/Berlin", 300},
	} {
		tc.spec.ID, tc.spec.Type, tc.spec.Port = "server", noryxv1.ServerType_SERVER_TYPE_PAPER, 25565
		opts, err := containerOptions(tc.spec, "/data", sharedNetwork)
		if err != nil {
			t.Fatal(err)
		}
		tz := ""
		if i := slices.IndexFunc(opts.Config.Env, func(v string) bool { return strings.HasPrefix(v, "TZ=") }); i >= 0 {
			tz = opts.Config.Env[i]
		}
		if tz != tc.tz || *opts.Config.StopTimeout != tc.seconds {
			t.Errorf("%+v: time zone %q, stop timeout %d", tc.spec, tz, *opts.Config.StopTimeout)
		}
	}
}

// Game servers get the health check of their image with their start period, so that Docker and
// Podman behave the same; proxies keep that of their image.
func TestHealthCheck(t *testing.T) {
	for _, tc := range []struct {
		spec  runtime.Spec
		start time.Duration // 0 for none
	}{
		{runtime.Spec{Type: noryxv1.ServerType_SERVER_TYPE_PAPER}, 2 * time.Minute},
		{runtime.Spec{Type: noryxv1.ServerType_SERVER_TYPE_NEOFORGE, HealthStart: 15}, 15 * time.Minute},
		{runtime.Spec{Type: noryxv1.ServerType_SERVER_TYPE_VELOCITY, HealthStart: 15}, 0},
	} {
		tc.spec.ID, tc.spec.Port = "server", 25565
		opts, err := containerOptions(tc.spec, "/data", sharedNetwork)
		if err != nil {
			t.Fatal(err)
		}
		want := &container.HealthConfig{Test: []string{"CMD-SHELL", "mc-health"}, Interval: 30 * time.Second, Timeout: 30 * time.Second, StartPeriod: tc.start, Retries: 2}
		if tc.start == 0 {
			want = nil
		}
		if got := opts.Config.Healthcheck; !reflect.DeepEqual(got, want) {
			t.Errorf("%+v: health check %+v, want %+v", tc.spec, got, want)
		}
	}
}

// A server's container runs as many processes as its settings allow, and Java starts with the
// initial heap the settings choose, up to MEMORY.
func TestProcessLimitAndInitialHeap(t *testing.T) {
	for _, tc := range []struct {
		spec runtime.Spec
		pids int64
		init string // the variable, or empty for none
	}{
		{runtime.Spec{MemoryMB: 4096}, 1024, ""},
		{runtime.Spec{MemoryMB: 4096, ProcessLimit: 8192, InitMemoryMB: 1024}, 8192, "INIT_MEMORY=1024M"},
	} {
		tc.spec.ID, tc.spec.Type, tc.spec.Port = "server", noryxv1.ServerType_SERVER_TYPE_FORGE, 25565
		opts, err := containerOptions(tc.spec, "/data", sharedNetwork)
		if err != nil {
			t.Fatal(err)
		}
		init := ""
		if i := slices.IndexFunc(opts.Config.Env, func(v string) bool { return strings.HasPrefix(v, "INIT_MEMORY=") }); i >= 0 {
			init = opts.Config.Env[i]
		}
		if *opts.HostConfig.PidsLimit != tc.pids || init != tc.init || !slices.Contains(opts.Config.Env, "MEMORY=4096M") {
			t.Errorf("%+v: processes %d, %v", tc.spec, *opts.HostConfig.PidsLimit, opts.Config.Env)
		}
	}
}

// A line longer than any the agent sends is cut, and the lines after it still come.
func TestEachLine(t *testing.T) {
	long := strings.Repeat("x", 3*maxLineBytes)
	var got []string
	err := eachLine(strings.NewReader("a\r\n"+long+"\nb\nc"), func(line string) bool {
		got = append(got, line)
		return line != "b"
	})
	if want := []string{"a", long[:maxLineBytes], "b"}; err != nil || !slices.Equal(got, want) {
		t.Fatalf("lines = %d, %v", len(got), err)
	}
}
