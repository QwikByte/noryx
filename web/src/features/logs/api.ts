import { type InfiniteData, infiniteQueryOptions, keepPreviousData, queryOptions, useQueryClient } from "@tanstack/react-query"
import { useRef } from "react"
import { api } from "@/lib/api"
import type { CsvFormat } from "@/lib/csv"
import { useEventStream } from "@/lib/use-event-stream"

export type Level = "debug" | "info" | "warn" | "error"
export type Source = "master" | "agent"

/** An entry of the log of the master and its agents. */
export interface LogEntry {
  id: number
  time: string
  level: Level
  /** The master, or the agent of the entry's node. */
  source: Source
  category: string
  message: string
  /** Who did it, for actions in the panel. */
  user?: string
  nodeId?: string
  nodeName?: string
  serverId?: string
  serverName?: string
  attrs: Record<string, string>
}

/** Selects entries; the master only returns those about the nodes and servers the user may see. */
export interface LogFilter {
  /** The least important level. */
  level?: Level
  category?: string
  source?: Source
  user?: string
  node?: string
  server?: string
  search?: string
  /** ISO timestamps; until is exclusive. */
  since?: string
  until?: string
}

/** How many entries of each level an hour has. */
export interface LogBucket {
  start: string
  debug: number
  info: number
  warn: number
  error: number
}

const pageSize = 100
/** Entries kept in the list while new ones stream in. */
const maxLive = 1000
const statsEvery = 5_000

/** The query string of a filter and further parameters, without empty values. */
export function filterQuery(filter: LogFilter, extra: Record<string, string | undefined> = {}) {
  const params = new URLSearchParams()
  for (const [key, value] of Object.entries({ ...filter, ...extra })) if (value) params.set(key, value)
  return params.toString()
}

export const logsQuery = (filter: LogFilter) =>
  infiniteQueryOptions({
    queryKey: ["logs", "list", filter],
    queryFn: ({ pageParam }) =>
      api<LogEntry[]>(`/logs?${filterQuery(filter, { limit: String(pageSize), before: pageParam || undefined })}`),
    initialPageParam: "",
    // Older entries continue before the oldest one shown.
    getNextPageParam: (last) => (last.length >= pageSize ? String(last[last.length - 1].id) : undefined),
    placeholderData: keepPreviousData,
  })

/** Counts per hour of the last 24 hours; the time range of the filter doesn't apply. */
export const statsQuery = (filter: LogFilter) =>
  queryOptions({
    queryKey: ["logs", "stats", { ...filter, since: undefined, until: undefined }],
    queryFn: () => api<LogBucket[]>(`/logs/stats?${filterQuery({ ...filter, since: undefined, until: undefined })}`),
    placeholderData: keepPreviousData,
    refetchInterval: 60_000,
  })

/** Where the entries of a filter download as JSON lines, or as CSV in the user's format. */
export const exportUrl = (filter: LogFilter, format: "jsonl" | CsvFormat) =>
  `/api/logs/export?${filterQuery(filter, format === "jsonl" ? { format } : { format: "csv", separator: format.separator, bom: format.bom ? "on" : "off" })}`

/**
 * Calls onEntry with every new entry that matches the filter while enabled, and tells whether the stream is down,
 * i.e. not back after the 5 seconds in which the browser reconnects when the master ends it every few minutes.
 */
export function useLogStream(filter: LogFilter, onEntry: (entry: LogEntry) => void, enabled = true) {
  const query = filterQuery({ ...filter, since: undefined, until: undefined })
  const state = useEventStream(enabled ? `/logs/stream?${query}` : undefined, {
    onMessage: (event) => onEntry(JSON.parse(event.data) as LogEntry),
    graceMs: 8_000,
  })
  return enabled && (state === "lost" || state === "failed")
}

/** Adds new entries to the top of the list of a filter as they are logged, and tells whether their stream is down. */
export function useLiveLogs(filter: LogFilter, enabled: boolean) {
  const queryClient = useQueryClient()
  const statsAt = useRef(0)
  return useLogStream(
    filter,
    (entry) => {
      queryClient.setQueryData<InfiniteData<LogEntry[], string>>(logsQuery(filter).queryKey, (data) => {
        const [first = [], ...older] = data?.pages ?? []
        if (!data || (first.length > 0 && entry.id <= first[0].id)) return data
        const top = [entry, ...first]
        // A long session keeps the newest entries only; older ones load again on demand.
        if (top.length > maxLive) return { pages: [top.slice(0, maxLive)], pageParams: [""] }
        return { ...data, pages: [top, ...older] }
      })
      // A burst of entries updates the statistics once.
      if (Date.now() - statsAt.current > statsEvery) {
        statsAt.current = Date.now()
        void queryClient.invalidateQueries({ queryKey: ["logs", "stats"] })
      }
    },
    enabled,
  )
}
