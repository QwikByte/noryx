import {
  CalendarCheckIcon,
  ChartBarIcon,
  ChartLineIcon,
  GraphIcon,
  HardDrivesIcon,
  type Icon,
  LightningIcon,
  PushPinIcon,
  ScrollIcon,
  SquaresFourIcon,
  UsersThreeIcon,
  WarningCircleIcon,
} from "@phosphor-icons/react"
import type { ReactNode } from "react"
import type { Access } from "@/features/access/use-access"
import type { Widget } from "@/features/preferences/api"
import { ranges } from "@/features/usage/api"
import { msg } from "@/lib/i18n"
import { RecentActivity } from "./activity"
import { Figures } from "./figures"
import { Attention, Networks, Nodes, Pinned, TopServers } from "./lists"
import { QuickActions } from "./quick-actions"
import { Resources } from "./resources"
import { Schedules } from "./schedules"
import { ServerMap } from "./server-map"
import type { WidgetOption, WidgetProps } from "./options"

export interface WidgetDef {
  id: string
  /** In English, marked with msg; translated where it shows. */
  title: string
  icon: Icon
  /** How many of the three columns it spans unless the user chose otherwise. */
  columns: Widget["columns"]
  visible: (a: Access) => boolean
  /** What the user can choose for it in Customize. */
  options?: WidgetOption[]
  Component: (props: WidgetProps) => ReactNode
}

const seesServers = (a: Access) => a.canSomewhere("servers.view")

/** How many entries a widget lists. */
const entries = (fallback: string, values = ["5", "10", "20"]): WidgetOption => ({
  key: "count",
  label: msg("Entries"),
  values: values.map((value) => ({ value })),
  default: fallback,
})
const nodes: WidgetOption = { key: "nodes", label: msg("Nodes") }

/** The widgets of the overview, in their default order. */
export const widgets: WidgetDef[] = [
  { id: "figures", title: msg("Key figures"), icon: ChartBarIcon, columns: 3, visible: () => true, Component: Figures },
  {
    id: "server-map",
    title: msg("Server map"),
    icon: SquaresFourIcon,
    columns: 3,
    visible: (a) => a.canSomewhere("nodes.view") || seesServers(a),
    options: [nodes],
    Component: ServerMap,
  },
  { id: "attention", title: msg("Needs attention"), icon: WarningCircleIcon, columns: 2, visible: () => true, Component: Attention },
  {
    id: "nodes",
    title: msg("Nodes"),
    icon: HardDrivesIcon,
    columns: 1,
    visible: (a) => a.canSomewhere("nodes.view") || seesServers(a),
    options: [nodes],
    Component: Nodes,
  },
  {
    id: "resources",
    title: msg("Node load"),
    icon: ChartLineIcon,
    columns: 2,
    visible: (a) => a.canSomewhere("nodes.view"),
    options: [
      {
        key: "measure",
        label: msg("Measure"),
        values: [
          { value: "cpu", label: msg("CPU") },
          { value: "memory", label: msg("Memory") },
        ],
        default: "cpu",
      },
      { key: "range", label: msg("Time range"), values: Object.entries(ranges).map(([value, r]) => ({ value, label: r.label })), default: "day" },
      nodes,
    ],
    Component: Resources,
  },
  { id: "pinned", title: msg("Pinned servers"), icon: PushPinIcon, columns: 1, visible: seesServers, Component: Pinned },
  {
    id: "networks",
    title: msg("Networks"),
    icon: GraphIcon,
    columns: 2,
    visible: (a) => a.can("networks.view"),
    options: [{ key: "networks", label: msg("Networks") }],
    Component: Networks,
  },
  {
    id: "top-servers",
    title: msg("Most players"),
    icon: UsersThreeIcon,
    columns: 1,
    visible: seesServers,
    options: [entries("5")],
    Component: TopServers,
  },
  {
    id: "activity",
    title: msg("Recent activity"),
    icon: ScrollIcon,
    columns: 2,
    visible: (a) => a.canSomewhere("logs.view"),
    options: [
      entries("8", ["5", "8", "10", "20"]),
      {
        key: "level",
        label: msg("Level"),
        values: [
          { value: "info", label: msg("Info and above") },
          { value: "warn", label: msg("Warnings and errors") },
          { value: "error", label: msg("Errors only") },
        ],
        default: "info",
      },
    ],
    Component: RecentActivity,
  },
  {
    id: "schedules",
    title: msg("Schedules"),
    icon: CalendarCheckIcon,
    columns: 1,
    visible: (a) => a.can("backupjobs.view") || a.can("policies.view"),
    options: [entries("8", ["5", "8", "10", "20"])],
    Component: Schedules,
  },
  {
    id: "actions",
    title: msg("Quick actions"),
    icon: LightningIcon,
    columns: 1,
    visible: (a) => a.canSomewhere("servers.create") || a.can("networks.manage") || a.can("nodes.enroll"),
    Component: QuickActions,
  },
]

export const widgetOf = (id: string) => widgets.find((w) => w.id === id)

/** The options of a widget that the user didn't choose. */
export const defaultOptions = (def: WidgetDef): WidgetProps["options"] =>
  Object.fromEntries((def.options ?? []).map((o) => [o.key, "default" in o ? o.default : undefined]))

/**
 * The widgets the user may see, in the order and widths they chose. Widgets they never
 * arranged, e.g. new ones, come last in their default width.
 */
export function arrange(stored: Widget[], available: WidgetDef[]): Widget[] {
  const kept = stored.filter((w) => available.some((d) => d.id === w.id))
  const added = available.filter((d) => !kept.some((w) => w.id === d.id)).map(({ id, columns }) => ({ id, columns }))
  return [...kept, ...added]
}
