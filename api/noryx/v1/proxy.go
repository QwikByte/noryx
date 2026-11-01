package noryxv1

import (
	"fmt"
	"regexp"
)

// ConfigFile returns the configuration file in the data of a proxy: velocity.toml of
// Velocity, config.yml of BungeeCord.
func (t ServerType) ConfigFile() string {
	if t.Bungee() {
		return "config.yml"
	}
	return "velocity.toml"
}

// MaintenanceFolder returns the folder in the data of a proxy in which the Maintenance
// plugin by kennytv keeps its files.
func (t ServerType) MaintenanceFolder() string {
	if t.Bungee() {
		return "plugins/Maintenance"
	}
	return "plugins/maintenance"
}

// MaxMaintenanceMinutes is the longest timer of the Maintenance plugin: 28 days.
const MaxMaintenanceMinutes = 28 * 24 * 60

// backendName matches the names of the game servers of a network, which players use with
// /server and which console commands of the proxy take as they are.
var backendName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)

// ValidBackendName reports whether name can name a game server of a network: up to 32
// lower-case letters, digits, - and _, but not try, which Velocity reserves.
func ValidBackendName(name string) bool { return backendName.MatchString(name) && name != "try" }

// Timer reports whether a change starts a timer of the Maintenance plugin.
func (c MaintenanceChange) Timer() bool {
	return c == MaintenanceChange_MAINTENANCE_CHANGE_START_TIMER || c == MaintenanceChange_MAINTENANCE_CHANGE_END_TIMER ||
		c == MaintenanceChange_MAINTENANCE_CHANGE_SCHEDULE
}

// Problem returns a message for the operator if a change of maintenance is invalid. Only
// changes of the team take a player, and they take no server of the network; the server
// can't be global, which version 5 of the plugin reads as the whole network. Timers take
// minutes, schedules also a duration, and other changes neither.
func (r *ChangeMaintenanceRequest) Problem() string {
	c := r.GetChange()
	team := c == MaintenanceChange_MAINTENANCE_CHANGE_ADD || c == MaintenanceChange_MAINTENANCE_CHANGE_REMOVE
	// takes reports whether minutes are valid for a change that takes them, or not.
	takes := func(takes bool, minutes uint32) bool {
		return takes == (minutes != 0) && minutes <= MaxMaintenanceMinutes
	}
	switch server := r.GetServer(); {
	case c == MaintenanceChange_MAINTENANCE_CHANGE_UNSPECIFIED || MaintenanceChange_name[int32(c)] == "":
		return "Choose a change."
	case team && !ValidPlayerName(r.GetPlayer()), !team && r.GetPlayer() != "":
		return "Enter the name of a player: up to 16 letters, digits and underscores."
	case server != "" && (team || !ValidBackendName(server)):
		return "Choose a server of the network."
	case server == "global":
		return "The Maintenance plugin reads global as the whole network. Rename the server in the network to change its maintenance alone."
	case !takes(c.Timer(), r.GetMinutes()), !takes(c == MaintenanceChange_MAINTENANCE_CHANGE_SCHEDULE, r.GetDurationMinutes()):
		return fmt.Sprintf("Choose times from 1 minute to %d days.", MaxMaintenanceMinutes/(24*60))
	}
	return ""
}
