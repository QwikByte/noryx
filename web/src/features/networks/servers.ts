import { useQuery } from "@tanstack/react-query"
import { useAccess } from "@/features/access/use-access"
import type { Runtime } from "@/features/nodes/api"
import type { NodeServer } from "@/features/servers/api"
import { isFabric, isPaper } from "@/features/servers/server-types"
import { type Forwarding, type Network, networksQuery, type ServerRef } from "./api"

export const proxyTypes = ["velocity", "bungeecord"]
/** BungeeCord's proxies differ from Velocity's: in their configuration, forwarding and commands. */
export const isBungee = (type: string) => type === "bungeecord"

/**
 * Game servers that can verify the players a proxy forwards: Paper and its forks themselves,
 * Fabric and Quilt with FabricProxy-Lite, which only knows modern forwarding, Forge and NeoForge
 * with Proxy-Compatible-Forge. The panel installs the mods.
 */
export function canJoin(type: string, forwarding: Forwarding) {
  return isPaper(type) || type === "forge" || type === "neoforge" || (isFabric(type) && forwarding === "modern")
}

/** The forwarding mod a server type gets, if any. */
export function forwardingMod(type: string) {
  if (isFabric(type)) return "FabricProxy-Lite"
  return { forge: "Proxy-Compatible-Forge", neoforge: "Proxy-Compatible-Forge" }[type]
}

export const key = ({ nodeId, serverId }: ServerRef) => `${nodeId}/${serverId}`

/** How the proxy reaches a server: on its own node, over the private network of the nodes, or at a public port. */
export type Route = "local" | "private" | "public"

export function routeOf(network: Network, server: ServerRef, isPrivate: (a: string, b: string) => boolean): Route {
  if (server.nodeId === network.proxy.nodeId) return "local"
  return isPrivate(network.proxy.nodeId, server.nodeId) ? "private" : "public"
}
export const refOf = (server: NodeServer): ServerRef => ({ nodeId: server.nodeId, serverId: server.id })

/** Finds a server; null while the servers are loading, undefined if it is unreachable. */
export function findServer(servers: NodeServer[] | undefined, ref: ServerRef) {
  return servers ? servers.find((s) => key(refOf(s)) === key(ref)) : null
}

/** Servers matching ok that are not part of a network yet, except those of network. */
export function availableServers(servers: NodeServer[] = [], networks: Network[] = [], ok: (s: NodeServer) => boolean, network?: Network) {
  const used = new Set(networks.filter((n) => n.id !== network?.id).flatMap((n) => [n.proxy, ...n.backends]).map(key))
  return servers.filter((s) => ok(s) && !used.has(key(refOf(s))))
}

/** Derives the name players use for a server, e.g. "Survival 2" becomes "survival-2", unique among taken. */
export function backendName(serverName: string, taken: string[]) {
  const slug = serverName.toLowerCase().replace(/[^a-z0-9]+/g, "-")
  let base = slug.slice(0, 28).replace(/^-+|-+$/g, "") || "server"
  if (base === "try") base = "try-server" // reserved for Velocity's list of servers to try
  let name = base
  for (let i = 2; taken.includes(name); i++) name = `${base}-${i}`
  return name
}

/** The name of a copy of a server in its network: the original's without its number and the next free one, e.g. lobby-2. */
export function copyName(original: string, taken: string[]) {
  const base = original.replace(/-[0-9]+$/, "")
  for (let i = 2; ; i++) {
    const name = `${base.slice(0, 32 - `-${i}`.length)}-${i}`
    if (!taken.includes(name)) return name
  }
}

/**
 * The command that lets only the proxy's node reach a server's port: for Docker's DOCKER-USER
 * chain, or for Podman, which has none, in an nftables table that comes before its rules.
 */
export function firewallCommand(port: number, proxyHost: string, runtime: Runtime = "docker") {
  const ipv6 = proxyHost.includes(":") // an IPv6 address needs the IPv6 tables
  if (runtime === "podman") {
    const rule = `meta l4proto tcp ct status dnat ct original proto-dst ${port} ${ipv6 ? "ip6" : "ip"} saddr != ${proxyHost} drop`
    return `nft 'add table inet noryx-firewall; add chain inet noryx-firewall forward { type filter hook forward priority -10; }; add rule inet noryx-firewall forward ${rule}'`
  }
  return `${ipv6 ? "ip6tables" : "iptables"} -I DOCKER-USER -p tcp -m conntrack --ctorigdstport ${port} --ctdir ORIGINAL ! -s ${proxyHost} -j DROP`
}

/** The host of a node's address, e.g. 203.0.113.10 of 203.0.113.10:7443. */
export function hostOf(address?: string) {
  if (!address) return undefined
  const host = address.slice(0, address.lastIndexOf(":"))
  return host.replace(/^\[|\]$/g, "")
}

/** The address players join at, e.g. 203.0.113.10:25565, or [2001:db8::10]:25565 for an IPv6 address. */
export const joinAddress = (host: string, port: number) => `${host.includes(":") ? `[${host}]` : host}:${port}`

/** The network each server is in, as its proxy or behind it; none without the permission to see networks. */
export function useNetworkOf() {
  const { data: networks } = useQuery({ ...networksQuery, enabled: useAccess().can("networks.view") })
  const byKey = new Map(networks?.flatMap((n) => [n.proxy, ...n.backends].map((ref) => [key(ref), n] as const)))
  return (ref: ServerRef) => byKey.get(key(ref))
}
