import { InfoIcon, MagnifyingGlassIcon, UsersThreeIcon } from "@phosphor-icons/react"
import { useQuery } from "@tanstack/react-query"
import { getRouteApi } from "@tanstack/react-router"
import { t } from "i18next"
import { useState } from "react"
import { Callout } from "@/components/callout"
import { Choice } from "@/components/choice"
import { PageHeader } from "@/components/page-header"
import { Segmented } from "@/components/segmented"
import { InputGroup, InputGroupAddon, InputGroupInput } from "@/components/ui/input-group"
import { SelectItem } from "@/components/ui/select"
import { Skeleton } from "@/components/ui/skeleton"
import { useAccess } from "@/features/access/use-access"
import { networksQuery } from "@/features/networks/api"
import { useChoice } from "@/features/preferences/api"
import { useOnlinePlayers } from "./online"
import { OnlinePlayers } from "./online-players"
import { PlayerBulkBar } from "./player-bulk-bar"
import { type PlayerDialog, PlayerDialogs } from "./player-dialog"
import { type ListKind, PlayerListTab } from "./player-lists"
import { listSorts, onlineSorts, type PlayerSearch, seenSorts, usePlayerSorting } from "./search"
import { SeenPlayers } from "./seen-players"
import { bulkActions, useSelection } from "./selection"

const route = getRouteApi("/_app/players")

/**
 * The players online on all servers, those seen, and who is banned, whitelisted and operator, to act on them, also on
 * several at once. Where the address doesn't say, the list and the sort chosen last apply.
 */
export function PlayersPage() {
  const search = route.useSearch()
  const navigate = route.useNavigate()
  const { can } = useAccess()
  const { data: networks = [] } = useQuery({ ...networksQuery, enabled: can("networks.view") })
  const { players, unnamed, isPending } = useOnlinePlayers()
  const [dialog, setDialog] = useState<PlayerDialog>()
  const selection = useSelection()
  const network = networks.find((n) => n.id === search.network)
  const [tab, chooseTab] = useChoice("playerTab", "online", search.tab)
  const query = (search.q ?? "").toLowerCase()
  const set = (change: Partial<PlayerSearch>) => void navigate({ search: (s) => ({ ...s, ...change }), replace: true })
  const sorting = usePlayerSorting(search, set)
  const online = players.filter(
    (p) =>
      (!network || p.network?.id === network.id) && (p.name.toLowerCase().includes(query) || p.server.name.toLowerCase().includes(query)),
  )

  return (
    <>
      <PageHeader
        icon={UsersThreeIcon}
        tone="info"
        title={t("Players")}
        description={t("{{count}} players online", { count: players.length + unnamed, defaultValue_one: "{{count}} player online" })}
      />
      <div className="mb-6 flex flex-wrap items-center gap-3">
        <Segmented<ListKind | "online" | "seen">
          label={t("List")}
          className="max-w-full overflow-x-auto"
          value={tab}
          onChange={(tab) => {
            selection.clear()
            chooseTab(tab)
            set({ tab })
          }}
          options={[
            { value: "online", label: t("Online") },
            { value: "seen", label: t("Seen") },
            { value: "banned", label: t("Banned") },
            { value: "whitelisted", label: t("Whitelist") },
            { value: "operators", label: t("Operators") },
          ]}
        />
        {networks.length > 0 && (
          <Choice
            label={t("Network")}
            value={network?.id}
            onChange={(network) => {
              selection.clear()
              set({ network })
            }}
            everything={t("All servers")}
          >
            {networks.map((n) => (
              <SelectItem key={n.id} value={n.id}>
                {n.name}
              </SelectItem>
            ))}
          </Choice>
        )}
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
      {tab === "online" ? (
        <>
          {unnamed > 0 && (
            <Callout icon={InfoIcon} className="mb-4">
              {t("{{count}} more players are online on servers whose console isn't responding. Those servers only report a player count.", {
                count: unnamed,
                defaultValue_one: "Another player is online on a server whose console isn't responding. That server only reports a player count.",
              })}
            </Callout>
          )}
          {isPending ? (
            <Skeleton className="h-64 rounded-xl" />
          ) : (
            <OnlinePlayers players={online} sorting={sorting(onlineSorts)} selection={selection} onAct={setDialog} />
          )}
        </>
      ) : tab === "seen" ? (
        <SeenPlayers network={network} query={query} sorting={sorting(seenSorts)} selection={selection} onAct={setDialog} />
      ) : (
        <PlayerListTab
          kind={tab}
          network={network}
          query={query}
          sorting={sorting(listSorts)}
          selection={selection}
          onAct={setDialog}
        />
      )}
      <PlayerBulkBar selection={selection} actions={bulkActions[tab].actions} where={bulkActions[tab].where()} network={network} onAct={setDialog} />
      <PlayerDialogs dialog={dialog} onClose={() => setDialog(undefined)} />
    </>
  )
}
