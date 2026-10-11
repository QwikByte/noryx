import { msg } from "@/lib/i18n"
import type { Level, LogFilter, Source } from "./api"

/** Time ranges the log page offers, with their length. */
export const ranges = {
  "1h": { label: msg("Last hour"), ms: 3_600_000 },
  "24h": { label: msg("Last 24 hours"), ms: 86_400_000 },
  "7d": { label: msg("Last 7 days"), ms: 7 * 86_400_000 },
  "30d": { label: msg("Last 30 days"), ms: 30 * 86_400_000 },
} as const

export type Range = keyof typeof ranges

/** The filter of the log page in its address, so that it can be shared and bookmarked. */
export interface LogSearch extends Omit<LogFilter, "since" | "until"> {
  range?: Range
  /**
   * A time from one ISO timestamp to another, e.g. an hour chosen in the chart, which replaces the range. Either end
   * may be open; to is exclusive.
   */
  from?: string
  to?: string
}

const levelNames: Level[] = ["debug", "info", "warn", "error"]
const sources: Source[] = ["master", "agent"]

/** Reads the filter from the address. Each key must be set explicitly, see the login route. */
export function validateLogSearch(search: Record<string, unknown>): LogSearch {
  const text = (key: string) => (typeof search[key] === "string" && search[key] ? search[key] : undefined)
  const pick = <T extends string>(key: string, allowed: readonly T[]) => allowed.find((v) => v === search[key])
  const time = (key: string) => {
    const ms = Date.parse(text(key) ?? "")
    return Number.isNaN(ms) ? undefined : new Date(ms).toISOString()
  }
  // Older addresses name the start of an hour chosen in the chart.
  const hour = time("hour")
  return {
    level: pick("level", levelNames),
    category: text("category"),
    source: pick("source", sources),
    user: text("user"),
    node: text("node"),
    server: text("server"),
    search: text("search"),
    range: pick("range", Object.keys(ranges) as Range[]),
    from: time("from") ?? hour,
    to: time("to") ?? (hour && new Date(Date.parse(hour) + 3_600_000).toISOString()),
  }
}

/** The time a search selects: from one time to another, either of which may be open, a range up to now, or all time. */
export function timeOf({ range, from, to }: LogSearch): Pick<LogFilter, "since" | "until"> {
  if (from || to) return { since: from, until: to }
  return range ? { since: new Date(Date.now() - ranges[range].ms).toISOString() } : {}
}
