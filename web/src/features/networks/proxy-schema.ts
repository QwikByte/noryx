import { t } from "i18next"
import type { Definition as PropertyDefinition, Kind } from "@/features/properties/schema"
import { msg } from "@/lib/i18n"

export const groups = [
  msg("Server list"),
  msg("Players and security"),
  msg("Connections"),
  msg("Commands and permissions"),
  msg("Query"),
  msg("Advanced"),
] as const
type Group = (typeof groups)[number]

export interface Definition extends Omit<PropertyDefinition, "group"> {
  group: Group
  /** The proxy only applies it when it starts again, not when it reloads its configuration. */
  restart?: boolean
}

const bool = { type: "boolean" } as const
const num = (min?: number, max?: number) => ({ type: "number", min, max }) as const
const text = { type: "text" } as const
const list = { type: "list" } as const
const def = (label: string, description: string, group: Group, kind: Kind, restart?: boolean): Definition => ({
  label,
  description,
  group,
  kind,
  restart,
})

/** Known settings of velocity.toml (Velocity 3 and 4), by their path. */
const velocity: Record<string, Definition> = {
  motd: def(msg("MOTD"), msg("Shown in the server list, in MiniMessage, e.g. <green>Welcome</green>."), "Server list", text),
  "show-max-players": def(msg("Shown maximum of players"), msg("Velocity doesn't limit the players; this is only shown."), "Server list", num(0)),
  "sample-players-in-ping": def(msg("Show players in the server list"), msg("Lists some online players when hovering over the player count."), "Server list", bool),
  "announce-forge": def(msg("Announce Forge"), msg("Tells clients that the network runs mods, for modded networks."), "Server list", bool),
  "ping-passthrough": def(
    msg("Ping passthrough"),
    msg("Takes the server list entry from the first server players would join."),
    "Server list",
    {
      type: "select",
      options: [
        ["DISABLED", msg("Off")],
        ["MODS", msg("Mods")],
        ["DESCRIPTION", msg("Description and mods")],
        ["ALL", msg("Everything")],
      ],
    },
  ),
  "ping-passthrough.version": def(msg("Pass through the version"), msg("Shows the version of the first server players would join."), "Server list", bool),
  "ping-passthrough.players": def(msg("Pass through the players"), msg("Shows the player count of the first server players would join."), "Server list", bool),
  "ping-passthrough.description": def(msg("Pass through the description"), msg("Shows the MOTD of the first server players would join."), "Server list", bool),
  "ping-passthrough.favicon": def(msg("Pass through the icon"), msg("Shows the icon of the first server players would join."), "Server list", bool),
  "ping-passthrough.modinfo": def(msg("Pass through the mods"), msg("Shows the mods of the first server players would join."), "Server list", bool),
  "online-mode": def(msg("Online mode"), msg("Checks with Mojang that players own Minecraft. When off, anyone can join under any name."), "Players and security", bool),
  "force-key-authentication": def(msg("Require signed chat keys"), msg("Requires the keys with which clients sign chat messages."), "Players and security", bool),
  "prevent-client-proxy-connections": def(msg("Block client proxies"), msg("Kicks players whose address differs from the one Mojang saw, e.g. some VPNs."), "Players and security", bool),
  "kick-existing-players": def(msg("Kick players who join twice"), msg("Kicks a player who is online when the same account joins again."), "Players and security", bool),
  "enable-player-address-logging": def(msg("Log player addresses"), msg("When off, the log shows <ip address withheld> instead."), "Players and security", bool),
  "advanced.accepts-transfers": def(msg("Accept transfers"), msg("Accepts players that other servers send over."), "Players and security", bool),
  "advanced.compression-threshold": def(msg("Compression threshold"), msg("Packets from this size in bytes on are compressed; -1 turns compression off."), "Connections", num(-1)),
  "advanced.compression-level": def(msg("Compression level"), msg("From 1 (fast) to 9 (small); -1 uses the default."), "Connections", num(-1, 9)),
  "advanced.login-ratelimit": def(msg("Login rate limit"), msg("Milliseconds an address has to wait between logins; 0 turns it off."), "Connections", num(0)),
  "advanced.connection-timeout": def(msg("Connection timeout"), msg("Milliseconds to wait for a server to accept a connection."), "Connections", num(1)),
  "advanced.read-timeout": def(msg("Read timeout"), msg("Milliseconds without data after which a connection is closed."), "Connections", num(1)),
  "advanced.haproxy-protocol": def(msg("HAProxy protocol"), msg("Expects the PROXY protocol, e.g. behind TCPShield. Players can't connect without such a proxy."), "Connections", bool, true),
  "advanced.tcp-fast-open": def(msg("TCP Fast Open"), msg("Speeds up connections on Linux."), "Connections", bool, true),
  "advanced.enable-reuse-port": def(msg("Reuse port"), msg("Spreads connections over several threads on Linux."), "Connections", bool, true),
  "advanced.failover-on-unexpected-server-disconnect": def(msg("Fall back when a server goes away"), msg("Sends players to the next server instead of disconnecting them when theirs crashes."), "Connections", bool),
  "advanced.announce-proxy-commands": def(msg("Announce proxy commands"), msg("Suggests the proxy's commands to clients."), "Commands and permissions", bool),
  "advanced.log-command-executions": def(msg("Log commands"), msg("Writes the commands players run on the proxy into the log."), "Commands and permissions", bool),
  "advanced.command-rate-limit": def(msg("Command rate limit"), msg("Milliseconds between commands of a player; 0 turns it off."), "Commands and permissions", num(0)),
  "advanced.forward-commands-if-rate-limited": def(msg("Pass on limited commands"), msg("Sends commands over the limit to the server instead of dropping them."), "Commands and permissions", bool),
  "advanced.kick-after-rate-limited-commands": def(msg("Kick after limited commands"), msg("Kicks a player after this many commands over the limit; 0 never."), "Commands and permissions", num(0)),
  "advanced.tab-complete-rate-limit": def(msg("Tab completion rate limit"), msg("Milliseconds between tab completions of a player; 0 turns it off."), "Commands and permissions", num(0)),
  "advanced.kick-after-rate-limited-tab-completes": def(msg("Kick after limited tab completions"), msg("Kicks a player after this many tab completions over the limit; 0 never."), "Commands and permissions", num(0)),
  "query.enabled": def(msg("Query"), msg("Answers GameSpy4 queries for information about the network."), "Query", bool, true),
  "query.port": def(msg("Query port"), msg("UDP port for queries inside the container."), "Query", num(1, 65535), true),
  "query.map": def(msg("Map name"), msg("The map name queries report."), "Query", text),
  "query.show-plugins": def(msg("Show plugins"), msg("Lists the proxy's plugins in queries."), "Query", bool),
  "advanced.log-player-connections": def(msg("Log connections"), msg("Writes players joining and leaving into the log."), "Advanced", bool),
  "advanced.show-ping-requests": def(msg("Log pings"), msg("Writes every server list ping into the log."), "Advanced", bool),
  "advanced.bungee-plugin-message-channel": def(msg("BungeeCord plugin channel"), msg("Lets plugins on the servers use BungeeCord's messaging channel."), "Advanced", bool),
  "packet-limiter.interval": def(msg("Packet limiter interval"), msg("Seconds over which the packets of a player are counted."), "Advanced", num(1)),
  "packet-limiter.packets-per-second": def(msg("Packets per second"), msg("Most packets a player may send per second; -1 has no limit."), "Advanced", num(-1)),
  "packet-limiter.bytes-per-second": def(msg("Bytes per second"), msg("Most bytes a player may send per second; -1 has no limit."), "Advanced", num(-1)),
}

/** Known settings of config.yml of BungeeCord and Waterfall, by their path. */
const bungee: Record<string, Definition> = {
  "listeners.0.motd": def(msg("MOTD"), msg("Shown in the server list; & starts colour codes, e.g. &aGreen."), "Server list", text),
  "listeners.0.max_players": def(msg("Shown maximum of players"), msg("Shown in the server list; Player limit below limits them."), "Server list", num(0)),
  "listeners.0.ping_passthrough": def(msg("Ping passthrough"), msg("Takes the server list entry from the first server players would join."), "Server list", bool),
  "listeners.0.tab_list": def(msg("Tab list"), msg("Which players the tab list shows."), "Server list", {
    type: "select",
    options: [
      ["GLOBAL_PING", msg("All players with their ping")],
      ["GLOBAL", msg("All players")],
      ["SERVER", msg("Players of the same server")],
    ],
  }),
  "listeners.0.tab_size": def(msg("Tab list size"), msg("Most entries of the tab list."), "Server list", num(1)),
  remote_ping_cache: def(msg("Ping cache"), msg("Milliseconds the answers of servers to pings are kept; -1 doesn't keep them."), "Server list", num(-1)),
  log_pings: def(msg("Log pings"), msg("Writes every server list ping into the log."), "Server list", bool),
  online_mode: def(msg("Online mode"), msg("Checks with Mojang that players own Minecraft. When off, anyone can join under any name."), "Players and security", bool),
  enforce_secure_profile: def(msg("Require signed chat keys"), msg("Requires the keys with which clients sign chat messages."), "Players and security", bool),
  prevent_proxy_connections: def(msg("Block client proxies"), msg("Kicks players whose address differs from the one Mojang saw, e.g. some VPNs."), "Players and security", bool),
  player_limit: def(msg("Player limit"), msg("Most players online at once; -1 has no limit."), "Players and security", num(-1)),
  "listeners.0.force_default_server": def(msg("Always join the first server"), msg("When off, players return to the server they were last on."), "Players and security", bool),
  reject_transfers: def(msg("Reject transfers"), msg("Turns away players that other servers send over."), "Players and security", bool),
  network_compression_threshold: def(msg("Compression threshold"), msg("Packets from this size in bytes on are compressed; -1 turns compression off."), "Connections", num(-1)),
  connection_throttle: def(msg("Connection throttle"), msg("Milliseconds in which an address may connect only a few times; -1 turns it off."), "Connections", num(-1)),
  connection_throttle_limit: def(msg("Connections within the throttle"), msg("How often an address may connect within the throttle."), "Connections", num(1)),
  server_connect_timeout: def(msg("Connection timeout"), msg("Milliseconds to wait for a server to accept a connection."), "Connections", num(1)),
  remote_ping_timeout: def(msg("Ping timeout"), msg("Milliseconds to wait for a server to answer a ping."), "Connections", num(1)),
  timeout: def(msg("Read timeout"), msg("Milliseconds without data after which a connection is closed."), "Connections", num(1)),
  "listeners.0.proxy_protocol": def(msg("HAProxy protocol"), msg("Expects the PROXY protocol, e.g. behind TCPShield. Players can't connect without such a proxy."), "Connections", bool),
  max_packets_per_second: def(msg("Packets per second"), msg("Most packets a player may send per second."), "Connections", num(1)),
  max_packets_data_per_second: def(msg("Bytes per second"), msg("Most bytes a player may send per second."), "Connections", num(1)),
  disabled_commands: def(msg("Disabled commands"), msg("Commands of the proxy that nobody can use, one per line."), "Commands and permissions", list),
  log_commands: def(msg("Log commands"), msg("Writes the commands players run on the proxy into the log."), "Commands and permissions", bool),
  "listeners.0.query_enabled": def(msg("Query"), msg("Answers GameSpy4 queries for information about the network."), "Query", bool),
  "listeners.0.query_port": def(msg("Query port"), msg("UDP port for queries inside the container."), "Query", num(1, 65535)),
  forge_support: def(msg("Forge support"), msg("Lets Forge clients join modded servers."), "Advanced", bool),
  "listeners.0.bind_local_address": def(msg("Bind to the local address"), msg("Connects to servers from the address players connected to."), "Advanced", bool),
}

/** The definition of a setting of a proxy; unknown ones go under Advanced with a control that fits their value. */
export function definition(bungeeFamily: boolean, key: string, value: unknown): Definition {
  const known = (bungeeFamily ? bungee : velocity)[key]
  if (known) return known
  // BungeeCord's permissions of groups and the groups of players are named by the operator.
  const [area, name] = key.split(".")
  if (bungeeFamily && area === "permissions" && name)
    return def(t("Permissions of {{group}}", { group: name }), msg("One permission per line."), "Commands and permissions", list)
  if (bungeeFamily && area === "groups" && name)
    return def(t("Groups of {{player}}", { player: name }), msg("One group per line."), "Commands and permissions", list)
  const kind: Kind = typeof value === "boolean" ? bool : typeof value === "number" ? num() : Array.isArray(value) ? list : { type: "text", mono: true }
  return def(key, "", "Advanced", kind)
}

/** The order of the settings: known ones as listed, then the others by name. */
export function order(bungeeFamily: boolean) {
  const keys = Object.keys(bungeeFamily ? bungee : velocity)
  return (a: string, b: string) => (keys.indexOf(a) + 1 || keys.length + 1) - (keys.indexOf(b) + 1 || keys.length + 1) || a.localeCompare(b)
}
