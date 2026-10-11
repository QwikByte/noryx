package policy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	"github.com/QwikByte/noryx/internal/logging"
	"github.com/QwikByte/noryx/internal/master/httpapi"
	"github.com/QwikByte/noryx/internal/master/modrinth"
	"github.com/QwikByte/noryx/internal/master/network"
	"github.com/QwikByte/noryx/internal/master/plugin"
	"github.com/QwikByte/noryx/internal/master/schedule"
	"github.com/QwikByte/noryx/internal/master/server"
)

const (
	backUp = "Back up server"
	// actionTimeout covers a graceful stop, in which a server saves its worlds, which may take
	// the longest stop timeout.
	actionTimeout = 2*time.Minute + noryxv1.MaxStopTimeout
	// imageTimeout covers pulling an image and recreating the container of a running server.
	imageTimeout = 10*time.Minute + noryxv1.MaxStopTimeout
)

// pollEvery is how often a run that waits for the players to leave a server counts them.
var pollEvery = 30 * time.Second

// run is a run of a policy.
type run struct {
	Policies
	task     schedule.Task
	settings Settings
	at       time.Time

	mu   sync.Mutex
	errs []error
	// backedUp tells of the servers that a run without condition backed up when it began
	// whether they may go on: those backed up, or without data to back up yet.
	backedUp map[string]bool
	locks    map[string]*sync.Mutex // of the nodes, which back up one server at a time each
}

// Run applies a policy at its scheduled time to the servers it concerns: running ones for a
// restart, stop or commands, stopped ones for a start, all for updates of images, and those
// that can have plugins or mods for updates of them.
//
// Without a condition, it backs them up when the run begins, while their players are warned,
// and acts at the scheduled time once all are backed up. With a condition, it acts on each
// server once it has no players, after backing it up then. Game servers that restart server
// by server in networks are backed up first and restart whatever the condition, as their
// players move to other servers of the network; the network's proxy, which restarts after
// them, follows the condition with the players of the whole network.
func (p Policies) Run(ctx context.Context, t schedule.Task, servers schedule.Servers, at time.Time) error {
	s, err := decode(t.Settings)
	if err != nil {
		return err
	}
	r := &run{Policies: p, task: t, settings: s, at: at, locks: map[string]*sync.Mutex{}}
	list, listErr := servers(ctx)
	if s.Condition == "" {
		backedUp := make(chan struct{})
		go func() {
			defer close(backedUp)
			r.backUpAll(ctx, list)
		}()
		// The warnings show as the settings say now, which a message of the policy's own may no
		// longer fit, e.g. as a title; the servers restart or stop anyway.
		how := p.config.Warnings()
		message, warnErr := how.Message(s.Action, s.Message)
		if len(s.Warnings) > 0 {
			r.report(warnErr)
		}
		err := server.Countdown(ctx, at, s.Warnings, func(minutes uint32) {
			commands, _ := how.Commands(message, minutes)
			for _, srv := range list {
				if warnErr == nil && server.Warnable(srv.Server) && concerns(s, srv) {
					_ = p.call(ctx, srv, command, commands...) // a server that misses a warning restarts anyway
				}
			}
		})
		<-backedUp
		if err != nil {
			return err
		}
		if len(s.Warnings) > 0 { // servers may have been started or stopped during the countdown
			list, listErr = servers(ctx)
		}
	}
	r.report(listErr)
	var groups []group
	if s.Action == restart && s.Rolling > 0 {
		list, groups, err = p.group(ctx, list)
		r.report(err)
	}
	var wg sync.WaitGroup
	for _, srv := range list {
		if concerns(s, srv) {
			wg.Go(func() { r.report(r.serve(ctx, srv)) })
		}
	}
	for _, g := range groups {
		wg.Go(func() { r.report(r.restartNetwork(ctx, g)) })
	}
	wg.Wait()
	return errors.Join(r.errs...)
}

func (r *run) report(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.errs = append(r.errs, err)
}

// concerns tells whether a policy applies to a server in its current state. Console commands
// go to game servers only, as proxies don't know theirs, such as say.
func concerns(s Settings, srv schedule.Server) bool {
	stopped := srv.GetState() == noryxv1.ServerState_SERVER_STATE_STOPPED
	switch s.Action {
	case start:
		return stopped
	case command:
		return srv.GetState() == noryxv1.ServerState_SERVER_STATE_RUNNING && !srv.GetType().Proxy()
	case image:
		return true
	case plugins:
		return len(modrinth.Loaders(srv.GetType())) > 0
	}
	return !stopped
}

// serve acts on a server: with a condition once it has no players, after backing it up then;
// without one, if it may go on after the backups when the run began.
func (r *run) serve(ctx context.Context, srv schedule.Server) error {
	s := r.settings
	switch fine, known := r.backedUp[key(srv)]; {
	case s.Condition != "":
		if err := r.empty(ctx, srv); err != nil {
			return srv.Report(r.task, logging.Policies, actions[s.Action], err)
		}
		if s.Backup != nil {
			if err := r.backUp(ctx, srv); err != nil {
				return err
			}
		}
	case s.Backup != nil && !known:
		return srv.Report(r.task, logging.Policies, actions[s.Action], schedule.Skipped("It wasn't backed up, as it started or stopped during the countdown."))
	case s.Backup != nil && !fine:
		return nil // backing it up failed, which the run reported
	}
	var change string
	var err error
	switch s.Action {
	case image:
		change, err = r.updateImage(ctx, srv)
	case plugins:
		change, err = r.updatePlugins(ctx, srv)
	default:
		err = r.call(ctx, srv, s.Action, s.Commands...)
	}
	return srv.ReportChange(r.task, logging.Policies, actions[s.Action], change, err)
}

func key(srv schedule.Server) string { return srv.NodeID + "/" + srv.GetId() }

// empty waits until a server has no players: it counts them once for the condition "empty",
// and until Wait minutes after the scheduled time for "wait". It returns Skipped if players are
// online then or can't be counted, or once the task is deleted or paused.
func (r *run) empty(ctx context.Context, srv schedule.Server) error {
	deadline := r.at.Add(time.Duration(r.settings.Wait) * time.Minute)
	for {
		online, err := r.players(ctx, srv)
		switch {
		case err == nil && online == 0:
			return nil
		case r.settings.Condition == waitEmpty && time.Now().Before(deadline):
		case err != nil:
			return err
		case r.settings.Condition == waitEmpty:
			return schedule.Skipped("Players were still online when the time to wait was up.")
		default:
			return schedule.Skipped(fmt.Sprintf("Players were online: %d.", online))
		}
		timer := time.NewTimer(min(pollEvery, time.Until(deadline)))
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-r.task.Withdrawn():
			timer.Stop()
			return schedule.Skipped("The schedule was deleted or paused while it waited for the players to leave.")
		case <-timer.C:
		}
	}
}

// players returns how many players are online on a server, after the latest measurement of
// its node; for a proxy those of its whole network.
func (r *run) players(ctx context.Context, srv schedule.Server) (uint32, error) {
	stats, err := r.usage.Latest(ctx, srv.NodeID)
	if err != nil {
		return 0, err
	}
	i := slices.IndexFunc(stats.GetServers(), func(s *noryxv1.ServerStats) bool { return s.GetId() == srv.GetId() })
	switch {
	case i >= 0 && !stats.GetServers()[i].GetRunning():
		return 0, nil
	case i < 0 || stats.GetServers()[i].GetPlayers() == nil:
		return 0, schedule.Skipped("Its players couldn't be counted, e.g. as it was starting.")
	}
	return stats.GetServers()[i].GetPlayers().GetOnline(), nil
}

// backUpAll backs up the servers that a run without condition concerns, if the policy backs
// up first, and remembers which may go on.
func (r *run) backUpAll(ctx context.Context, list []schedule.Server) {
	if r.settings.Backup == nil {
		return
	}
	backedUp := map[string]bool{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, srv := range list {
		if concerns(r.settings, srv) {
			wg.Go(func() {
				err := r.backUp(ctx, srv)
				r.report(err)
				mu.Lock()
				defer mu.Unlock()
				backedUp[key(srv)] = err == nil
			})
		}
	}
	wg.Wait()
	r.backedUp = backedUp
}

// backUp backs up a server as a backup job with the policy's settings would, after the server
// that its node backs up for the run, if any. A server without the selected data yet needs no
// backup.
func (r *run) backUp(ctx context.Context, srv schedule.Server) error {
	r.mu.Lock()
	node := r.locks[srv.NodeID]
	if node == nil {
		node = &sync.Mutex{}
		r.locks[srv.NodeID] = node
	}
	r.mu.Unlock()
	node.Lock()
	defer node.Unlock()
	job := r.task
	settings, err := json.Marshal(r.settings.Backup)
	var b *noryxv1.Backup
	if err == nil {
		job.Settings = settings
		b, err = r.backups.BackUp(ctx, srv.NodeID, srv.GetId(), job)
	}
	if err == nil && b == nil {
		err = schedule.Skipped("It has none of the selected data yet, e.g. as it never started.")
	}
	return srv.Report(r.task, logging.Policies, backUp, err)
}

// updateImage pulls the image of a server again, as Update image in its settings does, which
// recreates its container if the image changed, and tells so.
func (r *run) updateImage(ctx context.Context, srv schedule.Server) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, imageTimeout)
	defer cancel()
	conn, err := r.nodes.Conn(ctx, srv.NodeID)
	if err != nil {
		return "", err
	}
	res, err := noryxv1.NewServerServiceClient(conn).UpdateImage(ctx, &noryxv1.UpdateImageRequest{Id: srv.GetId()})
	switch {
	case err != nil || !res.GetUpdated():
		return "", err
	case srv.GetState() == noryxv1.ServerState_SERVER_STATE_RUNNING:
		return "It got a new image and restarted with it.", nil
	}
	return "It got a new image.", nil
}

// updatePlugins updates the plugins or mods of a server to their newest releases, and tells
// the files it wrote, if any, and then the projects it kept at their version.
func (r *run) updatePlugins(ctx context.Context, srv schedule.Server) (string, error) {
	res := r.plugins.Update(ctx, []plugin.Ref{{NodeID: srv.NodeID, ServerID: srv.GetId()}}, nil)[0]
	var change []string
	for _, i := range res.Installed {
		change = append(change, i.FileName)
	}
	if len(change) > 0 {
		change = []string{fmt.Sprintf("Updated %s.", strings.Join(change, ", "))}
		if len(res.Pinned) > 0 {
			change = append(change, fmt.Sprintf("Kept at their version: %s.", strings.Join(res.Pinned, ", ")))
		}
		if res.Restart {
			change = append(change, "It loads the new files once it restarts.")
		}
	}
	if res.Error != "" {
		return strings.Join(change, " "), errors.New(res.Error)
	}
	return strings.Join(change, " "), nil
}

// group is a network whose running game servers restart server by server, and its proxy, if
// it restarts too.
type group struct {
	network network.Network
	servers []schedule.Server
	proxy   *schedule.Server
}

// group takes the running game servers of networks out of servers, and the proxies of these
// networks, to restart them network by network.
func (p Policies) group(ctx context.Context, servers []schedule.Server) ([]schedule.Server, []group, error) {
	networks, err := p.networks.List(ctx)
	if err != nil { // they restart at the same time then
		return servers, nil, fmt.Errorf("the servers of networks restarted at the same time: %w", err)
	}
	var groups []group
	for _, n := range networks {
		g := group{network: n}
		servers = slices.DeleteFunc(servers, func(srv schedule.Server) bool {
			ref := network.Ref{NodeID: srv.NodeID, ServerID: srv.GetId()}
			switch {
			case ref == n.Proxy:
				g.proxy = &srv
			case srv.GetState() == noryxv1.ServerState_SERVER_STATE_RUNNING && slices.ContainsFunc(n.Backends, func(b network.Backend) bool { return b.Ref == ref }):
				g.servers = append(g.servers, srv)
			default:
				return false
			}
			return true
		})
		switch {
		case len(g.servers) > 0:
			groups = append(groups, g)
		case g.proxy != nil:
			servers = append(servers, *g.proxy)
		}
	}
	return servers, groups, nil
}

// restartNetwork restarts the game servers of a group server by server, after backing them up
// if the run does and didn't when it began, then serves its proxy like any other server.
func (r *run) restartNetwork(ctx context.Context, g group) error {
	var errs []error
	var only []network.Ref
	var restarting []schedule.Server
	for _, srv := range g.servers {
		fine, known := r.backedUp[key(srv)]
		switch {
		case r.settings.Backup == nil:
		case r.settings.Condition != "":
			if err := r.backUp(ctx, srv); err != nil {
				errs = append(errs, err)
				continue
			}
		case !known:
			errs = append(errs, srv.Report(r.task, logging.Policies, actions[restart], schedule.Skipped("It wasn't backed up, as it started during the countdown.")))
			continue
		case !fine:
			continue // backing it up failed, which the run reported
		}
		only, restarting = append(only, network.Ref{NodeID: srv.NodeID, ServerID: srv.GetId()}), append(restarting, srv)
	}
	if len(only) > 0 {
		err := r.networks.RollingRestart(ctx, g.network, r.settings.Rolling, only...)
		for _, srv := range restarting {
			_ = srv.Report(r.task, logging.Policies, actions[restart], err)
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %s", g.network.Name, httpapi.Message(err)))
		}
	}
	if g.proxy != nil {
		errs = append(errs, r.serve(ctx, *g.proxy))
	}
	return errors.Join(errs...)
}

// call sends a server the action of a policy, or the console commands one after the other.
func (p Policies) call(ctx context.Context, srv schedule.Server, action string, commands ...string) error {
	ctx, cancel := context.WithTimeout(ctx, actionTimeout)
	defer cancel()
	conn, err := p.nodes.Conn(ctx, srv.NodeID)
	if err != nil {
		return err
	}
	c, id := noryxv1.NewServerServiceClient(conn), srv.GetId()
	switch action {
	case restart:
		_, err = c.RestartServer(ctx, &noryxv1.RestartServerRequest{Id: id})
	case stop:
		_, err = c.StopServer(ctx, &noryxv1.StopServerRequest{Id: id})
	case start:
		_, err = c.StartServer(ctx, &noryxv1.StartServerRequest{Id: id})
	default:
		for _, cmd := range commands {
			if _, err = c.SendCommand(ctx, &noryxv1.SendCommandRequest{Id: id, Command: cmd}); err != nil {
				return err
			}
		}
	}
	return err
}
