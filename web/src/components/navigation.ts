import {
  ArchiveIcon,
  BooksIcon,
  CalendarCheckIcon,
  CalendarDotsIcon,
  CubeIcon,
  FilesIcon,
  FlowArrowIcon,
  GearSixIcon,
  GraphIcon,
  HardDrivesIcon,
  LightningIcon,
  PuzzlePieceIcon,
  ScrollIcon,
  SquaresFourIcon,
  StackIcon,
  UsersThreeIcon,
} from "@phosphor-icons/react"
import type { Access } from "@/features/access/use-access"
import { seesSettings } from "@/features/settings/tabs"
import { msg } from "@/lib/i18n"

const seesServers = (a: Access) => a.canSomewhere("servers.view")

const overview = {
  to: "/",
  label: msg("Overview"),
  icon: SquaresFourIcon,
  visible: (a: Access) => seesServers(a) || a.canSomewhere("nodes.view"),
} as const
const servers = { to: "/servers", label: msg("Servers"), icon: CubeIcon, visible: seesServers } as const
const networks = { to: "/networks", label: msg("Networks"), icon: GraphIcon, visible: (a: Access) => a.can("networks.view") } as const
const players = { to: "/players", label: msg("Players"), icon: UsersThreeIcon, visible: seesServers } as const
const nodes = {
  to: "/nodes",
  label: msg("Nodes"),
  icon: HardDrivesIcon,
  visible: (a: Access) => a.canSomewhere("nodes.view") || seesServers(a),
} as const

/** Pages that share a header and switch with tabs; the sidebar links them as one. */
export const library = {
  label: msg("Library"),
  icon: BooksIcon,
  tone: "info",
  tabs: [
    { to: "/templates", label: msg("Templates"), icon: StackIcon, visible: (a: Access) => a.can("templates.view") },
    { to: "/filesets", label: msg("File sets"), icon: FilesIcon, visible: (a: Access) => a.can("filesets.view") },
    { to: "/plugins", label: msg("Plugins & mods"), icon: PuzzlePieceIcon, visible: (a: Access) => a.canSomewhere("plugins.manage") },
  ],
} as const

export const automation = {
  label: msg("Automation"),
  icon: LightningIcon,
  tone: "warning",
  tabs: [
    { to: "/backups", label: msg("Backups"), icon: ArchiveIcon, visible: (a: Access) => a.can("backupjobs.view") },
    { to: "/policies", label: msg("Schedules"), icon: CalendarCheckIcon, visible: (a: Access) => a.can("policies.view") },
    { to: "/workflows", label: msg("Workflows"), icon: FlowArrowIcon, visible: (a: Access) => a.can("workflows.view") },
    {
      to: "/agenda",
      label: msg("Agenda"),
      icon: CalendarDotsIcon,
      visible: (a: Access) => a.can("backupjobs.view") || a.can("policies.view") || a.can("workflows.view"),
    },
  ],
} as const

export type Hub = typeof library | typeof automation

/** The sidebar: the sections of the panel, then those of the system at its foot. */
export const navigation = {
  main: [overview, servers, networks, players, nodes, library, automation],
  system: [
    { to: "/logs", label: msg("Logs"), icon: ScrollIcon, visible: (a: Access) => a.canSomewhere("logs.view") },
    { to: "/settings", label: msg("Settings"), icon: GearSixIcon, visible: seesSettings },
  ],
} as const

type Entry = (typeof navigation.main)[number] | (typeof navigation.system)[number]
type Page = Exclude<Entry, Hub> | Hub["tabs"][number]

/** Every page the user may see, with those of the hubs; e.g. for the search. */
export function pages(access: Access) {
  return [...navigation.main, ...navigation.system]
    .flatMap((entry): readonly Page[] => ("tabs" in entry ? entry.tabs : [entry]))
    .filter((page) => page.visible(access))
}

/** The first section the user may see, where the panel opens unless the user chose another. */
export const home = (access: Access) => pages(access)[0]?.to ?? "/settings"

/** Where the panel opens for the user: the page they chose if they may see it, otherwise the first section they may. */
export const startPage = (access: Access, chosen?: string) => pages(access).find((page) => page.to === chosen)?.to ?? home(access)
