import { queryOptions, useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { operate } from "@/features/operations/api"
import type { Followed } from "@/features/servers/api"
import { api } from "@/lib/api"

/** The private network of the nodes: its private IPv4 range, and the UDP port and MTU of its interfaces. */
export interface OverlaySettings {
  /** e.g. 10.213.0.0/24; only changes while no node is a member. */
  subnet: string
  port: number
  mtu: number
}

/** A node of the private network. */
export interface OverlayMember {
  nodeId: string
  /** Its address in the network, e.g. 10.213.0.3. */
  address: string
  publicKey: string
  /** host:port at which the others reach it; empty for the host of its agent's address and the network's port. */
  endpoint: string
  joinedAt: string
  /** Why it was last configured in vain. */
  problem?: string
  /** Members it had no handshake with for a while, e.g. as a firewall blocks the port. */
  unreached?: string[]
}

export interface Overlay extends OverlaySettings {
  /** The members on the nodes the user may see. */
  members: OverlayMember[]
}

/** Another member, as a node sees it. */
export interface OverlayPeer {
  nodeId: string
  address: string
  endpoint?: string
  latestHandshake?: string
  receivedBytes: number
  sentBytes: number
}

/** A member that reaches a published port. */
export interface OverlayClient {
  /** The member at the address, if one has it. */
  nodeId?: string
  address: string
  /** The member has another key than the one let in, e.g. as it was offline meanwhile: it reaches the port once the network is applied again. */
  stale?: boolean
}

/** A port that a member publishes in the private network, for a server or a datastore, each named only if the user may see it. */
export interface PublishedPort {
  port: number
  serverId?: string
  datastoreId?: string
  /** The members on the nodes the user may see that reach it. */
  clients: OverlayClient[]
}

/** Whether the nftables rules of a member are in place, as its agent checked them. */
export type FirewallState = "in_place" | "missing" | "incomplete"

/** A node's part in the private network, as its agent tells it. */
export interface NodeOverlay {
  /** The node's administrator lets it join, with noryx-agent overlay allow. */
  allowed: boolean
  /** Why it can't run WireGuard. */
  unsupported?: string
  member?: OverlayMember
  /** Where the others reach the member. */
  endpoint?: string
  /** The beginning of its public key, as noryx-agent overlay status shows it. */
  fingerprint?: string
  peers: OverlayPeer[]
  /** Only once the member is configured. */
  firewall?: FirewallState
  /** What differs, if the rules are incomplete. */
  firewallProblem?: string
  published: PublishedPort[]
}

/** The result of connecting to a port that a peer publishes for a node, for a server or a datastore the user may see. */
export interface PortTest {
  port: number
  serverId?: string
  datastoreId?: string
  /** Why no connection came about; empty if one did. */
  error?: string
  /** How long connecting took, or failing. */
  millis: number
}

export const overlayQuery = queryOptions({
  queryKey: ["overlay"],
  queryFn: () => api<Overlay>("/overlay"),
})

export const nodeOverlayQuery = (nodeId: string) =>
  queryOptions({
    queryKey: ["overlay", nodeId],
    queryFn: () => api<NodeOverlay>(`/nodes/${nodeId}/overlay`),
    refetchInterval: 30_000,
  })

/** Whether a proxy on node a reaches the servers of node b over the private network, which only lets it in. */
export function usePrivateRoute() {
  const { data } = useQuery(overlayQuery)
  const members = new Set(data?.members.map((m) => m.nodeId))
  return (a: string, b: string) => a !== b && members.has(a) && members.has(b)
}

function useOverlayChange<T, V>(fn: (v: V) => Promise<T>) {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: fn,
    onSettled: () => {
      void queryClient.invalidateQueries({ queryKey: overlayQuery.queryKey })
      void queryClient.invalidateQueries({ queryKey: ["networks"] })
      void queryClient.invalidateQueries({ queryKey: ["servers"] })
    },
  })
}

export const useUpdateOverlaySettings = () =>
  useOverlayChange((settings: OverlaySettings) => api<OverlaySettings>("/overlay", { method: "PUT", body: settings }))

/** Adds a node, whose administrator allowed it, to the private network; the others reach it at endpoint, or else at its agent's address. */
export const useJoinOverlay = (nodeId: string) =>
  useOverlayChange((endpoint: string) => api<OverlayMember>(`/nodes/${nodeId}/overlay`, { body: { endpoint } }))

export const useSetOverlayEndpoint = (nodeId: string) =>
  useOverlayChange((endpoint: string) => api<OverlayMember>(`/nodes/${nodeId}/overlay`, { method: "PUT", body: { endpoint } }))

/** Replaces a member's key; the networks with servers on it and on other nodes are applied again, as the others only let in the key a node had when it got access. */
export const useRotateOverlayKey = (nodeId: string) =>
  useOverlayChange(({ onStart }: Followed) => operate<OverlayMember>(`/nodes/${nodeId}/overlay/rotate`, { method: "POST" }, onStart))

/** Connects from a node to the ports that a peer publishes for it, and closes each connection at once. */
export const useTestOverlayPeer = (nodeId: string) =>
  useMutation({ mutationFn: (peerId: string) => api<PortTest[]>(`/nodes/${nodeId}/overlay/peers/${peerId}/test`, { method: "POST" }) })

/** Removes a node from the private network; the networks that reached its servers over it are applied again. */
export const useLeaveOverlay = (nodeId: string) =>
  useOverlayChange(({ onStart }: Followed) => operate(`/nodes/${nodeId}/overlay`, { method: "DELETE" }, onStart))
