import { queryOptions, useMutation, useQueryClient } from "@tanstack/react-query"
import { operate } from "@/features/operations/api"
import type { Followed } from "@/features/servers/api"
import { api } from "@/lib/api"

export type NodeStatus = "pending" | "online" | "offline"

/** The runtimes a node can run its servers with. */
export type Runtime = "docker" | "podman"

/** The agent, system and paths are only there with the permission to see the node. */
export interface NodeInfo {
  agentVersion?: string
  hostname?: string
  os?: string
  cpuCount: number
  memoryBytes: number
  /** E.g. "docker 29.0.0", or "unavailable" while the agent can't reach its runtime. */
  runtime?: string
  /** The runtime of the servers; also there without the permission to see the node. */
  runtimeName: Runtime
  /** Its version; empty while the agent can't reach it. */
  runtimeVersion?: string
  /** Directories the node allows for server data, the default location first. */
  storage: StorageLocation[]
  /** The types of servers the agent creates, e.g. bedrock; agents of older versions don't create Bedrock servers. */
  serverTypes?: string[]
}

export interface StorageLocation {
  name: string
  path?: string
  freeBytes: number
  totalBytes: number
}

/** Limits for the servers of a node; new nodes get the defaults of the master's settings. */
export interface NodeLimits {
  /** Port range of servers; null allows any port. */
  portMin: number | null
  portMax: number | null
  /** Memory kept free for the system; null allows assigning more than the node has. */
  memoryReserveMb: number | null
}

/** Settings that the agent of a node applies itself; agents of older versions don't take them. */
export interface AgentSettings {
  /** The runtime keeps logFiles files of up to logFileMb MB of the output of each container; 0 for both leaves it to the runtime. */
  logFileMb: number
  logFiles: number
  /** GiB that uploads and backups leave free on the node. */
  keepFreeGb: number
  /** Seconds between two measurements of what the node uses. */
  statsIntervalSeconds: number
}

/** Settings that apply to the servers of a node. */
export interface NodeSettings extends NodeLimits, AgentSettings {
  /** Storage location preselected for new servers. */
  defaultStorage: string
}

/** A join token works once, until it expires. */
export interface JoinToken {
  joinToken: string
  joinTokenExpiresAt: string
  /** Installs the agent in the master's version on the node and enrolls it with the token. */
  installCommand: string
}

export interface Node extends NodeSettings {
  id: string
  name: string
  /** Only there with the permission to see the node. */
  address?: string
  enrolledAt?: string
  createdAt: string
  status: NodeStatus
  info?: NodeInfo
  certificateExpiresAt?: string
}

/** Memory in MB that servers on a node can get, or undefined if it isn't limited or known. */
export function memoryLimitMb(node: Node): number | undefined {
  if (node.memoryReserveMb === null || !node.info?.memoryBytes) return undefined
  return Math.max(0, Math.floor(node.info.memoryBytes / 1024 ** 2) - node.memoryReserveMb)
}

/** Memory in MB that servers on a node can get in total: the limit, or else all of the node's memory. */
export function memoryCapacityMb(node: Node): number | undefined {
  return memoryLimitMb(node) ?? (node.info?.memoryBytes ? Math.floor(node.info.memoryBytes / 1024 ** 2) : undefined)
}

/** Memory in MB that the servers of the online nodes can get in total. */
export const onlineCapacityMb = (nodes: Node[]) =>
  nodes.filter((n) => n.status === "online").reduce((sum, n) => sum + (memoryCapacityMb(n) ?? 0), 0)

export const nodesQuery = queryOptions({
  queryKey: ["nodes"],
  queryFn: () => api<Node[]>("/nodes"),
  refetchInterval: 10_000,
})

export const nodeQuery = (id: string) =>
  queryOptions({
    queryKey: ["nodes", id],
    queryFn: () => api<Node>(`/nodes/${id}`),
    refetchInterval: 10_000,
  })

export function useCreateNode() {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: (input: { name: string; address: string }) => api<JoinToken & { node: Node }>("/nodes", { body: input }),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: nodesQuery.queryKey }),
  })
}

export function useUpdateNode(id: string) {
  const queryClient = useQueryClient()
  return useMutation({
    /** warning tells which networks couldn't follow a new address. */
    mutationFn: (input: NodeSettings & { name: string; address: string }) =>
      api<Node & { warning?: string }>(`/nodes/${id}`, { method: "PUT", body: input }),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: nodesQuery.queryKey }),
  })
}

export function useNewJoinToken(id: string) {
  return useMutation({
    mutationFn: () => api<JoinToken>(`/nodes/${id}/join-token`, { method: "POST" }),
  })
}

/** The code of the master's refusal to remove a node with servers of networks, which it names. */
export const networksOnNode = "networks-on-node"

/**
 * Removes a node. With release, its servers leave their networks first: a network left without its proxy or game
 * servers is deleted, and the others forget the servers of the node.
 */
export function useDeleteNode() {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: ({ id, release, onStart }: { id: string; release: boolean } & Followed) =>
      operate(`/nodes/${id}${release ? "?release=true" : ""}`, { method: "DELETE" }, onStart),
    onSettled: (_data, _error, { release }) => {
      void queryClient.invalidateQueries({ queryKey: nodesQuery.queryKey })
      if (!release) return
      void queryClient.invalidateQueries({ queryKey: ["networks"] })
      void queryClient.invalidateQueries({ queryKey: ["servers"] })
    },
  })
}

export function useRenewCertificate(id: string) {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: () => api<{ certificateExpiresAt: string }>(`/nodes/${id}/certificate`, { method: "POST" }),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: nodesQuery.queryKey }),
  })
}
