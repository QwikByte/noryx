import { type ServerUsage, useUsages } from "@/features/usage/api"
import type { Network, ServerRef } from "./api"

/** The latest usage of the servers of networks. */
export function useNetworkUsage(networks: Network[] = []) {
  const usages = useUsages(networks.flatMap((n) => [n.proxy, ...n.backends]).map((r) => r.nodeId))
  return (ref: ServerRef) => usages.server(ref.nodeId, ref.serverId)
}

/**
 * The players online in a network: those on its servers, as the Players page counts them, or
 * else those its proxy counts, e.g. while none of its servers could be counted. BungeeCord
 * doesn't answer the panel's status requests.
 */
export function playersOnline(network: Network, usage: (ref: ServerRef) => ServerUsage | undefined) {
  const counts = network.backends.map((b) => usage(b)?.players?.online)
  if (counts.some((c) => c !== undefined)) return counts.reduce<number>((sum, c) => sum + (c ?? 0), 0)
  return usage(network.proxy)?.players?.online
}
