import i18next from "i18next"
import { type KeyboardEvent, type PointerEvent, useState } from "react"
import { niceMax } from "@/lib/chart"
import { cn } from "@/lib/utils"
import type { HistoryPoint, UsagePoint } from "./api"
import { zoneOffset } from "@/lib/format"
import { locale, timeZone } from "@/lib/i18n"

/** The colour classes of the series, checked for colour vision deficiencies in both themes. */
const tones = {
  "series-1": { line: "stroke-series-1", area: "fill-series-1/10", key: "bg-series-1" },
  "series-2": { line: "stroke-series-2", area: "fill-series-2/10", key: "bg-series-2" },
}

export interface ChartSeries<P extends HistoryPoint = UsagePoint> {
  label: string
  tone: keyof typeof tones
  /** The value of a step; null where it is unknown. */
  value: (p: P) => number | null
}

// The plot is drawn in these units and stretched to its box; lines keep their width.
const W = 1000
const H = 100

/**
 * Lines of a history over time, from end - span to end. Steps that are more than a step
 * apart or have no value leave a gap, e.g. while a server was stopped. A crosshair shows
 * the values of a step, also with the arrow keys.
 */
export function TimeChart<P extends HistoryPoint>({
  title,
  points,
  step,
  span,
  end,
  series,
  format,
  max: fixedMax,
  nice = niceMax,
  className,
}: {
  title: string
  points: P[]
  /** Length of a step in seconds. */
  step: number
  span: number
  end: number
  series: ChartSeries<P>[]
  format: (value: number) => string
  /** Top of the axis, e.g. a limit; by default the highest value rounded up with nice. */
  max?: number
  nice?: (value: number) => number
  className?: string
}) {
  const [active, setActive] = useState<number>()
  const from = end - span
  const stepMs = step * 1000
  const max = fixedMax || nice(Math.max(0, ...points.flatMap((p) => series.map((s) => s.value(p) ?? 0))) || 1)
  const center = (p: P) => Date.parse(p.time) + stepMs / 2
  const x = (t: number) => ((t - from) / span) * W
  const y = (v: number) => H - (Math.min(v, max) / max) * H
  const shown = active === undefined ? undefined : points[active]

  function pick(event: PointerEvent<HTMLDivElement>) {
    const box = event.currentTarget.getBoundingClientRect()
    const t = from + ((event.clientX - box.left) / box.width) * span
    let nearest: number | undefined
    points.forEach((p, i) => {
      if (Math.abs(center(p) - t) <= stepMs && (nearest === undefined || Math.abs(center(p) - t) < Math.abs(center(points[nearest]) - t)))
        nearest = i
    })
    setActive(nearest)
  }

  function onKeyDown(event: KeyboardEvent) {
    const move = { ArrowLeft: -1, ArrowRight: 1, Home: -points.length, End: points.length }[event.key]
    if (move === undefined || points.length === 0) return
    event.preventDefault()
    setActive(Math.min(points.length - 1, Math.max(0, (active ?? points.length - 1) + move)))
  }

  return (
    <figure className={cn("surface min-w-0 rounded-xl p-4 sm:p-5", className)}>
      <figcaption className="flex flex-wrap items-center justify-between gap-x-4 gap-y-1">
        <span className="text-sm font-semibold">{title}</span>
        {series.length > 1 && (
          <span className="flex gap-4 text-xs text-muted-foreground">
            {series.map((s) => (
              <span key={s.label} className="flex items-center gap-1.5">
                <span aria-hidden className={cn("h-0.5 w-3 rounded-full", tones[s.tone].key)} />
                {s.label}
              </span>
            ))}
          </span>
        )}
      </figcaption>

      <div className="mt-4 grid grid-cols-[auto_1fr] gap-x-2">
        <div aria-hidden className="flex h-32 flex-col justify-between text-right text-[0.6875rem] text-muted-foreground tabular-nums">
          <span className="-translate-y-1/2">{format(max)}</span>
          <span>{format(max / 2)}</span>
          <span className="translate-y-1/2">{format(0)}</span>
        </div>
        <div
          role="group"
          tabIndex={0}
          aria-label={i18next.t("{{title}}, {{count}} values. Use the arrow keys to read them.", {
            title,
            count: points.length,
            defaultValue_one: "{{title}}, {{count}} value. Use the arrow keys to read it.",
          })}
          className="relative h-32 rounded-sm outline-none focus-visible:ring-2 focus-visible:ring-ring"
          onPointerMove={pick}
          onPointerLeave={() => setActive(undefined)}
          onKeyDown={onKeyDown}
          onBlur={() => setActive(undefined)}
        >
          <div aria-hidden className="absolute inset-0 flex flex-col justify-between">
            {[0, 1, 2].map((i) => (
              <span key={i} className="h-px bg-border" />
            ))}
          </div>
          {points.length === 0 ? (
            <p className="absolute inset-0 grid place-items-center text-xs text-muted-foreground">
              {i18next.t("Nothing recorded in this time")}
            </p>
          ) : (
            <svg aria-hidden viewBox={`0 0 ${W} ${H}`} preserveAspectRatio="none" className="absolute inset-0 size-full overflow-visible">
              {series.map((s) =>
                runs(points, s, stepMs).map((run, i) => {
                  const coords = (run.length === 1 ? [run[0], run[0]] : run).map((p, j) => {
                    const t = run.length === 1 ? Date.parse(p.time) + j * stepMs : center(p)
                    return `${x(t).toFixed(1)} ${y(s.value(p) ?? 0).toFixed(2)}`
                  })
                  const line = `M${coords.join("L")}`
                  const [first, last] = [coords[0].split(" ")[0], coords.at(-1)?.split(" ")[0]]
                  return (
                    <g key={`${s.label}-${i}`}>
                      <path d={`${line}L${last} ${H}L${first} ${H}Z`} className={tones[s.tone].area} />
                      <path
                        d={line}
                        fill="none"
                        strokeWidth={2}
                        strokeLinecap="round"
                        strokeLinejoin="round"
                        vectorEffect="non-scaling-stroke"
                        className={tones[s.tone].line}
                      />
                    </g>
                  )
                }),
              )}
            </svg>
          )}
          {shown && (
            <>
              <span
                aria-hidden
                className="pointer-events-none absolute inset-y-0 w-px bg-foreground/30"
                style={{ left: `${(x(center(shown)) / W) * 100}%` }}
              />
              {series.map((s) => {
                const v = s.value(shown)
                return (
                  v !== null && (
                    <span
                      key={s.label}
                      aria-hidden
                      className={cn(
                        "pointer-events-none absolute size-2.5 -translate-1/2 rounded-full ring-2 ring-card",
                        tones[s.tone].key,
                      )}
                      style={{ left: `${(x(center(shown)) / W) * 100}%`, top: `${(y(v) / H) * 100}%` }}
                    />
                  )
                )
              })}
              <div
                role="status"
                className="pointer-events-none absolute bottom-full z-10 mb-2 w-44 rounded-lg bg-popover p-3 text-xs text-popover-foreground shadow-lg ring-1 ring-foreground/10"
                style={{ left: `clamp(0px, calc(${(x(center(shown)) / W) * 100}% - 5.5rem), calc(100% - 11rem))` }}
              >
                <p className="text-muted-foreground">{formatTime(Date.parse(shown.time), span)}</p>
                {series.map((s) => {
                  const v = s.value(shown)
                  return (
                    <p key={s.label} className="mt-1.5 flex items-center gap-2">
                      <span aria-hidden className={cn("h-0.5 w-3 shrink-0 rounded-full", tones[s.tone].key)} />
                      <span className="font-semibold tabular-nums">{v === null ? "–" : format(v)}</span>
                      <span className="truncate text-muted-foreground">{s.label}</span>
                    </p>
                  )
                })}
              </div>
            </>
          )}
        </div>
        <div aria-hidden className="relative col-start-2 mt-1.5 h-4 text-[0.6875rem] text-muted-foreground">
          {ticks(from, end, span).map((t, i) => (
            // Every other one on small screens, so that they don't overlap.
            <span
              key={t}
              className={cn("absolute -translate-x-1/2 whitespace-nowrap", i % 2 === 1 && "max-sm:hidden")}
              style={{ left: `${((t - from) / span) * 100}%` }}
            >
              {formatTick(t, span)}
            </span>
          ))}
        </div>
      </div>
    </figure>
  )
}

/** Splits the points into runs of consecutive steps with a value. */
function runs<P extends HistoryPoint>(points: P[], s: ChartSeries<P>, stepMs: number) {
  const out: P[][] = []
  let prev: P | undefined
  for (const p of points) {
    if (s.value(p) === null) {
      prev = undefined
      continue
    }
    if (prev && Date.parse(p.time) - Date.parse(prev.time) <= stepMs * 1.5) out.at(-1)?.push(p)
    else out.push([p])
    prev = p
  }
  return out
}

const day = 24 * 3_600_000
const week = 7 * day
const month = 31 * day

/**
 * Every 6 hours for a day, every midnight for a week, every fifth for a month and the first of every month for longer,
 * in the viewer's time zone, away from the edges.
 */
function ticks(from: number, to: number, span: number) {
  const offset = zoneOffset(from)
  const all: number[] = []
  if (span > month) {
    // Ranges cover a year at most.
    const start = new Date(from + offset)
    for (let m = 1; m <= 12; m++) all.push(Date.UTC(start.getUTCFullYear(), start.getUTCMonth() + m, 1) - offset)
  } else {
    const step = span <= day ? day / 4 : span <= week ? day : 5 * day
    for (let t = Math.ceil((from + offset) / step) * step - offset; t < to; t += step) all.push(t)
  }
  return all.filter((t) => t - from > span * 0.04 && to - t > span * 0.04)
}

function formatTick(t: number, span: number) {
  const date = new Date(t)
  if (span <= day) return date.toLocaleTimeString(locale, { hour: "2-digit", minute: "2-digit", timeZone })
  return date.toLocaleDateString(locale, span <= month ? { month: "short", day: "numeric", timeZone } : { month: "short", timeZone })
}

function formatTime(t: number, span: number) {
  const date = new Date(t)
  if (span <= day) return date.toLocaleTimeString(locale, { hour: "2-digit", minute: "2-digit", timeZone })
  if (span <= week) return date.toLocaleString(locale, { weekday: "short", hour: "2-digit", minute: "2-digit", timeZone })
  return date.toLocaleString(locale, { month: "short", day: "numeric", hour: "2-digit", minute: "2-digit", timeZone })
}
