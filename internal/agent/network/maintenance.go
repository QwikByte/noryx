package network

import (
	"context"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	"github.com/QwikByte/noryx/internal/agent/datadir"
)

const (
	// maintenanceWait is how long the plugin may take to save a change; adding a player who
	// never joined asks Mojang for the player's ID first.
	maintenanceWait = 15 * time.Second
	maintenancePoll = 200 * time.Millisecond
	// maxMaintenanceServers is the most servers in maintenance that are read from the plugin's
	// configuration, which the proxy could have written.
	maxMaintenanceServers = 256
	// fixedSchedule is the first version of the plugin's configuration whose schedules of
	// single servers don't swap their times, that of version 5.
	fixedSchedule = 10
)

// maintenanceCommands are the subcommands of the Maintenance plugin by kennytv for the
// changes, which version 4 and 5 know.
var maintenanceCommands = map[noryxv1.MaintenanceChange]string{
	noryxv1.MaintenanceChange_MAINTENANCE_CHANGE_ON:          "on",
	noryxv1.MaintenanceChange_MAINTENANCE_CHANGE_OFF:         "off",
	noryxv1.MaintenanceChange_MAINTENANCE_CHANGE_ADD:         "add",
	noryxv1.MaintenanceChange_MAINTENANCE_CHANGE_REMOVE:      "remove",
	noryxv1.MaintenanceChange_MAINTENANCE_CHANGE_START_TIMER: "starttimer",
	noryxv1.MaintenanceChange_MAINTENANCE_CHANGE_END_TIMER:   "endtimer",
	noryxv1.MaintenanceChange_MAINTENANCE_CHANGE_SCHEDULE:    "scheduletimer",
	noryxv1.MaintenanceChange_MAINTENANCE_CHANGE_ABORT_TIMER: "aborttimer",
}

func (s *Service) GetMaintenance(ctx context.Context, req *noryxv1.GetMaintenanceRequest) (*noryxv1.GetMaintenanceResponse, error) {
	srv, _, dir, err := s.open(ctx, req.GetServerId())
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	m, _, err := readMaintenance(dir, srv.Type)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return &noryxv1.GetMaintenanceResponse{Maintenance: m}, nil
}

// ChangeMaintenance runs the command of the plugin for a change, for the whole network or
// one of the servers in the configuration of the proxy. Changes the plugin saves return
// once it did. Timers only run in the plugin: a new one replaces the one of its target
// that runs, if any, and they return once their commands were sent.
func (s *Service) ChangeMaintenance(ctx context.Context, req *noryxv1.ChangeMaintenanceRequest) (*noryxv1.ChangeMaintenanceResponse, error) {
	if problem := req.Problem(); problem != "" {
		return nil, status.Error(codes.InvalidArgument, problem)
	}
	change, player, server := req.GetChange(), req.GetPlayer(), req.GetServer()
	srv, p, dir, err := s.open(ctx, req.GetServerId())
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	if srv.State != noryxv1.ServerState_SERVER_STATE_RUNNING {
		return nil, status.Error(codes.FailedPrecondition, "Start the proxy first.")
	}
	if server != "" {
		if ok, err := hasServer(dir, p, server); err != nil || !ok {
			return nil, status.Errorf(codes.FailedPrecondition, "%s isn't a server in the configuration of the proxy. Apply the network again.", server)
		}
	}
	// on tells whether the target of the change is in maintenance.
	on := func(m *noryxv1.Maintenance) bool {
		if server != "" {
			return slices.Contains(m.GetServers(), server)
		}
		return m.GetEnabled()
	}
	// done tells whether the plugin's files show a change it saves.
	done := func(m *noryxv1.Maintenance) bool {
		has := slices.ContainsFunc(m.GetPlayers(), func(p *noryxv1.MaintenancePlayer) bool { return strings.EqualFold(p.GetName(), player) })
		switch change {
		case noryxv1.MaintenanceChange_MAINTENANCE_CHANGE_ON:
			return on(m)
		case noryxv1.MaintenanceChange_MAINTENANCE_CHANGE_OFF:
			return !on(m)
		case noryxv1.MaintenanceChange_MAINTENANCE_CHANGE_ADD:
			return has
		default:
			return !has
		}
	}
	m, version, err := readMaintenance(dir, srv.Type)
	switch {
	case err != nil:
		return nil, status.Error(codes.Internal, err.Error())
	case !m.GetInstalled():
		return nil, status.Error(codes.FailedPrecondition, "The proxy hasn't loaded the Maintenance plugin yet. Restart it.")
	case change == noryxv1.MaintenanceChange_MAINTENANCE_CHANGE_END_TIMER && !on(m):
		return nil, status.Error(codes.FailedPrecondition, "Maintenance is off.")
	case change.Timer() && change != noryxv1.MaintenanceChange_MAINTENANCE_CHANGE_END_TIMER && on(m):
		return nil, status.Error(codes.FailedPrecondition, "Maintenance is on already.")
	case change == noryxv1.MaintenanceChange_MAINTENANCE_CHANGE_SCHEDULE && server != "" && version < fixedSchedule:
		return nil, status.Error(codes.FailedPrecondition, "Update the Maintenance plugin of the proxy to version 5 to plan the maintenance of single servers: version 4 swaps their times.")
	case !change.Timer() && change != noryxv1.MaintenanceChange_MAINTENANCE_CHANGE_ABORT_TIMER && done(m):
		return &noryxv1.ChangeMaintenanceResponse{Maintenance: m}, nil
	}
	// All parts are checked, so they take nothing else into the command.
	args := []string{"maintenance", maintenanceCommands[change], server, player}
	for _, minutes := range []uint32{req.GetMinutes(), req.GetDurationMinutes()} {
		if minutes > 0 {
			args = append(args, strconv.FormatUint(uint64(minutes), 10))
		}
	}
	commands := []string{strings.Join(slices.DeleteFunc(args, func(a string) bool { return a == "" }), " ")}
	if change.Timer() { // the plugin keeps a timer that runs and ignores the new one
		commands = slices.Insert(commands, 0, strings.TrimSpace("maintenance aborttimer "+server))
	}
	for _, command := range commands {
		if _, err := s.rt.SendCommand(ctx, srv.ID, command); err != nil {
			return nil, status.Error(codes.Unavailable, err.Error())
		}
	}
	if change.Timer() || change == noryxv1.MaintenanceChange_MAINTENANCE_CHANGE_ABORT_TIMER {
		return &noryxv1.ChangeMaintenanceResponse{Maintenance: m}, nil
	}
	ctx, cancel := context.WithTimeout(ctx, maintenanceWait)
	defer cancel()
	t := time.NewTicker(maintenancePoll)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil, status.Error(codes.DeadlineExceeded, "The Maintenance plugin didn't save the change. The console of the proxy tells why.")
		case <-t.C:
		}
		if m, _, err := readMaintenance(dir, srv.Type); err == nil && done(m) {
			return &noryxv1.ChangeMaintenanceResponse{Maintenance: m}, nil
		}
	}
}

// hasServer reports whether the configuration of a proxy has a server of the given name,
// which the network wrote there.
func hasServer(dir *datadir.Dir, p Proxy, name string) (bool, error) {
	data, err := dir.ReadFile(p.File)
	if err != nil {
		return false, err
	}
	settings, err := parse(p.File, data)
	if err != nil {
		return false, err
	}
	servers, _ := settings["servers"].(map[string]any)
	_, ok := servers[name]
	return ok && name != "try", nil
}

// readMaintenance reads the files of the Maintenance plugin in its folder: config.yml with
// whether maintenance is on, for the whole network and for single servers, and
// WhitelistedPlayers.yml with the players who may join, by their ID. Without config.yml, the
// plugin isn't installed or wasn't loaded yet. It also returns the version of config.yml.
func readMaintenance(dir *datadir.Dir, typ noryxv1.ServerType) (*noryxv1.Maintenance, int, error) {
	m := &noryxv1.Maintenance{}
	folder := typ.MaintenanceFolder()
	data, err := dir.ReadOptional(filepath.FromSlash(path.Join(folder, "config.yml")))
	if err != nil || data == nil {
		return m, 0, err
	}
	var config struct {
		Enabled bool `yaml:"maintenance-enabled"`
		// Servers are a list of names before version 5 of the plugin, and then a map of names
		// to the mode of their messages.
		Servers  yaml.Node `yaml:"proxied-maintenance-servers"`
		Endtimer struct {
			Enabled bool  `yaml:"enabled"`
			End     int64 `yaml:"end"` // in milliseconds
		} `yaml:"continue-endtimer-after-restart"`
		Version int `yaml:"config-version"`
	}
	if err := yaml.Unmarshal(data, &config); err != nil {
		return nil, 0, err
	}
	data, err = dir.ReadOptional(filepath.FromSlash(path.Join(folder, "WhitelistedPlayers.yml")))
	var players map[string]string
	if err == nil {
		err = yaml.Unmarshal(data, &players)
	}
	if err != nil {
		return nil, 0, err
	}
	m.Installed, m.Enabled = true, config.Enabled
	for i, node := range config.Servers.Content {
		// A map alternates names and modes.
		if config.Servers.Kind == yaml.SequenceNode || config.Servers.Kind == yaml.MappingNode && i%2 == 0 {
			if noryxv1.ValidBackendName(node.Value) && len(m.Servers) < maxMaintenanceServers {
				m.Servers = append(m.Servers, node.Value)
			}
		}
	}
	slices.Sort(m.Servers)
	m.Servers = slices.Compact(m.Servers)
	if end := config.Endtimer.End / 1000; config.Endtimer.Enabled && config.Enabled && end > time.Now().Unix() {
		m.EndsAt = end
	}
	for uuid, name := range players {
		m.Players = append(m.Players, &noryxv1.MaintenancePlayer{Name: name, Uuid: uuid})
	}
	slices.SortFunc(m.Players, func(a, b *noryxv1.MaintenancePlayer) int {
		return strings.Compare(strings.ToLower(a.GetName()), strings.ToLower(b.GetName()))
	})
	return m, config.Version, nil
}
