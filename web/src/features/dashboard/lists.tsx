import { CheckCircleIcon, GraphIcon, PushPinIcon, UsersThreeIcon, WarningCircleIcon } from "@phosphor-icons/react"
import { Link } from "@tanstack/react-router"
import { t } from "i18next"
import { AnimatePresence, motion } from "motion/react"
import { IconTile } from "@/components/icon-tile"
import { Meter } from "@/components/meter"
import { rise } from "@/lib/motion"
import { StatusDot } from "@/components/status"
import { playersOnline } from "@/features/networks/usage"
import type { Node } from "@/features/nodes/api"
import { CpuTrend } from "@/features/nodes/cpu-trend"
import { usePinned } from "@/features/preferences/api"
import { ServerActions } from "@/features/servers/server-actions"
import { StateBar } from "@/features/servers/server-state"
import { serverLook, statusOf } from "@/features/servers/server-types"
import { formatCores, formatNumber } from "@/features/usage/format"
import { formatBytes } from "@/lib/format"
import { cn } from "@/lib/utils"
import { HiddenMenu, HideMenu } from "./attention-menus"
import { chosen, type WidgetProps } from "./options"
import { useOverview, useProblems } from "./overview"
import { Calm, Panel } from "./panel"

/** A row of a widget that links somewhere. */
export const row = "transition-colors hover:bg-muted/50"

/** What needs an operator, the most urgent first, but what the user hid; solved and hidden problems fade away. */
export function Attention({ title }: { title: string }) {
  const { problems, hidden } = useProblems()
  return (
    <Panel title={title} count={problems.length} actions={hidden.length > 0 && <HiddenMenu problems={hidden} />}>
      {problems.length === 0 ? (
        <Calm icon={CheckCircleIcon} tone="success">
          {hidden.length > 0 ? t("Nothing needs attention but what you hid.") : t("Everything is running smoothly.")}
        </Calm>
      ) : (
        <ul className="divide-y">
          <AnimatePresence initial={false}>
            {problems.map((p, i) => (
              <motion.li key={p.key} layout {...rise(i)} exit={{ opacity: 0, x: 16 }} className={cn("group flex items-center pr-2", row)}>
                <Link {...p.link} className="flex min-w-0 flex-1 items-center gap-3 py-3 pl-4">
                  <IconTile icon={WarningCircleIcon} tone={p.tone} size="sm" />
                  <span className="min-w-0">
                    <span className="block truncate text-sm font-medium">{p.title}</span>
                    <span className="block truncate text-xs text-muted-foreground">{p.detail}</span>
                  </span>
                </Link>
                <HideMenu problem={p} />
              </motion.li>
            ))}
          </AnimatePresence>
        </ul>
      )}
    </Panel>
  )
}

/** The nodes, all or those chosen, with what they use now, and their CPU during the last day. */
export function Nodes({ title, options }: WidgetProps) {
  const { nodes, servers = [] } = useOverview()
  return (
    <Panel title={title} more={{ to: "/nodes", label: t("All nodes") }}>
      <ul className="divide-y">
        {chosen(nodes, options.nodes).map((n, i) => (
          <motion.li key={n.id} {...rise(i)}>
            <NodeRow node={n} servers={servers.filter((s) => s.nodeId === n.id).length} />
          </motion.li>
        ))}
      </ul>
    </Panel>
  )
}

function NodeRow({ node, servers }: { node: Node; servers: number }) {
  const live = useOverview().usages.node(node.id)
  const tone = node.status === "online" ? "success" : node.status === "pending" ? "warning" : "destructive"
  return (
    <Link to="/nodes/$nodeId" params={{ nodeId: node.id }} className={cn("block space-y-2 px-4 py-3", row)}>
      <span className="flex items-center gap-2 text-sm font-medium">
        <StatusDot status={{ tone, label: "" }} />
        <span className="flex-1 truncate">{node.name}</span>
        <CpuTrend node={node} className="w-16 shrink-0 [&_svg]:h-5" />
        <span className="text-xs font-normal whitespace-nowrap text-muted-foreground">
          {t("{{count}} servers", { count: servers, defaultValue_one: "{{count}} server" })}
        </span>
      </span>
      {live?.cpuCount && live.memoryTotalBytes ? (
        <span className="grid grid-cols-2 gap-3 text-xs text-muted-foreground">
          <Load
            label={t("CPU")}
            value={live.cpuMillis / (live.cpuCount * 1000)}
            detail={t("{{used}} of {{total}}", { used: formatNumber(live.cpuMillis / 1000), total: formatCores(live.cpuCount * 1000) })}
          />
          <Load label={t("Memory")} value={live.memoryUsedBytes / live.memoryTotalBytes} detail={formatBytes(live.memoryUsedBytes)} />
        </span>
      ) : (
        node.status !== "online" && <span className="block text-xs text-muted-foreground">{t("Not reachable")}</span>
      )}
    </Link>
  )
}

function Load({ label, value, detail }: { label: string; value: number; detail: string }) {
  return (
    <span className="space-y-1">
      <span className="flex justify-between gap-2">
        <span>{label}</span>
        <span className="truncate tabular-nums">{detail}</span>
      </span>
      <Meter value={value} label={label} />
    </span>
  )
}

/** The networks, all or those chosen, with the states of their servers and their players. */
export function Networks({ title, options }: WidgetProps) {
  const overview = useOverview()
  const { servers = [], usage } = overview
  const networks = chosen(overview.networks, options.networks)
  return (
    <Panel title={title} more={{ to: "/networks", label: t("All networks") }}>
      {networks.length === 0 ? (
        <Calm icon={GraphIcon} tone="violet">
          {t("No networks yet.")}
        </Calm>
      ) : (
        <ul className="divide-y">
          {networks.map((n, i) => {
            const backends = servers.filter((s) => n.backends.some((b) => b.nodeId === s.nodeId && b.serverId === s.id))
            return (
              <motion.li key={n.id} {...rise(i)}>
                <Link
                  to="/networks/$networkId"
                  params={{ networkId: n.id }}
                  className={cn("grid gap-3 px-4 py-3 @lg:grid-cols-[12rem_1fr_auto] @lg:items-center", row)}
                >
                  <span className="flex min-w-0 items-center gap-3">
                    <IconTile icon={GraphIcon} tone="violet" size="sm" />
                    <span className="truncate text-sm font-medium">{n.name}</span>
                  </span>
                  <StateBar servers={backends} />
                  <span className="text-sm text-muted-foreground tabular-nums @lg:text-right">
                    {t("{{count}} online", { count: playersOnline(n, usage) ?? 0 })}
                  </span>
                </Link>
              </motion.li>
            )
          })}
        </ul>
      )}
    </Panel>
  )
}

/** The servers with the most players, each with a bar of its share. */
export function TopServers({ title, options }: WidgetProps) {
  const { gameServers, usage, ref } = useOverview()
  const players = (s: (typeof gameServers)[number]) => usage(ref(s))?.players?.online ?? 0
  const busiest = gameServers
    .filter(players)
    .sort((a, b) => players(b) - players(a))
    .slice(0, Number(options.count))
  return (
    <Panel title={title}>
      {busiest.length === 0 ? (
        <Calm icon={UsersThreeIcon} tone="info">
          {t("No players online right now.")}
        </Calm>
      ) : (
        <ol className="divide-y">
          {busiest.map((s, i) => (
            <motion.li key={`${s.nodeId}/${s.id}`} layout {...rise(i)}>
              <Link
                to="/nodes/$nodeId/servers/$serverId"
                params={{ nodeId: s.nodeId, serverId: s.id }}
                className={cn("relative isolate flex items-center gap-3 px-4 py-2.5", row)}
              >
                <motion.span
                  aria-hidden
                  className="absolute inset-y-1 left-1 -z-10 rounded-lg bg-info/8"
                  initial={{ width: 0 }}
                  animate={{ width: `calc(${(players(s) / players(busiest[0])) * 100}% - 0.5rem)` }}
                  transition={{ duration: 0.6, ease: "easeOut" }}
                />
                <IconTile {...serverLook(s.type)} size="sm" />
                <span className="min-w-0 flex-1 truncate text-sm font-medium">{s.name}</span>
                <span className="text-sm font-semibold tabular-nums">{players(s)}</span>
              </Link>
            </motion.li>
          ))}
        </ol>
      )}
    </Panel>
  )
}

/** The servers the user pinned, with what they use and their actions at hand. */
export function Pinned({ title }: { title: string }) {
  const { pinned } = usePinned()
  const { servers = [], usage, ref } = useOverview()
  const shown = pinned.flatMap((p) => servers.find((s) => s.id === p.serverId) ?? [])
  return (
    <Panel title={title} more={{ to: "/servers", label: t("All servers") }}>
      {shown.length === 0 ? (
        <Calm icon={PushPinIcon}>{t("Pin servers with the pin on their card to keep them at hand, here and in the sidebar.")}</Calm>
      ) : (
        <ul className="divide-y">
          <AnimatePresence initial={false}>
            {shown.map((s, i) => {
              const live = usage(ref(s))
              const { icon: Icon, tone } = serverLook(s.type)
              const status = statusOf(s)
              return (
                <motion.li key={s.id} layout {...rise(i)} exit={{ opacity: 0, x: 16 }} className="flex items-center gap-3 px-4 py-2.5">
                  <span className="relative">
                    <IconTile icon={Icon} tone={tone} size="sm" />
                    <StatusDot status={status} label={t(status.label)} className="absolute -right-0.5 -bottom-0.5 ring-2 ring-card" />
                  </span>
                  <span className="min-w-0 flex-1">
                    <Link
                      to="/nodes/$nodeId/servers/$serverId"
                      params={{ nodeId: s.nodeId, serverId: s.id }}
                      className="block truncate text-sm font-medium hover:underline"
                    >
                      {s.name}
                    </Link>
                    <span className="block truncate text-xs text-muted-foreground">
                      {[
                        s.nodeName,
                        live?.running && formatCores(live.cpuMillis),
                        live?.players && t("{{count}} players", { count: live.players.online, defaultValue_one: "{{count}} player" }),
                      ]
                        .filter(Boolean)
                        .join(" · ")}
                    </span>
                  </span>
                  <ServerActions nodeId={s.nodeId} server={s} compact pin />
                </motion.li>
              )
            })}
          </AnimatePresence>
        </ul>
      )}
    </Panel>
  )
}
