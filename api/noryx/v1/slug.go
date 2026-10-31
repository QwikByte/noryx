package noryxv1

import "strings"

// Slug returns the short lower-case name, e.g. "paper" for SERVER_TYPE_PAPER.
func (t ServerType) Slug() string { return slug(t.String(), "SERVER_TYPE_") }

// Proxy reports whether the type is a proxy, which connects servers to a network.
func (t ServerType) Proxy() bool { return t == ServerType_SERVER_TYPE_VELOCITY || t.Bungee() }

// Bungee reports whether the type is BungeeCord or a fork of it with the same configuration.
func (t ServerType) Bungee() bool {
	return t == ServerType_SERVER_TYPE_BUNGEECORD || t == ServerType_SERVER_TYPE_WATERFALL
}

// Paper reports whether the type is Paper or a fork of it with the same configuration.
func (t ServerType) Paper() bool {
	switch t {
	case ServerType_SERVER_TYPE_PAPER, ServerType_SERVER_TYPE_PURPUR, ServerType_SERVER_TYPE_FOLIA, ServerType_SERVER_TYPE_LEAF:
		return true
	}
	return false
}

// Fabric reports whether the type loads Fabric mods: Fabric itself, or Quilt.
func (t ServerType) Fabric() bool {
	return t == ServerType_SERVER_TYPE_FABRIC || t == ServerType_SERVER_TYPE_QUILT
}

// Modded reports whether the type loads mods, with a mod loader, rather than plugins.
func (t ServerType) Modded() bool {
	return t.Fabric() || t == ServerType_SERVER_TYPE_FORGE || t == ServerType_SERVER_TYPE_NEOFORGE
}

// Bedrock reports whether the type is Bedrock Dedicated Server, which runs without Java.
func (t ServerType) Bedrock() bool { return t == ServerType_SERVER_TYPE_BEDROCK }

// Addons reports whether the type loads plugins or mods, which the master installs from
// Modrinth and Hangar: Vanilla and Bedrock servers load neither.
func (t ServerType) Addons() bool { return t.Proxy() || t.Paper() || t.Modded() }

// Backend reports whether a game server of the type can be part of a network, as it verifies
// the players its proxy forwards: Vanilla servers can't, and Bedrock players reach a network
// through Geyser on its proxy instead.
func (t ServerType) Backend() bool { return t.Paper() || t.Modded() }

// Slug returns the short lower-case name, e.g. "running" for SERVER_STATE_RUNNING.
func (s ServerState) Slug() string { return slug(s.String(), "SERVER_STATE_") }

// Slug returns the short lower-case name, e.g. "on_crash" for RESTART_POLICY_ON_CRASH.
// The unspecified policy behaves like RESTART_POLICY_ALWAYS and is named alike.
func (p RestartPolicy) Slug() string {
	if p == RestartPolicy_RESTART_POLICY_UNSPECIFIED {
		p = RestartPolicy_RESTART_POLICY_ALWAYS
	}
	return slug(p.String(), "RESTART_POLICY_")
}

// Slug returns the short lower-case name, e.g. "in_place" for OVERLAY_FIREWALL_IN_PLACE, or
// "" if older agents didn't tell it.
func (f OverlayFirewall) Slug() string {
	if f == OverlayFirewall_OVERLAY_FIREWALL_UNSPECIFIED {
		return ""
	}
	return slug(f.String(), "OVERLAY_FIREWALL_")
}

// ParseRestartPolicy returns the policy with the given slug, or RESTART_POLICY_UNSPECIFIED.
func ParseRestartPolicy(slug string) RestartPolicy {
	return RestartPolicy(RestartPolicy_value["RESTART_POLICY_"+strings.ToUpper(slug)])
}

// ParseServerType returns the type with the given slug, or SERVER_TYPE_UNSPECIFIED.
func ParseServerType(slug string) ServerType {
	return ServerType(ServerType_value["SERVER_TYPE_"+strings.ToUpper(slug)])
}

func slug(name, prefix string) string { return strings.ToLower(strings.TrimPrefix(name, prefix)) }
