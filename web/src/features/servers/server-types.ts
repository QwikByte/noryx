import { ArrowsSplitIcon, CubeIcon } from "@phosphor-icons/react"
import { t } from "i18next"
import type { Status } from "@/components/status"
import { formatMegabytes } from "@/lib/format"
import { msg } from "@/lib/i18n"
import type { ServerState } from "./api"

export interface ServerType {
  value: string
  label: string
  proxy: boolean
  /** What the server loads from Modrinth, and the loaders those are made for; vanilla servers load nothing. */
  addons?: { kind: "plugins" | "mods"; loaders: string[] }
  /** Why the software reached its end of life: new servers no longer get it, existing ones keep it. */
  endOfLife?: string
}

const bukkit = ["paper", "spigot", "bukkit"]

export const serverTypes: ServerType[] = [
  { value: "paper", label: "Paper", proxy: false, addons: { kind: "plugins", loaders: bukkit } },
  { value: "purpur", label: "Purpur", proxy: false, addons: { kind: "plugins", loaders: ["purpur", ...bukkit] } },
  // Folia only loads plugins made for it.
  { value: "folia", label: "Folia", proxy: false, addons: { kind: "plugins", loaders: ["folia"] } },
  { value: "leaf", label: "Leaf", proxy: false, addons: { kind: "plugins", loaders: bukkit } },
  { value: "vanilla", label: "Vanilla", proxy: false },
  { value: "fabric", label: "Fabric", proxy: false, addons: { kind: "mods", loaders: ["fabric"] } },
  { value: "quilt", label: "Quilt", proxy: false, addons: { kind: "mods", loaders: ["quilt", "fabric"] } },
  { value: "forge", label: "Forge", proxy: false, addons: { kind: "mods", loaders: ["forge"] } },
  { value: "neoforge", label: "NeoForge", proxy: false, addons: { kind: "mods", loaders: ["neoforge"] } },
  { value: "velocity", label: "Velocity", proxy: true, addons: { kind: "plugins", loaders: ["velocity"] } },
  { value: "bungeecord", label: "BungeeCord", proxy: true, addons: { kind: "plugins", loaders: ["bungeecord", "waterfall"] } },
  {
    value: "waterfall",
    label: "Waterfall",
    proxy: true,
    addons: { kind: "plugins", loaders: ["waterfall", "bungeecord"] },
    endOfLife: msg(
      "It can't download its command modules anymore. Without them, it has no /send, /server, /glist, /alert and /find, and Noryx can't move players to other servers. Proxies that downloaded them before keep them. Use Velocity with modern forwarding instead, or BungeeCord, which reads the same config.yml.",
    ),
  },
]

/** Paper and its forks share Paper's configuration. */
export const isPaper = (type: string) => ["paper", "purpur", "folia", "leaf"].includes(type)
/** Fabric and Quilt load Fabric mods. */
export const isFabric = (type: string) => type === "fabric" || type === "quilt"

/** The usual memory of servers; any other from minMemoryMb to maxMemoryMb, which agents allow, can be entered. */
export const memoryOptionsMb = [512, 1024, 2048, 4096, 6144, 8192, 12288, 16384]
export const minMemoryMb = 512
export const maxMemoryMb = 65536

/** The limit of a server's container, which nodes count: its memory, a quarter more and 256 MB for what Java needs besides. */
export const containerMemoryMb = (memoryMb: number) => Math.floor((memoryMb * 5) / 4) + 256

/** Tells the memory of a server: its heap, and the limit of its container. */
export const memoryTitle = (s: { memoryMb: number; memoryLimitMb: number }) =>
  t("{{memory}} for the server, and up to {{limit}} with Java's overhead", {
    memory: formatMegabytes(s.memoryMb),
    limit: formatMegabytes(s.memoryLimitMb),
  })

/** Tells the Java version of a game server, e.g. Java 21; empty is the newest. */
export const javaLabel = (java: string) => (java ? t("Java {{version}}", { version: java }) : t("Newest Java"))

export function serverType(value: string): ServerType {
  return serverTypes.find((type) => type.value === value) ?? { value, label: value, proxy: false }
}

/**
 * Sensible defaults: proxies listen on 25577 and need far less memory than game servers, modpacks at least 4 GiB. The
 * settings of new servers decide the memory of game servers and proxies.
 */
export function defaults(type: string, settings = { memoryMb: 2048, proxyMemoryMb: 512 }) {
  if (type === modpack) return { port: 25565, memoryMb: Math.max(4096, settings.memoryMb) }
  return serverType(type).proxy ? { port: 25577, memoryMb: settings.proxyMemoryMb } : { port: 25565, memoryMb: settings.memoryMb }
}

/** Chosen as the software of a new server, a Modrinth modpack decides it. */
export const modpack = "modpack"

/** Fabric, Quilt, Forge and NeoForge load mods with a mod loader of a version that can be chosen. */
export const isModded = (type: string) => serverType(type).addons?.kind === "mods"

/** The first free port from the preferred one on, within the node's port range. */
export function suggestPort(used: number[], preferred: number, min = 1024, max = 65535): number {
  const start = preferred >= min && preferred <= max ? preferred : min
  const free = (p: number) => !used.includes(p)
  for (let p = start; p <= max; p++) if (free(p)) return p
  for (let p = min; p < start; p++) if (free(p)) return p
  return start
}

/** The ports of servers, also those at which proxies let Bedrock players join. */
export const usedPorts = (servers: { port: number; bedrockPort?: number }[] = []) =>
  servers.flatMap((s) => (s.bedrockPort ? [s.port, s.bedrockPort] : [s.port]))

/** Suggests a name for a copy, e.g. "Lobby" → "Lobby 2", "Lobby 2" → "Lobby 3". */
export function nextName(name: string, taken: string[]): string {
  const base = name.replace(/ \d+$/, "")
  for (let n = 2; ; n++) {
    const candidate = `${base.slice(0, 31 - String(n).length)} ${n}`
    if (!taken.includes(candidate)) return candidate
  }
}

/** Shows the version a server was created with; "LATEST" follows new releases. */
export function displayVersion(version: string) {
  return version === "LATEST" ? t("latest") : version
}

export const serverStates: Record<ServerState, Status> = {
  running: { tone: "success", label: msg("Running") },
  starting: { tone: "warning", label: msg("Starting"), pulse: true },
  crashing: { tone: "destructive", label: msg("Crashing"), pulse: true },
  stopped: { tone: "neutral", label: msg("Stopped") },
}

const crashed: Status = { tone: "warning", label: msg("Crashed") }
const unhealthy: Status = { tone: "warning", label: msg("Unhealthy"), pulse: true }

/**
 * The state of a server to show: one that stopped after crashing says that it crashed, and a
 * running one whose health check fails that it is unhealthy.
 */
export const statusOf = (s: { state: ServerState; crashes: number; unhealthy?: boolean }) =>
  s.state === "stopped" && s.crashes > 0 ? crashed : s.state === "running" && s.unhealthy ? unhealthy : serverStates[s.state]

/** The states in the order lists show them. */
export const states = Object.keys(serverStates) as ServerState[]

/** Game servers are emerald blocks, proxies violet forks. */
export function serverLook(type: string) {
  return serverType(type).proxy ? ({ icon: ArrowsSplitIcon, tone: "violet" } as const) : ({ icon: CubeIcon, tone: "success" } as const)
}

/** Splits JVM options entered one per line. */
export const splitOptions = (lines: string) => lines.split(/\s+/).filter(Boolean)
