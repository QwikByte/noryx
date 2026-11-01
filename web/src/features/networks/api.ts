import { queryOptions, useMutation, useQueryClient } from "@tanstack/react-query"
import { operate } from "@/features/operations/api"
import type { Message } from "@/features/players/api"
import type { Followed } from "@/features/servers/api"
import { api } from "@/lib/api"

export interface ServerRef {
  nodeId: string
  serverId: string
}

/** Velocity's modern forwarding, or BungeeCord's, which Velocity calls legacy. */
export type Forwarding = "modern" | "legacy"

/** A game server behind the proxy. Players switch to it with /server <name>. */
export interface Backend extends ServerRef {
  name: string
  /** BungeeCord: only players with the permission bungeecord.server.<name> may join it. */
  restricted: boolean
  /** BungeeCord: the MOTD shown for host names that lead to it; empty uses the proxy's. */
  motd: string
}

/** Players who connect through the host name join its servers, tried in this order. */
export interface ForcedHost {
  host: string
  servers: string[]
}

/** What can be changed about a network, except its proxy. */
export interface NetworkSettings {
  name: string
  forwarding: Forwarding
  /** The operator confirmed that only the proxy's node reaches the servers on other nodes. */
  firewalled: boolean
  backends: Backend[]
  /** Names of the servers players join and fall back to, in this order. */
  try: string[]
  forcedHosts: ForcedHost[]
  /** The UDP port at which Bedrock players join through Geyser on the proxy; 0 lets none join. */
  bedrockPort: number
}

export interface Network extends NetworkSettings {
  id: string
  proxy: ServerRef
  /** velocity, bungeecord or waterfall. */
  proxyType: string
  /** Why its servers were last configured in vain, until they are configured again. */
  applyError?: string
  createdAt: string
}

export interface NewNetwork {
  name: string
  proxy: ServerRef
  forwarding: Forwarding
  firewalled: boolean
  /** Players join the first. */
  servers: ServerRef[]
}

// Networks refresh while they are shown, as their problems change on their own, e.g. a proxy
// that is out of date, and other users change them.
export const networksQuery = queryOptions({
  queryKey: ["networks"],
  queryFn: () => api<Network[]>("/networks"),
  refetchInterval: 15_000,
})

export const networkQuery = (id: string) =>
  queryOptions({
    queryKey: ["networks", id],
    queryFn: () => api<Network>(`/networks/${id}`),
    refetchInterval: 15_000,
  })

export function useCreateNetwork() {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: ({ onStart, ...network }: NewNetwork & Followed) => operate<Network>("/networks", { body: network }, onStart),
    // A failed apply still saves the network, so the list is refreshed either way.
    onSettled: () => queryClient.invalidateQueries({ queryKey: networksQuery.queryKey }),
  })
}

/** Saves the settings of a network; the master configures its servers. */
export function useUpdateNetwork(id: string) {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: ({ settings, onStart }: { settings: NetworkSettings } & Followed) =>
      operate<Network>(`/networks/${id}`, { method: "PUT", body: settings }, onStart),
    onSettled: () => queryClient.invalidateQueries({ queryKey: networksQuery.queryKey }),
  })
}

/** Another proxy for a network, which must be free. */
export interface ProxySwap {
  proxy: ServerRef
  forwarding: Forwarding
  /** As in the settings of the network, for the new proxy's node. */
  firewalled: boolean
}

/** Gives a network another proxy, which takes over the settings it can of the old one. */
export function useSwapProxy(id: string) {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: ({ onStart, ...swap }: ProxySwap & Followed) => operate<Network>(`/networks/${id}/proxy`, { body: swap }, onStart),
    onSettled: () => {
      void queryClient.invalidateQueries({ queryKey: networksQuery.queryKey })
      void queryClient.invalidateQueries({ queryKey: ["servers"] })
    },
  })
}

/** What deleting a network had to leave as it was, e.g. the proxy and servers of a node that is offline. */
export interface Deleted {
  warning?: string
}

export type NetworkAction =
  | { action: "apply" | "delete" | "start" | "stop" | "restart" }
  /** A message to the players of all game servers, in the chat, as a title or above the hotbar. */
  | ({ action: "broadcast" } & Message)
  /** Restarts the running game servers a batch at a time, or only those named, e.g. one safely. */
  | { action: "rolling-restart"; batch: number; servers?: ServerRef[] }

/** Acts on a network or on all its servers. */
export function useNetworkAction(id: string) {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: (a: NetworkAction & Followed) => {
      const path = `/networks/${id}`
      switch (a.action) {
        case "delete":
          return operate<Deleted>(path, { method: "DELETE" }, a.onStart)
        case "broadcast":
          return api(`${path}/broadcast`, { body: { kind: a.kind, message: a.message, subtitle: a.subtitle } })
        case "rolling-restart":
          return operate(`${path}/rolling-restart`, { body: { batch: a.batch, servers: a.servers } }, a.onStart)
        default:
          return operate(`${path}/${a.action}`, { method: "POST" }, a.onStart)
      }
    },
    // A deleted network is only refreshed in the list, as its page is left.
    onSettled: (_data, _error, a) => {
      void queryClient.invalidateQueries({ queryKey: networksQuery.queryKey, exact: a.action === "delete" })
      if (a.action !== "apply" && a.action !== "delete") void queryClient.invalidateQueries({ queryKey: ["servers"] })
    },
  })
}

/** The maintenance of a network, as the Maintenance plugin of its proxy keeps it. */
export interface Maintenance {
  /** The proxy loaded the plugin; the first time maintenance turns on, the panel installs it. */
  installed: boolean
  enabled: boolean
  /** Who may join during maintenance. */
  players: { name: string; uuid: string }[]
  /** The servers of the network in maintenance on their own, by name. */
  servers: string[]
  /** When maintenance of the whole network ends, if the plugin keeps its end timer; it keeps other timers to itself. */
  endsAt?: string
  proxyRunning: boolean
}

/** Turns maintenance on or off, for the whole network or one of its servers, now or later, and for how long. */
export interface MaintenanceChange {
  enabled: boolean
  /** The name of a server of the network; none for the whole network. */
  server?: string
  /** Minutes until maintenance starts or ends; 0 for now. */
  delay?: number
  /** Minutes that maintenance lasts which starts; 0 until it is ended. */
  duration?: number
}

export const maintenanceQuery = (id: string) =>
  queryOptions({
    queryKey: ["networks", id, "maintenance"],
    queryFn: () => api<Maintenance>(`/networks/${id}/maintenance`),
    staleTime: 10_000,
    refetchInterval: 15_000, // it can be changed in the game too
  })

/** Changes the maintenance of a network or one of its servers. */
export function useSetMaintenance(id: string) {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: ({ onStart, ...change }: MaintenanceChange & Followed) =>
      operate<Maintenance>(`/networks/${id}/maintenance`, { body: change }, onStart),
    onSuccess: (m) => queryClient.setQueryData(maintenanceQuery(id).queryKey, m),
    onSettled: () => queryClient.invalidateQueries({ queryKey: maintenanceQuery(id).queryKey }),
  })
}

/** Aborts the timer of the Maintenance plugin for a network, or one of its servers by name. */
export function useAbortMaintenanceTimer(id: string) {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: (server: string) => api<Maintenance>(`/networks/${id}/maintenance/abort`, { body: { server } }),
    onSuccess: (m) => queryClient.setQueryData(maintenanceQuery(id).queryKey, m),
  })
}

/** Adds or removes a player who may join during maintenance. */
export function useMaintenancePlayer(id: string) {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: (body: { name: string; add: boolean }) => api<Maintenance>(`/networks/${id}/maintenance/players`, { body }),
    onSuccess: (m) => queryClient.setQueryData(maintenanceQuery(id).queryKey, m),
  })
}

/** A value of a proxy's configuration: text, a number, a switch or a list of texts. */
export type SettingValue = string | number | boolean | string[]

/** The settings of a proxy in its own configuration file, by their path in it. */
export interface ProxySettings {
  /** False until the proxy started once and created its configuration. */
  exists: boolean
  /** e.g. velocity.toml */
  file: string
  settings: Record<string, SettingValue>
  /** Settings the panel or the network decides, with the reason. */
  locked: { key: string; reason: string }[]
}

const proxyPath = ({ nodeId, serverId }: ServerRef) => `/nodes/${nodeId}/servers/${serverId}/proxy`

export const proxySettingsQuery = (proxy: ServerRef) =>
  queryOptions({
    queryKey: ["proxy-settings", proxy.nodeId, proxy.serverId],
    queryFn: () => api<ProxySettings>(proxyPath(proxy)),
  })

/** Changes settings of a proxy, which reloads its configuration if it runs. */
export function useUpdateProxySettings(proxy: ServerRef) {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: (settings: Record<string, SettingValue>) =>
      api<{ reloaded: boolean }>(proxyPath(proxy), { method: "PUT", body: { settings } }),
    onSettled: () => queryClient.invalidateQueries({ queryKey: proxySettingsQuery(proxy).queryKey }),
  })
}
