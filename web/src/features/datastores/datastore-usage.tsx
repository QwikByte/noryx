import { ChartLineIcon, CpuIcon, type Icon, MemoryIcon, PlugsConnectedIcon } from "@phosphor-icons/react"
import { useQuery } from "@tanstack/react-query"
import { t } from "i18next"
import type { ReactNode } from "react"
import { Meter } from "@/components/meter"
import { Sparkline } from "@/components/sparkline"
import { Button } from "@/components/ui/button"
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle, DialogTrigger } from "@/components/ui/dialog"
import { datastoreHistoryQuery, type DatastorePoint, usageQuery } from "@/features/usage/api"
import { formatCores, formatNumber } from "@/features/usage/format"
import { type ChartSpec, UsageHistory } from "@/features/usage/usage-history"
import { niceBytes } from "@/lib/chart"
import { formatBytes } from "@/lib/format"
import type { Datastore } from "./api"

/** What a datastore uses now, with its last day as small lines, and its history over a day or a week in a dialog. */
export function DatastoreUsage({ datastore: ds }: { datastore: Datastore }) {
  const { data: latest } = useQuery(usageQuery(ds.nodeId))
  const { data: day } = useQuery(datastoreHistoryQuery(ds.id, "day"))
  const usage = latest?.datastores?.find((u) => u.id === ds.id)
  const points = day?.points ?? []
  const cpuLimit = usage?.cpuLimitMillis ?? ds.cpuMillis
  const memoryLimit = usage?.memoryLimitBytes || ds.memoryMb * 1024 ** 2
  // A datastore that started just now has no measurement until the agent takes the next one.
  const idle = ds.state === "stopped" ? t("Not running") : t("Not measured yet")

  const charts: ChartSpec<DatastorePoint>[] = [
    {
      title: t("CPU"),
      series: [{ label: t("CPU"), tone: "series-1", value: (p) => p.cpuMillis }],
      format: formatCores,
      max: cpuLimit,
    },
    {
      title: t("Memory"),
      series: [{ label: t("Memory"), tone: "series-1", value: (p) => p.memoryBytes }],
      format: (v) => formatBytes(Math.round(v)),
      max: memoryLimit,
    },
    { title: t("Connections"), series: [{ label: t("Connections"), tone: "series-1", value: (p) => p.connections }], format: formatNumber },
    {
      title: t("Data"),
      series: [{ label: t("Data"), tone: "series-1", value: (p) => p.diskBytes }],
      format: (v) => formatBytes(Math.round(v)),
      nice: niceBytes,
    },
  ]

  return (
    <section aria-label={t("Usage")} className="space-y-2">
      <div className="flex items-center justify-between gap-3">
        <h3 className="text-sm font-semibold">{t("Usage")}</h3>
        <Dialog>
          <DialogTrigger asChild>
            <Button size="sm" variant="ghost">
              <ChartLineIcon />
              {t("History")}
            </Button>
          </DialogTrigger>
          <DialogContent className="max-h-[90dvh] overflow-y-auto sm:max-w-4xl">
            <DialogHeader>
              <DialogTitle>{t("Usage of {{name}}", { name: ds.name })}</DialogTitle>
              <DialogDescription>{t("Measured every minute while it runs, and kept for a week.")}</DialogDescription>
            </DialogHeader>
            <UsageHistory className="mt-2" history={(range) => datastoreHistoryQuery(ds.id, range)} charts={charts} />
          </DialogContent>
        </Dialog>
      </div>
      <div className="grid gap-2 sm:grid-cols-3">
        <Measure
          icon={CpuIcon}
          label={t("CPU")}
          value={usage?.running ? formatCores(usage.cpuMillis) : "–"}
          trend={points.map((p) => p.cpuMillis)}
          max={cpuLimit}
        >
          {!usage?.running ? idle : cpuLimit ? t("of {{limit}}", { limit: formatCores(cpuLimit) }) : t("Not limited")}
        </Measure>
        <Measure
          icon={MemoryIcon}
          label={t("Memory")}
          value={usage?.running ? formatBytes(usage.memoryBytes) : "–"}
          trend={points.map((p) => p.memoryBytes)}
          max={memoryLimit}
        >
          {usage?.running ? (
            <div className="space-y-1.5">
              <Meter value={usage.memoryBytes / memoryLimit} label={t("Memory used of the limit")} />
              <p>{t("of {{limit}}", { limit: formatBytes(memoryLimit) })}</p>
            </div>
          ) : (
            idle
          )}
        </Measure>
        <Measure
          icon={PlugsConnectedIcon}
          label={t("Connections")}
          value={usage?.running && usage.connections !== undefined ? formatNumber(usage.connections) : "–"}
          trend={points.flatMap((p) => (p.connections === null ? [] : [p.connections]))}
        >
          {!usage?.running ? idle : usage.connections === undefined ? t("It can't tell yet") : t("of plugins and other clients")}
        </Measure>
      </div>
    </section>
  )
}

/** A measure of the datastore: its value now, and its last day as a small line. */
function Measure({
  icon: Icon,
  label,
  value,
  trend,
  max,
  children,
}: {
  icon: Icon
  label: string
  value: string
  trend: number[]
  max?: number
  children: ReactNode
}) {
  return (
    <div className="flex min-w-0 flex-col gap-1.5 rounded-lg p-3 ring-1 ring-border">
      <p className="flex items-center gap-1.5 text-xs font-medium text-muted-foreground">
        <Icon className="size-3.5" />
        {label}
      </p>
      <p className="text-lg font-semibold tabular-nums">{value}</p>
      <div title={t("The last 24 hours")}>
        <Sparkline values={trend} max={max} className="text-series-1" />
      </div>
      <div className="text-xs text-muted-foreground">{children}</div>
    </div>
  )
}
