import { ClockIcon, CrownSimpleIcon, GavelIcon, ListChecksIcon, WarningIcon } from "@phosphor-icons/react"
import { useQuery } from "@tanstack/react-query"
import { t } from "i18next"
import { Callout, ErrorCallout } from "@/components/callout"
import { CsvButton } from "@/components/csv-button"
import { EmptyState } from "@/components/empty-state"
import { Pill } from "@/components/status"
import { Button } from "@/components/ui/button"
import { Skeleton } from "@/components/ui/skeleton"
import { SortableHead, Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table"
import { useAccess } from "@/features/access/use-access"
import type { Network, ServerRef } from "@/features/networks/api"
import { findServer, refOf } from "@/features/networks/servers"
import { allServersQuery, type NodeServer } from "@/features/servers/api"
import { formatDate, formatDateTime } from "@/lib/format"
import { msg } from "@/lib/i18n"
import { type Sorting, sortBy } from "@/lib/sort"
import { useNow } from "@/lib/use-now"
import { cn } from "@/lib/utils"
import { needs, playerActions } from "./actions"
import { type Listed, type PlayerAction, type PlayerLists, playerListsQuery } from "./api"
import { SelectCell, SelectHead } from "./player-bulk-bar"
import type { PlayerDialog } from "./player-dialog"
import { PlayerName } from "./player-name"
import { useScopes } from "./scopes"
import type { ListSort, PlayerSearch } from "./search"
import type { Picked, Selection } from "./selection"

export type ListKind = Exclude<NonNullable<PlayerSearch["tab"]>, "online" | "seen">

const kinds: Record<
  ListKind,
  { icon: typeof GavelIcon; add: PlayerAction; remove: PlayerAction; empty: string; where: (name: string) => string }
> = {
  banned: {
    icon: GavelIcon,
    add: "ban",
    remove: "pardon",
    empty: msg("Nobody is banned"),
    where: (name) => t("Where {{name}} is banned", { name }),
  },
  whitelisted: {
    icon: ListChecksIcon,
    add: "whitelist_add",
    remove: "whitelist_remove",
    empty: msg("Nobody is on the whitelist"),
    where: (name) => t("Where {{name}} is on the whitelist", { name }),
  },
  operators: {
    icon: CrownSimpleIcon,
    add: "op",
    remove: "deop",
    empty: msg("Nobody is an operator"),
    where: (name) => t("Where {{name}} is an operator", { name }),
  },
}

/**
 * Who is banned, whitelisted or operator on the game servers of a network or on all, joined by
 * player, or on one server, which may be part of network.
 */
export function PlayerListTab({
  kind,
  network,
  server,
  query,
  sorting,
  selection,
  onAct,
}: {
  kind: ListKind
  network?: Network
  server?: NodeServer
  query: string
  sorting: Sorting<ListSort>
  selection?: Selection
  onAct: (dialog: PlayerDialog) => void
}) {
  const { can } = useAccess()
  const scopesFor = useScopes()
  const { data, isPending, error } = useQuery(playerListsQuery(server ? { server: refOf(server) } : { network: network?.id }))
  const { data: servers } = useQuery(allServersQuery)
  if (isPending) return <Skeleton className="h-64 rounded-xl" />
  if (error) return <ErrorCallout error={error} />

  const { icon, add, remove, empty, where } = kinds[kind]
  const total = data.servers.length
  const entries = sortBy(
    data[kind].filter((e) => e.name.toLowerCase().includes(query.toLowerCase())),
    sorting.order,
    sorting.by === "servers" ? (e) => e.servers.length : (e) => e.name,
    (e) => e.name,
  )
  const nameOf = (ref: ServerRef) => findServer(servers, ref)?.name ?? ref.serverId
  const failed = data.servers.filter((s) => s.error)
  const pending = data.servers.flatMap((s) => s.pending.map((p) => ({ ...p, server: nameOf(s) })))
  const picked = (e: Listed): Picked => ({ name: e.name, servers: e.servers })
  const addScopes = scopesFor(add, { server, network })
  const allowed = (refs: ServerRef[]) => refs.filter((r) => needs(remove).every((p) => can(p, r.nodeId, r.serverId)))
  const AddIcon = playerActions[add].icon

  return (
    <div className="grid gap-4">
      <div className="flex flex-wrap items-center justify-between gap-3">
        {!server && (
          <p className="text-sm text-muted-foreground">
            {t("Lists of {{count}} game servers", { count: total, defaultValue_one: "Lists of {{count}} game server" })}
          </p>
        )}
        <div className="ml-auto flex flex-wrap gap-2">
          {kind === "whitelisted" && <WhitelistSwitch lists={data} network={network} server={server} onAct={onAct} />}
          {addScopes.length > 0 && (
            <Button variant="outline" onClick={() => onAct({ action: add, scopes: addScopes })}>
              <AddIcon />
              {playerActions[add].label()}
            </Button>
          )}
          {entries.length > 0 && (
            <CsvButton
              name={kind === "whitelisted" ? "whitelist" : kind}
              rows={() => [
                ["player", "uuid", ...(kind === "banned" ? ["reason", "since", "until", "source"] : []), "servers", "server_names"],
                ...entries.map((e) => [
                  e.name,
                  e.uuid,
                  ...(kind === "banned" ? [e.reason, e.since, e.until, e.source] : []),
                  e.servers.length,
                  e.servers.map(nameOf).join(", "),
                ]),
              ]}
            />
          )}
        </div>
      </div>
      {failed.length > 0 && (
        <Callout tone="warning" icon={WarningIcon} title={t("Some servers didn't answer")}>
          {failed.map((s) => `${nameOf(s)}: ${s.error}`).join(" · ")}
        </Callout>
      )}
      {pending.length > 0 && (
        <Callout
          icon={ClockIcon}
          title={t("{{count}} changes wait for stopped servers or their time", {
            count: pending.length,
            defaultValue_one: "A change waits for a stopped server or its time",
          })}
        >
          <ul className="mt-1 grid gap-0.5">
            {pending.slice(0, 10).map((p, i) => (
              <li key={i}>
                {p.server}: {playerActions[p.action].title(p.name ?? "")}
                {p.due && ` · ${formatDateTime(p.due)}`}
              </li>
            ))}
          </ul>
        </Callout>
      )}
      {entries.length === 0 ? (
        <EmptyState icon={icon} title={query ? t("Nobody found") : t(empty)} />
      ) : (
        <div className="surface overflow-hidden rounded-xl">
          <Table>
            <TableHeader>
              <TableRow className="hover:bg-transparent">
                <SelectHead selection={selection} players={entries.map(picked)} />
                <SortableHead sorting={sorting} column="name" className={cn(!selection && "pl-4")}>
                  {t("Player")}
                </SortableHead>
                {kind === "banned" && <TableHead className="max-md:hidden">{t("Reason")}</TableHead>}
                {!server && (
                  <SortableHead sorting={sorting} column="servers">
                    {t("Servers")}
                  </SortableHead>
                )}
                <TableHead className="w-0">
                  <span className="sr-only">{t("Actions")}</span>
                </TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {entries.map((entry) => (
                <ListRow
                  key={entry.name}
                  entry={entry}
                  selection={selection}
                  total={total}
                  banned={kind === "banned"}
                  names={server ? undefined : entry.servers.map(nameOf)}
                  onRemove={
                    allowed(entry.servers).length > 0
                      ? () =>
                          onAct({
                            action: remove,
                            names: [entry.name],
                            scopes: [{ label: where(entry.name), servers: allowed(entry.servers) }],
                          })
                      : undefined
                  }
                  remove={playerActions[remove]}
                />
              ))}
            </TableBody>
          </Table>
        </div>
      )}
    </div>
  )
}

function ListRow({
  entry,
  selection,
  total,
  banned,
  names,
  onRemove,
  remove,
}: {
  entry: Listed
  selection?: Selection
  total: number
  banned: boolean
  /** The servers whose list has the player, unless the list is of one server. */
  names?: string[]
  onRemove?: () => void
  remove: (typeof playerActions)[PlayerAction]
}) {
  // A temporary ban that ended stays in the list until the server lets the player join again.
  const now = useNow(entry.until !== undefined, 60_000)
  const ended = entry.until !== undefined && Date.parse(entry.until) <= now
  return (
    <TableRow className={cn(ended && "text-muted-foreground")} data-state={selection?.has(entry.name) ? "selected" : undefined}>
      <SelectCell selection={selection} player={{ name: entry.name, servers: entry.servers }} />
      <TableCell className={cn(!selection && "pl-4")}>
        <span className="flex items-center gap-2">
          <PlayerName name={entry.name} />
          {banned && entry.until && <Pill tone={ended ? "neutral" : "warning"}>{ended ? t("Ended") : t("Temporary")}</Pill>}
        </span>
      </TableCell>
      {banned && (
        <TableCell className="max-w-80 whitespace-normal text-muted-foreground max-md:hidden">
          {entry.reason}
          {entry.since && <span className="block text-xs">{t("Since {{date}}", { date: formatDate(entry.since) })}</span>}
          {entry.until && (
            <span className="block text-xs">
              {ended ? t("Ended {{date}}", { date: formatDateTime(entry.until) }) : t("Until {{date}}", { date: formatDateTime(entry.until) })}
            </span>
          )}
        </TableCell>
      )}
      {names && (
        <TableCell title={names.slice(0, 20).join(", ")} className="tabular-nums">
          {entry.servers.length === total
            ? t("All {{count}} servers", { count: total, defaultValue_one: "The only server" })
            : t("{{done}} of {{count}} servers", { done: entry.servers.length, count: total })}
        </TableCell>
      )}
      <TableCell className="pr-3 text-right">
        {onRemove && (
          <Button variant="ghost" size="sm" onClick={onRemove} aria-label={remove.label()}>
            <remove.icon />
            <span className="max-sm:hidden">{remove.label()}</span>
          </Button>
        )}
      </TableCell>
    </TableRow>
  )
}

/** Whether the whitelist is on, and switching it on or off on all servers. */
function WhitelistSwitch({
  lists,
  network,
  server,
  onAct,
}: {
  lists: PlayerLists
  network?: Network
  server?: NodeServer
  onAct: (dialog: PlayerDialog) => void
}) {
  const scopesFor = useScopes()
  const on = lists.servers.filter((s) => s.whitelistEnabled).length
  const next: PlayerAction = on === lists.servers.length ? "whitelist_off" : "whitelist_on"
  const scopes = scopesFor(next, { server, network })
  return (
    <>
      <span className="self-center text-sm text-muted-foreground">
        {server
          ? on
            ? t("The whitelist is on")
            : t("The whitelist is off")
          : t("Whitelist active on {{done}} of {{count}} servers", { done: on, count: lists.servers.length })}
      </span>
      {scopes.length > 0 && (
        <Button variant="outline" onClick={() => onAct({ action: next, scopes })}>
          {playerActions[next].label()}
        </Button>
      )}
    </>
  )
}
