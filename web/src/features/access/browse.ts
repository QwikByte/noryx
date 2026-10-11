import { msg } from "@/lib/i18n"
import { matches, validateQuery } from "@/lib/search"
import { type Order, orders, sortBy } from "@/lib/sort"
import type { Group, User } from "./api"

export type UserSort = "name" | "added"

/** The columns that sort users, with the direction each starts in: names from the top, the newest users first. */
export const userSortOrders: Record<UserSort, Order> = { name: "asc", added: "desc" }

/** The states users are filtered by; those without two-factor authentication may also be disabled or invited. */
export const userStates = {
  active: msg("Active"),
  invited: msg("Invited"),
  disabled: msg("Disabled"),
  nomfa: msg("Without two-factor authentication"),
} as const

export type UserState = keyof typeof userStates

const is: Record<UserState, (u: User) => boolean> = {
  active: (u) => !u.disabled && u.passwordSet,
  invited: (u) => !u.disabled && !u.passwordSet,
  disabled: (u) => u.disabled,
  nomfa: (u) => !u.mfa,
}

/** How the list of users is searched, filtered and sorted, kept in the address to share and bookmark it. */
export interface UserSearch {
  q?: string
  /** The ID of a group the users are in. */
  group?: string
  state?: UserState
  sort?: UserSort
  order?: Order
}

/** Reads the list's settings from the address. Each key must be set explicitly, see the login route. */
export function validateUserSearch(search: Record<string, unknown>): UserSearch {
  const pick = <T extends string>(key: string, allowed: readonly T[]) => allowed.find((v) => v === search[key])
  return {
    ...validateQuery(search),
    group: typeof search.group === "string" && search.group ? search.group : undefined,
    state: pick("state", Object.keys(userStates) as UserState[]),
    sort: pick("sort", Object.keys(userSortOrders) as UserSort[]),
    order: pick("order", orders),
  }
}

/** The users whose names or groups match the search and its filters, sorted; ties are sorted by name. */
export function browseUsers(users: User[], groups: Group[], search: UserSearch, sort: UserSort, order: Order) {
  const groupNames = (u: User) => u.groups.map((id) => groups.find((g) => g.id === id)?.name)
  const found = users.filter(
    (u) =>
      matches(search.q, u.username, ...groupNames(u)) &&
      (!search.group || u.groups.includes(search.group)) &&
      (!search.state || is[search.state](u)),
  )
  return sortBy(found, order, sort === "name" ? (u) => u.username : (u) => Date.parse(u.createdAt), (u) => u.username)
}
