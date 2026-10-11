import { ClockCounterClockwiseIcon } from "@phosphor-icons/react"
import { useQuery } from "@tanstack/react-query"
import { t } from "i18next"
import { ErrorCallout } from "@/components/callout"
import { CsvButton } from "@/components/csv-button"
import { EmptyState } from "@/components/empty-state"
import { Skeleton } from "@/components/ui/skeleton"
import { SortableHead, Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table"
import type { Network } from "@/features/networks/api"
import { findServer, refOf } from "@/features/networks/servers"
import { allServersQuery, type NodeServer } from "@/features/servers/api"
import { Ago } from "@/components/ago"
import { formatMinutes } from "@/lib/format"
import { type Sorting, sortBy } from "@/lib/sort"
import { useDebounced } from "@/lib/use-debounced"
import { useNow } from "@/lib/use-now"
import { cn } from "@/lib/utils"
import { type SeenPlayer, seenPlayersQuery } from "./api"
import { PlayerMenu } from "./online-players"
import { SelectCell, SelectHead } from "./player-bulk-bar"
import type { PlayerDialog } from "./player-dialog"
import { PlayerName } from "./player-name"
import type { SeenSort } from "./search"
import type { Picked, Selection } from "./selection"

const values: Record<SeenSort, (p: SeenPlayer) => string | number> = {
  seen: (p) => Date.parse(p.lastSeen),
  name: (p) => p.name,
  playtime: (p) => p.minutes,
}

const picked = (p: SeenPlayer): Picked => ({ name: p.name, servers: p.servers.map(({ nodeId, serverId }) => ({ nodeId, serverId })) })

/**
 * The players seen online on the game servers the user may see, of a network, of one server or all, with when they
 * were seen last and how long they played. The master searches, as it keeps more players than it sends.
 */
export function SeenPlayers({
  network,
  server,
  query,
  sorting,
  selection,
  onAct,
}: {
  network?: Network
  server?: NodeServer
  query: string
  sorting: Sorting<SeenSort>
  selection?: Selection
  onAct: (dialog: PlayerDialog) => void
}) {
  const q = useDebounced(query.trim())
  const { data, isPending, error } = useQuery(seenPlayersQuery({ q, network: network?.id, server: server && refOf(server) }))
  const { data: servers } = useQuery(allServersQuery)
  const now = useNow(true, 60_000)
  if (isPending) return <Skeleton className="h-64 rounded-xl" />
  if (error) return <ErrorCallout error={error} />
  if (data.players.length === 0) {
    return (
      <EmptyState
        icon={ClockCounterClockwiseIcon}
        title={q ? t("Nobody found") : t("Nobody was seen yet")}
        description={
          q
            ? undefined
            : data.recording
              ? t("The master notes every minute who plays on the game servers.")
              : t("The master no longer notes who plays, as its settings say.")
        }
      />
    )
  }
  const sorted = sortBy(data.players, sorting.order, values[sorting.by], (p) => p.name)
  const nameOf = (ref: SeenPlayer["servers"][number]) => findServer(servers, ref)?.name ?? ref.serverId

  return (
    <div className="grid gap-3">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <p className="text-sm text-muted-foreground">
          {data.total > data.players.length
            ? t("The {{shown}} players seen last of {{count}}. Search to find the others.", { shown: data.players.length, count: data.total })
            : t("{{count}} players seen", { count: data.total, defaultValue_one: "{{count}} player seen" })}
          {!data.recording && ` ${t("The master no longer notes who plays, as its settings say.")}`}
        </p>
        <CsvButton
          name="players-seen"
          rows={() => [
            ["player", "first_seen", "last_seen", "minutes", "servers"],
            ...sorted.map((p) => [p.name, p.firstSeen, p.lastSeen, p.minutes, p.servers.map(nameOf).join(", ")]),
          ]}
        />
      </div>
      <div className="surface overflow-hidden rounded-xl">
        <Table>
          <TableHeader>
            <TableRow className="hover:bg-transparent">
              <SelectHead selection={selection} players={sorted.map(picked)} />
              <SortableHead sorting={sorting} column="name" className={cn(!selection && "pl-4")}>
                {t("Player")}
              </SortableHead>
              <SortableHead sorting={sorting} column="seen">{t("Last seen")}</SortableHead>
              <SortableHead sorting={sorting} column="playtime" className="text-right max-sm:hidden">{t("Playtime")}</SortableHead>
              {!server && <TableHead className="max-md:hidden">{t("Servers")}</TableHead>}
              <TableHead className="w-0">
                <span className="sr-only">{t("Actions")}</span>
              </TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {sorted.map((p) => (
              <TableRow key={p.name} data-state={selection?.has(p.name) ? "selected" : undefined}>
                <SelectCell selection={selection} player={picked(p)} />
                <TableCell className={cn(!selection && "pl-4")}>
                  <PlayerName name={p.name} />
                </TableCell>
                <TableCell className="text-muted-foreground">
                  <Ago time={p.lastSeen} now={now} />
                </TableCell>
                <TableCell className="text-right tabular-nums max-sm:hidden">{formatMinutes(p.minutes)}</TableCell>
                {!server && (
                  <TableCell className="max-w-64 truncate text-muted-foreground max-md:hidden" title={p.servers.map(nameOf).join(", ")}>
                    {p.servers.length > 1
                      ? t("{{server}} and {{count}} more", { server: nameOf(p.servers[0]), count: p.servers.length - 1 })
                      : nameOf(p.servers[0])}
                  </TableCell>
                )}
                <TableCell className="pr-3 text-right">
                  <PlayerMenu player={{ name: p.name, server, network, offline: true }} onAct={onAct} />
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      </div>
    </div>
  )
}
