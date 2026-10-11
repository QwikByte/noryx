import { CubeIcon, HardDrivesIcon, MemoryIcon, UsersThreeIcon } from "@phosphor-icons/react"
import { t } from "i18next"
import { Meter } from "@/components/meter"
import { AnimatedNumber } from "@/components/animated-number"
import { Sparkline } from "@/components/sparkline"
import { StatCard, StatStrip } from "@/components/stat-card"
import { useAccess } from "@/features/access/use-access"
import { onlineCapacityMb } from "@/features/nodes/api"
import { assignedMemoryMb, runningCount } from "@/features/servers/api"
import { StateBar } from "@/features/servers/server-state"
import { formatMegabytes } from "@/lib/format"
import { locale } from "@/lib/i18n"
import { useTrend } from "@/lib/use-trend"
import { useOverview } from "./overview"

const percent = new Intl.NumberFormat(locale, { style: "percent", maximumFractionDigits: 0 })

/** The players, servers, nodes and memory at a glance; players and CPU load show how they went while the panel is open. */
export function Figures({ title }: { title: string }) {
  const { canSomewhere } = useAccess()
  const { servers = [], nodes, online, players, load } = useOverview()
  const playerTrend = useTrend("players", players)
  const loadTrend = useTrend("load", load)
  const assignedMb = assignedMemoryMb(servers)
  const capacityMb = onlineCapacityMb(nodes)
  const seesServers = canSomewhere("servers.view")
  return (
    <StatStrip label={title} className="grid-cols-2 @4xl:grid-cols-4">
      <StatCard
        to={seesServers ? "/players" : undefined}
        // The players page lists those online then, rather than the list chosen last.
        search={{ tab: "online" }}
        icon={UsersThreeIcon}
        tone="info"
        label={t("Players online")}
        value={<AnimatedNumber value={players} />}
      >
        <Sparkline values={playerTrend} className="text-info" />
      </StatCard>
      <StatCard
        to={seesServers ? "/servers" : undefined}
        icon={CubeIcon}
        tone="success"
        label={t("Servers running")}
        value={
          <>
            <AnimatedNumber value={runningCount(servers)} /> / {servers.length}
          </>
        }
      >
        <StateBar servers={servers} />
      </StatCard>
      <StatCard
        to="/nodes"
        icon={HardDrivesIcon}
        tone="info"
        label={t("Nodes online")}
        value={
          <>
            <AnimatedNumber value={online.length} /> / {nodes.length}
          </>
        }
      >
        {load !== undefined && (
          <div className="space-y-1">
            <p className="flex justify-between gap-2">
              {t("CPU load")}
              <span className="tabular-nums">{percent.format(load)}</span>
            </p>
            <Sparkline values={loadTrend} max={1} className="text-info" />
          </div>
        )}
      </StatCard>
      <StatCard
        icon={MemoryIcon}
        tone="violet"
        label={t("Memory assigned")}
        value={<AnimatedNumber value={assignedMb} format={(v) => formatMegabytes(Math.round(v))} />}
      >
        {capacityMb > 0 && (
          <div className="space-y-2">
            <Meter value={assignedMb / capacityMb} label={t("Memory assigned on online nodes")} />
            <p>{t("of {{memory}} on online nodes", { memory: formatMegabytes(capacityMb) })}</p>
          </div>
        )}
      </StatCard>
    </StatStrip>
  )
}
