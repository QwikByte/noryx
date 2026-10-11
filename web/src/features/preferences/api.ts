import { queryOptions, useIsMutating, useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { useEffect } from "react"
import { toast } from "sonner"
import type { UserSort } from "@/features/access/browse"
import type { Level, LogEntry } from "@/features/logs/api"
import type { NetworkSort } from "@/features/networks/browse"
import type { NodeSort } from "@/features/nodes/browse"
import type { PlayerSearch } from "@/features/players/search"
import type { Sort as PluginSort, Source as PluginSource } from "@/features/plugins/api"
import type { Grouping, Sort, View } from "@/features/servers/browse"
import type { ServerTab } from "@/features/servers/tabs"
import { defaultsQuery } from "@/features/settings/public"
import type { UsageRange } from "@/features/usage/api"
import { api } from "@/lib/api"
import { type CsvFormat, languageSeparator } from "@/lib/csv"
import { chooseFormats, type Formats, msg, setDefaultLanguage } from "@/lib/i18n"
import type { Order } from "@/lib/sort"
import { type Accent, type Density, type Motion, setLook, type Theme, useDark, type Width } from "@/lib/theme"

/** A widget of the overview and how many of its three columns it spans on large screens. */
export interface Widget {
  id: string
  columns: 1 | 2 | 3
  hidden?: boolean
  /**
   * What the user chose for it, e.g. { count: "10" }, with IDs of nodes and networks separated by commas; the widget has
   * its defaults for the others.
   */
  options?: Record<string, string>
}

/** An item of Needs attention that the user hid until a time, or before that until it changes from the state it had. */
export interface HiddenItem {
  key: string
  state?: string
  until: string
}

export interface ServerRef {
  nodeId: string
  serverId: string
}

/**
 * The user's other choices. The master takes the same keys and values (internal/master/preference);
 * those the user never made follow the browser.
 */
export interface Settings extends Formats {
  theme?: Theme
  accent?: Accent
  density?: Density
  /** How lists of servers are shown where their address doesn't say. */
  serverView?: View
  serverSort?: Sort
  serverOrder?: Order
  serverGroup?: Grouping
  /** The size of the text of the console, the terminal and the editor, and whether they follow the colour theme or stay dark. */
  codeSize?: "small" | "medium" | "large"
  codeTheme?: "dark" | "panel"
  /** Whether the console and the terminal wrap long lines; the console does unless chosen, the terminal doesn't. */
  consoleWrap?: "wrap" | "scroll"
  terminalWrap?: "scroll" | "wrap"
  consoleTimes?: "hide" | "show"
  /** How many lines the console keeps. */
  consoleLines?: "2000" | "5000" | "10000"
  consoleFilter?: "all" | "problems"
  editorWrap?: "off" | "on"
  /** Two or four spaces, or tabs; YAML always indents with spaces. */
  editorIndent?: "2" | "4" | "tab"
  editorKeys?: "standard" | "vim"
  csvSeparator?: CsvFormat["separator"]
  csvBom?: "off" | "on"
  /** What the user chose last in lists, which applies where the address doesn't say, see useChoice. */
  playerTab?: NonNullable<PlayerSearch["tab"]>
  playerSort?: PlayerSearch["sort"]
  playerOrder?: Order
  pluginSource?: PluginSource
  pluginSort?: PluginSort
  /** The software whose plugins and whose mods the plugin search finds, or "all". */
  pluginType?: string
  modType?: string
  serverPluginSort?: "name" | "size"
  usageRange?: UsageRange
  usageView?: "charts" | "table"
  /** The least level the log shows where its address names none; without, it shows all. */
  logLevel?: Exclude<Level, "debug">
  /** Where the panel opens, the address of a page of the sidebar, and the tab a server opens on; both only if the user may see them. */
  startPage?: string
  serverTab?: ServerTab
  /** Whether pages fill wide screens and move less whatever the operating system asks; the browser keeps them like the theme. */
  width?: Width
  motion?: Motion
  /** Whether shortcuts of single keys work; Ctrl+K always does. */
  shortcuts?: "on" | "off"
  /** Whether a single server stops or restarts right away, after asking or with a warning to its players, and whether only with players. */
  power?: "now" | "ask" | "warn"
  powerWhen?: "always" | "players"
  /** The columns that tables of servers show besides the server and its state, separated by commas; empty for none. */
  serverColumns?: string
  /** How the lists of nodes, networks and users are shown and sorted where their address doesn't say. */
  nodeView?: View
  nodeSort?: NodeSort
  nodeOrder?: Order
  networkSort?: NetworkSort
  networkOrder?: Order
  userSort?: UserSort
  userOrder?: Order
}

/** A change of settings; null takes one back to the browser's. */
export type SettingsChange = { [K in keyof Settings]?: Settings[K] | null }

/**
 * Which new warnings and errors pop up as toasts and on the desktop: those of at least a level, and only
 * those of pinned servers and of the chosen nodes, servers and categories if only is set, unless quietUntil
 * hasn't passed yet. The bell lists all of them either way.
 */
export interface Alerts {
  level: "warn" | "error"
  only: boolean
  pinned: boolean
  nodes: string[]
  servers: string[]
  categories: string[]
  quietUntil?: string
}

/** What the signed-in user set up in the panel; the master keeps it, so it applies in all their browsers. */
export interface Preferences {
  /** The order of the widgets of the overview; empty shows the default layout. */
  dashboard: Widget[]
  pinned: ServerRef[]
  alerts: Alerts
  settings: Settings
  hidden: HiddenItem[]
  /** The groups of server lists that the user folded away, e.g. "network/<id>", in all lists of servers. */
  folded: string[]
  /** The views of the servers page that the user saved, in their order. */
  views: SavedView[]
}

/** A view of the servers page that the user saved by name, with the search of its address, which is checked again when it is shown. */
export interface SavedView {
  name: string
  search: Record<string, string>
}

export const preferencesQuery = queryOptions({
  queryKey: ["preferences"],
  queryFn: () => api<Preferences>("/preferences"),
  // Loaded again now and then, e.g. when the window gets the focus, for changes in other tabs.
  staleTime: 30_000,
})

/**
 * Changes a part of the preferences right away and stores it. Changes are sent one after the
 * other, so that the last one wins, and a failed one is undone.
 */
function useChange<K extends keyof Preferences>(part: K, field?: string) {
  const queryClient = useQueryClient()
  const key = preferencesQuery.queryKey
  return useMutation({
    scope: { id: `preferences/${part}` },
    mutationFn: (value: Preferences[K]) => api<Preferences>(`/preferences/${part}`, { method: "PUT", body: field ? { [field]: value } : value }),
    onMutate: async (value) => {
      await queryClient.cancelQueries({ queryKey: key })
      const before = queryClient.getQueryData(key)
      if (before) queryClient.setQueryData(key, { ...before, [part]: value })
      return { before }
    },
    onError: (error, _, context) => {
      if (context?.before) queryClient.setQueryData(key, context.before)
      toast.error(error.message)
    },
  })
}

export const useSetDashboard = () => useChange("dashboard", "widgets")

/** The items of Needs attention the user hid, and a change of them. */
export function useHidden() {
  const { data } = useQuery(preferencesQuery)
  const set = useChange("hidden", "items")
  return { hidden: data?.hidden ?? [], set: set.mutate }
}

export const defaultAlerts: Alerts = { level: "warn", only: false, pinned: false, nodes: [], servers: [], categories: [] }

/** How long the user can keep warnings and errors from popping up. */
export const quietHours = [1, 8, 24]

/** Whether the user keeps warnings and errors from popping up now. */
export const quiet = (alerts: Alerts, now = Date.now()) => !!alerts.quietUntil && Date.parse(alerts.quietUntil) > now

/** Whether a new entry pops up as the user chose: of at least their level, of what they chose if they did, and not while quiet. */
export function popsUp(e: LogEntry, alerts: Alerts, pinned: ServerRef[]) {
  if (quiet(alerts) || (alerts.level === "error" && e.level !== "error")) return false
  if (!alerts.only) return true
  const server = e.serverId ?? ""
  return (
    (alerts.pinned && pinned.some((p) => p.serverId === server)) ||
    alerts.servers.includes(server) ||
    alerts.nodes.includes(e.nodeId ?? "") ||
    alerts.categories.includes(e.category)
  )
}

/** The alerts the user chose, and a change of them. */
export function useAlerts() {
  const { data } = useQuery(preferencesQuery)
  const set = useChange("alerts")
  const alerts = data?.alerts ?? defaultAlerts
  return { alerts, change: (change: Partial<Alerts>) => set.mutate({ ...alerts, ...change }) }
}

/**
 * The servers the user pinned, and a toggle that pins or unpins one. Servers are found by
 * their ID, which stays the same when they move to another node.
 */
export function usePinned() {
  const { data } = useQuery(preferencesQuery)
  const set = useChange("pinned", "servers")
  const pinned = data?.pinned ?? []
  const isPinned = (serverId: string) => pinned.some((p) => p.serverId === serverId)
  return {
    pinned,
    isPinned,
    toggle: (s: ServerRef) => set.mutate(isPinned(s.serverId) ? pinned.filter((p) => p.serverId !== s.serverId) : [...pinned, s]),
  }
}

const settingsKey = ["preferences", "settings"]

/**
 * The user's settings, and a change of some of them. It shows right away, and changes are
 * stored one after the other with only their own keys, so that changes in other browsers stay.
 */
export function useSettings() {
  const queryClient = useQueryClient()
  const key = preferencesQuery.queryKey
  const { data } = useQuery(preferencesQuery)
  const { mutate } = useMutation({
    mutationKey: settingsKey,
    scope: { id: "preferences/settings" },
    mutationFn: (change: SettingsChange) => api<Preferences>("/preferences/settings", { method: "PATCH", body: change }),
    onMutate: async (change) => {
      await queryClient.cancelQueries({ queryKey: key })
      // What the change replaces comes back if it fails, before the panel applies it, e.g. reloads for another clock.
      const settings = queryClient.getQueryData(key)?.settings ?? {}
      const before: SettingsChange = Object.fromEntries(Object.keys(change).map((k) => [k, settings[k as keyof Settings] ?? null]))
      queryClient.setQueryData(key, (p) => p && { ...p, settings: merge(p.settings, change) })
      return { before }
    },
    onError: (error, _, context) => {
      if (context) queryClient.setQueryData(key, (p) => p && { ...p, settings: merge(p.settings, context.before) })
      toast.error(error.message)
      void queryClient.invalidateQueries({ queryKey: key })
    },
  })
  return {
    settings: data?.settings ?? {},
    change: (change: SettingsChange) => {
      setLook(change)
      mutate(change)
    },
  }
}

/** Settings with a change, without those it takes back. */
const merge = (settings: Settings, change: SettingsChange): Settings =>
  Object.fromEntries(Object.entries({ ...settings, ...change }).filter(([, v]) => v !== null))

/** How the user wants CSV files: with the separator of their language unless they chose one. */
export function useCsvFormat(): CsvFormat {
  const { settings } = useSettings()
  return { separator: settings.csvSeparator ?? languageSeparator, bom: settings.csvBom === "on" }
}

/**
 * Applies the signed-in user's settings in this browser, which keeps them for the next visit, and the panel's
 * defaults where the user chose none.
 */
export function useApplySettings() {
  const { data } = useQuery(preferencesQuery)
  const { data: defaults } = useQuery(defaultsQuery)
  // Other formats of times reload the panel, so it waits until the changes are stored.
  const storing = useIsMutating({ mutationKey: settingsKey }) > 0
  const { theme, accent, density, width, motion, clock, timeZone, times, weekStart, codeSize, codeTheme } = { ...defaults, ...data?.settings }
  useEffect(() => setLook({ theme, accent, density, width, motion }), [theme, accent, density, width, motion])
  // The console, the terminal and the editor take their size and colours from <html>, see index.css.
  useEffect(() => {
    const root = document.documentElement.dataset
    root.codeSize = codeSize ?? "small"
    root.codeTheme = codeTheme ?? "dark"
  }, [codeSize, codeTheme])
  // The defaults are waited for too, so that a reload for the default language takes the formats along, and
  // the formats this browser shows stay while the defaults can't be loaded.
  const loaded = data !== undefined && defaults !== undefined
  const language = defaults?.language ?? ""
  useEffect(() => {
    if (!loaded || storing) return
    setDefaultLanguage(language)
    chooseFormats({ clock, timeZone, times, weekStart })
  }, [loaded, storing, language, clock, timeZone, times, weekStart])
}

/** The sizes of the text of the console, the terminal and the editor. */
export const codeSizes = [
  { value: "small", label: msg("Small") },
  { value: "medium", label: msg("Medium") },
  { value: "large", label: msg("Large") },
] satisfies { value: NonNullable<Settings["codeSize"]>; label: string }[]

/** Where each of them wraps long lines: the console unless chosen otherwise, the terminal and the editor only if chosen. */
export const wraps = {
  consoleWrap: { on: (s: Settings) => s.consoleWrap !== "scroll", set: (on: boolean): Settings => ({ consoleWrap: on ? "wrap" : "scroll" }) },
  terminalWrap: { on: (s: Settings) => s.terminalWrap === "wrap", set: (on: boolean): Settings => ({ terminalWrap: on ? "wrap" : "scroll" }) },
  editorWrap: { on: (s: Settings) => s.editorWrap === "on", set: (on: boolean): Settings => ({ editorWrap: on ? "on" : "off" }) },
}

/** Whether the console, the terminal and the editor are dark: in both themes, unless the user lets them follow it. */
export function useCodeDark() {
  const dark = useDark()
  return useSettings().settings.codeTheme !== "panel" || dark
}

/**
 * A choice of a list, e.g. its sort: the address's wins, otherwise the one the user chose last applies, in all their
 * browsers, else fallback. choose keeps another one.
 */
export function useChoice<K extends keyof Settings>(key: K, fallback: NonNullable<Settings[K]>, address?: Settings[K]) {
  const { settings, change } = useSettings()
  return [address ?? settings[key] ?? fallback, (value: NonNullable<Settings[K]>) => change({ [key]: value } as SettingsChange)] as const
}
/** Stores the groups of server lists that the user folded away, and the views of the servers page they saved. */
export const useSetFolded = () => useChange("folded", "groups")
export const useSetViews = () => useChange("views", "views")
