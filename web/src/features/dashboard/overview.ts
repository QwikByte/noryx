import { useQuery, useQueryClient } from "@tanstack/react-query"
import { useEffect } from "react"
import { useAccess } from "@/features/access/use-access"
import { datastoresQuery } from "@/features/datastores/api"
import { networksQuery } from "@/features/networks/api"
import { useNetworkOf } from "@/features/networks/servers"
import { playersOnline } from "@/features/networks/usage"
import { nodesQuery } from "@/features/nodes/api"
import { overlayQuery } from "@/features/overlay/api"
import { preferencesQuery, useHidden } from "@/features/preferences/api"
import { allServersQuery, type NodeServer } from "@/features/servers/api"
import { serverType } from "@/features/servers/server-types"
import { useUsages, warningsQuery } from "@/features/usage/api"
import { useNow } from "@/lib/use-now"
import { isOf, type Problem, problemsOf } from "./attention"
import { useAutomationTasks } from "./tasks"

/** What the widgets of the overview show. Each widget loads it; the queries are shared. */
export function useOverview() {
  const access = useAccess()
  const { data: servers } = useQuery(allServersQuery)
  const { data: nodes = [] } = useQuery(nodesQuery)
  const { data: networks = [] } = useQuery({ ...networksQuery, enabled: access.can("networks.view") })
  const online = nodes.filter((n) => n.status === "online")
  const usages = useUsages(online.map((n) => n.id))
  const networkOf = useNetworkOf()
  const usage = (s: { nodeId: string; serverId: string }) => usages.server(s.nodeId, s.serverId)
  const ref = (s: NodeServer) => ({ nodeId: s.nodeId, serverId: s.id })
  const gameServers = (servers ?? []).filter((s) => !serverType(s.type).proxy)
  // A player counts once: in the network they joined, or on a server outside of networks.
  const players =
    networks.reduce((sum, n) => sum + (playersOnline(n, usage) ?? 0), 0) +
    gameServers.filter((s) => !networkOf(ref(s))).reduce((sum, s) => sum + (usage(ref(s))?.players?.online ?? 0), 0)
  // The share of the CPU cores of the online nodes in use, once they told it.
  const live = online.flatMap((n) => usages.node(n.id) ?? [])
  const cores = live.reduce((sum, u) => sum + u.cpuCount * 1000, 0)
  const load = cores ? live.reduce((sum, u) => sum + u.cpuMillis, 0) / cores : undefined
  return { servers, nodes, online, networks, usages, usage, ref, gameServers, players, load }
}

/** What needs an operator, the most urgent first: all of it, what the user hid for now, and the other problems. */
export function useProblems() {
  const access = useAccess()
  const { nodes, servers = [], networks, usages } = useOverview()
  const { data: overlay } = useQuery(overlayQuery)
  const { data: datastores } = useQuery({ ...datastoresQuery, enabled: access.can("datastores.view") })
  const tasks = useAutomationTasks()
  const { data: warnings } = useQuery(warningsQuery)
  const { hidden: items } = useHidden()
  // Renders again now and then, so that what was hidden for a while shows again on time.
  const now = useNow(true, 30_000)
  const all = problemsOf(nodes, servers, networks, usages, overlay, datastores, tasks, warnings)
  const isHidden = (p: Problem) => items.some((h) => isOf(h, p) && Date.parse(h.until) > now)
  return { all, hidden: all.filter(isHidden), problems: all.filter((p) => !isHidden(p)) }
}

/**
 * Forgets the items hidden until they change once they are gone from all problems or changed, e.g. a node that is back
 * online, so that they show when they come back. What is still loading would seem gone, so it waits until all the panel
 * shows has loaded.
 */
export function useForgetChanged(all: Problem[]) {
  const queryClient = useQueryClient()
  const { hidden, set } = useHidden()
  const changed = hidden.filter((h) => h.state && !all.some((p) => isOf(h, p)))
  const gone = changed.map((h) => `${h.key}:${h.state}`).join(" ")
  useEffect(() => {
    if (!gone) return
    const timer = setInterval(() => {
      if (queryClient.getQueryCache().findAll({ type: "active" }).some((q) => q.state.status === "pending")) return
      clearInterval(timer)
      // What the user hid in the meantime stays.
      const current = queryClient.getQueryData(preferencesQuery.queryKey)?.hidden ?? []
      set(current.filter((h) => !gone.split(" ").includes(`${h.key}:${h.state}`)))
    }, 5_000)
    return () => clearInterval(timer)
  }, [gone, queryClient, set])
}
