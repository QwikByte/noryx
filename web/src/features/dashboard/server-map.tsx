import { HardDrivesIcon } from "@phosphor-icons/react"
import { Link } from "@tanstack/react-router"
import { t } from "i18next"
import { motion, useReducedMotionConfig } from "motion/react"
import { StatusDot, type Status } from "@/components/status"
import type { Tone } from "@/components/tone"
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip"
import type { Node } from "@/features/nodes/api"
import type { NodeServer } from "@/features/servers/api"
import { serverLook, serverStates, statusOf } from "@/features/servers/server-types"
import { locale } from "@/lib/i18n"
import { cn } from "@/lib/utils"
import { chosen, type WidgetProps } from "./options"
import { useOverview } from "./overview"
import { Calm, Panel } from "./panel"

/** Running servers are solid blocks, stopped ones hollow; those that need a look are amber or red. */
const blocks: Record<Tone, string> = {
  success: "bg-success text-card",
  warning: "bg-warning text-card",
  destructive: "bg-destructive text-card",
  neutral: "bg-muted text-muted-foreground ring-1 ring-inset ring-border",
  info: "bg-info text-card",
  violet: "bg-violet text-card",
}

const percent = new Intl.NumberFormat(locale, { style: "percent", maximumFractionDigits: 0 })

const nodeTone = (n: Node): Tone => (n.status === "online" ? "success" : n.status === "pending" ? "warning" : "destructive")

/**
 * Every server as a block in the row of its node, in the colour of its state, which opens it: what
 * runs where, and what doesn't, at a glance. It shows all nodes or those chosen.
 */
export function ServerMap({ title, options }: WidgetProps) {
  const overview = useOverview()
  const nodes = chosen(overview.nodes, options.nodes)
  const servers = (overview.servers ?? []).filter((s) => nodes.some((n) => n.id === s.nodeId))
  const running = servers.filter((s) => s.state === "running").length
  return (
    <Panel title={title} more={{ to: "/servers", label: t("All servers") }}>
      {nodes.length === 0 ? (
        <Calm icon={HardDrivesIcon} tone="info">
          {t("No nodes yet")}
        </Calm>
      ) : (
        <>
          <ul className="divide-y">
            {nodes.map((node, i) => (
              <NodeRow key={node.id} node={node} index={i} servers={servers.filter((s) => s.nodeId === node.id)} />
            ))}
          </ul>
          <div className="flex flex-wrap items-center gap-x-4 gap-y-1.5 border-t bg-muted/30 px-4 py-2.5 text-xs text-muted-foreground">
            <span className="font-medium text-foreground tabular-nums">
              {t("{{running}} of {{count}} running", { running, count: servers.length })}
            </span>
            <span aria-hidden className="ml-auto flex flex-wrap gap-x-4 gap-y-1.5">
              {(["running", "starting", "crashing", "stopped"] as const).map((state) => (
                <span key={state} className="inline-flex items-center gap-1.5">
                  <span className={cn("size-2.5 rounded-[2px]", blocks[serverStates[state].tone])} />
                  {t(serverStates[state].label)}
                </span>
              ))}
            </span>
          </div>
        </>
      )}
    </Panel>
  )
}

function NodeRow({ node, servers, index }: { node: Node; servers: NodeServer[]; index: number }) {
  const live = useOverview().usages.node(node.id)
  const status: Status = { tone: nodeTone(node), label: "" }
  const facts =
    live?.cpuCount && live.memoryTotalBytes
      ? [
          `${t("CPU")} ${percent.format(live.cpuMillis / (live.cpuCount * 1000))}`,
          `${t("Memory")} ${percent.format(live.memoryUsedBytes / live.memoryTotalBytes)}`,
        ]
      : node.status !== "online"
        ? [t("Not reachable")]
        : []
  return (
    <li className="grid gap-x-6 gap-y-3 px-4 py-3.5 @2xl:grid-cols-[14rem_1fr]">
      <div className="min-w-0">
        <Link
          to="/nodes/$nodeId"
          params={{ nodeId: node.id }}
          className="flex items-center gap-2 rounded-sm text-sm font-semibold outline-none hover:underline focus-visible:ring-2 focus-visible:ring-ring"
        >
          <StatusDot status={status} />
          <span className="truncate">{node.name}</span>
        </Link>
        {facts.length > 0 && <p className="mt-0.5 truncate pl-4 text-xs text-muted-foreground tabular-nums">{facts.join(" · ")}</p>}
      </div>
      {servers.length === 0 ? (
        <p className="self-center text-xs text-muted-foreground">{t("No servers on this node.")}</p>
      ) : (
        <ul aria-label={t("Servers on {{node}}", { node: node.name })} className="flex flex-wrap content-start gap-1.5">
          {servers.map((s, i) => (
            <Block key={s.id} server={s} delay={(index * 6 + i) * 0.015} />
          ))}
        </ul>
      )}
    </li>
  )
}

function Block({ server, delay }: { server: NodeServer; delay: number }) {
  const reduced = useReducedMotionConfig()
  const status = statusOf(server)
  const { icon: Icon } = serverLook(server.type)
  const label = `${server.name} · ${t(status.label)}`
  return (
    <motion.li initial={reduced ? false : { opacity: 0, scale: 0.6 }} animate={{ opacity: 1, scale: 1 }} transition={{ delay, duration: 0.25 }}>
      <Tooltip>
        <TooltipTrigger asChild>
          <Link
            to="/nodes/$nodeId/servers/$serverId"
            params={{ nodeId: server.nodeId, serverId: server.id }}
            aria-label={label}
            className={cn(
              "grid size-8 place-items-center rounded-[4px] outline-none transition-[transform,filter] hover:brightness-110 focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-card",
              blocks[status.tone],
              status.pulse && "animate-pulse motion-reduce:animate-none",
            )}
          >
            <Icon aria-hidden className="size-4" weight="bold" />
          </Link>
        </TooltipTrigger>
        <TooltipContent>{label}</TooltipContent>
      </Tooltip>
    </motion.li>
  )
}
