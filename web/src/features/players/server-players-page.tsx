import { InfoIcon, MagnifyingGlassIcon, UsersThreeIcon } from "@phosphor-icons/react"
import { useQuery } from "@tanstack/react-query"
import { getRouteApi } from "@tanstack/react-router"
import { t } from "i18next"
import { useState } from "react"
import { Callout, ErrorCallout } from "@/components/callout"
import { EmptyState } from "@/components/empty-state"
import { Segmented } from "@/components/segmented"
import { InputGroup, InputGroupAddon, InputGroupInput } from "@/components/ui/input-group"
import { Skeleton } from "@/components/ui/skeleton"
import type { Network } from "@/features/networks/api"
import { useNetworkOf } from "@/features/networks/servers"
import { nodeQuery } from "@/features/nodes/api"
import { type NodeServer, useServer } from "@/features/servers/api"
import { type ServerUsage, useServerUsage } from "@/features/usage/api"
import { PlayerMenu } from "./online-players"
import { PlayerBulkBar } from "./player-bulk-bar"
import { type PlayerDialog, PlayerDialogs } from "./player-dialog"
import { type ListKind, PlayerListTab } from "./player-lists"
import { PlayerName } from "./player-name"
import { listSorts, type PlayerSearch, seenSorts, usePlayerSorting } from "./search"
import { SeenPlayers } from "./seen-players"
import { bulkActions, useSelection } from "./selection"

const route = getRouteApi("/_app/nodes/$nodeId/servers/$serverId/players")

/** The Players tab of a game server: who is online, and its ban list, whitelist and operators, to act on them. */
export function ServerPlayersPage() {
  const { nodeId, serverId } = route.useParams()
  const search = route.useSearch()
  const navigate = route.useNavigate()
  const { server } = useServer(nodeId, serverId)
  const { data: node } = useQuery(nodeQuery(nodeId))
  const { usage, isPending, error } = useServerUsage(nodeId, serverId)
  const network = useNetworkOf()({ nodeId, serverId })
  const [dialog, setDialog] = useState<PlayerDialog>()
  const selection = useSelection()
  const set = (change: Partial<PlayerSearch>) => void navigate({ search: (s) => ({ ...s, ...change }), replace: true })
  const sorting = usePlayerSorting(search, set)
  if (!server) return null

  const tab = search.tab ?? "online"
  const query = (search.q ?? "").toLowerCase()
  const nodeServer: NodeServer = { ...server, nodeId, nodeName: node?.name ?? "" }

  return (
    <>
      <div className="mb-6 flex flex-wrap items-center gap-3">
        <Segmented<ListKind | "online" | "seen">
          label={t("List")}
          className="max-w-full overflow-x-auto"
          value={tab}
          onChange={(tab) => {
            selection.clear()
            set({ tab: tab === "online" ? undefined : tab })
          }}
          options={[
            { value: "online", label: t("Online") },
            { value: "seen", label: t("Seen") },
            { value: "banned", label: t("Banned") },
            { value: "whitelisted", label: t("Whitelist") },
            { value: "operators", label: t("Operators") },
          ]}
        />
        <InputGroup className="w-full sm:max-w-xs">
          <InputGroupAddon>
            <MagnifyingGlassIcon />
          </InputGroupAddon>
          <InputGroupInput
            type="search"
            placeholder={t("Search players")}
            aria-label={t("Search players")}
            value={search.q ?? ""}
            onChange={(e) => set({ q: e.target.value || undefined })}
          />
        </InputGroup>
      </div>
      {tab === "seen" ? (
        <SeenPlayers
          network={network}
          server={nodeServer}
          query={query}
          sorting={sorting(seenSorts)}
          selection={selection}
          onAct={setDialog}
        />
      ) : tab !== "online" ? (
        <PlayerListTab
          kind={tab}
          network={network}
          server={nodeServer}
          query={query}
          sorting={sorting(listSorts)}
          selection={selection}
          onAct={setDialog}
        />
      ) : error ? (
        <ErrorCallout error={error} />
      ) : isPending ? (
        <Skeleton className="h-40 rounded-xl" />
      ) : (
        <Online server={nodeServer} network={network} usage={usage} query={query} onAct={setDialog} />
      )}
      {tab !== "online" && (
        <PlayerBulkBar selection={selection} actions={bulkActions[tab].actions} where={bulkActions[tab].where()} network={network} onAct={setDialog} />
      )}
      <PlayerDialogs dialog={dialog} onClose={() => setDialog(undefined)} />
    </>
  )
}

/** The players online on the server, as its node measured them last. */
function Online({
  server,
  network,
  usage,
  query,
  onAct,
}: {
  server: NodeServer
  network?: Network
  usage?: ServerUsage
  query: string
  onAct: (dialog: PlayerDialog) => void
}) {
  const players = usage?.players
  if (!usage?.running)
    return <EmptyState icon={UsersThreeIcon} tone="neutral" title={t("Not running")} description={t("Players show up here while they play.")} />
  if (!players) return <EmptyState icon={UsersThreeIcon} tone="neutral" title={t("The server doesn't answer yet")} />
  const names = players.names.filter((name) => name.toLowerCase().includes(query))
  const unnamed = Math.max(players.online - players.names.length, 0)

  return (
    <div className="grid gap-4">
      {unnamed > 0 && (
        <Callout icon={InfoIcon}>
          {t("{{count}} more players are online. The server's console isn't responding, so it only reports how many.", {
            count: unnamed,
            defaultValue_one: "Another player is online. The server's console isn't responding, so it only reports how many.",
          })}
        </Callout>
      )}
      {names.length === 0 ? (
        unnamed === 0 && (
          <EmptyState
            icon={UsersThreeIcon}
            title={query ? t("Nobody found") : t("No players online")}
            description={query ? undefined : t("Players show up here while they play.")}
          />
        )
      ) : (
        <ul className="grid gap-2 sm:grid-cols-2 lg:grid-cols-3" aria-label={t("Players online")}>
          {names.map((name) => (
            <li key={name} className="surface flex items-center gap-3 rounded-xl py-2 pr-2 pl-3">
              <PlayerName name={name} size="md" className="flex-1 text-sm" />
              <PlayerMenu player={{ name, server, network }} onAct={onAct} />
            </li>
          ))}
        </ul>
      )}
    </div>
  )
}
