import { ArrowClockwiseIcon, ArrowCircleUpIcon, CaretRightIcon, DotsThreeIcon, MagnifyingGlassIcon, PushPinIcon, PuzzlePieceIcon, TrashIcon } from "@phosphor-icons/react"
import { useQuery } from "@tanstack/react-query"
import { Link } from "@tanstack/react-router"
import { t } from "i18next"
import { useState } from "react"
import { Callout, ErrorCallout } from "@/components/callout"
import { EmptyState } from "@/components/empty-state"
import { Segmented } from "@/components/segmented"
import { Pill } from "@/components/status"
import { Button } from "@/components/ui/button"
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from "@/components/ui/dropdown-menu"
import { InputGroup, InputGroupAddon, InputGroupInput } from "@/components/ui/input-group"
import { Skeleton } from "@/components/ui/skeleton"
import { useAccess } from "@/features/access/use-access"
import { key, refOf } from "@/features/networks/servers"
import { allServersQuery, type NodeServer } from "@/features/servers/api"
import { serverType } from "@/features/servers/server-types"
import { locale } from "@/lib/i18n"
import { cn } from "@/lib/utils"
import { type Everywhere, installedQuery, type Kind } from "./api"
import { ChannelPill, TracksPill } from "./channel-pill"
import { type EverywhereAction, EverywhereDialog } from "./everywhere-dialog"
import { PluginIcon } from "./plugin-icon"

type Entry = Everywhere["projects"][number]

/** The plugins or mods of all servers the user may see, by project, with their versions and updates. */
export function InstalledTab({ kind }: { kind: Kind }) {
  const installed = useQuery(installedQuery)
  const { data: servers } = useQuery(allServersQuery)
  const [search, setSearch] = useState("")
  const [show, setShow] = useState<"all" | "updates">("all")
  // The project as the dialog opened, which stays open after it was removed everywhere.
  const [dialog, setDialog] = useState<{ action: EverywhereAction; entry: Entry }>()
  if (installed.isPending || !servers) return <Skeleton className="h-64 rounded-xl" />
  if (installed.error) return <ErrorCallout error={installed.error} />

  const byKey = new Map(servers.map((s) => [key(refOf(s)), s]))
  const projects = installed.data.projects
    .map((p) => ({ ...p, servers: p.servers.filter((s) => serverType(byKey.get(key(s))?.type ?? "").addons?.kind === kind) }))
    .filter((p) => p.servers.length > 0)
  const updates = projects.filter((p) => p.servers.some((s) => s.update))
  const needle = search.trim().toLowerCase()
  const shown = (show === "updates" ? updates : projects).filter((p) => p.project.title.toLowerCase().includes(needle))
  const { unreachable, catalogueError } = installed.data

  return (
    <div className="grid gap-4">
      {unreachable.length > 0 && (
        <Callout tone="warning" title={t("These couldn't be asked, so their plugins are missing")}>
          <ul className="grid gap-1">
            {unreachable.map((u) => (
              <li key={`${u.nodeId}/${u.serverId ?? ""}`}>
                <span className="font-medium">{u.serverId ? `${byKey.get(key({ nodeId: u.nodeId, serverId: u.serverId }))?.name ?? u.serverId} · ${u.nodeName}` : u.nodeName}</span>
                : {u.error}
              </li>
            ))}
          </ul>
        </Callout>
      )}
      {catalogueError && <Callout tone="warning">{t("The files couldn't be identified: {{error}}", { error: catalogueError })}</Callout>}
      <div className="flex flex-wrap items-center gap-2">
        <InputGroup className="min-w-48 flex-1">
          <InputGroupAddon>
            <MagnifyingGlassIcon />
          </InputGroupAddon>
          <InputGroupInput type="search" placeholder={t("Search by name")} aria-label={t("Search the installed projects")} value={search} onChange={(e) => setSearch(e.target.value)} />
        </InputGroup>
        <Segmented
          label={t("Show")}
          value={show}
          onChange={setShow}
          options={[
            { value: "all", label: t("All ({{number}})", { number: projects.length }) },
            { value: "updates", label: t("Updates ({{number}})", { number: updates.length }) },
          ]}
        />
        <Button
          variant="outline"
          size="icon"
          disabled={installed.isFetching}
          onClick={() => void installed.refetch()}
          aria-label={t("Ask the servers again")}
          title={t("Ask the servers again")}
        >
          <ArrowClockwiseIcon className={cn(installed.isFetching && "animate-spin motion-reduce:animate-none")} />
        </Button>
      </div>
      {projects.length === 0 ? (
        <EmptyState icon={PuzzlePieceIcon} title={kind === "plugins" ? t("No server has plugins from Modrinth or Hangar") : t("No server has mods from Modrinth")} />
      ) : shown.length === 0 ? (
        <p className="surface rounded-xl py-8 text-center text-sm text-muted-foreground">{t("Nothing matches the filters.")}</p>
      ) : (
        <ul className="surface divide-y rounded-xl">
          {shown.map((entry) => (
            <ProjectRow key={entry.project.id} entry={entry} servers={byKey} onAction={(action) => setDialog({ action, entry })} />
          ))}
        </ul>
      )}
      {dialog && <EverywhereDialog action={dialog.action} entry={dialog.entry} servers={byKey} onClose={() => setDialog(undefined)} />}
    </div>
  )
}

/** A project with the servers that have it, which it lists when opened, and its actions on all of them. */
function ProjectRow({ entry, servers, onAction }: { entry: Entry; servers: Map<string, NodeServer>; onAction: (action: EverywhereAction) => void }) {
  const { can } = useAccess()
  const [open, setOpen] = useState(false)
  const { project } = entry
  const manageable = entry.servers.filter((s) => can("plugins.manage", s.nodeId, s.serverId))
  const updates = entry.servers.filter((s) => s.update && !s.disabled)
  const updatable = updates.filter((s) => !s.pinned && manageable.includes(s)).length
  const versions = [...new Set(entry.servers.map((s) => s.version ?? ""))].sort((a, b) => b.localeCompare(a, locale, { numeric: true }))
  const rows = [...entry.servers].sort((a, b) => (servers.get(key(a))?.name ?? "").localeCompare(servers.get(key(b))?.name ?? "", locale, { numeric: true }))

  return (
    <li>
      <div className="flex items-center gap-3 px-4 py-3">
        <button
          type="button"
          aria-expanded={open}
          onClick={() => setOpen(!open)}
          className="flex min-w-0 flex-1 items-center gap-3 rounded-lg text-left outline-none focus-visible:ring-2 focus-visible:ring-ring"
        >
          <CaretRightIcon className={cn("size-4 shrink-0 text-muted-foreground transition-transform", open && "rotate-90")} />
          <PluginIcon src={project.icon} />
          <span className="min-w-0">
            <span className="block truncate text-sm font-semibold">{project.title}</span>
            <span className="block truncate text-xs text-muted-foreground">
              {t("On {{count}} servers", { count: new Set(entry.servers.map(key)).size, defaultValue_one: "On {{count}} server" })} · <span className="font-mono">{versions.join(", ")}</span>
            </span>
          </span>
        </button>
        {updates.length > 0 && (
          <Pill tone="info" className="max-sm:hidden">
            {t("{{count}} updates", { count: updates.length, defaultValue_one: "{{count}} update" })}
          </Pill>
        )}
        {updatable > 0 && (
          <Button size="sm" variant="outline" onClick={() => onAction("update")}>
            <ArrowCircleUpIcon />
            <span className="max-sm:sr-only">{t("Update everywhere")}</span>
          </Button>
        )}
        {manageable.length > 0 && (
          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <Button
                size="icon-sm"
                variant="ghost"
                className="shrink-0 text-muted-foreground"
                aria-label={t("More actions for {{name}}", { name: project.title })}
                title={t("More actions")}
              >
                <DotsThreeIcon weight="bold" />
              </Button>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end" className="w-56">
              <DropdownMenuItem variant="destructive" onSelect={() => onAction("remove")}>
                <TrashIcon />
                {t("Remove everywhere…")}
              </DropdownMenuItem>
            </DropdownMenuContent>
          </DropdownMenu>
        )}
      </div>
      {open && (
        <ul className="divide-y border-t bg-muted/20">
          {rows.map((s) => {
            const server = servers.get(key(s))
            return (
              <li key={key(s)} className="flex flex-wrap items-center gap-x-3 gap-y-1 py-2 pr-4 pl-11 text-sm sm:pl-[4.25rem]">
                <Link to="/nodes/$nodeId/servers/$serverId/plugins" params={{ nodeId: s.nodeId, serverId: s.serverId }} className="font-medium hover:underline">
                  {server?.name ?? s.serverId}
                </Link>
                {server && <span className="text-xs text-muted-foreground">{server.nodeName}</span>}
                <span className="font-mono text-xs text-muted-foreground">{s.version}</span>
                <ChannelPill channel={s.channel} />
                <TracksPill channel={s.tracks} />
                {s.pinned && (
                  <span title={t("Kept at this version")} className="text-muted-foreground">
                    <PushPinIcon className="size-3.5" weight="fill" aria-hidden />
                    <span className="sr-only">{t("Kept at this version")}</span>
                  </span>
                )}
                {s.disabled && <Pill tone="neutral">{t("Off")}</Pill>}
                {s.update && <Pill tone="info">{t("Update to {{version}}", { version: s.update })}</Pill>}
                {s.update && <ChannelPill channel={s.updateChannel} />}
              </li>
            )
          })}
        </ul>
      )}
    </li>
  )
}
