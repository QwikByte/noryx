import { t } from "i18next"
import type { View } from "@/components/list-toolbar"
import type { Network } from "@/features/networks/api"
import type { ServerUsage } from "@/features/usage/api"
import type { Cell } from "@/lib/csv"
import { msg } from "@/lib/i18n"
import { matches } from "@/lib/search"
import { type Order, orders, sortBy } from "@/lib/sort"
import type { NodeServer, ServerState } from "./api"
import { serverType, states } from "./server-types"

export const sorts = {
  name: msg("Name"),
  state: msg("State"),
  players: msg("Players"),
  cpu: msg("CPU"),
  memory: msg("Memory"),
  node: msg("Node"),
  network: msg("Network"),
  type: msg("Type"),
  version: msg("Version"),
  port: msg("Port"),
  tps: msg("TPS"),
} as const

/**
 * The direction each sort starts in: names, states and the like from the top, figures with the largest first, and
 * ticks per second with the lowest, where servers lag.
 */
export const sortOrders: Record<Sort, Order> = {
  name: "asc",
  state: "asc",
  players: "desc",
  cpu: "desc",
  memory: "desc",
  node: "asc",
  network: "asc",
  type: "asc",
  version: "asc",
  port: "asc",
  tps: "asc",
}

/** The columns that tables of servers can show besides the server and its state, in their order. */
export const columns = {
  node: msg("Node"),
  network: msg("Network"),
  type: msg("Type"),
  version: msg("Version"),
  port: msg("Port"),
  tags: msg("Tags"),
  players: msg("Players"),
  tps: msg("TPS"),
  cpu: msg("CPU"),
  memory: msg("Memory"),
} as const

export type Column = keyof typeof columns
const allColumns = Object.keys(columns) as Column[]

/** The columns tables show until the user chooses others. */
const defaultColumns: Column[] = ["node", "network", "players", "cpu", "memory"]

/** The columns that a setting chooses, in their order: some of them separated by commas, or the default ones without it. */
export const columnsOf = (setting: string | undefined): Column[] =>
  setting === undefined ? defaultColumns : allColumns.filter((c) => setting.split(",").includes(c))

/** The setting of a choice of columns, which the master keeps in their order; undefined takes it back to the default ones. */
export const columnsSetting = (chosen: Column[] | undefined) => chosen && allColumns.filter((c) => chosen.includes(c)).join(",")

export const groupings = {
  none: msg("No grouping"),
  network: msg("Network"),
  node: msg("Node"),
  type: msg("Type"),
  tag: msg("Tag"),
} as const

export type Sort = keyof typeof sorts
export type Grouping = keyof typeof groupings
/** What servers are filtered and grouped by. */
export type Property = Exclude<Grouping, "none">
export const properties: Property[] = ["network", "node", "type", "tag"]
export type { View }

/** How the list of servers is filtered, sorted and shown, kept in the address to share and bookmark it. */
export interface ServerSearch {
  q?: string
  state?: ServerState
  type?: string
  node?: string
  /** The ID of a network, or "none" for servers outside of networks. */
  network?: string
  tag?: string
  sort?: Sort
  order?: Order
  group?: Grouping
  view?: View
}

/** Reads the list's settings from the address. Each key must be set explicitly, see the login route. */
export function validateServerSearch(search: Record<string, unknown>): ServerSearch {
  const text = (key: string) => (typeof search[key] === "string" && search[key] ? search[key] : undefined)
  const pick = <T extends string>(key: string, allowed: readonly T[]) => allowed.find((v) => v === search[key])
  return {
    q: text("q"),
    state: pick("state", states),
    type: text("type"),
    node: text("node"),
    network: text("network"),
    tag: text("tag"),
    sort: pick("sort", Object.keys(sorts) as Sort[]),
    order: pick("order", orders),
    group: pick("group", Object.keys(groupings) as Grouping[]),
    view: pick("view", ["grid", "table"] as const),
  }
}

/** What the list knows about servers besides themselves. */
export interface Facts {
  usage: (s: NodeServer) => ServerUsage | undefined
  network: (s: NodeServer) => Network | undefined
}

/**
 * The values of a property of a server, with their labels: its network, node, type or tags.
 * Servers outside of networks have "none", those without tags "".
 */
export function valuesOf(property: Property, s: NodeServer, facts: Facts): [string, string][] {
  switch (property) {
    case "node":
      return [[s.nodeId, s.nodeName]]
    case "type":
      return [[s.type, serverType(s.type).label]]
    case "network": {
      const network = facts.network(s)
      return [network ? [network.id, network.name] : ["none", t("No network")]]
    }
    case "tag":
      return s.tags.length > 0 ? s.tags.map((tag) => [tag, `#${tag}`]) : [["", t("No tags")]]
  }
}

/** Whether a value stands for no network or no tags. */
const isNone = (value: string) => value === "" || value === "none"

/** The servers matching the search, and how many of those match each state regardless of the state filter. */
export function filterServers(servers: NodeServer[], search: ServerSearch, facts: Facts) {
  const matching = servers.filter((s) => {
    const labels = properties.flatMap((p) =>
      valuesOf(p, s, facts)
        .filter(([v]) => !isNone(v))
        .map(([, label]) => label),
    )
    return (
      matches(search.q, s.name, s.version, s.port, ...labels, s.notes) &&
      properties.every((p) => !search[p] || valuesOf(p, s, facts).some(([v]) => v === search[p]))
    )
  })
  const counts = Object.fromEntries(states.map((state) => [state, matching.filter((s) => s.state === state).length])) as Record<
    ServerState,
    number
  >
  return { found: matching.filter((s) => !search.state || s.state === search.state), counts, total: matching.length }
}

const live = (facts: Facts, s: NodeServer) => (facts.usage(s)?.running ? facts.usage(s) : undefined)

/** Sorts servers; ties are sorted by name, and servers that don't run come last by their figures, as do those outside of networks by network. */
export function sortServers(servers: NodeServer[], sort: Sort, order: Order, facts: Facts) {
  const value: Record<Sort, (s: NodeServer) => number | string | undefined> = {
    name: (s) => s.name,
    state: (s) => states.indexOf(s.state),
    players: (s) => live(facts, s)?.players?.online,
    cpu: (s) => live(facts, s)?.cpuMillis,
    memory: (s) => live(facts, s)?.memoryBytes,
    node: (s) => s.nodeName,
    network: (s) => facts.network(s)?.name,
    type: (s) => serverType(s.type).label,
    version: (s) => s.version,
    port: (s) => s.port,
    tps: (s) => live(facts, s)?.tps,
  }
  return sortBy(servers, order, value[sort], (s) => s.name)
}

/**
 * The columns of the CSV file of servers: what the table shows, as values for spreadsheets. Those of a column of
 * the table come with it; the table shows the type, version, port and tags of servers either way.
 */
const csvColumns: [string, (s: NodeServer, usage: ServerUsage | undefined, facts: Facts) => Cell, Column?][] = [
  ["name", (s) => s.name],
  ["id", (s) => s.id],
  ["node", (s) => s.nodeName, "node"],
  ["node_id", (s) => s.nodeId, "node"],
  ["network", (s, _, facts) => facts.network(s)?.name, "network"],
  ["type", (s) => s.type],
  ["version", (s) => s.version],
  ["port", (s) => s.port],
  ["state", (s) => s.state],
  ["tags", (s) => s.tags.join(" ")],
  ["players", (_, usage) => usage?.players?.online, "players"],
  ["max_players", (_, usage) => usage?.players?.max, "players"],
  ["tps", (_, usage) => usage?.tps, "tps"],
  ["cpu_cores", (_, usage) => usage && usage.cpuMillis / 1000, "cpu"],
  ["memory_used_bytes", (_, usage) => usage?.memoryBytes, "memory"],
  ["memory_mb", (s) => s.memoryMb, "memory"],
]

/** The servers as rows of a CSV file with the chosen columns, after a header. */
export function serverRows(servers: NodeServer[], facts: Facts, chosen: Column[]): Cell[][] {
  const shown = csvColumns.filter(([, , column]) => !column || chosen.includes(column))
  return [shown.map(([name]) => name), ...servers.map((s) => shown.map(([, value]) => value(s, live(facts, s), facts)))]
}

export interface Group {
  key: string
  label: string
  servers: NodeServer[]
}

/** Splits sorted servers into groups sorted by name; those without a network or tag come last. With tags, a server is in the group of each of its tags. */
export function groupServers(servers: NodeServer[], grouping: Grouping = "none", facts: Facts): Group[] {
  if (grouping === "none") return [{ key: "", label: "", servers }]
  const groups = new Map<string, Group>()
  for (const s of servers) {
    for (const [key, label] of valuesOf(grouping, s, facts)) {
      if (!groups.has(key)) groups.set(key, { key, label, servers: [] })
      groups.get(key)!.servers.push(s)
    }
  }
  const last = (g: Group) => Number(isNone(g.key))
  return [...groups.values()].sort((a, b) => last(a) - last(b) || a.label.localeCompare(b.label, undefined, { numeric: true }))
}
