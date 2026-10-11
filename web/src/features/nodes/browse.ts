import type { View } from "@/components/list-toolbar"
import { assignedMemoryMb, type NodeServer, runningCount } from "@/features/servers/api"
import type { NodeUsage } from "@/features/usage/api"
import type { Cell } from "@/lib/csv"
import { msg } from "@/lib/i18n"
import { matches, validateQuery } from "@/lib/search"
import { type Order, orders, sortBy } from "@/lib/sort"
import type { Node, NodeStatus } from "./api"

export const nodeSorts = {
  name: msg("Name"),
  state: msg("State"),
  cpu: msg("CPU"),
  memory: msg("Memory"),
  servers: msg("Servers"),
} as const

export type NodeSort = keyof typeof nodeSorts

/** The direction each sort starts in: names and states from the top, the busiest nodes and those with the most servers first. */
export const nodeSortOrders: Record<NodeSort, Order> = { name: "asc", state: "asc", cpu: "desc", memory: "desc", servers: "desc" }

/** How the list of nodes is searched, sorted and shown, kept in the address to share and bookmark it. */
export interface NodeSearch {
  q?: string
  sort?: NodeSort
  order?: Order
  view?: View
}

/** Reads the list's settings from the address. Each key must be set explicitly, see the login route. */
export function validateNodeSearch(search: Record<string, unknown>): NodeSearch {
  const pick = <T extends string>(key: string, allowed: readonly T[]) => allowed.find((v) => v === search[key])
  return {
    ...validateQuery(search),
    sort: pick("sort", Object.keys(nodeSorts) as NodeSort[]),
    order: pick("order", orders),
    view: pick("view", ["grid", "table"] as const),
  }
}

/** What the list knows about nodes besides themselves, where it knows it. */
export interface NodeFacts {
  /** What a node uses now, if the user may see it and the agent measures the machine. */
  usage: (n: Node) => NodeUsage | undefined
  /** The servers of an online node, as the master can't list those of others. */
  servers: (n: Node) => NodeServer[] | undefined
}

const states: NodeStatus[] = ["online", "pending", "offline"]

/** The nodes matching a search, sorted; ties are sorted by name, and nodes without a figure come last by it. */
export function browseNodes(nodes: Node[], q: string | undefined, sort: NodeSort, order: Order, facts: NodeFacts) {
  const value: Record<NodeSort, (n: Node) => number | string | undefined> = {
    name: (n) => n.name,
    state: (n) => states.indexOf(n.status),
    cpu: (n) => cpuLoad(facts.usage(n)),
    memory: (n) => memoryLoad(facts.usage(n)),
    servers: (n) => facts.servers(n)?.length,
  }
  const found = nodes.filter((n) => matches(q, n.name, n.address, n.info?.hostname, n.info?.os, n.info?.agentVersion))
  return sortBy(found, order, value[sort], (n) => n.name)
}

/** How much of its CPU and memory a node uses, from 0 to 1. */
export const cpuLoad = (usage?: NodeUsage) => usage && usage.cpuMillis / (usage.cpuCount * 1000)
export const memoryLoad = (usage?: NodeUsage) => usage && usage.memoryUsedBytes / usage.memoryTotalBytes

/** The columns of the CSV file of nodes: what the table shows, as values for spreadsheets. */
const csvColumns: Record<string, (n: Node, usage: NodeUsage | undefined, servers: NodeServer[] | undefined) => Cell> = {
  name: (n) => n.name,
  id: (n) => n.id,
  address: (n) => n.address,
  state: (n) => n.status,
  servers: (_, __, servers) => servers?.length,
  servers_running: (_, __, servers) => servers && runningCount(servers),
  cpus: (n) => n.info?.cpuCount,
  cpu_cores: (_, usage) => usage && usage.cpuMillis / 1000,
  memory_bytes: (n) => n.info?.memoryBytes,
  memory_used_bytes: (_, usage) => usage?.memoryUsedBytes,
  memory_assigned_mb: (_, __, servers) => servers && assignedMemoryMb(servers),
  agent_version: (n) => n.info?.agentVersion,
}

/** The nodes as rows of a CSV file, after a header. */
export const nodeRows = (nodes: Node[], facts: NodeFacts): Cell[][] => [
  Object.keys(csvColumns),
  ...nodes.map((n) => Object.values(csvColumns).map((value) => value(n, facts.usage(n), facts.servers(n)))),
]
