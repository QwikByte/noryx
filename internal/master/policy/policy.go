// Package policy is the kind of scheduled task that rules servers or whole nodes: restarts
// with a countdown for the players, also server by server in networks, operating hours by
// starting and stopping servers, console commands, and updates of images, plugins and mods;
// also only once the players left, and after backing up the servers.
package policy

import (
	"cmp"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode"

	"google.golang.org/grpc"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	"github.com/QwikByte/noryx/internal/logging"
	"github.com/QwikByte/noryx/internal/master/access"
	"github.com/QwikByte/noryx/internal/master/backup"
	"github.com/QwikByte/noryx/internal/master/httpapi"
	"github.com/QwikByte/noryx/internal/master/network"
	"github.com/QwikByte/noryx/internal/master/plugin"
	"github.com/QwikByte/noryx/internal/master/schedule"
	"github.com/QwikByte/noryx/internal/master/server"
)

// TaskKind identifies policies among the scheduled tasks.
const TaskKind = "policy"

const (
	restart = "restart"
	stop    = "stop"
	start   = "start"
	command = "command"
	image   = "image"
	plugins = "plugins"

	// Conditions: only servers without players, or waiting for that at most Wait minutes.
	ifEmpty   = "empty"
	waitEmpty = "wait"

	maxWarnings = 5
	maxMinutes  = 60
	maxCommand  = 1000
	maxCommands = 20
	maxWait     = 360
)

// actions are what the log and the steps of runs call what a policy does to a server.
var actions = map[string]string{
	restart: "Restart server", stop: "Stop server", start: "Start server", command: "Send console commands",
	image: "Update image", plugins: "Update plugins",
}

// Settings say what a policy does.
type Settings struct {
	// Action is restart, stop, start, command, image or plugins.
	Action string `json:"action"`
	// Warnings are the minutes before a restart or stop at which players are warned.
	Warnings []uint32 `json:"warnings"`
	// Message warns the players; {minutes} is replaced by the minutes left. Empty is the
	// text of the settings when the policy runs.
	Message string `json:"message"`
	// Commands are the console commands of the command action, sent one after the other.
	Commands []string `json:"commands"`
	// Command is the console command of policies saved before they could have several.
	Command string `json:"command,omitempty"`
	// Rolling, unless 0, restarts the running game servers of networks this many at a time,
	// so that their players move to other servers first; see network.RollingRestart.
	Rolling int `json:"rolling,omitempty"`
	// Condition leaves servers with players alone: empty acts on all servers, "empty" only on
	// those without players, and "wait" waits for them to leave, at most Wait minutes.
	Condition string `json:"condition,omitempty"`
	Wait      uint32 `json:"wait,omitempty"`
	// Backup, if set, backs up each server before the action as a backup job with these
	// settings would, without datastores.
	Backup *backup.JobSettings `json:"backup,omitempty"`
}

// decode reads the settings of a policy, also of one saved before it could have several
// commands.
func decode(raw json.RawMessage) (Settings, error) {
	var s Settings
	err := json.Unmarshal(raw, &s)
	if s.Command != "" && len(s.Commands) == 0 {
		s.Commands = []string{s.Command}
	}
	s.Command = ""
	return s, err
}

// Nodes provides connections to node agents.
type Nodes interface {
	Conn(ctx context.Context, nodeID string) (grpc.ClientConnInterface, error)
}

// Networks restart the game servers of networks server by server.
type Networks interface {
	List(ctx context.Context) ([]network.Network, error)
	RollingRestart(ctx context.Context, n network.Network, batch int, only ...network.Ref) error
}

// Usage tells the latest measurements of the servers of a node, with their players.
type Usage interface {
	Latest(ctx context.Context, nodeID string) (*noryxv1.GetStatsResponse, error)
}

// Backups back up servers as backup jobs do, see backup.Jobs.BackUp.
type Backups interface {
	BackUp(ctx context.Context, nodeID, serverID string, t schedule.Task) (*noryxv1.Backup, error)
}

// Plugins update the plugins and mods of servers, see plugin.Service.Update.
type Plugins interface {
	Update(ctx context.Context, servers []plugin.Ref, projects []string) []plugin.Result
}

// Config tells how the players are warned, as the settings say when a policy runs.
type Config interface {
	Warnings() server.Warnings
}

// Policies is the kind of task that runs policies.
type Policies struct {
	nodes    Nodes
	networks Networks
	usage    Usage
	backups  Backups
	plugins  Plugins
	config   Config
}

func New(nodes Nodes, networks Networks, usage Usage, backups Backups, plugins Plugins, config Config) Policies {
	return Policies{nodes: nodes, networks: networks, usage: usage, backups: backups, plugins: plugins, config: config}
}

func (Policies) Check(raw json.RawMessage) (json.RawMessage, error) {
	s, err := decode(raw)
	if err != nil {
		return nil, httpapi.Errorf(http.StatusBadRequest, "Choose what the schedule does.")
	}
	if (s.Action == restart || s.Action == stop) && s.Condition == "" {
		slices.SortFunc(s.Warnings, func(a, b uint32) int { return cmp.Compare(b, a) })
		s.Warnings = slices.Compact(s.Warnings)
		if s.Message = strings.TrimSpace(s.Message); s.Message != "" {
			err = server.CheckWarning(s.Message)
		}
	} else {
		s.Warnings, s.Message = []uint32{}, ""
	}
	if s.Action != command {
		s.Commands = nil
	}
	if s.Action != restart {
		s.Rolling = 0
	}
	if s.Action == start {
		s.Condition = ""
	}
	if s.Condition != waitEmpty {
		s.Wait = 0
	}
	if s.Action == start || s.Action == command {
		s.Backup = nil
	}
	for i, c := range s.Commands {
		s.Commands[i] = strings.TrimSpace(c)
	}
	badCommand := func(c string) bool {
		return c == "" || len(c) > maxCommand || strings.ContainsFunc(c, unicode.IsControl)
	}
	switch {
	case actions[s.Action] == "":
		return nil, httpapi.Errorf(http.StatusBadRequest, "Choose what the schedule does.")
	case len(s.Warnings) > maxWarnings || slices.ContainsFunc(s.Warnings, func(m uint32) bool { return m == 0 || m > maxMinutes }):
		return nil, httpapi.Errorf(http.StatusBadRequest, "Warn the players up to %d times, 1 to %d minutes before.", maxWarnings, maxMinutes)
	case err != nil:
		return nil, err
	case s.Action == command && (len(s.Commands) == 0 || len(s.Commands) > maxCommands || slices.ContainsFunc(s.Commands, badCommand)):
		return nil, httpapi.Errorf(http.StatusBadRequest, "Enter 1 to %d console commands, each on one line with up to %d characters.", maxCommands, maxCommand)
	case s.Rolling < 0 || s.Rolling > network.MaxBatch:
		return nil, httpapi.Errorf(http.StatusBadRequest, "Restart from 1 to %d servers of a network at a time.", network.MaxBatch)
	case !slices.Contains([]string{"", ifEmpty, waitEmpty}, s.Condition):
		return nil, httpapi.Errorf(http.StatusBadRequest, "Choose whether servers with players are left alone.")
	case s.Condition == waitEmpty && (s.Wait == 0 || s.Wait > maxWait):
		return nil, httpapi.Errorf(http.StatusBadRequest, "Wait from 1 to %d minutes for the players to leave.", maxWait)
	}
	if s.Backup != nil {
		if s.Backup, err = backupSettings(*s.Backup); err != nil {
			return nil, err
		}
	}
	if s.Warnings == nil {
		s.Warnings = []uint32{}
	}
	if s.Commands == nil {
		s.Commands = []string{}
	}
	return json.Marshal(s)
}

// backupSettings checks the settings of backing up first as those of a backup job, which
// dumps no datastores.
func backupSettings(b backup.JobSettings) (*backup.JobSettings, error) {
	b.Datastores = nil
	raw, err := json.Marshal(b)
	if err == nil {
		raw, err = backup.Jobs{}.Check(raw)
	}
	if err == nil {
		err = json.Unmarshal(raw, &b)
	}
	return &b, err
}

func (Policies) Category() slog.Attr { return logging.Policies }

// Lead is the time of the earliest warning before a restart or stop. A policy with a
// condition warns nobody, as it leaves servers with players alone.
func (Policies) Lead(raw json.RawMessage) time.Duration {
	s, err := decode(raw)
	if err != nil || len(s.Warnings) == 0 || s.Condition != "" {
		return 0
	}
	return time.Duration(slices.Max(s.Warnings)) * time.Minute
}

// Needs are the permissions on all servers that backing up first and updates need, as they
// would by hand: those to back up servers, to change their settings for their images, and to
// manage their plugins and mods.
func (Policies) Needs(raw json.RawMessage) []access.Permission {
	s, _ := decode(raw)
	var perms []access.Permission
	if s.Backup != nil {
		perms = append(perms, access.BackupsCreate)
	}
	switch s.Action {
	case image:
		perms = append(perms, access.ServersSettings)
	case plugins:
		perms = append(perms, access.Plugins)
	}
	return perms
}
