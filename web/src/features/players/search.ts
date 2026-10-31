import { useSortings } from "@/features/preferences/sorting"
import { searchParams } from "@/lib/search"
import { type Order, orders } from "@/lib/sort"

export type OnlineSort = "name" | "server" | "network"
export type SeenSort = "seen" | "name" | "playtime"
export type ListSort = "name" | "servers"

/** The columns that sort the players online, seen and in the lists, with the direction each starts in. */
export const onlineSorts: Record<OnlineSort, Order> = { name: "asc", server: "asc", network: "asc" }
export const seenSorts: Record<SeenSort, Order> = { seen: "desc", name: "asc", playtime: "desc" }
export const listSorts: Record<ListSort, Order> = { name: "asc", servers: "desc" }
const sorts = Object.keys({ ...onlineSorts, ...seenSorts, ...listSorts }) as (OnlineSort | SeenSort | ListSort)[]
const tabs = ["online", "seen", "banned", "whitelisted", "operators"] as const

/** The search of the players page, in the address so that it can be shared and bookmarked. */
export interface PlayerSearch {
  q?: string
  tab?: (typeof tabs)[number]
  network?: string
  sort?: OnlineSort | SeenSort | ListSort
  order?: Order
}

export function validatePlayerSearch(search: Record<string, unknown>): PlayerSearch {
  const { text, pick } = searchParams(search)
  return {
    q: text("q"),
    tab: pick("tab", tabs),
    network: text("network"),
    sort: pick("sort", sorts),
    order: pick("order", orders),
  }
}

/** Sorts the tables of players as the address says, otherwise as the user sorted them last, on the players page and those of servers alike. */
export const usePlayerSorting = (search: PlayerSearch, set: (change: Partial<PlayerSearch>) => void) =>
  useSortings(search, { sort: "playerSort", order: "playerOrder" }, set)
