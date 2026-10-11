import { ChartLineIcon } from "@phosphor-icons/react"
import { useQueries, useQuery } from "@tanstack/react-query"
import { t } from "i18next"
import { Segmented } from "@/components/segmented"
import { Skeleton } from "@/components/ui/skeleton"
import { useAccess } from "@/features/access/use-access"
import { nodesQuery } from "@/features/nodes/api"
import { historyQuery, ranges, type UsagePoint, type UsageRange } from "@/features/usage/api"
import { formatCores } from "@/features/usage/format"
import { TimeChart } from "@/features/usage/time-chart"
import type { ChartSpec } from "@/features/usage/usage-history"
import { niceBytes } from "@/lib/chart"
import { formatBytes } from "@/lib/format"
import { chosen, type WidgetProps } from "./options"
import { Calm, Panel } from "./panel"

/**
 * What the nodes the user may see used together during the last day or week, all of them or those chosen, one
 * measure at a time, which the widget keeps.
 */
export function Resources({ title, options, setOption }: WidgetProps) {
  const { can } = useAccess()
  const measure = options.measure === "memory" ? "memory" : "cpu"
  const range: UsageRange = options.range === "week" ? "week" : "day"
  const { data: nodes = [] } = useQuery(nodesQuery)
  const all = nodes.filter((n) => n.info && can("nodes.view", n.id))
  const shown = chosen(all, options.nodes)
  const histories = useQueries({ queries: shown.map((n) => historyQuery(n.id, undefined, range)) })
  const loaded = histories.flatMap((h) => h.data ?? [])
  // The steps of all nodes start at the same times, so their values add up.
  const sums = new Map<string, UsagePoint>()
  for (const p of loaded.flatMap((h) => h.points)) {
    const sum = sums.get(p.time) ?? { ...p, cpuMillis: 0, memoryBytes: 0 }
    sums.set(p.time, { ...sum, cpuMillis: sum.cpuMillis + p.cpuMillis, memoryBytes: sum.memoryBytes + p.memoryBytes })
  }
  const points = [...sums.values()].sort((a, b) => Date.parse(a.time) - Date.parse(b.time))
  const total = (f: (info: NonNullable<(typeof shown)[number]["info"]>) => number) => shown.reduce((sum, n) => sum + (n.info ? f(n.info) : 0), 0)
  const of = { nodes: shown.length < all.length ? shown.map((n) => n.name).join(", ") : t("all nodes"), range: t(ranges[range].label) }
  const chart: ChartSpec =
    measure === "cpu"
      ? {
          title: t("CPU of {{nodes}}, last {{range}}", of),
          series: [{ label: t("CPU"), tone: "series-1", value: (p) => p.cpuMillis }],
          format: formatCores,
          max: total((i) => i.cpuCount * 1000),
        }
      : {
          title: t("Memory of {{nodes}}, last {{range}}", of),
          series: [{ label: t("Memory"), tone: "series-2", value: (p) => p.memoryBytes }],
          format: (v) => formatBytes(Math.round(v)),
          max: total((i) => i.memoryBytes),
          nice: niceBytes,
        }

  return (
    <Panel
      title={title}
      actions={
        <Segmented
          label={t("Measure")}
          value={measure}
          onChange={(m) => setOption("measure", m)}
          options={[
            { value: "cpu", label: t("CPU") },
            { value: "memory", label: t("Memory") },
          ]}
        />
      }
    >
      {shown.length === 0 ? (
        <Calm icon={ChartLineIcon} tone="info">
          {t("No node has reported its load yet.")}
        </Calm>
      ) : loaded.length === 0 ? (
        <Skeleton className="m-4 h-48 rounded-xl" />
      ) : (
        <TimeChart
          {...chart}
          points={points}
          step={loaded[0]?.step ?? 300}
          span={ranges[range].span}
          end={Math.max(...histories.map((h) => h.dataUpdatedAt))}
          className="rounded-none shadow-none ring-0"
        />
      )}
    </Panel>
  )
}
