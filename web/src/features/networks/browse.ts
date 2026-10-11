import type { NodeServer } from "@/features/servers/api"
import { serverType } from "@/features/servers/server-types"
import type { ServerUsage } from "@/features/usage/api"
import { msg } from "@/lib/i18n"
import { matches, validateQuery } from "@/lib/search"
import { type Order, orders, sortBy } from "@/lib/sort"
import type { Network, ServerRef } from "./api"
import { findServer } from "./servers"
import { playersOnline } from "./usage"

export const networkSorts = {
  name: msg("Name"),
  players: msg("Players"),
  servers: msg("Servers"),
} as const

export type NetworkSort = keyof typeof networkSorts

/** The direction each sort starts in: names from the top, the networks with the most players and servers first. */
export const networkSortOrders: Record<NetworkSort, Order> = { name: "asc", players: "desc", servers: "desc" }

/** How the list of networks is searched and sorted, kept in the address to share and bookmark it. */
export interface NetworkSearch {
  q?: string
  sort?: NetworkSort
  order?: Order
}

/** Reads the list's settings from the address. Each key must be set explicitly, see the login route. */
export function validateNetworkSearch(search: Record<string, unknown>): NetworkSearch {
  const pick = <T extends string>(key: string, allowed: readonly T[]) => allowed.find((v) => v === search[key])
  return {
    ...validateQuery(search),
    sort: pick("sort", Object.keys(networkSorts) as NetworkSort[]),
    order: pick("order", orders),
  }
}

/**
 * The networks whose names, proxies or servers match a search, sorted; ties are sorted by name, and networks whose
 * players aren't known come last by them.
 */
export function browseNetworks(
  networks: Network[],
  q: string | undefined,
  sort: NetworkSort,
  order: Order,
  servers: NodeServer[] | undefined,
  usage: (ref: ServerRef) => ServerUsage | undefined,
) {
  const value: Record<NetworkSort, (n: Network) => number | string | undefined> = {
    name: (n) => n.name,
    players: (n) => playersOnline(n, usage),
    servers: (n) => n.backends.length,
  }
  const found = networks.filter((n) =>
    matches(q, n.name, findServer(servers, n.proxy)?.name, serverType(n.proxyType).label, ...n.backends.map((b) => b.name)),
  )
  return sortBy(found, order, value[sort], (n) => n.name)
}
