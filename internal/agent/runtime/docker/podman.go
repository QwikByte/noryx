package docker

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"math"
	"net"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/events"
	"github.com/moby/moby/client"
	"golang.org/x/mod/semver"

	"github.com/QwikByte/noryx/internal/agent/runtime"
	"github.com/QwikByte/noryx/internal/logging"
)

// Podman runs the same containers as Docker through its Docker-compatible API, as root, with
// the same users, capabilities, limits and networks. This file makes up for where it differs.

// PodmanSocket is the socket of the Docker-compatible API of Podman as root.
const PodmanSocket = "/run/podman/podman.sock"

const (
	// minPodman is the oldest Podman the agent runs servers with.
	minPodman = "v4.9.0"
	// bootPodman is the first Podman that starts containers whose restart policy is
	// unless-stopped when the node boots.
	bootPodman = "v5.8.0"
)

// podman is what the agent knows of Podman from its answers.
type podman struct {
	version atomic.Pointer[string] // e.g. "v5.4.2"
	root    atomic.Pointer[string] // the version that told it runs as root
}

// newer reports whether the Podman that answered last is version or newer.
func (p *podman) newer(version string) bool {
	v := p.version.Load()
	return v != nil && semver.Compare(*v, version) >= 0
}

// check looks at every answer of the runtime. While another runtime answers at its socket than
// the agent is set to use, e.g. as the package podman-docker points Docker's socket to Podman,
// or a Podman too old or rootless, the call fails: the agent would rely on what that runtime
// does differently, such as how it keeps servers apart.
func (d *Docker) check(res *http.Response) {
	err := d.accept(res.Request.Context(), res.Header.Get("Libpod-Api-Version")) // Podman's version, which Docker leaves out
	var refusal *error
	if err != nil {
		refusal = &err
	}
	if old := d.refused.Swap(refusal); (old == nil) != (refusal == nil) {
		if err != nil {
			slog.Error("The agent can't use the runtime", logging.Servers, "err", err)
		} else {
			slog.Info("The runtime answers as the agent expects again", logging.Servers)
		}
	}
	if err == nil {
		return
	}
	// The headers stay, as the client takes the API's version from them.
	_ = res.Body.Close()
	body, _ := json.Marshal(map[string]string{"message": err.Error()})
	res.StatusCode, res.Status = http.StatusInternalServerError, "500 Internal Server Error"
	res.Header.Set("Content-Type", "application/json")
	res.Body, res.ContentLength = io.NopCloser(bytes.NewReader(body)), int64(len(body))
}

// accept checks the runtime that answered by the version of Podman it tells, if any, and asks
// each version of Podman once whether it runs as root. Rootless Podman would publish ports past
// the firewall of the private network, give containers no addresses on the node and map their
// users to others.
func (d *Docker) accept(ctx context.Context, libpod string) error {
	switch {
	case d.podman == nil && libpod != "":
		return fmt.Errorf("%w: Podman %s answers at the socket of Docker; set the agent to Podman with --runtime podman, or to the socket of Docker with --runtime-socket", runtime.ErrWrongRuntime, libpod)
	case d.podman == nil:
		return nil
	case libpod == "":
		return fmt.Errorf("%w: Docker answers at the socket of Podman; set the agent to Docker with --runtime docker, or to the socket of Podman with --runtime-socket", runtime.ErrWrongRuntime)
	case semver.Compare("v"+libpod, minPodman) < 0:
		return fmt.Errorf("%w: Podman %s is too old, the agent needs %s or newer", runtime.ErrWrongRuntime, libpod, strings.TrimPrefix(minPodman, "v"))
	}
	v := "v" + libpod
	if root := d.podman.root.Load(); root == nil || *root != v {
		var info struct {
			Host struct{ Security struct{ Rootless bool } }
		}
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute) // also if the call ends with its answer
		defer cancel()
		if err := d.libpod(ctx, "/info", &info); err != nil {
			return fmt.Errorf("%w: can't tell whether Podman runs as root: %w", runtime.ErrWrongRuntime, err)
		}
		if info.Host.Security.Rootless {
			return fmt.Errorf("%w: Podman runs rootless, but the agent needs it as root; set the agent to its socket with --runtime-socket, by default %s", runtime.ErrWrongRuntime, PodmanSocket)
		}
		d.podman.root.Store(&v)
	}
	d.podman.version.Store(&v)
	return nil
}

// libpod gets path from Podman's own API, e.g. /info, which tells what Docker's leaves out, and
// decodes the answer into v. It fails with cerrdefs.ErrNotFound for what doesn't exist. Its
// requests bypass the client of Docker's API, and so check.
func (d *Docker) libpod(ctx context.Context, path string, v any) error {
	dial := d.cli.Dialer()
	api := http.Client{Transport: &http.Transport{
		DisableKeepAlives: true,
		DialContext:       func(ctx context.Context, _, _ string) (net.Conn, error) { return dial(ctx) },
	}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://podman/v4.0.0/libpod"+path, nil)
	if err != nil {
		return err
	}
	res, err := api.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	switch {
	case res.StatusCode == http.StatusNotFound:
		return cerrdefs.ErrNotFound
	case res.StatusCode != http.StatusOK:
		return fmt.Errorf("%s: %s", path, res.Status)
	}
	return json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(v)
}

// refusal returns why the agent refuses the runtime, if it does.
func (d *Docker) refusal() error {
	if err := d.refused.Load(); err != nil {
		return *err
	}
	return nil
}

// imageChecks are the health checks of the images of itzg, which Podman doesn't take from
// images in the OCI format, as these are. Game servers get theirs on both runtimes, with their
// start period; see healthCheck.
var imageChecks = map[string]*container.HealthConfig{
	serverImage: {Test: []string{"CMD-SHELL", "mc-health"}, Interval: 30 * time.Second, Timeout: 30 * time.Second, StartPeriod: 2 * time.Minute, Retries: 2},
	proxyImage:  {Test: []string{"CMD-SHELL", "/usr/bin/health.sh"}, Interval: 30 * time.Second, Timeout: 30 * time.Second, StartPeriod: 10 * time.Second, Retries: 3},
}

// adapt fits the options of a new container to Podman: it gets the health check of its image
// itself, and before bootPodman, the restart policy always instead of unless-stopped, as
// Podman wouldn't start it when the node boots otherwise; then a server stopped in the panel
// starts at boot too. With SELinux, e.g. on RHEL, Podman confines containers to files labelled
// for them, which Z does for the mounts of each container alone; elsewhere it changes nothing.
func (d *Docker) adapt(opts *client.ContainerCreateOptions) {
	if d.podman == nil {
		return
	}
	if repository, _, _ := strings.Cut(opts.Config.Image, ":"); opts.Config.Healthcheck == nil {
		opts.Config.Healthcheck = imageChecks[repository]
	}
	for i, bind := range opts.HostConfig.Binds {
		if strings.Count(bind, ":") > 1 {
			opts.HostConfig.Binds[i] = bind + ",Z"
		} else {
			opts.HostConfig.Binds[i] = bind + ":Z"
		}
	}
	if policy := &opts.HostConfig.RestartPolicy; policy.Name == container.RestartPolicyUnlessStopped && !d.podman.newer(bootPodman) {
		policy.Name = container.RestartPolicyAlways
	}
}

// forceRemove removes a container right away, also a running one, as Docker does. Podman would
// stop a running one gracefully before, for up to its stop timeout of minutes, so it is
// stopped without waiting first.
func (d *Docker) forceRemove(ctx context.Context, name string) error {
	if d.podman != nil {
		_, _ = d.cli.ContainerStop(ctx, name, client.ContainerStopOptions{Timeout: new(0)}) // fails for one that doesn't run
	}
	_, err := d.cli.ContainerRemove(ctx, name, client.ContainerRemoveOptions{Force: true})
	return err
}

// withHealth adds the health of running containers to a list in which the runtime left it
// out, as Podman and Docker before API 1.52 do, from the lists of those that start or fail.
func (d *Docker) withHealth(ctx context.Context, items []container.Summary) error {
	if !slices.ContainsFunc(items, func(c container.Summary) bool { return c.State == container.StateRunning && c.Health == nil }) {
		return nil
	}
	for _, status := range []container.HealthStatus{container.Starting, container.Unhealthy} {
		res, err := d.cli.ContainerList(ctx, client.ContainerListOptions{Filters: make(client.Filters).Add("label", managed).Add("health", string(status))})
		if err != nil {
			return err
		}
		for _, c := range res.Items {
			if i := slices.IndexFunc(items, func(item container.Summary) bool { return item.ID == c.ID }); i >= 0 && items[i].Health == nil {
				items[i].Health = &container.HealthSummary{Status: status}
			}
		}
	}
	return nil
}

// life is what Podman's events told of a container.
type life struct {
	started    time.Time     // when it started last, if known
	died       *events.Actor // how it exited last, until it starts again
	restarting bool          // whether its restart policy starts it again
}

// podmanEvent handles an event of Podman like Docker's. Podman tells crashes differently: it
// has no state of a container that restarts, but tells "restart" after "died" if its restart
// policy starts the container again, and it doesn't tell how long the container ran. Its
// health_status events leave the status out for Docker's clients.
func (d *Docker) podmanEvent(ctx context.Context, msg events.Message, w watched) {
	id := msg.Actor.ID
	l := w.lives[id]
	if l == nil && msg.Action != events.ActionHealthStatus {
		l = &life{}
		w.lives[id] = l
	}
	at := time.Unix(0, msg.TimeNano)
	switch msg.Action {
	case events.ActionStart:
		if !l.restarting {
			delete(w.inRow, id) // started by the agent, which starts a new row
			delete(w.carried, id)
		}
		l.started, l.died, l.restarting = at, nil, false
	case events.ActionDie:
		delete(w.unhealthy, id)
		delete(w.checks, id)
		// The agent restarts it as it stayed unhealthy, which keeps the row like its restart policy.
		l.restarting = w.restarting[id]
		delete(w.restarting, id)
		if spec, ok := specOf(msg.Actor.Attributes); ok {
			d.consoles.Close(spec.ID)
		}
		actor := events.Actor{ID: id, Attributes: maps.Clone(msg.Actor.Attributes)}
		if !l.started.IsZero() && actor.Attributes != nil {
			actor.Attributes["execDuration"] = strconv.Itoa(int(at.Sub(l.started).Seconds()))
		}
		l.died = &actor
	case events.ActionRestart:
		// A restart through the API, which the agent doesn't use, comes before the stop.
		if l.died == nil {
			return
		}
		died := *l.died
		l.died, l.restarting = nil, true
		if spec, ok := specOf(died.Attributes); ok {
			d.count(ctx, died, spec, math.MaxInt, w.inRow)
		}
	case events.ActionHealthStatus:
		res, err := d.cli.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
		if err == nil && res.Container.State != nil && res.Container.State.Health != nil {
			msg.Action = events.Action(string(events.ActionHealthStatus) + ": " + string(res.Container.State.Health.Status))
			health(msg, w.unhealthy)
		}
	}
}

// podmanConsole writes a command to the console of a proxy on Podman, and reads the answer
// like console does. Podman closes the standard input of a container once anyone attached to
// it leaves, so that a proxy could read a single command. So a shell of the proxy's user
// writes the command to the input of the proxy's process, and the answer comes from the log.
func (d *Docker) podmanConsole(ctx context.Context, id, command string, answer func(line string) (bool, error)) error {
	since := time.Now()
	if err := d.run(ctx, containerName(id), proxyUser, []string{"sh", "-c", "cat >/proc/1/fd/0"}, nil, strings.NewReader(command+"\n"), nil); err != nil {
		return err
	}
	if answer == nil {
		return nil
	}
	for line, err := range d.logs(ctx, containerName(id), -1, since) {
		if err != nil {
			return err
		}
		if done, err := answer(line.Text); done {
			return err
		}
	}
	return cmp.Or(ctx.Err(), errors.New("the proxy's console closed"))
}
