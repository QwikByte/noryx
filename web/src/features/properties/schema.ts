import { t } from "i18next"
import { isBedrock } from "@/features/servers/server-types"
import { msg } from "@/lib/i18n"

export type Kind =
  | { type: "boolean" }
  | { type: "number"; min?: number; max?: number }
  | { type: "select"; options: [value: string, label: string][] }
  | { type: "text"; mono?: boolean }
  | { type: "motd" }
  /** A list of texts, one per line. */
  | { type: "list" }

export const groups = [
  msg("Gameplay"),
  msg("World"),
  msg("Players"),
  msg("Server list"),
  msg("Resource pack"),
  msg("Performance"),
  msg("Advanced"),
] as const
type Group = (typeof groups)[number]

export interface Definition {
  /** In English, marked with msg, like the description and the labels of options. */
  label: string
  description: string
  group: Group
  kind: Kind
}

const bool = { type: "boolean" } as const
const num = (min?: number, max?: number) => ({ type: "number", min, max }) as const
const permissionLevels: [string, string][] = [
  ["1", msg("1 – bypass spawn protection")],
  ["2", msg("2 – cheats and command blocks")],
  ["3", msg("3 – manage players")],
  ["4", msg("4 – all commands")],
]

/**
 * Known properties of server.properties. Only those in the file are shown, because
 * Minecraft versions differ in which ones they have; others appear under Advanced.
 */
export const definitions: Record<string, Definition> = {
  gamemode: {
    label: msg("Game mode"),
    description: msg("Game mode of players who join for the first time."),
    group: "Gameplay",
    kind: {
      type: "select",
      options: [
        ["survival", msg("Survival")],
        ["creative", msg("Creative")],
        ["adventure", msg("Adventure")],
        ["spectator", msg("Spectator")],
      ],
    },
  },
  difficulty: {
    label: msg("Difficulty"),
    description: msg("How dangerous the world is."),
    group: "Gameplay",
    kind: {
      type: "select",
      options: [
        ["peaceful", msg("Peaceful")],
        ["easy", msg("Easy")],
        ["normal", msg("Normal")],
        ["hard", msg("Hard")],
      ],
    },
  },
  "force-gamemode": {
    label: msg("Force game mode"),
    description: msg("Puts players into the game mode above every time they join."),
    group: "Gameplay",
    kind: bool,
  },
  hardcore: {
    label: msg("Hardcore"),
    description: msg("Players become spectators when they die, and the difficulty stays on hard."),
    group: "Gameplay",
    kind: bool,
  },
  pvp: { label: msg("PvP"), description: msg("Players can hurt each other."), group: "Gameplay", kind: bool },
  "allow-flight": {
    label: msg("Allow flight"),
    description: msg("Lets players fly in survival, e.g. with plugins or mods. Otherwise flying players are kicked."),
    group: "Gameplay",
    kind: bool,
  },
  "allow-nether": { label: msg("Nether"), description: msg("Players can travel to the Nether."), group: "Gameplay", kind: bool },
  "enable-command-block": {
    label: msg("Command blocks"),
    description: msg("Command blocks run their commands."),
    group: "Gameplay",
    kind: bool,
  },
  "spawn-protection": {
    label: msg("Spawn protection"),
    description: msg("Radius in blocks around the world spawn that only operators can change. 0 turns it off."),
    group: "Gameplay",
    kind: num(0),
  },
  "spawn-monsters": { label: msg("Monsters"), description: msg("Monsters spawn."), group: "Gameplay", kind: bool },
  "spawn-animals": { label: msg("Animals"), description: msg("Animals spawn."), group: "Gameplay", kind: bool },
  "spawn-npcs": { label: msg("Villagers"), description: msg("Villagers spawn."), group: "Gameplay", kind: bool },

  "level-name": {
    label: msg("World folder"),
    description: msg("Folder of the world. A new name creates a new world on the next start."),
    group: "World",
    kind: { type: "text", mono: true },
  },
  "level-seed": {
    label: msg("Seed"),
    description: msg("Seed of a newly generated world. Empty picks a random one."),
    group: "World",
    kind: { type: "text", mono: true },
  },
  "level-type": {
    label: msg("World type"),
    description: msg("Type of a newly generated world."),
    group: "World",
    kind: {
      type: "select",
      options: [
        ["minecraft:normal", msg("Default")],
        ["minecraft:flat", msg("Superflat")],
        ["minecraft:large_biomes", msg("Large biomes")],
        ["minecraft:amplified", msg("Amplified")],
        ["minecraft:single_biome_surface", msg("Single biome")],
      ],
    },
  },
  "generate-structures": {
    label: msg("Structures"),
    description: msg("New chunks get villages, temples and other structures."),
    group: "World",
    kind: bool,
  },
  "generator-settings": {
    label: msg("Generator settings"),
    description: msg("Settings of superflat worlds as JSON."),
    group: "World",
    kind: { type: "text", mono: true },
  },
  "initial-enabled-packs": {
    label: msg("Enabled data packs"),
    description: msg("Data packs turned on in a newly generated world, separated by commas."),
    group: "World",
    kind: { type: "text", mono: true },
  },
  "initial-disabled-packs": {
    label: msg("Disabled data packs"),
    description: msg("Data packs turned off in a newly generated world, separated by commas."),
    group: "World",
    kind: { type: "text", mono: true },
  },
  "max-world-size": {
    label: msg("World size"),
    description: msg("Radius of the world border in blocks."),
    group: "World",
    kind: num(1, 29999984),
  },
  "view-distance": {
    label: msg("View distance"),
    description: msg("Chunks around players that are sent to them."),
    group: "World",
    kind: num(3, 32),
  },
  "simulation-distance": {
    label: msg("Simulation distance"),
    description: msg("Chunks around players that are updated, so that crops grow and mobs move."),
    group: "World",
    kind: num(3, 32),
  },

  "max-players": {
    label: msg("Player slots"),
    description: msg("Players who can be online at the same time."),
    group: "Players",
    kind: num(0),
  },
  "white-list": {
    label: msg("Whitelist"),
    description: msg("Only players on the whitelist can join. Add them in the console with whitelist add <name>."),
    group: "Players",
    kind: bool,
  },
  "enforce-whitelist": {
    label: msg("Enforce whitelist"),
    description: msg("Kicks online players who aren't on the whitelist when it is reloaded."),
    group: "Players",
    kind: bool,
  },
  "online-mode": {
    label: msg("Online mode"),
    description: msg("Checks with Mojang that players own their account. Turned off, anyone can join with any name."),
    group: "Players",
    kind: bool,
  },
  "op-permission-level": {
    label: msg("Operator permissions"),
    description: msg("What operators are allowed to do."),
    group: "Players",
    kind: { type: "select", options: permissionLevels },
  },
  "function-permission-level": {
    label: msg("Function permissions"),
    description: msg("Permission level of functions in data packs."),
    group: "Players",
    kind: { type: "select", options: permissionLevels },
  },
  "player-idle-timeout": {
    label: msg("Idle timeout"),
    description: msg("Kicks players who are idle for this many minutes. 0 turns it off."),
    group: "Players",
    kind: num(0),
  },
  "enforce-secure-profile": {
    label: msg("Signed chat"),
    description: msg("Players need a profile key signed by Mojang to chat."),
    group: "Players",
    kind: bool,
  },
  "prevent-proxy-connections": {
    label: msg("Block VPNs"),
    description: msg("Kicks players whose connection doesn't come from the network they signed in from."),
    group: "Players",
    kind: bool,
  },
  "enable-code-of-conduct": {
    label: msg("Code of conduct"),
    description: msg("Shows players the code of conduct from the codeofconduct folder when they join."),
    group: "Players",
    kind: bool,
  },
  "log-ips": {
    label: msg("Log IP addresses"),
    description: msg("Writes the IP addresses of players to the log."),
    group: "Players",
    kind: bool,
  },

  motd: {
    label: msg("Message of the day"),
    description: msg("Shown below the server name in the server list, in up to two lines."),
    group: "Server list",
    kind: { type: "motd" },
  },
  "enable-status": {
    label: msg("Show as online"),
    description: msg("The server answers status requests of the server list."),
    group: "Server list",
    kind: bool,
  },
  "hide-online-players": {
    label: msg("Hide player names"),
    description: msg("The server list doesn't show who is online."),
    group: "Server list",
    kind: bool,
  },

  "resource-pack": {
    label: msg("URL"),
    description: msg("Resource pack offered to players when they join."),
    group: "Resource pack",
    kind: { type: "text", mono: true },
  },
  "resource-pack-sha1": {
    label: msg("SHA-1"),
    description: msg("Hash of the pack, so that players notice when it changed."),
    group: "Resource pack",
    kind: { type: "text", mono: true },
  },
  "resource-pack-id": {
    label: msg("ID"),
    description: msg("UUID of the pack."),
    group: "Resource pack",
    kind: { type: "text", mono: true },
  },
  "require-resource-pack": {
    label: msg("Required"),
    description: msg("Players who decline the pack are disconnected."),
    group: "Resource pack",
    kind: bool,
  },
  "resource-pack-prompt": {
    label: msg("Prompt"),
    description: msg("Message shown when the pack is offered."),
    group: "Resource pack",
    kind: { type: "text" },
  },

  "network-compression-threshold": {
    label: msg("Compression threshold"),
    description: msg("Packets bigger than this many bytes are compressed. -1 turns compression off."),
    group: "Performance",
    kind: num(-1),
  },
  "max-tick-time": {
    label: msg("Watchdog"),
    description: msg("Stops the server when a tick takes longer than this many milliseconds. -1 turns it off."),
    group: "Performance",
    kind: num(-1),
  },
  "sync-chunk-writes": {
    label: msg("Synchronous chunk writes"),
    description: msg("Safer against data loss, but slower."),
    group: "Performance",
    kind: bool,
  },
  "entity-broadcast-range-percentage": {
    label: msg("Entity range"),
    description: msg("How far away players see entities, in percent of the default."),
    group: "Performance",
    kind: num(10, 1000),
  },
  "rate-limit": {
    label: msg("Packet limit"),
    description: msg("Kicks players who send more packets per second. 0 turns it off."),
    group: "Performance",
    kind: num(0),
  },
  "pause-when-empty-seconds": {
    label: msg("Pause when empty"),
    description: msg("Pauses the server after it was empty for this many seconds. 0 or less never pauses."),
    group: "Performance",
    kind: num(),
  },
  "region-file-compression": {
    label: msg("Region compression"),
    description: msg("How the world's region files are compressed."),
    group: "Performance",
    kind: {
      type: "select",
      options: [
        ["deflate", msg("Deflate")],
        ["lz4", msg("LZ4 (faster, larger)")],
        ["none", msg("None")],
      ],
    },
  },
  "use-native-transport": {
    label: msg("Native transport"),
    description: msg("Uses the faster network transport of Linux."),
    group: "Performance",
    kind: bool,
  },

  "broadcast-rcon-to-ops": {
    label: msg("Show console commands to operators"),
    description: msg("Operators see the output of commands from the panel's console, which uses RCON."),
    group: "Advanced",
    kind: bool,
  },
  "broadcast-console-to-ops": {
    label: msg("Show server console to operators"),
    description: msg("Operators see the output of commands typed in the server console."),
    group: "Advanced",
    kind: bool,
  },
  "enable-query": {
    label: msg("Query"),
    description: msg("Answers GameSpy4 queries for server information."),
    group: "Advanced",
    kind: bool,
  },
  "query.port": {
    label: msg("Query port"),
    description: msg("Port for queries inside the container."),
    group: "Advanced",
    kind: num(1, 65535),
  },
  "management-server-enabled": {
    label: msg("Management API"),
    description: msg("Turns on Minecraft's management API (JSON-RPC over WebSocket) inside the container."),
    group: "Advanced",
    kind: bool,
  },
  "bug-report-link": {
    label: msg("Bug report link"),
    description: msg("Link players see to report problems with the server."),
    group: "Advanced",
    kind: { type: "text", mono: true },
  },
  debug: { label: msg("Debug logging"), description: msg("Writes more details to the log."), group: "Advanced", kind: bool },
  "accepts-transfers": {
    label: msg("Accept transfers"),
    description: msg("Accepts players sent over by other servers."),
    group: "Advanced",
    kind: bool,
  },
}

/** Known properties of the server.properties of Bedrock Dedicated Server, which has its own; others appear under Advanced. */
export const bedrockDefinitions: Record<string, Definition> = {
  gamemode: {
    ...definitions.gamemode,
    kind: {
      type: "select",
      options: [
        ["survival", msg("Survival")],
        ["creative", msg("Creative")],
        ["adventure", msg("Adventure")],
      ],
    },
  },
  difficulty: definitions.difficulty,
  "force-gamemode": definitions["force-gamemode"],
  "allow-cheats": {
    label: msg("Cheats"),
    description: msg("Commands such as /gamemode and /give work, for the players whose permissions allow them."),
    group: "Gameplay",
    kind: bool,
  },
  "level-name": definitions["level-name"],
  "level-seed": definitions["level-seed"],
  "level-type": {
    ...definitions["level-type"],
    kind: {
      type: "select",
      options: [
        ["DEFAULT", msg("Default")],
        ["FLAT", msg("Superflat")],
        ["LEGACY", msg("Old, limited world")],
      ],
    },
  },
  "view-distance": { ...definitions["view-distance"], kind: num(5) },
  "tick-distance": { ...definitions["simulation-distance"], kind: num(4, 12) },
  "max-players": definitions["max-players"],
  "allow-list": {
    label: msg("Allowlist"),
    description: msg("Only players on the allowlist can join. Add them on the Players tab, or in the console with allowlist add <name>."),
    group: "Players",
    kind: bool,
  },
  "online-mode": {
    label: msg("Online mode"),
    description: msg("Checks with Xbox Live that players own their account. Turned off, anyone can join with any name."),
    group: "Players",
    kind: bool,
  },
  "default-player-permission-level": {
    label: msg("Permissions of new players"),
    description: msg("What players may do when they join for the first time."),
    group: "Players",
    kind: {
      type: "select",
      options: [
        ["visitor", msg("Visitor – only looks around")],
        ["member", msg("Member – plays")],
        ["operator", msg("Operator – also runs commands")],
      ],
    },
  },
  "player-idle-timeout": definitions["player-idle-timeout"],
  "server-name": {
    label: msg("Server name"),
    description: msg("Shown to players in their list of servers."),
    group: "Server list",
    kind: { type: "text" },
  },
  "enable-lan-visibility": {
    label: msg("Visible in the local network"),
    description: msg("Players in the same network as the node find the server among their LAN games."),
    group: "Server list",
    kind: bool,
  },
  "texturepack-required": {
    label: msg("Required"),
    description: msg("Players must download the resource packs of the world to join."),
    group: "Resource pack",
    kind: bool,
  },
  "compression-threshold": {
    label: msg("Compression threshold"),
    description: msg("Packets bigger than this many bytes are compressed."),
    group: "Performance",
    kind: num(0, 65535),
  },
  "max-threads": {
    label: msg("Threads"),
    description: msg("Threads the server uses at most. 0 uses as many as it can."),
    group: "Performance",
    kind: num(0),
  },
}

/** The known properties of the server.properties of servers of a type. */
export const definitionsOf = (type: string) => (isBedrock(type) ? bedrockDefinitions : definitions)

/** The definition of a property among the known ones. Unknown ones go under Advanced, as a switch if they look boolean. */
export function definition(key: string, value?: string, known = definitions): Definition {
  const kind: Kind = value === "true" || value === "false" ? { type: "boolean" } : { type: "text", mono: true }
  return known[key] ?? { label: key, description: "", group: "Advanced", kind }
}

/** Returns an error message if the value doesn't fit the property among the known ones. */
export function check(key: string, value: string, known = definitions): string | undefined {
  return checkValue(definition(key, undefined, known).kind, value)
}

/** Returns an error message if the value doesn't fit a setting of the kind. */
export function checkValue(kind: Kind, value: string): string | undefined {
  if (kind.type !== "number") return undefined
  // Minecraft reads whole numbers only as digits, and others as the default, e.g. 1e3 or 10.0 of a number input.
  if (!/^-?\d+$/.test(value)) return t("Enter a whole number.")
  const n = Number(value)
  if (kind.min !== undefined && n < kind.min) return t("The minimum is {{min}}.", { min: kind.min })
  if (kind.max !== undefined && n > kind.max) return t("The maximum is {{max}}.", { max: kind.max })
}

/** Whether a search of the settings finds the server icon, which sits next to the MOTD. */
export const iconMatches = (term: string) => !term || ["server-icon.png", t("Server icon")].some((s) => s.toLowerCase().includes(term))
