import { keepPreviousData, queryOptions, useMutation, useQueryClient } from "@tanstack/react-query"
import type { ServerRef } from "@/features/networks/api"
import { operate } from "@/features/operations/api"
import type { Followed } from "@/features/servers/api"
import { api } from "@/lib/api"

/** What can be done to a player on game servers; turning the whitelist on or off takes no player. */
export type PlayerAction =
  | "kick"
  | "ban"
  | "pardon"
  | "whitelist_add"
  | "whitelist_remove"
  | "op"
  | "deop"
  | "whitelist_on"
  | "whitelist_off"

/** How a change ended on a server. */
export interface PlayerResult extends ServerRef {
  /** The player, unless the change was of the whole server. */
  name?: string
  /** The server doesn't run and changes the player once it does. */
  pending?: boolean
  output?: string
  error?: string
}

/** A player in the lists of servers. */
export interface Listed {
  name: string
  uuid?: string
  /** Why, since when, until when and by whom the player is banned, of the newest ban; until only for temporary ones. */
  reason?: string
  since?: string
  until?: string
  source?: string
  /** The servers whose list has the player. */
  servers: ServerRef[]
}

/** What the lists of a server don't tell. */
interface ServerLists extends ServerRef {
  whitelistEnabled: boolean
  /** Changes that wait for the server to run, or until due, e.g. the pardon at the end of a temporary ban. */
  pending: { action: PlayerAction; name?: string; reason?: string; due?: string }[]
  /** Why the lists of the server are missing. */
  error?: string
}

/** The lists of servers, joined by player, and if asked for the players who joined them. */
export interface PlayerLists {
  servers: ServerLists[]
  banned: Listed[]
  whitelisted: Listed[]
  operators: Listed[]
  joined?: Listed[]
}

/**
 * The lists of the game servers of a network, of one game server, or of all game servers the user may see; with
 * joined, also the players who joined them, whom the servers keep in their cache.
 */
export function playerListsQuery({ network, server, joined }: { network?: string; server?: ServerRef; joined?: boolean } = {}) {
  const filter = new URLSearchParams(server ? { node: server.nodeId, server: server.serverId } : network ? { network } : {})
  if (joined) filter.set("joined", "1")
  return queryOptions({
    queryKey: ["players", "lists", filter.toString()],
    queryFn: () => api<PlayerLists>(`/players/lists?${filter}`),
    refetchInterval: 30_000, // the lists change in the game and through plugins too
  })
}


/** When a player was first and last seen online, and for how many minutes. */
interface Span {
  firstSeen: string
  lastSeen: string
  minutes: number
}

/** A player seen online, with the servers, those seen last first. */
export interface SeenPlayer extends Span {
  name: string
  servers: (ServerRef & Span)[]
}

/** Where and when a player was online, with the minutes of each day (as 2026-10-01, in UTC). */
export interface PlayerHistory extends SeenPlayer {
  days: { day: string; minutes: number }[]
}

/**
 * The players seen on the servers the user may see, of a network, of one server or all, those seen last first, and
 * whether the master records them, which the settings can turn off.
 */
export function seenPlayersQuery({ q = "", network, server, limit }: { q?: string; network?: string; server?: ServerRef; limit?: number } = {}) {
  const search = new URLSearchParams({
    q,
    ...(network && { network }),
    ...(server && { node: server.nodeId, server: server.serverId }),
    ...(limit && { limit: String(limit) }),
  })
  return queryOptions({
    queryKey: ["players", "seen", search.toString()],
    queryFn: () => api<{ players: SeenPlayer[]; total: number; recording: boolean }>(`/players/seen?${search}`),
    placeholderData: keepPreviousData,
    refetchInterval: 60_000, // the master notes the players online every minute
  })
}

export const playerHistoryQuery = (name: string) =>
  queryOptions({
    queryKey: ["players", "seen", "player", name.toLowerCase()],
    queryFn: () => api<PlayerHistory>(`/players/seen/${encodeURIComponent(name)}`),
    refetchInterval: 60_000,
  })

/** Deletes where and when all players were seen, e.g. once the settings no longer record it. */
export function useDeleteSeenPlayers() {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: () => api("/players/seen", { method: "DELETE" }),
    onSettled: () => queryClient.invalidateQueries({ queryKey: ["players", "seen"] }),
  })
}

/** Whether a name is that of a player that the master and the agents take: of Java players, or of Bedrock players with Floodgate's dot. */
export const validPlayerName = (name: string) => /^\.?[A-Za-z0-9_]{1,16}$/.test(name)

/** The most players of an action or a message. */
export const maxPlayers = 50

export interface PlayerChange {
  action: PlayerAction
  /** The players; none for turning the whitelist on or off. */
  names?: string[]
  reason?: string
  /** When a ban ends, as an ISO time; none for good. */
  until?: string
  servers: ServerRef[]
}

/** Changes players on servers: at once on those that run, on the others once they do. */
export function useChangePlayer() {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: ({ onStart, ...change }: PlayerChange & Followed) =>
      operate<{ results: PlayerResult[] }>("/players/actions", { body: change }, onStart).then((r) => r.results),
    onSettled: () => queryClient.invalidateQueries({ queryKey: ["players"] }),
  })
}

/** Where a message shows: in the chat, as a title in the middle of the screen, or above the hotbar. */
export type MessageKind = "chat" | "title" | "actionbar"

export interface Message {
  kind: MessageKind
  message: string
  /** Only titles have one. */
  subtitle?: string
}

/** Shows a message to players on the servers they're on; the chat gets a whisper of the server. */
export function useMessagePlayers() {
  return useMutation({
    mutationFn: (body: Message & { names: string[]; servers: ServerRef[] }) =>
      api<{ results: PlayerResult[] }>("/players/message", { body }).then((r) => r.results),
  })
}

/** The address of a player's face, which the master cuts from their skin. */
export const faceUrl = (name: string) => `/api/players/faces/${encodeURIComponent(name)}`

/** Sends a player to another server of a network, through its proxy. */
export function useSendPlayer(networkId: string) {
  return useMutation({
    mutationFn: (body: { name: string; server: string }) => api(`/networks/${networkId}/players/send`, { body }),
  })
}

/** Sends the players of a game server of a network to another server of it, the one players join first if it runs. */
export function useMovePlayers(networkId: string) {
  return useMutation({
    mutationFn: (server: ServerRef) => api<{ players: number }>(`/networks/${networkId}/players/move`, { body: { server } }),
  })
}
