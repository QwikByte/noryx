import { useQuery } from "@tanstack/react-query"
import { t } from "i18next"
import { useState } from "react"
import { StatCard, StatStrip } from "@/components/stat-card"
import { Skeleton } from "@/components/ui/skeleton"
import { niceMax } from "@/lib/chart"
import { cn } from "@/lib/utils"
import { type LogBucket, type LogFilter, statsQuery } from "./api"
import { levels } from "./meta"
import { formatTime } from "@/lib/format"
import { locale } from "@/lib/i18n"

/** The stacked series, bottom first. Info stays quiet so that warnings and errors stand out. */
const series = [
  { key: "info", fill: "bg-muted-foreground/35" },
  { key: "warn", fill: "bg-warning" },
  { key: "error", fill: "bg-destructive" },
] as const

const count = new Intl.NumberFormat(locale)
const hourOf = (b: LogBucket) => formatTime(b.start, { hour: "2-digit", minute: "2-digit" })
const totalOf = (b: LogBucket) => b.debug + b.info + b.warn + b.error

/** Key figures and a chart of the last 24 hours, for the entries of a filter. */
export function LogOverview({ filter, onSelectHour }: { filter: LogFilter; onSelectHour: (start: Date) => void }) {
  const { data: buckets, isPlaceholderData } = useQuery(statsQuery(filter))
  if (!buckets) return <Skeleton className="h-80 rounded-xl" />
  const sum = (key: "warn" | "error") => buckets.reduce((n, b) => n + b[key], 0)
  return (
    <div className={cn("space-y-4 transition-opacity", isPlaceholderData && "opacity-60")}>
      <StatStrip className="grid-cols-3">
        <StatCard
          icon={levels.info.icon}
          tone="info"
          label={t("Entries")}
          value={count.format(buckets.reduce((n, b) => n + totalOf(b), 0))}
        >
          {t("last 24 hours")}
        </StatCard>
        <StatCard icon={levels.warn.icon} tone="warning" label={t("Warnings")} value={count.format(sum("warn"))}>
          {t("denied or failed")}
        </StatCard>
        <StatCard icon={levels.error.icon} tone="destructive" label={t("Errors")} value={count.format(sum("error"))}>
          {t("failures to look into")}
        </StatCard>
      </StatStrip>
      <HourChart buckets={buckets} onSelectHour={onSelectHour} />
    </div>
  )
}

function HourChart({ buckets, onSelectHour }: { buckets: LogBucket[]; onSelectHour: (start: Date) => void }) {
  const [active, setActive] = useState<number>()
  const max = niceMax(Math.max(...buckets.map(totalOf)))
  const shown = active === undefined ? undefined : buckets[active]

  return (
    <figure className="surface rounded-xl p-4 sm:p-5">
      <figcaption className="flex flex-wrap items-center justify-between gap-x-6 gap-y-2">
        <span className="text-sm font-semibold">{t("Entries per hour")}</span>
        <span className="flex flex-wrap gap-x-4 gap-y-1 text-xs text-muted-foreground">
          {series.toReversed().map(({ key, fill }) => {
            const { plural, icon: Icon } = levels[key]
            return (
              <span key={key} className="flex items-center gap-1.5">
                <span aria-hidden className={cn("size-2.5 rounded-[3px]", fill)} />
                <Icon aria-hidden className="size-3.5" />
                {t(plural)}
              </span>
            )
          })}
        </span>
      </figcaption>

      <div className="relative mt-5 grid grid-cols-[auto_1fr] gap-x-2">
        <div aria-hidden className="flex h-36 flex-col justify-between text-right text-[0.6875rem] text-muted-foreground tabular-nums">
          <span className="-translate-y-1/2">{count.format(max)}</span>
          <span>{Number.isInteger(max / 2) ? count.format(max / 2) : ""}</span>
          <span className="translate-y-1/2">0</span>
        </div>
        <div className="relative h-36" onPointerLeave={() => setActive(undefined)}>
          <div aria-hidden className="absolute inset-0 flex flex-col justify-between">
            {[0, 1, 2].map((i) => (
              <span key={i} className="h-px bg-border" />
            ))}
          </div>
          <div
            role="group"
            aria-label={t("Entries per hour, select an hour to show its entries")}
            className="relative flex h-full items-end gap-0.5"
          >
            {buckets.map((b, i) => (
              <button
                key={b.start}
                type="button"
                aria-label={t("{{hour}}: {{info}} info, {{warnings}} warnings, {{errors}} errors", {
                  hour: hourOf(b),
                  info: b.info + b.debug,
                  warnings: b.warn,
                  errors: b.error,
                })}
                onPointerEnter={() => setActive(i)}
                onFocus={() => setActive(i)}
                onBlur={() => setActive(undefined)}
                onClick={() => onSelectHour(new Date(b.start))}
                className="group flex h-full flex-1 items-end justify-center rounded-sm outline-none focus-visible:ring-2 focus-visible:ring-ring"
              >
                {totalOf(b) > 0 && (
                  <span
                    className="flex w-full max-w-6 flex-col-reverse gap-0.5 overflow-hidden rounded-t-[4px] transition-[filter] group-hover:brightness-110 group-focus-visible:brightness-110"
                    style={{ height: `max(${(totalOf(b) / max) * 100}%, 3px)` }}
                  >
                    {series.map(({ key, fill }) => {
                      const n = key === "info" ? b.info + b.debug : b[key]
                      return n > 0 && <span key={key} className={cn("min-h-px", fill)} style={{ flex: `${n} 0 0` }} />
                    })}
                  </span>
                )}
              </button>
            ))}
          </div>
          {shown && active !== undefined && (
            <div
              role="status"
              className="pointer-events-none absolute bottom-full z-10 mb-2 w-44 rounded-lg bg-popover p-3 text-xs text-popover-foreground shadow-lg ring-1 ring-foreground/10"
              // Centred over the hour, but kept within the chart.
              style={{ left: `clamp(0px, calc(${((active + 0.5) / buckets.length) * 100}% - 5.5rem), calc(100% - 11rem))` }}
            >
              <p className="mb-2 font-medium text-muted-foreground">{t("from {{hour}}", { hour: hourOf(shown) })}</p>
              {series.toReversed().map(({ key, fill }) => (
                <p key={key} className="flex items-center gap-2">
                  <span aria-hidden className={cn("h-0.5 w-3 rounded-full", fill)} />
                  <span className="font-semibold tabular-nums">{count.format(key === "info" ? shown.info + shown.debug : shown[key])}</span>
                  <span className="text-muted-foreground">{t(levels[key].plural)}</span>
                </p>
              ))}
            </div>
          )}
        </div>
        <div aria-hidden className="relative col-start-2 mt-2 h-4 text-[0.6875rem] text-muted-foreground tabular-nums">
          {buckets.map(
            (b, i) =>
              i % 6 === 0 && (
                <span
                  key={b.start}
                  className="absolute -translate-x-1/2 whitespace-nowrap"
                  style={{ left: `${((i + 0.5) / buckets.length) * 100}%` }}
                >
                  {hourOf(b)}
                </span>
              ),
          )}
        </div>
      </div>
    </figure>
  )
}
