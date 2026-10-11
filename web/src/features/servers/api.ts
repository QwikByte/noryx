import { type QueryClient, queryOptions, useMutation, useMutationState, useQuery, useQueryClient } from "@tanstack/react-query"
import type { ModpackChoice } from "@/features/modpacks/api"
import { memoryLimitMb, type Node } from "@/features/nodes/api"
import { type Operation, operate } from "@/features/operations/api"
import type { MessageKind } from "@/features/players/api"
import { api, send } from "@/lib/api"

export type ServerState = "stopped" | "starting" | "running" | "crashing"

export interface Server {
  id: string
  name: string
  type: string
  version: string
  /** The heap that Java gets. */
  memoryMb: number
  /** The limit of the server's container: its heap and what Java needs besides. Nodes count it against their memory. */
  memoryLimitMb: number
  port: number
  state: ServerState
  /** Crashes since the server was last started, while it crashes or after it stopped because of one. */
  crashes: number
  /** Exit code of the latest crash; 0 if unknown. */
  exitCode: number
  storage: string
  java: string
  restartPolicy: RestartPolicy
  aikarFlags: boolean
  jvmOptions: string[]
  /** JVM options set before the agent refused them, which the server starts with until they are removed. */
  refusedJvmOptions?: string[]
  /** CPU cores the server may use; 0 means no limit. */
  cpuLimit: number
  /** Version of the mod loader of a modded server, e.g. one a modpack needs; empty for the newest. */
  loaderVersion: string
  /** The UDP port at which Bedrock players join a proxy, which its network sets. */
  bedrockPort?: number
  /** The node publishes the port only in the private network of the nodes, for the node of the server's proxy. */
  overlay?: boolean
  /** A running server whose health check fails, e.g. as it hangs; it is treated like any running one. */
  unhealthy?: boolean
  /** Seconds the server gets to stop, e.g. to save its worlds, before it is killed. */
  stopTimeout: number
  /** IANA time zone such as Europe/Berlin; empty for UTC. */
  timeZone: string
  /** Labels such as lobby, sorted; only in lists of servers. */
  tags: string[]
  /** What the server is for, as plain text; only in lists of servers. */
  notes?: string
}

export type RestartPolicy = "always" | "on_crash" | "never"

/** Settings of a server that can be changed after it was created. */
export type ServerSettings = Pick<
  Server,
  | "name"
  | "version"
  | "memoryMb"
  | "port"
  | "java"
  | "restartPolicy"
  | "aikarFlags"
  | "jvmOptions"
  | "cpuLimit"
  | "loaderVersion"
  | "stopTimeout"
  | "timeZone"
>

/** What the panel creates servers with, as the settings of the master say, unless a template is chosen. */
export interface NewServerSettings {
  type: string
  memoryMb: number
  /** The memory of proxies, which need far less than game servers. */
  proxyMemoryMb: number
  /** The Java version of game servers; empty is the newest. */
  java: string
  stopTimeout: number
  timeZone: string
  /** The ID of the template chosen instead, for those who may see templates; empty for none. */
  template: string
}

/** What to create servers with, also for those who may create servers but not see the settings. */
export const newServersQuery = queryOptions({
  queryKey: ["new-servers"],
  queryFn: () => api<NewServerSettings>("/servers/defaults"),
})

/** A server together with the node it runs on. */
export interface NodeServer extends Server {
  nodeId: string
  nodeName: string
}

/** Memory that servers take from their nodes: the limits of their containers, as the master counts them. */
export const assignedMemoryMb = (servers: Server[]) => servers.reduce((sum, s) => sum + s.memoryLimitMb, 0)

/**
 * Memory in MB left on a node for the container of a new server, or of the server `except`
 * instead of its current one, which may always keep or reduce its memory; undefined if the
 * node doesn't limit it.
 */
export function freeMemoryMb(node: Node | undefined, servers: Server[] | undefined, except?: Server) {
  const limit = node && memoryLimitMb(node)
  if (limit === undefined || !servers) return undefined
  const free = limit - assignedMemoryMb(servers.filter((s) => s.id !== except?.id))
  return Math.max(free, except?.memoryLimitMb ?? 0)
}

/** How many servers run, not counting those that start or crash. */
export const runningCount = (servers: Server[]) => servers.filter((s) => s.state === "running").length

/** Identifies a server across nodes, e.g. to select it. */
export const serverKey = (s: { nodeId: string; id: string }) => `${s.nodeId}/${s.id}`

export interface NewServer
  extends Partial<
    Pick<Server, "java" | "restartPolicy" | "aikarFlags" | "jvmOptions" | "cpuLimit" | "loaderVersion" | "stopTimeout" | "timeZone">
  > {
  name: string
  type: string
  version: string
  memoryMb: number
  port: number
  acceptEula: boolean
  storage: string
  /** Written to server.properties before the first start. */
  properties?: Record<string, string>
  /** A modpack decides the type and the versions of the server. */
  modpack?: ModpackChoice
  /** Given to the new server, e.g. those of its template. */
  tags?: string[]
  /** The IDs of the versions to install of plugins, by project ID, e.g. those a template keeps. */
  versions?: Record<string, string>
}

/** How often lists of servers are checked: more often while servers start, to show them running soon. */
const interval = (servers?: Server[]) => (servers?.some((s) => s.state === "starting") ? 2_000 : 5_000)

export const serversQuery = (nodeId: string) =>
  queryOptions({
    queryKey: ["nodes", nodeId, "servers"],
    queryFn: () => api<Server[]>(`/nodes/${nodeId}/servers`),
    refetchInterval: (query) => interval(query.state.data),
  })

/** A server of a node, looked up in the list of the node's servers. */
export function useServer(nodeId: string, serverId: string) {
  const query = useQuery(serversQuery(nodeId))
  return { ...query, server: query.data?.find((s) => s.id === serverId) }
}

/** The servers of all reachable nodes; servers of offline nodes are missing. */
export const allServersQuery = queryOptions({
  queryKey: ["servers"],
  queryFn: () => api<NodeServer[]>("/servers"),
  refetchInterval: (query) => interval(query.state.data),
})

/** Learns of the operation an action becomes, to follow it; see operate. */
export interface Followed {
  onStart?: (op: Operation) => void
}

/**
 * Creates a server, with the projects of Modrinth or Hangar to install on it, e.g. the plugins of a
 * template. pluginError tells why they couldn't be installed, and warning what else the server didn't
 * get, e.g. a stop timeout from an older agent; the server exists anyway.
 */
export function useCreateServer() {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: ({ nodeId, server, plugins, onStart }: { nodeId: string; server: NewServer; plugins?: string[] } & Followed) =>
      operate<Server & { pluginError?: string; warning?: string }>(`/nodes/${nodeId}/servers`, { body: { ...server, plugins } }, onStart),
    onSettled: (_data, _error, { nodeId }) => refreshServers(queryClient, nodeId),
  })
}

/** The settings of a server created from an archive, which brings its own server.properties. */
export type ImportedSettings = Pick<NewServer, "name" | "type" | "version" | "memoryMb" | "port" | "acceptEula" | "storage" | "java" | "stopTimeout" | "timeZone">

/**
 * Creates a server whose data is a ZIP or .tar.gz archive of a server from elsewhere, with upload progress. leftOut
 * are the files of the archive it didn't get, e.g. those with secrets of the server it came from.
 */
export function useImportServer() {
  const queryClient = useQueryClient()
  return async (
    nodeId: string,
    settings: ImportedSettings,
    archive: File,
    { onProgress, signal }: { onProgress: (fraction: number) => void; signal: AbortSignal },
  ) => {
    const form = new FormData()
    form.append("server", JSON.stringify(settings))
    form.append("archive", archive)
    try {
      return await send<Server & { leftOut: string[]; warning?: string }>("POST", `/nodes/${nodeId}/servers/import`, form, { onProgress, signal })
    } finally {
      void refreshServers(queryClient, nodeId)
    }
  }
}

/** Copies a server with all its data into a new server on the same node. */
export function useDuplicateServer(nodeId: string) {
  const queryClient = useQueryClient()
  return useMutation({
    /** With network, the copy of a game server of a network joins the network next to it. */
    mutationFn: ({ id, onStart, ...body }: { id: string; name: string; port: number; network: boolean } & Followed) =>
      operate<Server>(`/nodes/${nodeId}/servers/${id}/duplicate`, { body }, onStart),
    onSettled: (_data, _error, { network }) => {
      void refreshServers(queryClient, nodeId)
      if (network) void queryClient.invalidateQueries({ queryKey: ["networks"] })
    },
  })
}

export type MovePhase = "stopping" | "copying" | "backups" | "finishing" | "done" | "failed"

/** A server moving to another node; the master shows finished moves for an hour. */
export interface Move {
  serverId: string
  serverName: string
  from: string
  to: string
  toName: string
  phase: MovePhase
  /** How much of the data and backups was copied, as compressed archives. */
  bytes: number
  backups: number
  backupsTotal: number
  /** Why it failed; the server stayed where it was. */
  error?: string
  /** What went wrong once the server was on the new node. */
  warnings: string[]
  startedAt: string
  finishedAt?: string
}

/** The moves of the servers the user may see, checked often while one is in progress. */
export const movesQuery = queryOptions({
  queryKey: ["moves"],
  queryFn: () => api<Move[]>("/moves"),
  refetchInterval: (query) => (query.state.data?.some((m) => !m.finishedAt) ? 1_500 : 15_000),
})

/** The latest move of a server, if it moved within the last hour or is moving. */
export function useMove(serverId: string) {
  return useQuery(movesQuery).data?.find((m) => m.serverId === serverId)
}

export interface MoveRequest {
  node: string
  port: number
  storage: string
  /** Whether the backups move too; otherwise they are deleted with the server on its old node. */
  backups: boolean
  /** Confirms a move to a node that doesn't reach the databases of the server's network. */
  withoutDatabases?: boolean
}

export function useMoveServer(nodeId: string, serverId: string) {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: (input: MoveRequest) => api<Move>(`/nodes/${nodeId}/servers/${serverId}/move`, { body: input }),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: movesQuery.queryKey }),
  })
}

export type ServerAction = "start" | "stop" | "restart" | "delete"

/** Warns the players before servers stop or restart, and again at the steps of the settings. */
export interface Warning {
  /** 1 to the longest lead time of the settings. */
  minutes: number
  /** {minutes} becomes the minutes left; empty is the text of the settings. A message of one's own needs the permission to send console commands. */
  message?: string
}

/** How the players are warned before restarts and stops, as the settings of the master say. */
export interface WarningSettings {
  /** The texts of restarts and stops without a warning of their own; {minutes} becomes the minutes left. */
  restart: string
  stop: string
  /** The minutes before at which a warning by hand repeats, the most first. */
  steps: number[]
  /** How long before at most a warning by hand starts, up to 60 minutes. */
  maxMinutes: number
  kind: MessageKind
}

/** How the players are warned, also for those who warn them but may not see the settings. */
export const warningsQuery = queryOptions({
  queryKey: ["warnings"],
  queryFn: () => api<WarningSettings>("/servers/warnings"),
})

export type BulkAction =
  | { action: "start" }
  | { action: "stop" | "restart"; warning?: Warning }
  | { action: "command"; command: string }

/** How an action ended on each server; failed ones have an error. */
export interface BulkResult {
  nodeId: string
  serverId: string
  error?: string
}

const refsOf = (servers: Pick<NodeServer, "nodeId" | "id">[]) => servers.map((s) => ({ nodeId: s.nodeId, serverId: s.id }))

/** Refreshes the lists of servers of a node, or of all nodes, after a change to servers on it. */
const refreshServers = (queryClient: QueryClient, nodeId?: string) =>
  queryClient.invalidateQueries({
    predicate: ({ queryKey }) =>
      queryKey[0] === "servers" || (queryKey[0] === "nodes" && (!nodeId || queryKey[1] === nodeId) && queryKey[2] === "servers"),
  })

/** Starts, stops or restarts servers on any nodes, or sends them a console command. */
export function useBulkAction() {
  const queryClient = useQueryClient()
  return useMutation({
    mutationKey: ["server-action"],
    mutationFn: ({ servers, onStart, ...action }: BulkAction & { servers: NodeServer[] } & Followed) =>
      operate<{ results: BulkResult[] }>("/servers/actions", { body: { ...action, servers: refsOf(servers) } }, onStart).then(
        (r) => r.results,
      ),
    onSettled: () => refreshServers(queryClient),
  })
}

/** Adds and removes tags of servers. */
export function useChangeTags() {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: ({ servers, add = [], remove = [] }: { servers: Pick<NodeServer, "nodeId" | "id">[]; add?: string[]; remove?: string[] }) =>
      api("/servers/tags", { body: { servers: refsOf(servers), add, remove } }),
    onSettled: () => refreshServers(queryClient),
  })
}

/**
 * The action that runs on a server right now, started in this browser, e.g. stop while the
 * server shuts down.
 */
export function usePendingAction(nodeId: string, serverId: string): BulkAction["action"] | "delete" | undefined {
  const pending = useMutationState({
    filters: { mutationKey: ["server-action"], status: "pending" },
    select: (m) => ({
      node: m.options.mutationKey?.[1],
      vars: m.state.variables as { id: string; action: ServerAction } | (BulkAction & { servers: NodeServer[] }),
    }),
  })
  for (const { node, vars } of pending) {
    if ("servers" in vars && vars.servers.some((s) => s.nodeId === nodeId && s.id === serverId)) return vars.action
    if ("id" in vars && node === nodeId && vars.id === serverId) return vars.action
  }
  return undefined
}

/**
 * Starts, stops, restarts or deletes a server; stopping and restarting may become operations, as a server may take
 * minutes to stop, and may warn the players first.
 */
export function useServerAction(nodeId: string) {
  const queryClient = useQueryClient()
  return useMutation({
    mutationKey: ["server-action", nodeId],
    mutationFn: ({ id, action, warning, onStart }: { id: string; action: ServerAction; warning?: Warning } & Followed) =>
      action === "delete"
        ? api(`/nodes/${nodeId}/servers/${id}`, { method: "DELETE" })
        : operate(`/nodes/${nodeId}/servers/${id}/${action}`, warning ? { body: { warning } } : { method: "POST" }, onStart),
    onSettled: () => queryClient.invalidateQueries({ queryKey: serversQuery(nodeId).queryKey }),
  })
}

export function useUpdateServer(nodeId: string, serverId: string) {
  const queryClient = useQueryClient()
  return useMutation({
    /** warning tells what didn't follow the change, e.g. the proxy of the server's network. */
    mutationFn: ({ settings, onStart }: { settings: ServerSettings } & Followed) =>
      operate<Server & { warning?: string }>(`/nodes/${nodeId}/servers/${serverId}`, { method: "PUT", body: settings }, onStart),
    onSettled: () => refreshServers(queryClient, nodeId),
  })
}

/** Replaces the notes of a server, which doesn't restart it; empty notes delete them. */
export function useSetNotes(nodeId: string, serverId: string) {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: (notes: string) => api(`/nodes/${nodeId}/servers/${serverId}/notes`, { method: "PUT", body: { notes } }),
    onSettled: () => refreshServers(queryClient, nodeId),
  })
}

/** Pulls the server's image again; updated tells whether a newer one came and the container was replaced. */
export function useUpdateImage(nodeId: string, serverId: string) {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: ({ onStart }: Followed = {}) =>
      operate<{ updated: boolean }>(`/nodes/${nodeId}/servers/${serverId}/update-image`, { method: "POST" }, onStart),
    onSettled: () => refreshServers(queryClient, nodeId),
  })
}

/** Runs a console command; formatted is its output with Minecraft's codes of colours, if it has any. */
export function useSendCommand(nodeId: string, serverId: string) {
  return useMutation({
    mutationFn: (command: string) =>
      api<{ output: string; formatted?: string }>(`/nodes/${nodeId}/servers/${serverId}/command`, { body: { command } }),
  })
}

/** Lines of a server's console before the one with the given ID, oldest first, with Minecraft's codes of colours. */
export const earlierOutput = (nodeId: string, serverId: string, before: string) =>
  api<{ lines: { id: string; text: string }[] }>(`/nodes/${nodeId}/servers/${serverId}/logs/earlier?before=${encodeURIComponent(before)}`)

/** A server, or all servers of a node (including later ones) if serverId is empty. */
export interface Target {
  nodeId: string
  serverId: string
}
