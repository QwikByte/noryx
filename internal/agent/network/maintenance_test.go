package network

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	"github.com/QwikByte/noryx/internal/agent/datadir"
	"github.com/QwikByte/noryx/internal/agent/runtime"
)

// pluginRuntime has one Velocity proxy whose console acts like the Maintenance plugin.
type pluginRuntime struct {
	runtime.Runtime
	dir      string
	mu       sync.Mutex
	srv      runtime.Server
	commands []string
}

const proxyID = "aaaaaaaaaaaaaaaaaaaaaaaaaa"

func (p *pluginRuntime) List(context.Context) ([]runtime.Server, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return []runtime.Server{p.srv}, nil
}

func (p *pluginRuntime) Data(context.Context, string) (*datadir.Dir, error) {
	return datadir.Open(p.dir)
}

func (p *pluginRuntime) SendCommand(_ context.Context, _, command string) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.commands = append(p.commands, command)
	switch command {
	case "maintenance on":
		p.write("config.yml", "# Enables maintenance mode.\nmaintenance-enabled: true\n")
	case "maintenance add Steve":
		p.write("WhitelistedPlayers.yml", "8667ba71-b85a-4004-af54-457a9734eed7: Steve\n")
	case "maintenance on lobby":
		p.write("config.yml", "maintenance-enabled: false\nproxied-maintenance-servers:\n  game: default\n  lobby: default\nconfig-version: 9\n")
	}
	return "", nil
}

func (p *pluginRuntime) write(name, content string) {
	_ = os.WriteFile(filepath.Join(p.dir, "plugins", "maintenance", name), []byte(content), 0o600)
}

func TestMaintenance(t *testing.T) {
	ctx := t.Context()
	rt := &pluginRuntime{dir: t.TempDir(), srv: runtime.Server{
		Spec: runtime.Spec{ID: proxyID, Type: noryxv1.ServerType_SERVER_TYPE_VELOCITY}, State: noryxv1.ServerState_SERVER_STATE_RUNNING,
	}}
	s := NewService(rt)
	change := func(c noryxv1.MaintenanceChange, player string) (*noryxv1.Maintenance, error) {
		res, err := s.ChangeMaintenance(ctx, &noryxv1.ChangeMaintenanceRequest{ServerId: proxyID, Change: c, Player: player})
		return res.GetMaintenance(), err
	}

	// Without its files, the plugin isn't loaded and can't be told anything.
	res, err := s.GetMaintenance(ctx, &noryxv1.GetMaintenanceRequest{ServerId: proxyID})
	if err != nil || res.GetMaintenance().GetInstalled() {
		t.Fatalf("maintenance without the plugin = %v, %v", res, err)
	}
	if _, err := change(noryxv1.MaintenanceChange_MAINTENANCE_CHANGE_ON, ""); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("turning on without the plugin: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(rt.dir, "plugins", "maintenance"), 0o750); err != nil {
		t.Fatal(err)
	}
	rt.write("config.yml", "maintenance-enabled: false\n")

	for _, tc := range []struct {
		change noryxv1.MaintenanceChange
		player string
	}{
		{noryxv1.MaintenanceChange_MAINTENANCE_CHANGE_ON, "Steve"},
		{noryxv1.MaintenanceChange_MAINTENANCE_CHANGE_ADD, ""},
		{noryxv1.MaintenanceChange_MAINTENANCE_CHANGE_ADD, "@a"},
		{noryxv1.MaintenanceChange_MAINTENANCE_CHANGE_UNSPECIFIED, ""},
	} {
		if _, err := change(tc.change, tc.player); status.Code(err) != codes.InvalidArgument {
			t.Errorf("%v %q: %v", tc.change, tc.player, err)
		}
	}

	// A change returns once the plugin saved it; one that is done already runs no command.
	if m, err := change(noryxv1.MaintenanceChange_MAINTENANCE_CHANGE_ON, ""); err != nil || !m.GetEnabled() {
		t.Fatalf("turning on = %v, %v", m, err)
	}
	m, err := change(noryxv1.MaintenanceChange_MAINTENANCE_CHANGE_ADD, "Steve")
	if err != nil || len(m.GetPlayers()) != 1 || m.GetPlayers()[0].GetName() != "Steve" {
		t.Fatalf("adding a player = %v, %v", m, err)
	}
	if _, err := change(noryxv1.MaintenanceChange_MAINTENANCE_CHANGE_ON, ""); err != nil {
		t.Fatal(err)
	}
	if want := []string{"maintenance on", "maintenance add Steve"}; !slices.Equal(rt.commands, want) {
		t.Fatalf("commands = %q, want %q", rt.commands, want)
	}

	// The proxy must run.
	rt.srv.State = noryxv1.ServerState_SERVER_STATE_STOPPED
	if _, err := change(noryxv1.MaintenanceChange_MAINTENANCE_CHANGE_OFF, ""); status.Code(err) != codes.FailedPrecondition || !strings.Contains(err.Error(), "Start") {
		t.Fatalf("change on a stopped proxy: %v", err)
	}
}

func TestMaintenanceOfServersAndTimers(t *testing.T) {
	ctx := t.Context()
	rt := &pluginRuntime{dir: t.TempDir(), srv: runtime.Server{
		Spec: runtime.Spec{ID: proxyID, Type: noryxv1.ServerType_SERVER_TYPE_VELOCITY}, State: noryxv1.ServerState_SERVER_STATE_RUNNING,
	}}
	s := NewService(rt)
	change := func(req *noryxv1.ChangeMaintenanceRequest) (*noryxv1.Maintenance, error) {
		req.ServerId = proxyID
		res, err := s.ChangeMaintenance(ctx, req)
		return res.GetMaintenance(), err
	}
	get := func() *noryxv1.Maintenance {
		res, err := s.GetMaintenance(ctx, &noryxv1.GetMaintenanceRequest{ServerId: proxyID})
		if err != nil {
			t.Fatal(err)
		}
		return res.GetMaintenance()
	}
	if err := os.MkdirAll(filepath.Join(rt.dir, "plugins", "maintenance"), 0o750); err != nil {
		t.Fatal(err)
	}
	config := "[servers]\nlobby = \"lobby:25565\"\ngame = \"game:25565\"\nsurvival = \"survival:25565\"\nglobal = \"global:25565\"\ntry = [\"lobby\"]\n"
	if err := os.WriteFile(filepath.Join(rt.dir, "velocity.toml"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}

	// Version 4 of the plugin lists the servers in maintenance; version 5 maps them to a mode,
	// and keeps when its end timer ends if it is told to.
	rt.write("config.yml", "maintenance-enabled: false\nproxied-maintenance-servers:\n  - game\n  - PaperServer1\nconfig-version: 9\n")
	if m := get(); !slices.Equal(m.GetServers(), []string{"game"}) || m.GetEndsAt() != 0 {
		t.Fatalf("maintenance of version 4 = %v", m)
	}
	end := time.Now().Add(time.Hour).Unix()
	rt.write("config.yml", fmt.Sprintf("maintenance-enabled: true\nproxied-maintenance-servers:\n  game: default\n"+
		"continue-endtimer-after-restart:\n  enabled: true\n  end: %d\nconfig-version: 11\n", end*1000))
	if m := get(); !slices.Equal(m.GetServers(), []string{"game"}) || m.GetEndsAt() != end {
		t.Fatalf("maintenance of version 5 = %v", m)
	}

	// Commands only get servers in the configuration of the proxy and times within bounds.
	for _, req := range []*noryxv1.ChangeMaintenanceRequest{
		{Change: noryxv1.MaintenanceChange_MAINTENANCE_CHANGE_ON, Server: "elsewhere"},
		{Change: noryxv1.MaintenanceChange_MAINTENANCE_CHANGE_ON, Server: "lobby off"},
		{Change: noryxv1.MaintenanceChange_MAINTENANCE_CHANGE_ON, Server: "global"},
		{Change: noryxv1.MaintenanceChange_MAINTENANCE_CHANGE_ON, Server: "try"},
		{Change: noryxv1.MaintenanceChange_MAINTENANCE_CHANGE_ON, Minutes: 5},
		{Change: noryxv1.MaintenanceChange_MAINTENANCE_CHANGE_ON, Player: "@a"},
		{Change: noryxv1.MaintenanceChange_MAINTENANCE_CHANGE_ON, Player: "lobby off"},
		{Change: noryxv1.MaintenanceChange_MAINTENANCE_CHANGE_ADD, Player: "Steve", Server: "lobby"},
		{Change: noryxv1.MaintenanceChange_MAINTENANCE_CHANGE_START_TIMER, Server: "lobby"},
		{Change: noryxv1.MaintenanceChange_MAINTENANCE_CHANGE_START_TIMER, Server: "lobby", Minutes: noryxv1.MaxMaintenanceMinutes + 1},
		{Change: noryxv1.MaintenanceChange_MAINTENANCE_CHANGE_START_TIMER, Server: "lobby", Minutes: 5, DurationMinutes: 60},
		{Change: noryxv1.MaintenanceChange_MAINTENANCE_CHANGE_SCHEDULE, Server: "lobby", Minutes: 5},
		{Change: noryxv1.MaintenanceChange(99)},
	} {
		if _, err := change(req); status.Code(err) != codes.InvalidArgument && status.Code(err) != codes.FailedPrecondition {
			t.Errorf("%v: %v", req, err)
		}
	}

	// Maintenance of a server returns once the plugin saved it. A timer returns once its
	// commands were sent, the first of which aborts the timer that runs; it needs maintenance
	// to be on to end it, and off to start it.
	m, err := change(&noryxv1.ChangeMaintenanceRequest{Change: noryxv1.MaintenanceChange_MAINTENANCE_CHANGE_ON, Server: "lobby"})
	if err != nil || !slices.Equal(m.GetServers(), []string{"game", "lobby"}) {
		t.Fatalf("maintenance of lobby = %v, %v", m, err)
	}
	if _, err := change(&noryxv1.ChangeMaintenanceRequest{Change: noryxv1.MaintenanceChange_MAINTENANCE_CHANGE_END_TIMER, Server: "lobby", Minutes: 60}); err != nil {
		t.Fatal(err)
	}
	for _, req := range []*noryxv1.ChangeMaintenanceRequest{
		{Change: noryxv1.MaintenanceChange_MAINTENANCE_CHANGE_END_TIMER, Minutes: 5},
		{Change: noryxv1.MaintenanceChange_MAINTENANCE_CHANGE_SCHEDULE, Server: "lobby", Minutes: 5, DurationMinutes: 60},
		// Version 4 of the plugin swaps the times of schedules of single servers.
		{Change: noryxv1.MaintenanceChange_MAINTENANCE_CHANGE_SCHEDULE, Server: "survival", Minutes: 5, DurationMinutes: 60},
	} {
		if _, err := change(req); status.Code(err) != codes.FailedPrecondition {
			t.Errorf("%v: %v", req, err)
		}
	}
	if _, err := change(&noryxv1.ChangeMaintenanceRequest{Change: noryxv1.MaintenanceChange_MAINTENANCE_CHANGE_SCHEDULE, Minutes: 5, DurationMinutes: 60}); err != nil {
		t.Fatal(err)
	}
	if _, err := change(&noryxv1.ChangeMaintenanceRequest{Change: noryxv1.MaintenanceChange_MAINTENANCE_CHANGE_ABORT_TIMER, Server: "game"}); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"maintenance on lobby",
		"maintenance aborttimer lobby", "maintenance endtimer lobby 60",
		"maintenance aborttimer", "maintenance scheduletimer 5 60",
		"maintenance aborttimer game",
	}
	if !slices.Equal(rt.commands, want) {
		t.Fatalf("commands = %q, want %q", rt.commands, want)
	}
}
