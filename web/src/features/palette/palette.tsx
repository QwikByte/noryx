import {
  ArchiveIcon,
  ArrowClockwiseIcon,
  BookmarkSimpleIcon,
  CalendarCheckIcon,
  FilesIcon,
  FlowArrowIcon,
  GraphIcon,
  HardDrivesIcon,
  KeyboardIcon,
  PlayIcon,
  StackIcon,
  StopIcon,
  TerminalIcon,
  UserCircleIcon,
  UserIcon,
} from "@phosphor-icons/react"
import { useQuery } from "@tanstack/react-query"
import { useLocation, useNavigate, useRouter } from "@tanstack/react-router"
import { t } from "i18next"
import { type ReactNode, useState } from "react"
import { IconTile } from "@/components/icon-tile"
import { pages as visiblePages } from "@/components/navigation"
import { type Status, StatusDot } from "@/components/status"
import {
  Command,
  CommandDialog,
  CommandEmpty,
  CommandGroup,
  CommandInput,
  CommandItem,
  CommandList,
  CommandShortcut,
} from "@/components/ui/command"
import { usersQuery } from "@/features/access/api"
import type { Permission } from "@/features/access/permissions"
import { useAccess } from "@/features/access/use-access"
import { meQuery } from "@/features/auth/api"
import { jobs } from "@/features/backups/api"
import { fileSetsQuery } from "@/features/filesets/api"
import { networksQuery } from "@/features/networks/api"
import { nodesQuery } from "@/features/nodes/api"
import { useOperation } from "@/features/operations/use-operation"
import { seenPlayersQuery } from "@/features/players/api"
import { useOnlinePlayers } from "@/features/players/online"
import { actions as policyActions, policies } from "@/features/policies/api"
import { preferencesQuery } from "@/features/preferences/api"
import { workflowsQuery } from "@/features/workflows/api"
import { describeTrigger } from "@/features/workflows/catalog"
import { describeSchedule } from "@/features/schedules/describe"
import { allServersQuery, type NodeServer, serverKey, useBulkAction, type Warning } from "@/features/servers/api"
import { validateServerSearch } from "@/features/servers/browse"
import { usePower } from "@/features/servers/power"
import { serverLook, serverType, statusOf } from "@/features/servers/server-types"
import { settings } from "@/features/settings/tabs"
import { templatesQuery } from "@/features/templates/api"
import { formatAgo } from "@/lib/format"
import { useDebounced } from "@/lib/use-debounced"
import { cn } from "@/lib/utils"
import { openedPath, readRecent } from "./recent"
import { account, type Creation, creations, pageKeys, useSingleKeys } from "./shortcuts"
import { Keys } from "./shortcuts-dialog"

/** Actions on a server that the palette offers once a search starts with their name, e.g. "restart lobby". */
const actions = [
  { kind: "console", icon: TerminalIcon, label: () => t("Console"), permission: "console.view", when: () => true },
  { kind: "start", icon: PlayIcon, label: () => t("Start"), permission: "servers.start", when: (s: NodeServer) => s.state === "stopped" },
  {
    kind: "restart",
    icon: ArrowClockwiseIcon,
    label: () => t("Restart"),
    permission: "servers.restart",
    when: (s: NodeServer) => s.state !== "stopped",
  },
  { kind: "stop", icon: StopIcon, label: () => t("Stop"), permission: "servers.stop", when: (s: NodeServer) => s.state !== "stopped" },
] as const satisfies readonly { permission: Permission; [key: string]: unknown }[]

/** Something the palette finds and opens. */
interface Entry {
  /** Unique among the entries; the search matches it too, e.g. "template" finds the templates. */
  value: string
  /** The address it opens, by which it is found among those opened recently, too. */
  path: string
  label: string
  keywords?: string[]
  icon: ReactNode
  status?: Status
  detail?: string
  labelClass?: string
}

/** An address of the panel from its parts, e.g. of a template from its ID. */
const at = (...parts: string[]) => `/${parts.map(encodeURIComponent).join("/")}`

const recentLimit = 5

/**
 * Searches servers, saved views of them, players, networks, nodes, the library, the automation, users and pages, acts
 * on servers, and creates servers, networks and nodes; onOpen opens the dialogs for these.
 */
export function Palette({ onClose, onOpen }: { onClose: () => void; onOpen: (what: Creation | "shortcuts") => void }) {
  const access = useAccess()
  const navigate = useNavigate()
  const bulk = useBulkAction()
  const operation = useOperation()
  const power = usePower({ onClose })
  const [query, setQuery] = useState("")
  const { data: me } = useQuery(meQuery)
  const [recent] = useState(() => (me ? readRecent(me.id) : []))
  const here = useLocation({ select: (l) => openedPath(l.pathname) })
  const { data: servers = [] } = useQuery({ ...allServersQuery, enabled: access.canSomewhere("servers.view") })
  const { data: networks = [] } = useQuery({ ...networksQuery, enabled: access.can("networks.view") })
  const { data: nodes = [] } = useQuery({
    ...nodesQuery,
    enabled: access.canSomewhere("nodes.view") || access.canSomewhere("servers.view"),
  })
  const { data: templates = [] } = useQuery({ ...templatesQuery, enabled: access.can("templates.view") })
  const { data: fileSets = [] } = useQuery({ ...fileSetsQuery, enabled: access.can("filesets.view") })
  const { data: backupJobs = [] } = useQuery({ ...jobs.tasksQuery, enabled: access.can("backupjobs.view") })
  const { data: schedules = [] } = useQuery({ ...policies.tasksQuery, enabled: access.can("policies.view") })
  const { data: workflows = [] } = useQuery({ ...workflowsQuery, enabled: access.can("workflows.view") })
  const { data: users = [] } = useQuery({ ...usersQuery, enabled: access.can("users.view") })
  const views = useQuery(preferencesQuery).data?.views ?? []
  const router = useRouter()
  const { players } = useOnlinePlayers(access.canSomewhere("servers.view"))
  const needle = query.trim().toLowerCase()
  const searched = useDebounced(needle, 200)
  const { data: seen } = useQuery({
    ...seenPlayersQuery({ q: searched, limit: 8 }),
    enabled: searched.length >= 2 && access.canSomewhere("servers.view"),
  })
  // The players online, then those seen before, each once.
  const online = needle.length >= 2 ? players.filter((p) => p.name.toLowerCase().includes(needle)).slice(0, 8) : []
  const offline = (needle.length >= 2 && searched === needle ? (seen?.players ?? []) : []).filter(
    (s) => !online.some((p) => p.name.toLowerCase() === s.name.toLowerCase()),
  )
  // Notes match as written, as fuzzy matching would find nearly every search in a longer text.
  const inNotes = (s: NodeServer) => (needle.length >= 2 && s.notes?.toLowerCase().includes(needle) ? [needle] : [])
  const words = query.toLowerCase().split(/\s+/).filter(Boolean)
  // "neustart" names "Neu starten" too.
  const named = actions.filter((a) => words.some((w) => w.length >= 3 && a.label().toLowerCase().replace(/\s/g, "").startsWith(w)))
  // The tabs of the settings are found by name too, e.g. "users".
  const pages = [...visiblePages(access), ...settings.tabs.filter((tab) => tab.to !== "/settings" && tab.visible(access)), account]
  const creatable = creations.filter((c) => c.allowed(access))
  // The keys of shortcuts only show while they work.
  const singleKeys = useSingleKeys()

  // The library, the automation and the users only show once something is searched.
  const groups: { heading: string; entries: Entry[]; searched?: boolean }[] = [
    {
      heading: t("Servers"),
      entries: servers.map((s) => ({
        // cmdk only takes new keywords with a new value, so a match in the notes changes it.
        value: `server/${serverKey(s)}${inNotes(s).length > 0 ? "/notes" : ""}`,
        path: at("nodes", s.nodeId, "servers", s.id),
        label: s.name,
        keywords: [serverType(s.type).label, s.nodeName, String(s.port), ...s.tags, ...inNotes(s)],
        icon: <IconTile {...serverLook(s.type)} size="sm" className="size-6 rounded-md [&>svg]:size-3.5" />,
        status: statusOf(s),
        detail: [...s.tags.map((tag) => `#${tag}`), s.nodeName].join(" · "),
      })),
    },
    {
      heading: t("Saved views"),
      // Their searches are checked again, as on the servers page.
      entries: views.map((v) => ({
        value: `view/${v.name}`,
        path: router.buildLocation({ to: "/servers", search: validateServerSearch(v.search) }).href,
        label: v.name,
        icon: <BookmarkSimpleIcon className="text-primary" />,
        detail: t("Servers"),
      })),
    },
    {
      heading: t("Players"),
      entries: [
        ...online.map((p) => ({
          value: `player/${p.name}@${serverKey(p.server)}`,
          path: at("players", p.name),
          label: p.name,
          labelClass: "font-mono",
          icon: <UserIcon className="text-success" />,
          detail: p.server.name,
        })),
        ...offline.map((p) => ({
          value: `player/${p.name}`,
          path: at("players", p.name),
          label: p.name,
          labelClass: "font-mono",
          icon: <UserIcon className="text-info" />,
          detail: t("Seen {{ago}}", { ago: formatAgo(p.lastSeen) }),
        })),
      ],
    },
    {
      heading: t("Networks"),
      entries: networks.map((n) => ({
        value: `network/${n.id}`,
        path: at("networks", n.id),
        label: n.name,
        keywords: [serverType(n.proxyType).label],
        icon: <GraphIcon className="text-violet" />,
        detail: t("{{count}} servers", { count: n.backends.length, defaultValue_one: "{{count}} server" }),
      })),
    },
    {
      heading: t("Nodes"),
      entries: nodes.map((n) => ({
        value: `node/${n.id}`,
        path: at("nodes", n.id),
        label: n.name,
        keywords: [n.address ?? ""],
        icon: <HardDrivesIcon className="text-info" />,
        status: { tone: n.status === "online" ? "success" : n.status === "pending" ? "warning" : "destructive", label: "" },
      })),
    },
    {
      heading: t("Templates"),
      searched: true,
      entries: templates.map((tpl) => ({
        value: `template/${tpl.id}`,
        path: at("templates", tpl.id),
        label: tpl.name,
        keywords: [tpl.description],
        icon: <StackIcon className="text-info" />,
        detail: serverType(tpl.type).label,
      })),
    },
    {
      heading: t("File sets"),
      searched: true,
      entries: fileSets.map((set) => ({
        value: `fileset/${set.id}`,
        path: at("filesets", set.id),
        label: set.name,
        keywords: [set.description],
        icon: <FilesIcon className="text-info" />,
        detail: t("{{count}} files", { count: set.paths.length, defaultValue_one: "{{count}} file" }),
      })),
    },
    {
      heading: t("Backup jobs"),
      searched: true,
      entries: backupJobs.map((job) => ({
        value: `backupjob/${job.id}`,
        path: at("backups", job.id),
        label: job.name,
        icon: <ArchiveIcon className="text-warning" />,
        detail: describeSchedule(job.schedule),
      })),
    },
    {
      heading: t("Schedules"),
      searched: true,
      entries: schedules.map((policy) => ({
        value: `schedule/${policy.id}`,
        path: at("policies", policy.id),
        label: policy.name,
        keywords: [t(policyActions[policy.settings.action].label)],
        icon: <CalendarCheckIcon className="text-warning" />,
        detail: describeSchedule(policy.schedule),
      })),
    },
    {
      heading: t("Workflows"),
      searched: true,
      entries: workflows.map((w) => ({
        value: `workflow/${w.id}`,
        path: at("workflows", w.id),
        label: w.name,
        keywords: [w.description],
        icon: <FlowArrowIcon className="text-warning" />,
        detail: w.triggers[0] ? describeTrigger(w.triggers[0]) : t("Started by hand"),
      })),
    },
    {
      heading: t("Users"),
      searched: true,
      entries: users.map((user) => ({
        value: `user/${user.id}`,
        path: "/settings/users",
        label: user.username,
        icon: <UserCircleIcon />,
        detail: user.disabled ? t("Disabled") : undefined,
      })),
    },
  ]
  const all = groups.flatMap((group) => group.entries)
  // What is open now isn't offered again.
  const opened = needle
    ? []
    : recent
        .filter((path) => path !== here)
        .flatMap((path) => all.find((e) => e.path === path) ?? [])
        .slice(0, recentLimit)

  function go(to: () => Promise<void>) {
    onClose()
    void to()
  }

  const item = (entry: Entry, value = entry.value) => (
    <CommandItem
      key={value}
      value={value}
      keywords={[entry.label, ...(entry.keywords ?? [])]}
      onSelect={() => go(() => navigate({ href: entry.path }))}
    >
      {entry.icon}
      <span className={cn("truncate font-medium", entry.labelClass)}>{entry.label}</span>
      {entry.status && <StatusDot status={entry.status} label={entry.status.label ? t(entry.status.label) : undefined} />}
      {entry.detail && <CommandShortcut className="truncate tracking-normal">{entry.detail}</CommandShortcut>}
    </CommandItem>
  )

  function act(kind: (typeof actions)[number]["kind"], server: NodeServer) {
    const params = { nodeId: server.nodeId, serverId: server.id }
    if (kind === "console") return go(() => navigate({ to: "/nodes/$nodeId/servers/$serverId", params, state: { console: true } }))
    // A notification follows the action, also while the server warns its players.
    const run = (warning?: Warning) =>
      operation.run(
        (onStart) =>
          bulk.mutateAsync({ action: kind, warning, servers: [server], onStart }).then(([r]) => {
            if (r?.error) throw new Error(r.error)
          }),
        {
          title: t("{{action}}: {{name}}…", { action: actions.find((a) => a.kind === kind)!.label(), name: server.name }),
          notify: true,
          done: () => ({ message: t("Done: {{name}}", { name: server.name }) }),
        },
      )
    if (kind !== "start") return power.request({ nodeId: server.nodeId, server, action: kind, run })
    onClose()
    run()
  }

  // Asking before a stop or restart takes the place of the search.
  if (power.dialog) return power.dialog

  return (
    <CommandDialog
      open
      onOpenChange={(open) => !open && onClose()}
      title={t("Search")}
      description={t("Search servers, players, networks and more, or create something")}
      className="sm:max-w-xl"
    >
      <Command loop>
        <CommandInput value={query} onValueChange={setQuery} placeholder={t("Search servers, players, networks and more…")} />
        <CommandList className="max-h-[min(28rem,60svh)]">
          <CommandEmpty>{t("Nothing found.")}</CommandEmpty>
          {opened.length > 0 && <CommandGroup heading={t("Recently opened")}>{opened.map((e) => item(e, `recent/${e.value}`))}</CommandGroup>}
          {named.length > 0 && (
            <CommandGroup heading={t("Actions")}>
              {named.flatMap((a) =>
                servers
                  .filter((s) => a.when(s) && access.can(a.permission, s.nodeId, s.id))
                  .map((s) => (
                    <CommandItem
                      key={`${a.kind}/${serverKey(s)}`}
                      value={`${a.kind}/${serverKey(s)}`}
                      keywords={[a.label(), a.label().replace(/\s/g, ""), s.name, s.nodeName, ...s.tags]}
                      onSelect={() => act(a.kind, s)}
                    >
                      <a.icon />
                      <span className="truncate">
                        {a.label()}: <span className="font-medium">{s.name}</span>
                      </span>
                      <CommandShortcut className="tracking-normal">{s.nodeName}</CommandShortcut>
                    </CommandItem>
                  )),
              )}
            </CommandGroup>
          )}
          {groups.map(
            (group) =>
              group.entries.length > 0 &&
              (needle || !group.searched) && (
                <CommandGroup key={group.heading} heading={group.heading}>
                  {group.entries.map((e) => item(e))}
                </CommandGroup>
              ),
          )}
          {creatable.length > 0 && (
            <CommandGroup heading={t("Create")}>
              {creatable.map((c) => (
                <CommandItem key={c.opens} value={`create/${c.opens}`} keywords={[t(c.label)]} onSelect={() => onOpen(c.opens)}>
                  <c.icon />
                  {t(c.label)}
                  {singleKeys && (
                    <CommandShortcut>
                      <Keys keys={["c", c.key]} />
                    </CommandShortcut>
                  )}
                </CommandItem>
              ))}
            </CommandGroup>
          )}
          <CommandGroup heading={t("Pages")}>
            {pages.map(({ to, label, icon: Icon }) => {
              const key = singleKeys && pageKeys[to]
              return (
                <CommandItem key={to} value={`page${to}`} keywords={[t(label)]} onSelect={() => go(() => navigate({ to }))}>
                  <Icon />
                  {t(label)}
                  {key && (
                    <CommandShortcut>
                      <Keys keys={["g", key]} />
                    </CommandShortcut>
                  )}
                </CommandItem>
              )
            })}
            <CommandItem value="shortcuts" keywords={[t("Keyboard shortcuts")]} onSelect={() => onOpen("shortcuts")}>
              <KeyboardIcon />
              {t("Keyboard shortcuts")}
              {singleKeys && (
                <CommandShortcut>
                  <Keys keys={["?"]} />
                </CommandShortcut>
              )}
            </CommandItem>
          </CommandGroup>
        </CommandList>
        <p className="border-t px-3 py-2 text-xs text-muted-foreground">{t("Tip: type an action and a server, e.g. “restart lobby”.")}</p>
      </Command>
    </CommandDialog>
  )
}
