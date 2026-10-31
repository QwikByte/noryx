package docker

import (
	"context"
	"errors"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	"github.com/QwikByte/noryx/internal/agent/runtime"
)

const liveImage = "docker.io/library/alpine:3.22"

// liveContainer creates and starts a container like the agent's for a server, of alpine.
func liveContainer(t *testing.T, d *Docker, id string, cfg *container.Config, hc *container.HostConfig) {
	t.Helper()
	liveContainerIn(t, d, id, sharedNetwork, cfg, hc)
}

// liveContainerIn is liveContainer in a network of the agent.
func liveContainerIn(t *testing.T, d *Docker, id, net string, cfg *container.Config, hc *container.HostConfig) {
	t.Helper()
	ctx := t.Context()
	must(t, d.pull(ctx, liveImage))
	must(t, d.ensureNetwork(ctx, net))
	cfg.Image = liveImage
	cfg.Labels = map[string]string{labelManaged: "true", labelSpec: `{"id":"` + id + `"}`}
	_, err := d.cli.ContainerCreate(ctx, client.ContainerCreateOptions{
		Name: containerName(id), Config: cfg, HostConfig: hc,
		NetworkingConfig: &network.NetworkingConfig{EndpointsConfig: map[string]*network.EndpointSettings{net: {}}},
	})
	must(t, err)
	t.Cleanup(func() { _ = d.forceRemove(context.WithoutCancel(ctx), containerName(id)) })
	_, err = d.cli.ContainerStart(ctx, containerName(id), client.ContainerStartOptions{})
	must(t, err)
}

// TestPodmanLive checks what Podman does differently with containers of alpine: the options of
// servers, the console of proxies, crashes, health and logs. It needs Podman as root and runs
// with NORYX_DOCKER_TEST=podman; without systemd, health checks run only as it runs them.
func TestPodmanLive(t *testing.T) {
	d := live(t)
	if d.podman == nil {
		t.Skip("Podman only")
	}
	ctx := t.Context()
	info, err := d.Info(ctx)
	must(t, err)
	t.Logf("Podman %s on %s", info.Version, info.OS)

	t.Run("wrong runtime", func(t *testing.T) {
		docker, err := New(d.storage, Options{Socket: PodmanSocket})
		must(t, err)
		if _, err := docker.Info(ctx); !errors.Is(err, runtime.ErrWrongRuntime) {
			t.Errorf("Docker's runtime at Podman's socket: %v", err)
		}
	})

	t.Run("options", func(t *testing.T) {
		id := runtime.NewID()
		spec := runtime.Spec{ID: id, Type: noryxv1.ServerType_SERVER_TYPE_PAPER, MemoryMB: 1024, CPUMillis: 1500, Port: 25999, Overlay: "127.0.0.1", BehindProxy: true}
		opts, err := containerOptions(spec, t.TempDir(), sharedNetwork)
		must(t, err)
		d.adapt(&opts)
		opts.Config.Image, opts.Config.Cmd, opts.Config.Entrypoint = liveImage, []string{"sleep", "300"}, nil
		must(t, d.pull(ctx, liveImage))
		must(t, d.ensureNetwork(ctx, sharedNetwork))
		_, err = d.cli.ContainerCreate(ctx, opts)
		must(t, err)
		t.Cleanup(func() { must(t, d.forceRemove(context.WithoutCancel(ctx), containerName(id))) })
		_, err = d.cli.ContainerStart(ctx, containerName(id), client.ContainerStartOptions{})
		must(t, err)
		out, err := exec.CommandContext(ctx, "podman", "exec", containerName(id), "sh", "-c",
			"grep -E '^(NoNewPrivs|CapEff)' /proc/1/status; cat /sys/fs/cgroup/pids.max /sys/fs/cgroup/pids/pids.max /sys/fs/cgroup/memory.max /sys/fs/cgroup/memory/memory.limit_in_bytes 2>/dev/null; true").CombinedOutput()
		must(t, err)
		t.Logf("in the container:\n%s", out)
		for _, want := range []string{"NoNewPrivs:\t1", "CapEff:\t00000000000000c1", "1024\n", strconv.Itoa(int(noryxv1.ContainerMemoryMB(1024)) << 20)} {
			if !strings.Contains(string(out), want) {
				t.Errorf("missing %q", want)
			}
		}
		out, err = exec.CommandContext(ctx, "podman", "inspect", containerName(id), "--format",
			"{{.HostConfig.RestartPolicy.Name}} {{.HostConfig.PortBindings}} {{.Config.Healthcheck.Test}} {{.HostConfig.CpuQuota}} {{.Config.StopTimeout}}").CombinedOutput()
		must(t, err)
		t.Logf("inspected: %s", out)
		if want := "always map[25565/tcp:[{127.0.0.1 25999}]] [CMD-SHELL mc-health] 150000 60"; strings.TrimSpace(string(out)) != want {
			t.Errorf("want %s", want)
		}
		bridge, err := exec.CommandContext(ctx, "podman", "network", "inspect", sharedNetwork, "--format", "{{.NetworkInterface}}").CombinedOutput()
		if err != nil || strings.TrimSpace(string(bridge)) != sharedBridge {
			t.Errorf("bridge of the shared network: %s, %v", bridge, err)
		}
	})

	t.Run("console", func(t *testing.T) {
		id := runtime.NewID()
		liveContainer(t, d, id, &container.Config{User: imageUser, OpenStdin: true, Cmd: []string{"sh", "-c", "while read l; do echo got $l; done"}}, &container.HostConfig{})
		time.Sleep(time.Second)
		for _, cmd := range []string{"first", "second", "third"} {
			ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			err := d.console(ctx, id, cmd, func(line string) (bool, error) { return line == "got "+cmd, nil })
			cancel()
			if err != nil {
				t.Fatalf("%s: %v", cmd, err)
			}
		}
	})

	t.Run("crashes", func(t *testing.T) {
		id := runtime.NewID()
		logged := captureLog(t)
		wctx, cancel := context.WithCancel(ctx)
		watching := make(chan struct{})
		go func() {
			d.Watch(wctx)
			close(watching)
		}()
		time.Sleep(time.Second)
		liveContainer(t, d, id, &container.Config{Cmd: []string{"sh", "-c", "sleep 2; exit 3"}}, &container.HostConfig{RestartPolicy: container.RestartPolicy{Name: container.RestartPolicyAlways}})
		// Each run takes 2 seconds; the agent stops the server after its crash limit.
		maxCrashes := noryxv1.DefaultCrashLimit
		time.Sleep(time.Duration(maxCrashes)*3*time.Second + 10*time.Second)
		cancel()
		<-watching
		entries := logged()
		crashes := 0
		for _, e := range entries {
			if e["server"] == id {
				crashes++
				t.Logf("%v", e)
			}
		}
		if crashes != maxCrashes {
			t.Errorf("logged %d crashes, want %d", crashes, maxCrashes)
		}
	})

	t.Run("health", func(t *testing.T) {
		id := runtime.NewID()
		liveContainer(t, d, id, &container.Config{
			Cmd:         []string{"sleep", "300"},
			Healthcheck: &container.HealthConfig{Test: []string{"CMD-SHELL", "test -e /tmp/fail && exit 1 || exit 0"}, Interval: time.Hour, Retries: 1},
		}, &container.HostConfig{})
		state := func() runtime.Server {
			srv, err := runtime.Find(ctx, d, id)
			must(t, err)
			return srv
		}
		if s := state(); s.State != noryxv1.ServerState_SERVER_STATE_STARTING {
			t.Errorf("before a check: %v", s.State)
		}
		must(t, exec.CommandContext(ctx, "podman", "healthcheck", "run", containerName(id)).Run())
		if s := state(); s.State != noryxv1.ServerState_SERVER_STATE_RUNNING || s.Unhealthy {
			t.Errorf("healthy: %v %v", s.State, s.Unhealthy)
		}
		must(t, exec.CommandContext(ctx, "podman", "exec", containerName(id), "touch", "/tmp/fail").Run())
		_ = exec.CommandContext(ctx, "podman", "healthcheck", "run", containerName(id)).Run()
		if s := state(); s.State != noryxv1.ServerState_SERVER_STATE_RUNNING || !s.Unhealthy {
			t.Errorf("unhealthy: %v %v", s.State, s.Unhealthy)
		}
	})

	t.Run("logs", func(t *testing.T) {
		id := runtime.NewID()
		liveContainer(t, d, id, &container.Config{Cmd: []string{"sh", "-c", "for i in 1 2 3 4 5 6; do echo line $i; sleep 0.5; done; sleep 60"}}, &container.HostConfig{})
		time.Sleep(4 * time.Second)
		var lines []string
		ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		for line, err := range d.Logs(ctx, id, 2, time.Time{}) {
			if err != nil {
				t.Fatal(err)
			}
			lines = append(lines, line.Time.Format(time.RFC3339Nano)+" "+line.Text)
		}
		t.Logf("%s", strings.Join(lines, "\n"))
		if len(lines) < 2 || !strings.HasSuffix(lines[len(lines)-1], "line 6") {
			t.Errorf("lines %v", lines)
		}
	})
}

// TestPodmanNetworksLive checks that containers in two networks of the agent can't reach each
// other on Podman, also once the networks were inspected through Docker's API, after which
// some versions no longer keep them apart, and that a network of the agent without its bridge,
// e.g. one created by hand, is only created again once no container uses it. It writes the agent's table between
// networks, which stays, and runs with NORYX_DOCKER_TEST=podman.
func TestPodmanNetworksLive(t *testing.T) {
	d := live(t)
	if d.podman == nil {
		t.Skip("Podman only")
	}
	ctx := t.Context()
	must(t, isolate(networkIsolation)) // without the bridge table, which needs nft_meta_bridge
	a, b := proxyNetwork(runtime.NewID()), proxyNetwork(runtime.NewID())
	for _, n := range []string{a, b} {
		t.Cleanup(func() { must(t, d.removeNetwork(context.WithoutCancel(ctx), n)) })
		must(t, d.ensureNetwork(ctx, n))
		_, err := d.cli.NetworkInspect(ctx, n, client.NetworkInspectOptions{}) // as anyone may, e.g. the agent in release
		must(t, err)
		if got := podmanBridge(t, n); got != bridge(n) {
			t.Errorf("bridge of %s: %s", n, got)
		}
	}
	listener, inA, inB := runtime.NewID(), runtime.NewID(), runtime.NewID()
	locked := func() *container.HostConfig {
		return &container.HostConfig{CapDrop: []string{"ALL"}, SecurityOpt: []string{"no-new-privileges:true"}}
	}
	liveContainerIn(t, d, listener, b, &container.Config{Cmd: []string{"sh", "-c", "while true; do echo hi | nc -l -p 8080; done"}}, locked())
	liveContainerIn(t, d, inA, a, &container.Config{Cmd: []string{"sleep", "300"}}, locked())
	liveContainerIn(t, d, inB, b, &container.Config{Cmd: []string{"sleep", "300"}}, locked())
	res, err := d.cli.ContainerInspect(ctx, containerName(listener), client.ContainerInspectOptions{})
	must(t, err)
	connect := func(from string) error {
		cmd := "echo | nc -w 3 " + res.Container.NetworkSettings.Networks[b].IPAddress.String() + " 8080 | grep -q hi"
		return d.run(ctx, containerName(from), "", []string{"sh", "-c", cmd}, nil, nil, nil)
	}
	err = connect(inB)
	for i := 0; err != nil && i < 5; i++ { // until it listens
		time.Sleep(time.Second)
		err = connect(inB)
	}
	if err != nil {
		t.Fatalf("within a network: %v", err)
	}
	if err := connect(inA); err == nil {
		t.Error("reached a container in another network")
	}

	// A network created by hand, without the bridge, which a container uses.
	old, user := proxyNetwork(runtime.NewID()), runtime.NewID()
	t.Cleanup(func() { must(t, d.removeNetwork(context.WithoutCancel(ctx), old)) })
	_, err = d.cli.NetworkCreate(ctx, old, client.NetworkCreateOptions{Driver: "bridge", Labels: map[string]string{labelManaged: "true"}})
	must(t, err)
	_, err = d.cli.ContainerCreate(ctx, client.ContainerCreateOptions{
		Name: containerName(user), Config: &container.Config{Image: liveImage, Cmd: []string{"true"}},
		NetworkingConfig: &network.NetworkingConfig{EndpointsConfig: map[string]*network.EndpointSettings{old: {}}},
	})
	must(t, err)
	if err := d.ensureNetwork(ctx, old); err == nil || podmanBridge(t, old) == bridge(old) {
		t.Errorf("replaced a network in use: %v", err)
	}
	must(t, d.forceRemove(ctx, containerName(user)))
	must(t, d.ensureNetwork(ctx, old))
	if got := podmanBridge(t, old); got != bridge(old) {
		t.Errorf("bridge of the network created again: %s", got)
	}
}

// podmanBridge returns the name of the bridge of a network of Podman.
func podmanBridge(t *testing.T, name string) string {
	t.Helper()
	out, err := exec.CommandContext(t.Context(), "podman", "network", "inspect", name, "--format", "{{.NetworkInterface}}").Output()
	must(t, err)
	return strings.TrimSpace(string(out))
}
