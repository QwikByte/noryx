package docker

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/events"
	"github.com/moby/moby/client"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	"github.com/QwikByte/noryx/internal/agent/runtime"
	"github.com/QwikByte/noryx/internal/logging"
)

const (
	watchRetry = 10 * time.Second
	// hangCheck is how often the agent looks for servers that stay unhealthy.
	hangCheck = 30 * time.Second
)

// mayHaveCrashed reports whether a container is worth inspecting for crashes: one that
// runs fine, never ran or stopped cleanly didn't crash.
func mayHaveCrashed(c container.Summary) bool {
	healthy := c.State == container.StateRunning && (c.Health == nil || c.Health.Status != container.Starting)
	return !healthy && c.State != container.StateCreated && !strings.HasPrefix(c.Status, "Exited (0)")
}

// healthCheck is the health check of a game server: that of its image, with the server's start
// period, in which failures don't count. Docker and Podman get it alike, so that they behave the
// same; proxies keep that of their image.
func healthCheck(spec runtime.Spec) *container.HealthConfig {
	if images[spec.Type].ref != serverImage {
		return nil
	}
	check := *imageChecks[serverImage]
	check.StartPeriod = noryxv1.HealthStart(spec.HealthStart)
	return &check
}

// addCrashes adds to srv how often it crashed since it was last started. A server that
// starts again after a crash is crashing.
func (d *Docker) addCrashes(ctx context.Context, containerID string, srv *runtime.Server) {
	res, err := d.cli.ContainerInspect(ctx, containerID, client.ContainerInspectOptions{})
	if err != nil || res.Container.RestartCount == 0 {
		return
	}
	c := res.Container
	if srv.State == noryxv1.ServerState_SERVER_STATE_STOPPED && c.State.ExitCode == 0 {
		return // stopped cleanly after it ran again
	}
	srv.Crashes, srv.ExitCode = c.RestartCount, c.State.ExitCode
	if srv.State == noryxv1.ServerState_SERVER_STATE_STARTING {
		srv.State = noryxv1.ServerState_SERVER_STATE_CRASHING
	}
}

// watched is what the agent remembers about the containers it watches, by their IDs.
type watched struct {
	inRow     map[string]int  // crashes in a row
	unhealthy map[string]bool // whose health check failed last
	lives     map[string]*life
	checks    map[string]*checks
	// restarting are the servers the agent restarts as they stayed unhealthy, until they stop,
	// which keeps their crashes in a row. Docker's count of restarts leaves those restarts out,
	// so carried are the crashes in a row they had then.
	restarting map[string]bool
	carried    map[string]int
}

// Watch logs crashes and stops servers that keep crashing, logs when the health check of a
// server fails and passes again, restarts servers that stay unhealthy if they are to, and closes
// the connections to the consoles of servers that stop, until ctx is done.
func (d *Docker) Watch(ctx context.Context) {
	w := watched{
		inRow: map[string]int{}, unhealthy: map[string]bool{}, lives: map[string]*life{},
		checks: map[string]*checks{}, restarting: map[string]bool{}, carried: map[string]int{},
	}
	for ctx.Err() == nil {
		err := d.watch(ctx, w)
		if ctx.Err() == nil {
			slog.Debug("Can't watch the servers for crashes", logging.Servers, "err", err)
		}
		select {
		case <-ctx.Done():
		case <-time.After(watchRetry): // e.g. while the runtime restarts
		}
	}
}

func (d *Docker) watch(ctx context.Context, w watched) error {
	// health_status matches the events of all states of health.
	actions := []string{string(events.ActionDie), string(events.ActionHealthStatus)}
	if d.podman != nil {
		actions = append(actions, string(events.ActionStart), string(events.ActionRestart))
	}
	filters := make(client.Filters).Add("type", string(events.ContainerEventType)).Add("event", actions...).Add("label", managed)
	res := d.cli.Events(ctx, client.EventsListOptions{Filters: filters})
	tick := time.NewTicker(hangCheck)
	defer tick.Stop()
	for {
		select {
		case msg := <-res.Messages:
			switch {
			case d.podman != nil:
				d.podmanEvent(ctx, msg, w)
			case msg.Action == events.ActionDie:
				delete(w.unhealthy, msg.Actor.ID)
				delete(w.checks, msg.Actor.ID)
				d.crashed(ctx, msg.Actor, w)
			default:
				health(msg, w.unhealthy)
			}
		case <-tick.C:
			d.restartHanging(ctx, w)
		case err := <-res.Err:
			return err
		}
	}
}

// health logs when the health check of a server fails, e.g. as it hangs, and when it passes
// again; Docker tells each change. Passing after the start isn't worth an entry.
func health(msg events.Message, unhealthy map[string]bool) {
	spec, ok := specOf(msg.Actor.Attributes)
	switch {
	case !ok:
	case msg.Action == events.ActionHealthStatusUnhealthy && !unhealthy[msg.Actor.ID]:
		unhealthy[msg.Actor.ID] = true
		slog.Warn("A server is unhealthy: it runs, but its health check fails, e.g. as it hangs", logging.Servers, logging.KeyServer, spec.ID)
	case msg.Action == events.ActionHealthStatusHealthy && unhealthy[msg.Actor.ID]:
		delete(unhealthy, msg.Actor.ID)
		slog.Info("A server is healthy again", logging.Servers, logging.KeyServer, spec.ID)
	}
}

// restarts reports whether Docker starts a container that exited again. It starts it again
// right away after its first crash, so it may run already.
func restarts(s *container.State) bool { return s != nil && (s.Restarting || s.Running) }

// crashed closes the console of a container that exited, and logs and counts its crash if
// Docker starts it again, stopping its server after its crash limit in a row.
func (d *Docker) crashed(ctx context.Context, actor events.Actor, w watched) {
	// The event carries the labels of the container; one that doesn't restart was stopped.
	spec, ok := specOf(actor.Attributes)
	if ok {
		d.consoles.Close(spec.ID)
	}
	res, err := d.cli.ContainerInspect(ctx, actor.ID, client.ContainerInspectOptions{})
	if err != nil || !ok || !restarts(res.Container.State) {
		if !w.restarting[actor.ID] {
			delete(w.inRow, actor.ID)
			delete(w.carried, actor.ID)
		}
		delete(w.restarting, actor.ID)
		return
	}
	d.count(ctx, actor, spec, res.Container.RestartCount+w.carried[actor.ID], w.inRow)
}

// count logs and counts the crash of a server that its runtime starts again, and stops it
// after its crash limit in a row; see noteCrash.
func (d *Docker) count(ctx context.Context, actor events.Actor, spec runtime.Spec, restarts int, inRow map[string]int) {
	if !noteCrash(inRow, actor, spec, restarts) {
		return
	}
	// A stop waits for the server's stop timeout, up to minutes, while the events of the other
	// servers go on.
	go func() {
		if err := d.Stop(ctx, spec.ID); err != nil {
			slog.Warn("Can't stop a crashing server", logging.Servers, logging.KeyServer, spec.ID, "err", err)
		}
	}()
}

// noteCrash logs the crash of a server that its runtime starts again, with how many crashes
// in a row it was, and reports whether that makes its crash limit, so that the server is to be
// stopped. The attributes of actor are those of Docker's die event. restarts is how often the
// runtime restarted it since it was last started, e.g. by a user.
func noteCrash(inRow map[string]int, actor events.Actor, spec runtime.Spec, restarts int) bool {
	n := inRow[actor.ID] + 1
	if ran, _ := strconv.Atoi(actor.Attributes["execDuration"]); time.Duration(ran)*time.Second >= noryxv1.StableAfter(spec.StableAfter) {
		n = 1
	}
	return limit(inRow, actor.ID, spec, min(n, restarts),
		"A server crashed and starts again; its console and crash reports tell why",
		"Stop a server that crashed %d times in a row; its console and crash reports tell why",
		"exit_code", actor.Attributes["exitCode"])
}

// limit counts a crash of a server as the nth in a row of its container and logs it, with
// again, or with stop and the server's crash limit if n makes it. Then the row ends, and limit
// reports that the server is to be stopped.
func limit(inRow map[string]int, id string, spec runtime.Spec, n int, again, stop string, attrs ...any) bool {
	attrs = append([]any{logging.Servers, logging.KeyServer, spec.ID, "crashes", n}, attrs...)
	if most := noryxv1.CrashLimit(spec.CrashLimit); n >= most {
		delete(inRow, id)
		slog.Warn(fmt.Sprintf(stop, most), attrs...)
		return true
	}
	inRow[id] = n
	slog.Warn(again, attrs...)
	return false
}

// checks is what the agent saw of the health check of a running server: since when it passes,
// and since when it fails.
type checks struct{ passes, fails time.Time }

// restartHanging restarts the servers whose health check keeps failing for as many minutes as
// their settings allow, e.g. as they hang, and stops those for which that makes their crash
// limit; see noteHang. Docker and Podman only tell a server unhealthy after the start period of
// its health check. Servers in which nobody chose this aren't looked at.
func (d *Docker) restartHanging(ctx context.Context, w watched) {
	res, err := d.cli.ContainerList(ctx, client.ContainerListOptions{Filters: make(client.Filters).Add("label", managed)})
	watching := func(c container.Summary) bool { spec, ok := specOf(c.Labels); return ok && spec.UnhealthyRestart > 0 }
	if err != nil || !slices.ContainsFunc(res.Items, watching) || d.withHealth(ctx, res.Items) != nil {
		return
	}
	now, running := time.Now(), map[string]bool{}
	for _, c := range res.Items {
		spec, _ := specOf(c.Labels)
		if !watching(c) || state(c) != noryxv1.ServerState_SERVER_STATE_RUNNING || w.restarting[c.ID] {
			continue
		}
		running[c.ID] = true
		s := w.checks[c.ID]
		if s == nil {
			s = &checks{}
			w.checks[c.ID] = s
		}
		switch {
		case !unhealthy(c):
			if s.passes.IsZero() || !s.fails.IsZero() {
				s.passes = now
			}
			s.fails = time.Time{}
		case s.fails.IsZero():
			s.fails = now
		case now.Sub(s.fails) >= time.Duration(spec.UnhealthyRestart)*time.Minute:
			delete(w.checks, c.ID)
			d.hung(ctx, c.ID, spec, s, w)
		}
	}
	for id := range w.checks {
		if !running[id] {
			delete(w.checks, id)
		}
	}
}

// hung restarts a server that stayed unhealthy, or stops it if that makes its crash limit.
func (d *Docker) hung(ctx context.Context, containerID string, spec runtime.Spec, s *checks, w watched) {
	var healthy time.Duration
	if !s.passes.IsZero() {
		healthy = s.fails.Sub(s.passes)
	}
	stop := noteHang(w.inRow, containerID, spec, healthy)
	if !stop {
		w.restarting[containerID], w.carried[containerID] = true, w.inRow[containerID]
	}
	// Like the stop of a crashing server, while the events and other servers go on.
	go func() {
		act := d.restartUnlessStopped
		if stop {
			act = d.Stop
		}
		if err := act(ctx, spec.ID); err != nil {
			slog.Warn("Can't restart an unhealthy server", logging.Servers, logging.KeyServer, spec.ID, "err", err)
		}
	}()
}

// noteHang logs that the agent restarts a server that stays unhealthy, which counts as a crash,
// and reports whether that makes its crash limit, so that the server is to be stopped instead.
// healthy is how long its health check passed before; that long a run counts as stable.
func noteHang(inRow map[string]int, containerID string, spec runtime.Spec, healthy time.Duration) bool {
	n := inRow[containerID] + 1
	if healthy >= noryxv1.StableAfter(spec.StableAfter) {
		n = 1
	}
	return limit(inRow, containerID, spec, n,
		"Restart a server whose health check keeps failing, e.g. as it hangs; this counts as a crash, and its console tells why",
		"Stop a server whose health check keeps failing, after %d crashes in a row; its console tells why",
		"unhealthy_minutes", spec.UnhealthyRestart)
}
