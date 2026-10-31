package noryxv1

import (
	"cmp"
	"fmt"
	"regexp"
)

// A Bedrock server lets up to MaxBedrockPlayers players join at once, DefaultBedrockPlayers
// unless its settings say otherwise, like Bedrock Dedicated Server itself. Players join at the
// server's TCP port, then each plays over a UDP port of their own, which the node publishes:
// the server's port and the ones after it.
const (
	DefaultBedrockPlayers = 10
	MaxBedrockPlayers     = 100
	// BedrockContainerPort is the TCP port inside its container at which a Bedrock server lets
	// players join.
	BedrockContainerPort = 19132
)

// BedrockPlayers is how many players a Bedrock server with max_players lets join at once; 0
// means DefaultBedrockPlayers.
func BedrockPlayers(maxPlayers uint32) uint32 { return cmp.Or(maxPlayers, DefaultBedrockPlayers) }

// LastPort returns the last port of the node that a server of the type with port and
// max_players uses itself: the UDP port of the last player of a Bedrock server, otherwise port.
func LastPort(t ServerType, port, maxPlayers uint32) uint32 {
	if !t.Bedrock() {
		return port
	}
	return port + BedrockPlayers(maxPlayers) - 1
}

// bedrockVersion matches versions of Bedrock Dedicated Server, e.g. 1.26.61.1.
var bedrockVersion = regexp.MustCompile(`^[0-9]{1,4}(\.[0-9]{1,4}){3}$`)

// ValidBedrockVersion reports whether version is LATEST, PREVIEW, or a version of Bedrock
// Dedicated Server with its four numbers, which the image downloads from Mojang. It ends up in a
// variable of the image.
func ValidBedrockVersion(version string) bool {
	return version == "LATEST" || version == "PREVIEW" || bedrockVersion.MatchString(version)
}

// BedrockSettings are the settings of a server that differ between Bedrock servers and others.
type BedrockSettings struct {
	Type          ServerType
	Port          uint32
	MaxPlayers    uint32
	Java          string
	AikarFlags    bool
	JVMOptions    int
	InitMemoryMB  uint32
	LoaderVersion string
	ExtraPorts    int
}

// Problem returns a message for the operator if the settings don't fit the type: a Bedrock
// server runs without Java and plugins, so it takes none of their settings, and the ports of
// its players end at 65535; only Bedrock servers take max_players.
func (s BedrockSettings) Problem() string {
	switch {
	case !s.Type.Bedrock() && s.MaxPlayers != 0:
		return "Only Bedrock servers take the number of players in their settings. Others take it from server.properties."
	case !s.Type.Bedrock():
	case s.Java != "" || s.AikarFlags || s.JVMOptions > 0 || s.InitMemoryMB != 0 || s.LoaderVersion != "":
		return "Bedrock servers don't run Java: they have no Java version, Aikar's flags, JVM options, initial heap or mod loader."
	case s.ExtraPorts > 0:
		return "Bedrock servers load no plugins that listen at ports of their own, so they publish no extra ports."
	case s.MaxPlayers > MaxBedrockPlayers:
		return fmt.Sprintf("Let 1 to %d players join a Bedrock server at once.", MaxBedrockPlayers)
	case uint64(s.Port)+uint64(BedrockPlayers(s.MaxPlayers)) > 65536:
		return "A Bedrock server needs a port for each player after its own, up to 65535. Choose a lower port or fewer players."
	}
	return ""
}

const bedrockPortProperty = "Inside its container, the server always uses this port. Change the port in the server's settings."

// BedrockManagedProperties are the properties of the server.properties of a Bedrock server that
// its image sets on every start from the server's settings, with the reason, like
// ManagedProperties of other game servers.
var BedrockManagedProperties = map[string]string{
	"server-port":      bedrockPortProperty,
	"server-portv6":    bedrockPortProperty,
	"server-ip":        "The server listens on all addresses of its container.",
	"server-udp-ports": "Players play over the ports after the server's port, one for each of them. Change the port and the players in the server's settings.",
	"max-players":      "Each player plays over a port of their own, which the node publishes. Change the players in the server's settings.",
	"transport":        "Players join over NetherNet, at the server's port.",
}

// ManagedPropertiesOf returns the properties of server.properties that Noryx sets on a game
// server of the type itself, with the reason.
func ManagedPropertiesOf(t ServerType) map[string]string {
	if t.Bedrock() {
		return BedrockManagedProperties
	}
	return ManagedProperties
}

// BedrockProblem returns a message for the operator if a valid change can't be made on a
// Bedrock server: it has no bans, and knows players by their gamertag, without the dot that
// Floodgate gives Bedrock players on Java servers.
func (c *PlayerChange) BedrockProblem() string {
	switch a := c.GetAction(); {
	case a == PlayerAction_PLAYER_ACTION_BAN || a == PlayerAction_PLAYER_ACTION_PARDON:
		return "Bedrock servers have no bans. Take the player off the allowlist instead."
	case c.GetUuid() != "" || BedrockPlayer(c.GetName()):
		return "Bedrock servers know players by their gamertag, without Floodgate's dot."
	}
	return ""
}
