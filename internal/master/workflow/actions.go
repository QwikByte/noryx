package workflow

import (
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	"github.com/QwikByte/noryx/internal/buildinfo"
	"github.com/QwikByte/noryx/internal/logging"
	"github.com/QwikByte/noryx/internal/master/access"
	"github.com/QwikByte/noryx/internal/master/backup"
	"github.com/QwikByte/noryx/internal/master/logs"
	"github.com/QwikByte/noryx/internal/master/modrinth"
	"github.com/QwikByte/noryx/internal/master/network"
	"github.com/QwikByte/noryx/internal/master/operation"
	"github.com/QwikByte/noryx/internal/master/plugin"
	"github.com/QwikByte/noryx/internal/master/schedule"
	"github.com/QwikByte/noryx/internal/master/server"
)

const (
	maxCommands = 20
	maxCommand  = 1000
	maxWarnings = 5
	maxHeaders  = 20
	maxURL      = 2048
	maxBody     = 64 << 10
	// actionTimeout covers a graceful stop, in which a server saves its worlds.
	actionTimeout = 2*time.Minute + noryxv1.MaxStopTimeout
	imageTimeout  = 10*time.Minute + noryxv1.MaxStopTimeout
	// pollEvery is how often waiting for servers checks them.
	pollEvery = 10 * time.Second
)

var (
	headerName = regexp.MustCompile(`^[A-Za-z0-9!#$%&'*+.^_|~-]{1,64}$`)
	// headersRefused are set by the master itself.
	headersRefused = []string{"host", "content-length", "transfer-encoding", "connection", "upgrade", "te", "trailer"}
	methods        = []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete}
	playerActions  = []string{"kick", "ban", "pardon", "whitelist_add", "whitelist_remove", "op", "deop", "whitelist_on", "whitelist_off"}
)

func actionKinds() map[string]kind {
	return map[string]kind{
		"start": define([]access.Permission{access.ServersStart}, func(ctx context.Context, x *scope, _ Step, s *Servers) (any, error) {
			return x.power(ctx, *s, "start", nil, "")
		}),
		"stop": define([]access.Permission{access.ServersStop}, func(ctx context.Context, x *scope, _ Step, s *powerSettings) (any, error) {
			return x.power(ctx, s.Servers, "stop", s.Warnings, s.Message)
		}),
		"restart": define([]access.Permission{access.ServersRestart}, func(ctx context.Context, x *scope, _ Step, s *powerSettings) (any, error) {
			return x.power(ctx, s.Servers, "restart", s.Warnings, s.Message)
		}),
		"command":     define([]access.Permission{access.ConsoleCommands}, runCommand),
		"message":     define([]access.Permission{access.ConsoleCommands}, runMessage),
		"image":       define([]access.Permission{access.ServersSettings}, runImage),
		"plugins":     define([]access.Permission{access.Plugins}, runPlugins),
		"backup":      define([]access.Permission{access.BackupsCreate}, runBackup),
		"servers":     define([]access.Permission{access.ServersView}, runServers),
		"players":     define([]access.Permission{access.ServersView}, runPlayers),
		"waitfor":     define([]access.Permission{access.ServersView}, runWaitFor),
		"player":      define([]access.Permission{access.PlayersManage}, runPlayer),
		"send":        define([]access.Permission{access.PlayersManage, access.NetworksView}, runSend),
		"maintenance": define([]access.Permission{access.NetworksManage}, runMaintenance),
		"rolling":     define([]access.Permission{access.ServersRestart, access.NetworksView}, runRolling),
		"notify":      define([]access.Permission{access.NotificationsManage}, runNotify),
		"http":        define([]access.Permission{access.NotificationsManage}, runHTTP),
		"log":         define(nil, runLog),
	}
}

// Servers choose the servers of a step: targets, and a template whose value names servers,
// e.g. {{trigger.server}} or {{item}}: servers of data, lists of them, or IDs as node/server.
type Servers struct {
	Targets []schedule.Target `json:"targets,omitempty"`
	From    string            `json:"from,omitempty"`
}

func (s *Servers) check() error {
	var err error
	s.From = strings.TrimSpace(s.From)
	if s.Targets, err = targets(s.Targets); err != nil {
		return err
	}
	if len(s.Targets) == 0 && s.From == "" {
		return bad("Choose servers.")
	}
	return nil
}

// resolve returns the servers in their current state. Its error names those that couldn't be
// reached; the others are returned anyway.
func (x *scope) resolve(ctx context.Context, s Servers) ([]schedule.Server, error) {
	list := slices.Clone(s.Targets)
	if s.From != "" {
		v, err := x.value(s.From)
		if err != nil {
			return nil, err
		}
		named := items(v)
		if _, one := v.(map[string]any); one {
			named = []any{v}
		}
		for _, item := range named {
			t, ok := ref(item)
			if !ok {
				return nil, bad("%q names no server.", clip(text(item), 100))
			}
			list = append(list, t)
		}
		if list, err = schedule.CheckTargets(list); err != nil {
			return nil, err
		}
	}
	if len(list) == 0 {
		return nil, nil
	}
	return x.run.svc.deps.Targets.Resolve(ctx, list)
}

// ref reads a server of data, {"id": …, "nodeId": …}, or node/server.
func ref(v any) (schedule.Target, bool) {
	var nodeID, serverID string
	switch v := v.(type) {
	case map[string]any:
		nodeID, _ = v["nodeId"].(string)
		serverID, _ = v["id"].(string)
	case string:
		nodeID, serverID, _ = strings.Cut(strings.TrimSpace(v), "/")
	}
	return schedule.Target{Kind: schedule.KindServer, NodeID: nodeID, ServerID: serverID}, nodeID != "" && idPattern.MatchString(serverID)
}

// serverData is a server as templates see it.
func serverData(s schedule.Server) map[string]any {
	return map[string]any{
		"id": s.GetId(), "nodeId": s.NodeID, "name": s.GetName(), "node": s.NodeName, "state": state(s.GetState()),
		"type": strings.ToLower(strings.TrimPrefix(s.GetType().String(), "SERVER_TYPE_")), "proxy": s.GetType().Proxy(),
		"version": s.GetVersion(),
	}
}

func state(s noryxv1.ServerState) string {
	return strings.ToLower(strings.TrimPrefix(s.String(), "SERVER_STATE_"))
}

func running(s schedule.Server) bool { return s.GetState() == noryxv1.ServerState_SERVER_STATE_RUNNING }

func stopped(s schedule.Server) bool { return s.GetState() == noryxv1.ServerState_SERVER_STATE_STOPPED }

// action is what a step does on each server: what the log calls it, how its record tells it,
// and which servers it concerns, nil for all.
type action struct {
	log, done string
	concerns  func(schedule.Server) bool
	left      string // why it leaves the others out
}

// each runs fn on the servers that an action concerns, those of a node a few at a time, with
// {{server}} for each. Its output lists them with what fn told about each; it fails if fn
// failed on any, or if servers couldn't be reached.
func (x *scope) each(ctx context.Context, list []schedule.Server, listErr error, a action,
	fn func(ctx context.Context, x *scope, srv schedule.Server) (map[string]any, error),
) (any, error) {
	var mine []schedule.Server
	left := []any{}
	for _, srv := range list {
		if a.concerns == nil || a.concerns(srv) {
			mine = append(mine, srv)
		} else {
			left = append(left, srv.GetName())
		}
	}
	results := make([]any, len(mine))
	errs := make([]error, len(mine))
	nodes := make([]string, len(mine))
	for i, srv := range mine {
		nodes[i] = srv.NodeID
	}
	operation.Each(ctx, nodes, func(ctx context.Context, i int) {
		srv := mine[i]
		data := serverData(srv)
		extra, err := fn(ctx, x.child(map[string]any{"server": data}), srv)
		maps.Copy(data, extra)
		attrs := []any{logging.Workflows, "workflow", x.run.wf.Name, "workflow_id", x.run.wf.ID,
			logging.KeyNode, srv.NodeID, logging.KeyNodeName, srv.NodeName, logging.KeyServer, srv.GetId(), logging.KeyServerName, srv.GetName()}
		if err != nil {
			msg := message(err)
			data["error"] = msg
			errs[i] = fmt.Errorf("%s on %s: %s", srv.GetName(), srv.NodeName, msg)
			slog.Warn(a.log+" failed", append(attrs, "err", msg)...)
		} else {
			slog.Info(a.log, attrs...)
		}
		results[i] = data
	}, nil)
	failed := len(slices.DeleteFunc(slices.Clone(errs), func(err error) bool { return err == nil }))
	detail := fmt.Sprintf("%s %d of %d servers.", a.done, len(mine)-failed, len(mine))
	if len(left) > 0 {
		detail += fmt.Sprintf(" Left out %d that %s.", len(left), a.left)
	}
	out := map[string]any{"servers": results, "count": len(mine), "failed": failed, "left": left, "detail": detail}
	if len(results) > 0 {
		out["server"] = results[0]
	}
	return out, errors.Join(append(errs, listErr)...)
}

// call calls the agent of a server.
func (x *scope) call(ctx context.Context, srv schedule.Server, timeout time.Duration, fn func(ctx context.Context, c noryxv1.ServerServiceClient) error) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	conn, err := x.run.svc.deps.Nodes.Conn(ctx, srv.NodeID)
	if err != nil {
		return err
	}
	return fn(ctx, noryxv1.NewServerServiceClient(conn))
}

type powerSettings struct {
	Servers
	// Warnings are the minutes before at which the players are warned, as the settings say.
	Warnings []uint32 `json:"warnings"`
	// Message is the warning, with {minutes}; empty for the text of the settings.
	Message string `json:"message,omitempty"`
}

func (s *powerSettings) check() error {
	slices.SortFunc(s.Warnings, func(a, b uint32) int { return cmp.Compare(b, a) })
	s.Warnings = slices.Compact(s.Warnings)
	if s.Warnings == nil {
		s.Warnings = []uint32{}
	}
	if len(s.Warnings) > maxWarnings || slices.ContainsFunc(s.Warnings, func(m uint32) bool { return m == 0 || m > 60 }) {
		return bad("Warn the players up to %d times, 1 to 60 minutes before.", maxWarnings)
	}
	if s.Message = strings.TrimSpace(s.Message); len(s.Message) > 200 || strings.ContainsFunc(s.Message, unicode.IsControl) {
		return bad("Keep the warning on one line with up to 200 characters.")
	}
	return s.Servers.check()
}

// needs: a warning of one's own is a console command.
func (s *powerSettings) needs() []access.Permission {
	if s.Message != "" {
		return []access.Permission{access.ConsoleCommands}
	}
	return nil
}

var powers = map[string]action{
	"start":   {log: "Start server", done: "Started", concerns: stopped, left: "weren't stopped"},
	"stop":    {log: "Stop server", done: "Stopped", concerns: func(s schedule.Server) bool { return !stopped(s) }, left: "were stopped"},
	"restart": {log: "Restart server", done: "Restarted", concerns: func(s schedule.Server) bool { return !stopped(s) }, left: "were stopped"},
}

// power starts, stops or restarts servers, after warning the players of game servers.
func (x *scope) power(ctx context.Context, s Servers, what string, warnings []uint32, message string) (any, error) {
	list, listErr := x.resolve(ctx, s)
	if len(warnings) > 0 {
		how := x.run.svc.deps.Config.Warnings()
		msg, err := how.Message(what, message)
		if err != nil {
			return nil, err
		}
		err = server.Countdown(ctx, time.Now().Add(time.Duration(warnings[0])*time.Minute), warnings, func(minutes uint32) {
			commands, _ := how.Commands(msg, minutes)
			for _, srv := range list {
				if server.Warnable(srv.Server) {
					_ = x.call(ctx, srv, actionTimeout, func(ctx context.Context, c noryxv1.ServerServiceClient) error {
						for _, command := range commands {
							if _, err := c.SendCommand(ctx, &noryxv1.SendCommandRequest{Id: srv.GetId(), Command: command}); err != nil {
								return err
							}
						}
						return nil
					})
				}
			}
		})
		if err != nil {
			return nil, err
		}
		list, listErr = x.resolve(ctx, s) // servers may have started or stopped meanwhile
	}
	return x.each(ctx, list, listErr, powers[what], func(ctx context.Context, x *scope, srv schedule.Server) (map[string]any, error) {
		return nil, x.call(ctx, srv, actionTimeout, func(ctx context.Context, c noryxv1.ServerServiceClient) error {
			var err error
			switch what {
			case "start":
				_, err = c.StartServer(ctx, &noryxv1.StartServerRequest{Id: srv.GetId()})
			case "stop":
				_, err = c.StopServer(ctx, &noryxv1.StopServerRequest{Id: srv.GetId()})
			default:
				_, err = c.RestartServer(ctx, &noryxv1.RestartServerRequest{Id: srv.GetId()})
			}
			return err
		})
	})
}

type commandSettings struct {
	Servers
	Commands []string `json:"commands"`
}

func (s *commandSettings) check() error {
	s.Commands = slices.DeleteFunc(s.Commands, func(c string) bool { return strings.TrimSpace(c) == "" })
	if len(s.Commands) == 0 || len(s.Commands) > maxCommands || slices.ContainsFunc(s.Commands, func(c string) bool { return len(c) > maxCommand }) {
		return bad("Enter 1 to %d console commands, each with up to %d characters.", maxCommands, maxCommand)
	}
	return s.Servers.check()
}

// runCommand runs console commands on the running servers, one after the other, and tells
// what each server answered to the last.
func runCommand(ctx context.Context, x *scope, _ Step, s *commandSettings) (any, error) {
	list, listErr := x.resolve(ctx, s.Servers)
	a := action{log: "Send console commands", done: "Sent the commands to", concerns: running, left: "weren't running"}
	out, err := x.each(ctx, list, listErr, a, func(ctx context.Context, x *scope, srv schedule.Server) (map[string]any, error) {
		var output string
		err := x.call(ctx, srv, actionTimeout, func(ctx context.Context, c noryxv1.ServerServiceClient) error {
			for _, t := range s.Commands {
				command, err := x.line(t)
				if err != nil {
					return err
				}
				if command == "" {
					continue
				}
				res, err := c.SendCommand(ctx, &noryxv1.SendCommandRequest{Id: srv.GetId(), Command: command})
				if err != nil {
					return err
				}
				output = res.GetOutput()
			}
			return nil
		})
		return map[string]any{"output": clip(output, maxText)}, err
	})
	if m, ok := out.(map[string]any); ok && m["server"] != nil {
		m["output"] = m["server"].(map[string]any)["output"]
	}
	return out, err
}

type messageSettings struct {
	Servers
	// Kind is chat, title or actionbar.
	Kind     string `json:"kind"`
	Text     string `json:"text"`
	Subtitle string `json:"subtitle"`
	// Player gets the message alone; empty for everyone on the servers.
	Player string `json:"player"`
}

func (s *messageSettings) check() error {
	s.Kind = cmp.Or(s.Kind, "chat")
	switch {
	case !slices.Contains([]string{"chat", "title", "actionbar"}, s.Kind):
		return bad("Show the message in the chat, as a title or above the hotbar.")
	case strings.TrimSpace(s.Text) == "":
		return bad("Enter the message.")
	}
	if s.Kind != "title" {
		s.Subtitle = ""
	}
	return s.Servers.check()
}

// runMessage shows a message to the players of running game servers, or to one player.
func runMessage(ctx context.Context, x *scope, _ Step, s *messageSettings) (any, error) {
	list, listErr := x.resolve(ctx, s.Servers)
	a := action{log: "Show a message", done: "Showed the message on", left: "weren't running game servers",
		concerns: func(srv schedule.Server) bool { return server.Warnable(srv.Server) }}
	return x.each(ctx, list, listErr, a, func(ctx context.Context, x *scope, srv schedule.Server) (map[string]any, error) {
		var m network.Message
		var err error
		target := network.Everyone
		m.Kind = s.Kind
		if m.Text, err = x.line(s.Text); err == nil {
			m.Subtitle, err = x.line(s.Subtitle)
		}
		if err == nil && s.Player != "" {
			target, err = x.line(s.Player)
		}
		var commands []string
		if err == nil {
			commands, err = m.Commands(target)
		}
		if err != nil {
			return nil, err
		}
		return nil, x.call(ctx, srv, actionTimeout, func(ctx context.Context, c noryxv1.ServerServiceClient) error {
			for _, command := range commands {
				if _, err := c.SendCommand(ctx, &noryxv1.SendCommandRequest{Id: srv.GetId(), Command: command}); err != nil {
					return err
				}
			}
			return nil
		})
	})
}

// runImage pulls the newest image of each server's software; a running server whose image
// changed restarts with it.
func runImage(ctx context.Context, x *scope, _ Step, s *Servers) (any, error) {
	list, listErr := x.resolve(ctx, *s)
	a := action{log: "Update image", done: "Updated the images of"}
	return x.each(ctx, list, listErr, a, func(ctx context.Context, x *scope, srv schedule.Server) (map[string]any, error) {
		var updated bool
		err := x.call(ctx, srv, imageTimeout, func(ctx context.Context, c noryxv1.ServerServiceClient) error {
			res, err := c.UpdateImage(ctx, &noryxv1.UpdateImageRequest{Id: srv.GetId()})
			updated = res.GetUpdated()
			return err
		})
		return map[string]any{"updated": updated}, err
	})
}

// runPlugins updates the plugins and mods of servers to their newest releases, never betas.
func runPlugins(ctx context.Context, x *scope, _ Step, s *Servers) (any, error) {
	list, listErr := x.resolve(ctx, *s)
	a := action{log: "Update plugins", done: "Updated the plugins of", left: "can't have plugins or mods",
		concerns: func(srv schedule.Server) bool { return len(modrinth.Loaders(srv.GetType())) > 0 }}
	return x.each(ctx, list, listErr, a, func(ctx context.Context, x *scope, srv schedule.Server) (map[string]any, error) {
		res := x.run.svc.deps.Plugins.Update(ctx, []plugin.Ref{{NodeID: srv.NodeID, ServerID: srv.GetId()}}, nil)[0]
		files := []string{}
		for _, i := range res.Installed {
			files = append(files, i.FileName)
		}
		out := map[string]any{"updated": files, "restart": res.Restart}
		if res.Error != "" {
			return out, errors.New(res.Error)
		}
		return out, nil
	})
}

type backupSettings struct {
	Servers
	Backup backup.JobSettings `json:"backup"`
	// Label is that of the backups; empty for the name of the workflow.
	Label string `json:"label"`
}

func (s *backupSettings) check() error {
	s.Backup.Datastores, s.Backup.Copy = nil, nil
	raw, err := json.Marshal(s.Backup)
	if err == nil {
		raw, err = backup.Jobs{}.Check(raw)
	}
	if err == nil {
		err = json.Unmarshal(raw, &s.Backup)
	}
	if err != nil {
		return err
	}
	return s.Servers.check()
}

// runBackup backs up servers as a backup job would, keeping the newest backups of the step.
func runBackup(ctx context.Context, x *scope, st Step, s *backupSettings) (any, error) {
	list, listErr := x.resolve(ctx, s.Servers)
	label, err := x.line(s.Label)
	if err != nil {
		return nil, err
	}
	settings, err := json.Marshal(s.Backup)
	if err != nil {
		return nil, err
	}
	// The job of the backups is the step, so that its retention only deletes its own.
	job := sha256.Sum256([]byte(x.run.wf.ID + "/" + st.ID))
	task := schedule.Task{
		ID: hex.EncodeToString(job[:16]), Name: clip(cmp.Or(label, x.run.wf.Name), 64), Settings: settings,
		Schedule: schedule.Schedule{TimeZone: x.run.wf.TimeZone},
	}
	a := action{log: "Back up server", done: "Backed up"}
	return x.each(ctx, list, listErr, a, func(ctx context.Context, x *scope, srv schedule.Server) (map[string]any, error) {
		b, err := x.run.svc.deps.Backups.BackUp(ctx, srv.NodeID, srv.GetId(), task)
		if b == nil {
			return map[string]any{"backup": nil}, err
		}
		return map[string]any{"backup": b.GetId()}, err
	})
}

type serversSettings struct {
	Servers
	// State keeps only running or stopped servers; empty keeps all.
	State string `json:"state"`
	// Usage adds what each server uses and its players, from the latest measurement.
	Usage bool `json:"usage"`
}

func (s *serversSettings) check() error {
	if !slices.Contains([]string{"", "running", "stopped"}, s.State) {
		return bad("Choose running or stopped servers.")
	}
	return s.Servers.check()
}

// runServers finds servers, e.g. to go through them in a loop.
func runServers(ctx context.Context, x *scope, _ Step, s *serversSettings) (any, error) {
	list, err := x.resolve(ctx, s.Servers)
	list = slices.DeleteFunc(list, func(srv schedule.Server) bool {
		return s.State == "running" && !running(srv) || s.State == "stopped" && !stopped(srv)
	})
	var stats measured
	if s.Usage {
		stats = x.measure(ctx, list)
	}
	servers := []any{}
	names := []any{}
	for _, srv := range list {
		data := serverData(srv)
		if s.Usage {
			maps.Copy(data, stats.of(srv))
		}
		servers, names = append(servers, data), append(names, srv.GetName())
	}
	out := map[string]any{"servers": servers, "names": names, "count": len(servers), "detail": fmt.Sprintf("It found %d servers.", len(servers))}
	if len(servers) > 0 {
		out["server"] = servers[0]
	}
	return out, err
}

// measured are the latest measurements of nodes.
type measured map[string]*noryxv1.GetStatsResponse

// measure gets the latest measurements of the nodes of servers.
func (x *scope) measure(ctx context.Context, list []schedule.Server) measured {
	m := measured{}
	for _, srv := range list {
		if _, ok := m[srv.NodeID]; !ok {
			m[srv.NodeID], _ = x.run.svc.deps.Usage.Latest(ctx, srv.NodeID) // an offline node tells nothing
		}
	}
	return m
}

// of returns what a server used at the latest measurement of its node, and its players.
func (m measured) of(srv schedule.Server) map[string]any {
	stats := m[srv.NodeID]
	i := slices.IndexFunc(stats.GetServers(), func(s *noryxv1.ServerStats) bool { return s.GetId() == srv.GetId() })
	if i < 0 {
		return map[string]any{}
	}
	return usageData(stats, stats.GetServers()[i])
}

// usageData is what a server used as templates see it: CPU in percent of its limit or of all
// cores of its node, memory in percent of its limit, data in GB, and its players.
func usageData(stats *noryxv1.GetStatsResponse, s *noryxv1.ServerStats) map[string]any {
	d := map[string]any{"running": s.GetRunning()}
	for _, m := range measures {
		if v, ok := measure(stats, s, m); ok {
			d[m] = v
		}
	}
	if p := s.GetPlayers(); p != nil {
		names := []any{}
		for _, n := range p.GetNames() {
			names = append(names, n)
		}
		d["maxPlayers"], d["playerNames"] = float64(p.GetMax()), names
	}
	return d
}

// measure returns a measure of a server, if it is known.
func measure(stats *noryxv1.GetStatsResponse, s *noryxv1.ServerStats, name string) (float64, bool) {
	switch name {
	case "players":
		return float64(s.GetPlayers().GetOnline()), s.GetPlayers() != nil
	case "cpu":
		limit := cmp.Or(float64(s.GetCpuLimitMillis()), float64(stats.GetNode().GetCpuCount())*1000)
		return round(float64(s.GetCpuMillis()) / limit * 100), limit > 0 && s.GetRunning()
	case "memory":
		return round(float64(s.GetMemoryBytes()) / float64(s.GetMemoryLimitBytes()) * 100), s.GetMemoryLimitBytes() > 0 && s.GetRunning()
	case "tps":
		return s.GetTps(), s.GetTps() > 0 && s.GetTps() <= 20
	case "disk":
		return round(float64(s.GetDiskBytes()) / (1 << 30)), s.GetDiskBytes() > 0
	}
	return 0, false
}

func round(v float64) float64 { return float64(int64(v*10+0.5)) / 10 }

// runPlayers lists the players online on game servers.
func runPlayers(ctx context.Context, x *scope, _ Step, s *Servers) (any, error) {
	list, err := x.resolve(ctx, *s)
	stats := x.measure(ctx, list)
	players, names := []any{}, []any{}
	for _, srv := range list {
		if srv.GetType().Proxy() {
			continue // its players are those of the game servers of its network
		}
		online, _ := stats.of(srv)["playerNames"].([]any)
		for _, n := range online {
			players = append(players, map[string]any{"name": n, "server": serverData(srv)})
			names = append(names, n)
		}
	}
	return map[string]any{"players": players, "names": names, "count": len(names), "detail": fmt.Sprintf("%d players are online.", len(names))}, err
}

type waitForSettings struct {
	Servers
	// State is running, stopped or empty, without players.
	State   string `json:"state"`
	Minutes uint32 `json:"minutes"`
}

func (s *waitForSettings) check() error {
	switch {
	case !slices.Contains([]string{"running", "stopped", "empty"}, s.State):
		return bad("Choose whether to wait until the servers run, are stopped or have no players.")
	case s.Minutes == 0 || s.Minutes > maxMinutes:
		return bad("Wait 1 to %d minutes at most.", maxMinutes)
	}
	return s.Servers.check()
}

// runWaitFor waits until all servers run, are stopped or have no players, and fails if they
// don't in time.
func runWaitFor(ctx context.Context, x *scope, _ Step, s *waitForSettings) (any, error) {
	deadline := time.Now().Add(time.Duration(s.Minutes) * time.Minute)
	for {
		list, err := x.resolve(ctx, s.Servers)
		var stats measured
		if s.State == "empty" {
			stats = x.measure(ctx, list)
		}
		waiting := []string{}
		for _, srv := range list {
			var ok bool
			switch s.State {
			case "running":
				ok = running(srv)
			case "stopped":
				ok = stopped(srv)
			default:
				players, known := stats.of(srv)["players"].(float64)
				ok = stopped(srv) || known && players == 0
			}
			if !ok {
				waiting = append(waiting, srv.GetName())
			}
		}
		switch {
		case len(waiting) == 0 && err == nil:
			return map[string]any{"detail": fmt.Sprintf("%d servers were %s.", len(list), s.State)}, nil
		case time.Now().After(deadline):
			if err != nil {
				return nil, err
			}
			return map[string]any{"waiting": waiting}, bad("After %d minutes, these weren't %s: %s.", s.Minutes, s.State, strings.Join(waiting, ", "))
		}
		if err := sleep(ctx, min(pollEvery, time.Until(deadline))); err != nil {
			return nil, err
		}
	}
}

type playerSettings struct {
	Servers
	// Action is kick, ban, pardon, whitelist_add, whitelist_remove, op, deop, whitelist_on or
	// whitelist_off.
	Action string `json:"action"`
	// Player is a template of a name or a list of names.
	Player string `json:"player"`
	Reason string `json:"reason"`
	// Minutes is how long a ban lasts; 0 for ever.
	Minutes uint32 `json:"minutes"`
}

func (s *playerSettings) check() error {
	a := noryxv1.ParsePlayerAction(s.Action)
	if !slices.Contains(playerActions, s.Action) {
		return bad("Choose what to do with the player.")
	}
	if a.Global() {
		s.Player = ""
	} else if strings.TrimSpace(s.Player) == "" {
		return bad("Enter the name of the player.")
	}
	if s.Action != "kick" && s.Action != "ban" {
		s.Reason = ""
	}
	if s.Action != "ban" {
		s.Minutes = 0
	}
	return s.Servers.check()
}

func (s *playerSettings) needs() []access.Permission {
	if s.Action == "op" || s.Action == "deop" {
		return []access.Permission{access.ConsoleCommands}
	}
	return nil
}

// runPlayer kicks, bans, whitelists or ops players on game servers, at once on those that run
// and on the others once they do.
func runPlayer(ctx context.Context, x *scope, _ Step, s *playerSettings) (any, error) {
	a := noryxv1.ParsePlayerAction(s.Action)
	reason, err := x.line(s.Reason)
	if err != nil {
		return nil, err
	}
	var changes []*noryxv1.PlayerChange
	names := []any{""}
	if !a.Global() {
		v, err := x.value(s.Player)
		if err != nil {
			return nil, err
		}
		names = items(v)
	}
	if len(names) > noryxv1.MaxPlayerChanges {
		return nil, bad("Change up to %d players at once.", noryxv1.MaxPlayerChanges)
	}
	for _, name := range names {
		c := &noryxv1.PlayerChange{Action: a, Name: strings.TrimSpace(text(name)), Reason: reason}
		if s.Minutes > 0 {
			c.EndsUnix = time.Now().Add(time.Duration(s.Minutes) * time.Minute).Unix()
		}
		if msg := c.Problem(); msg != "" {
			return nil, bad("%s", msg)
		}
		changes = append(changes, c)
	}
	list, listErr := x.resolve(ctx, s.Servers)
	var refs []network.Ref
	for _, srv := range list {
		if !srv.GetType().Proxy() {
			refs = append(refs, network.Ref{NodeID: srv.NodeID, ServerID: srv.GetId()})
		}
	}
	if len(changes) == 0 || len(refs) == 0 {
		return map[string]any{"results": []any{}, "detail": "There was nobody or nowhere to change."}, listErr
	}
	results := x.run.svc.deps.Players.Change(ctx, changes, refs)
	var errs []error
	for _, r := range results {
		if r.Error != "" {
			errs = append(errs, fmt.Errorf("%s: %s", cmp.Or(r.Name, r.ServerID), r.Error))
		}
	}
	slog.Info("Change players", logging.Workflows, "workflow", x.run.wf.Name, "workflow_id", x.run.wf.ID, "action", s.Action, "players", len(changes), "servers", len(refs))
	return map[string]any{"results": results, "detail": fmt.Sprintf("Made %d changes on %d servers.", len(changes), len(refs))}, errors.Join(append(errs, listErr)...)
}

type sendSettings struct {
	Network string `json:"network"`
	Player  string `json:"player"`
	// Server is the name of a game server of the network.
	Server string `json:"server"`
}

func (s *sendSettings) check() error {
	switch {
	case !idPattern.MatchString(s.Network):
		return bad("Choose a network.")
	case strings.TrimSpace(s.Player) == "" || strings.TrimSpace(s.Server) == "":
		return bad("Enter the player and the server to send them to.")
	}
	return nil
}

// runSend sends a player to another server of a network.
func runSend(ctx context.Context, x *scope, _ Step, s *sendSettings) (any, error) {
	player, err := x.line(s.Player)
	var to string
	if err == nil {
		to, err = x.line(s.Server)
	}
	var n network.Network
	if err == nil {
		n, err = x.run.svc.deps.Networks.Get(ctx, s.Network)
	}
	if err == nil {
		err = x.run.svc.deps.Players.Send(ctx, n, player, to)
	}
	return map[string]any{"detail": fmt.Sprintf("Sent %s to %s.", player, to)}, err
}

type maintenanceSettings struct {
	Network string `json:"network"`
	Enabled bool   `json:"enabled"`
	// Server is the name of a game server of the network; empty for the whole network.
	Server string `json:"server"`
	// Minutes is how long maintenance lasts that starts; 0 until it is ended.
	Minutes uint32 `json:"minutes"`
}

func (s *maintenanceSettings) check() error {
	if !idPattern.MatchString(s.Network) {
		return bad("Choose a network.")
	}
	if !s.Enabled {
		s.Minutes = 0
	}
	return nil
}

// runMaintenance starts or ends maintenance of a network or of one of its servers.
func runMaintenance(ctx context.Context, x *scope, _ Step, s *maintenanceSettings) (any, error) {
	srv, err := x.line(s.Server)
	var n network.Network
	if err == nil {
		n, err = x.run.svc.deps.Networks.Get(ctx, s.Network)
	}
	if err == nil {
		_, err = x.run.svc.deps.Networks.SetMaintenance(ctx, n, network.MaintenanceChange{Enabled: s.Enabled, Server: srv, Duration: s.Minutes})
	}
	return nil, err
}

type rollingSettings struct {
	Network string `json:"network"`
	Batch   int    `json:"batch"`
}

func (s *rollingSettings) check() error {
	s.Batch = cmp.Or(s.Batch, 1)
	switch {
	case !idPattern.MatchString(s.Network):
		return bad("Choose a network.")
	case s.Batch < 1 || s.Batch > network.MaxBatch:
		return bad("Restart 1 to %d servers at a time.", network.MaxBatch)
	}
	return nil
}

// runRolling restarts the running game servers of a network a few at a time, moving their
// players to its other servers first, and its proxy after them.
func runRolling(ctx context.Context, x *scope, _ Step, s *rollingSettings) (any, error) {
	n, err := x.run.svc.deps.Networks.Get(ctx, s.Network)
	if err == nil {
		err = x.run.svc.deps.Networks.RollingRestart(ctx, n, s.Batch)
	}
	return nil, err
}

type notifySettings struct {
	Channel string `json:"channel"`
	// Level is info, warn or error.
	Level   string `json:"level"`
	Message string `json:"message"`
}

func (s *notifySettings) check() error {
	s.Level = cmp.Or(s.Level, "info")
	switch {
	case !idPattern.MatchString(s.Channel):
		return bad("Choose a notification channel.")
	case !slices.Contains([]string{"info", "warn", "error"}, s.Level):
		return bad("Choose the level info, warn or error.")
	case strings.TrimSpace(s.Message) == "":
		return bad("Enter the message.")
	}
	return nil
}

// runNotify sends a message through a notification channel, as an entry of the log would go.
func runNotify(_ context.Context, x *scope, _ Step, s *notifySettings) (any, error) {
	msg, err := x.text(s.Message)
	if err != nil {
		return nil, err
	}
	level, _ := logging.ParseLevel(s.Level)
	e := logs.Entry{
		Time: time.Now(), Level: level, Source: logs.FromMaster, Category: logging.Workflows.Value.String(),
		Message: clip(strings.TrimSpace(msg), 2000), Attrs: map[string]string{"workflow": x.run.wf.Name},
	}
	if err := x.run.svc.deps.Notify.Post(s.Channel, e); err != nil {
		return nil, err
	}
	return map[string]any{"detail": "The message is on its way."}, nil
}

type httpSettings struct {
	Method  string   `json:"method"`
	URL     string   `json:"url"`
	Headers []Header `json:"headers"`
	Body    string   `json:"body"`
	// AnyStatus succeeds whatever the status of the answer; otherwise it must be 2xx.
	AnyStatus bool `json:"anyStatus"`
}

// Header is a header of a request. The value of a secret one is never shown; it is used as
// it is, not as a template.
type Header struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Secret bool   `json:"secret"`
}

func (s *httpSettings) check() error {
	s.Method, s.URL = cmp.Or(strings.ToUpper(s.Method), http.MethodGet), strings.TrimSpace(s.URL)
	switch {
	case !slices.Contains(methods, s.Method):
		return bad("Choose GET, POST, PUT, PATCH or DELETE.")
	case len(s.URL) > maxURL || !strings.HasPrefix(s.URL, "https://") && !strings.HasPrefix(s.URL, "{{"):
		return bad("Enter a URL starting with https://.")
	case len(s.Headers) > maxHeaders:
		return bad("Add up to %d headers.", maxHeaders)
	case len(s.Body) > maxBody:
		return bad("Keep the body within %d KB.", maxBody>>10)
	}
	if s.Headers == nil {
		s.Headers = []Header{}
	}
	for i := range s.Headers {
		h := &s.Headers[i]
		h.Name = strings.TrimSpace(h.Name)
		switch {
		case !headerName.MatchString(h.Name):
			return bad("%q is no name of a header.", h.Name)
		case slices.Contains(headersRefused, strings.ToLower(h.Name)):
			return bad("The master sets the header %s itself.", h.Name)
		case len(h.Value) > maxTemplate || strings.ContainsAny(h.Value, "\r\n\x00"):
			return bad("Keep the value of the header %s on one line.", h.Name)
		}
	}
	return nil
}

// runHTTP sends a request over HTTPS to a public address, and tells its answer: the status,
// the text and the JSON it holds, if any.
func runHTTP(ctx context.Context, x *scope, _ Step, s *httpSettings) (any, error) {
	url, err := x.line(s.URL)
	var body string
	if err == nil {
		body, err = x.text(s.Body)
	}
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, s.Method, url, strings.NewReader(body))
	if err != nil {
		return nil, bad("%q is no URL.", clip(url, 200))
	}
	req.Header.Set("User-Agent", "Noryx/"+buildinfo.Version)
	if body != "" {
		req.Header.Set("Content-Type", "text/plain; charset=utf-8")
		if json.Valid([]byte(body)) {
			req.Header.Set("Content-Type", "application/json")
		}
	}
	for _, h := range s.Headers {
		v := h.Value
		if !h.Secret {
			if v, err = x.line(h.Value); err != nil {
				return nil, err
			}
		}
		req.Header.Set(h.Name, v)
	}
	res, err := x.run.svc.deps.Notify.Fetch(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, maxBody+1))
	if err != nil {
		return nil, bad("The answer broke off: %v", err)
	}
	truncated := len(raw) > maxBody
	raw = raw[:min(len(raw), maxBody)]
	out := map[string]any{
		"status": res.StatusCode, "ok": res.StatusCode >= 200 && res.StatusCode < 300, "text": string(bytes.ToValidUTF8(raw, []byte("�"))),
		"truncated": truncated, "contentType": res.Header.Get("Content-Type"),
		"detail": fmt.Sprintf("%s %s answered %d.", s.Method, req.URL.Host, res.StatusCode),
	}
	var data any
	if !truncated && json.Unmarshal(raw, &data) == nil {
		out["json"] = data
	}
	if !s.AnyStatus && (res.StatusCode < 200 || res.StatusCode > 299) {
		return out, bad("%s answered %d %s.", req.URL.Host, res.StatusCode, http.StatusText(res.StatusCode))
	}
	return out, nil
}

type logSettings struct {
	// Level is info, warn or error.
	Level   string `json:"level"`
	Message string `json:"message"`
}

func (s *logSettings) check() error {
	s.Level = cmp.Or(s.Level, "info")
	switch {
	case !slices.Contains([]string{"info", "warn", "error"}, s.Level):
		return bad("Choose the level info, warn or error.")
	case strings.TrimSpace(s.Message) == "":
		return bad("Enter the message.")
	}
	return nil
}

// runLog writes an entry into the log, which notification rules can send on.
func runLog(ctx context.Context, x *scope, _ Step, s *logSettings) (any, error) {
	msg, err := x.line(s.Message)
	if err != nil {
		return nil, err
	}
	level, _ := logging.ParseLevel(s.Level)
	slog.Log(ctx, level, clip(msg, 2000), logging.Workflows, "workflow", x.run.wf.Name, "workflow_id", x.run.wf.ID)
	return nil, nil
}
