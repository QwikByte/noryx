import { useQuery } from "@tanstack/react-query"
import { t } from "i18next"
import { ErrorCallout } from "@/components/callout"
import { Section } from "@/components/section"
import { Segmented } from "@/components/segmented"
import { Skeleton } from "@/components/ui/skeleton"
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table"
import { useChoice } from "@/features/preferences/api"
import { cn } from "@/lib/utils"
import { type HistoryPoint, type HistoryQuery, type UsagePoint, type UsageRange, ranges } from "./api"
import { type ChartSeries, TimeChart } from "./time-chart"
import { locale, timeZone } from "@/lib/i18n"

/** A chart of the history: one measure, so one scale. */
export interface ChartSpec<P extends HistoryPoint = UsagePoint> {
  title: string
  series: ChartSeries<P>[]
  format: (value: number) => string
  /** Top of the axis, e.g. a limit; 0 or missing rounds up the highest value. */
  max?: number
  nice?: (value: number) => number
}

/**
 * The history of a node, of one of its servers or of a datastore, as charts or a table. The
 * range applies to all of them; the range and the view chosen last apply again, also to other nodes, servers and
 * datastores.
 */
export function UsageHistory<P extends HistoryPoint>({
  history,
  charts,
  className,
}: {
  history: (range: UsageRange) => HistoryQuery<P>
  charts: ChartSpec<P>[]
  className?: string
}) {
  const [range, setRange] = useChoice("usageRange", "day")
  const [view, setView] = useChoice("usageView", "charts")
  const { data, error, isPlaceholderData, dataUpdatedAt } = useQuery(history(range))

  return (
    <Section
      className={className}
      title={t("History")}
      actions={
        <div className="flex flex-wrap gap-2">
          <Segmented
            label={t("Time range")}
            value={range}
            onChange={setRange}
            options={Object.entries(ranges).map(([value, r]) => ({ value: value as UsageRange, label: t(r.label) }))}
          />
          <Segmented
            label={t("View")}
            value={view}
            onChange={setView}
            options={[
              { value: "charts", label: t("Charts") },
              { value: "table", label: t("Table") },
            ]}
          />
        </div>
      }
    >
      {error ? (
        <ErrorCallout error={error} />
      ) : !data ? (
        <Skeleton className="h-64 rounded-xl" />
      ) : (
        // While another range loads, the current one stays, dimmed.
        <div className={cn("transition-opacity", isPlaceholderData && "opacity-60")}>
          {view === "table" ? (
            <UsageTable points={data.points} charts={charts} range={range} />
          ) : (
            <div className="grid gap-4 lg:grid-cols-2">
              {charts.map((chart) => (
                <TimeChart
                  key={chart.title}
                  {...chart}
                  points={data.points}
                  step={data.step}
                  span={ranges[range].span}
                  end={dataUpdatedAt}
                />
              ))}
            </div>
          )}
        </div>
      )}
    </Section>
  )
}

/** The values of the charts by step, newest first. */
function UsageTable<P extends HistoryPoint>({ points, charts, range }: { points: P[]; charts: ChartSpec<P>[]; range: UsageRange }) {
  const columns = charts.flatMap((c) =>
    c.series.map((s) => ({
      label: c.series.length > 1 ? t("{{chart}} {{series}}", { chart: c.title, series: s.label.toLowerCase() }) : c.title,
      value: s.value,
      format: c.format,
    })),
  )
  const time = (iso: string) =>
    new Date(iso).toLocaleString(
      locale,
      range === "day" ? { hour: "2-digit", minute: "2-digit", timeZone } : { dateStyle: "short", timeStyle: "short", timeZone },
    )
  return (
    <div className="surface max-h-[28rem] overflow-auto rounded-xl">
      <Table>
        <TableHeader className="sticky top-0 bg-card">
          <TableRow>
            <TableHead>{t("Time")}</TableHead>
            {columns.map((c) => (
              <TableHead key={c.label} className="text-right">
                {c.label}
              </TableHead>
            ))}
          </TableRow>
        </TableHeader>
        <TableBody>
          {points.toReversed().map((p) => (
            <TableRow key={p.time}>
              <TableCell className="text-muted-foreground">{time(p.time)}</TableCell>
              {columns.map((c) => {
                const v = c.value(p)
                return (
                  <TableCell key={c.label} className="text-right tabular-nums">
                    {v === null ? "–" : c.format(v)}
                  </TableCell>
                )
              })}
            </TableRow>
          ))}
          {points.length === 0 && (
            <TableRow>
              <TableCell colSpan={columns.length + 1} className="text-center text-muted-foreground">
                {t("Nothing recorded in this time")}
              </TableCell>
            </TableRow>
          )}
        </TableBody>
      </Table>
    </div>
  )
}
