package noryxv1

import (
	"strings"
	"testing"
)

func TestProxyFiles(t *testing.T) {
	for typ, want := range map[ServerType][2]string{
		ServerType_SERVER_TYPE_VELOCITY:   {"velocity.toml", "plugins/maintenance"},
		ServerType_SERVER_TYPE_BUNGEECORD: {"config.yml", "plugins/Maintenance"},
	} {
		if got := [2]string{typ.ConfigFile(), typ.MaintenanceFolder()}; got != want {
			t.Errorf("%v: %v, want %v", typ, got, want)
		}
	}
}

// Names of the game servers of a network go into console commands of the proxy as they are.
func TestValidBackendName(t *testing.T) {
	checkValid(t, ValidBackendName,
		[]string{"lobby", "survival-1", "skyblock_2", "0", "global", strings.Repeat("a", 32)},
		[]string{"", "try", "Lobby", "-lobby", "_lobby", "lobby server", "lobby;stop", "lobby\n", "lobby.1", strings.Repeat("a", 33)})
}

// Only changes of the team take a player, and no server; the server can't be global; timers
// take minutes, schedules a duration too, and other changes neither.
func TestChangeMaintenanceProblem(t *testing.T) {
	const (
		on, add, remove = MaintenanceChange_MAINTENANCE_CHANGE_ON, MaintenanceChange_MAINTENANCE_CHANGE_ADD, MaintenanceChange_MAINTENANCE_CHANGE_REMOVE
		start, end      = MaintenanceChange_MAINTENANCE_CHANGE_START_TIMER, MaintenanceChange_MAINTENANCE_CHANGE_END_TIMER
		schedule, abort = MaintenanceChange_MAINTENANCE_CHANGE_SCHEDULE, MaintenanceChange_MAINTENANCE_CHANGE_ABORT_TIMER
	)
	for _, tc := range []struct {
		name string
		req  *ChangeMaintenanceRequest
		want string
	}{
		{"turn on", &ChangeMaintenanceRequest{Change: on}, ""},
		{"turn on for a server", &ChangeMaintenanceRequest{Change: on, Server: "lobby"}, ""},
		{"add to the team", &ChangeMaintenanceRequest{Change: add, Player: "Steve"}, ""},
		{"remove a Bedrock player from the team", &ChangeMaintenanceRequest{Change: remove, Player: ".Steve"}, ""},
		{"start a timer", &ChangeMaintenanceRequest{Change: start, Minutes: 30}, ""},
		{"end in 28 days", &ChangeMaintenanceRequest{Change: end, Minutes: MaxMaintenanceMinutes}, ""},
		{"schedule", &ChangeMaintenanceRequest{Change: schedule, Minutes: 60, DurationMinutes: 120}, ""},
		{"start a timer of a server", &ChangeMaintenanceRequest{Change: start, Server: "lobby", Minutes: 5}, ""},
		{"abort a timer", &ChangeMaintenanceRequest{Change: abort}, ""},

		{"no change", &ChangeMaintenanceRequest{}, "Choose a change"},
		{"an unknown change", &ChangeMaintenanceRequest{Change: 42}, "Choose a change"},
		{"no player for the team", &ChangeMaintenanceRequest{Change: add}, "name of a player"},
		{"a selector for the team", &ChangeMaintenanceRequest{Change: add, Player: "@a"}, "name of a player"},
		{"a player turning on", &ChangeMaintenanceRequest{Change: on, Player: "Steve"}, "name of a player"},
		{"a server for the team", &ChangeMaintenanceRequest{Change: add, Player: "Steve", Server: "lobby"}, "server of the network"},
		{"an invalid server", &ChangeMaintenanceRequest{Change: on, Server: "Lobby"}, "server of the network"},
		{"the server try", &ChangeMaintenanceRequest{Change: on, Server: "try"}, "server of the network"},
		{"a server named global", &ChangeMaintenanceRequest{Change: on, Server: "global"}, "whole network"},
		{"a timer without minutes", &ChangeMaintenanceRequest{Change: start}, "from 1 minute to 28 days"},
		{"a timer of more than 28 days", &ChangeMaintenanceRequest{Change: end, Minutes: MaxMaintenanceMinutes + 1}, "from 1 minute to 28 days"},
		{"minutes without a timer", &ChangeMaintenanceRequest{Change: on, Minutes: 5}, "from 1 minute to 28 days"},
		{"a schedule without a duration", &ChangeMaintenanceRequest{Change: schedule, Minutes: 5}, "from 1 minute to 28 days"},
		{"a duration of a timer", &ChangeMaintenanceRequest{Change: start, Minutes: 5, DurationMinutes: 5}, "from 1 minute to 28 days"},
		{"a duration of more than 28 days", &ChangeMaintenanceRequest{Change: schedule, Minutes: 5, DurationMinutes: MaxMaintenanceMinutes + 1}, "from 1 minute to 28 days"},
	} {
		checkProblem(t, tc.name, tc.req.Problem(), tc.want)
	}
}
