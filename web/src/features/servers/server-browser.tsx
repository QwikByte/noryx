import { useQuery } from "@tanstack/react-query"
import { t } from "i18next"
import { useState } from "react"
import { NoMatch } from "@/components/list-toolbar"
import { useNetworkOf } from "@/features/networks/servers"
import { preferencesQuery, useSetFolded, useSettings } from "@/features/preferences/api"
import { useSorting } from "@/features/preferences/sorting"
import { useUsages } from "@/features/usage/api"
import { type NodeServer, serverKey } from "./api"
import { BulkBar } from "./bulk-bar"
import {
  columnsOf,
  columnsSetting,
  type Facts,
  filterServers,
  groupServers,
  type Property,
  type ServerSearch,
  serverRows,
  sortOrders,
  sortServers,
} from "./browse"
import { SavedViews } from "./saved-views"
import { ServerToolbar } from "./server-toolbar"
import { ServerGrid, ServerTable } from "./server-views"

/** How many folded groups the master keeps; those folded first unfold once more are. */
const maxFolded = 100

/**
 * Lists servers as cards or a table, searched, filtered, sorted and grouped by the settings in
 * the address. Where it doesn't say, the view, sort and grouping chosen last apply, which the user
 * keeps in all lists and browsers, as well as the columns of the table and the groups folded away.
 * Selected servers can be started, stopped, restarted, sent a command and tagged at once; the
 * selection only counts the servers that are listed. With views, the user saves the view shown by
 * name and shows those saved.
 */
export function ServerBrowser({
  servers,
  search,
  onSearch,
  hidden = [],
  views = false,
}: {
  servers: NodeServer[]
  search: ServerSearch
  onSearch: (change: Partial<ServerSearch>) => void
  hidden?: Property[]
  views?: boolean
}) {
  const usages = useUsages(servers.map((s) => s.nodeId))
  const networkOf = useNetworkOf()
  const facts: Facts = { usage: (s) => usages.server(s.nodeId, s.id), network: (s) => networkOf({ nodeId: s.nodeId, serverId: s.id }) }
  const { settings, change: changeSettings } = useSettings()
  // A list that doesn't offer the grouping chosen last, e.g. by node on a node's page, doesn't group.
  const shown: ServerSearch = {
    ...search,
    group: search.group ?? (hidden.includes(settings.serverGroup as Property) ? undefined : settings.serverGroup),
  }
  const { found, counts, total } = filterServers(servers, search, facts)
  const sorting = useSorting(search, sortOrders, { sort: "serverSort", order: "serverOrder" }, onSearch)
  const groups = groupServers(sortServers(found, sorting.by, sorting.order, facts), shown.group, facts)
  const view = search.view ?? settings.serverView ?? (servers.length > 12 ? "table" : "grid")
  const columns = columnsOf(settings.serverColumns)
  const [selection, setSelection] = useState(new Set<string>())
  // Groups stay folded in all lists of servers, e.g. a network on the servers page and on a node's page.
  const folded = useQuery(preferencesQuery).data?.folded ?? []
  const fold = useSetFolded()
  const foldKey = (g: { key: string }) => `${shown.group}/${g.key}`
  const selected = found.filter((s) => selection.has(serverKey(s)))
  const toggle = (set: Set<string>, keys: string[], on: boolean) => {
    const next = new Set(set)
    for (const k of keys) {
      if (on) next.add(k)
      else next.delete(k)
    }
    return next
  }

  function change(c: Partial<ServerSearch>) {
    if (c.view) changeSettings({ serverView: c.view })
    if ("group" in c) changeSettings({ serverGroup: c.group ?? "none" })
    onSearch(c)
  }

  const props = {
    groups,
    facts,
    sorting,
    showNode: !hidden.includes("node"),
    columns,
    selected: (s: NodeServer) => selection.has(serverKey(s)),
    onSelect: (list: NodeServer[], on: boolean) => setSelection((sel) => toggle(sel, list.map(serverKey), on)),
    collapsed: (g: { key: string }) => folded.includes(foldKey(g)),
    onCollapse: (g: { key: string }) =>
      fold.mutate(folded.includes(foldKey(g)) ? folded.filter((k) => k !== foldKey(g)) : [...folded, foldKey(g)].slice(-maxFolded)),
  }

  return (
    <>
      <ServerToolbar
        servers={servers}
        search={shown}
        onSearch={change}
        counts={counts}
        total={total}
        facts={facts}
        hidden={hidden}
        view={view}
        sorting={sorting}
        columns={columns}
        onColumns={(chosen) => changeSettings({ serverColumns: columnsSetting(chosen) ?? null })}
        // In the order shown, each server once, also when grouped by tags.
        rows={() => serverRows([...new Set(groups.flatMap((g) => g.servers))], facts, columns)}
        views={
          views && (
            // What the list shows, also by the sort, grouping and layout chosen last, so that a saved view shows the same.
            <SavedViews current={{ ...search, sort: sorting.by, order: sorting.order, group: shown.group ?? "none", view }} onShow={onSearch} />
          )
        }
      />
      {found.length === 0 ? (
        <NoMatch>{t("No server matches your search.")}</NoMatch>
      ) : view === "table" ? (
        <ServerTable {...props} />
      ) : (
        <ServerGrid {...props} />
      )}
      {selected.length > 0 && <BulkBar selected={selected} onClear={() => setSelection(new Set())} />}
    </>
  )
}
