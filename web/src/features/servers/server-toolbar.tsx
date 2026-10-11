import { ColumnsIcon, FunnelSimpleIcon, RowsIcon } from "@phosphor-icons/react"
import { t } from "i18next"
import type { ReactNode } from "react"
import { CsvButton } from "@/components/csv-button"
import { FilterChip } from "@/components/filter-chip"
import { ListToolbar, MenuChoice, SearchField, SortMenu, ViewSwitch } from "@/components/list-toolbar"
import { radios } from "@/components/radios"
import { StatusDot } from "@/components/status"
import { Button } from "@/components/ui/button"
import {
  DropdownMenu,
  DropdownMenuCheckboxItem,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuRadioGroup,
  DropdownMenuRadioItem,
  DropdownMenuSeparator,
  DropdownMenuSub,
  DropdownMenuSubContent,
  DropdownMenuSubTrigger,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import type { Cell } from "@/lib/csv"
import type { Sorting } from "@/lib/sort"
import { cn } from "@/lib/utils"
import type { NodeServer, ServerState } from "./api"
import {
  type Column,
  columns as columnLabels,
  type Facts,
  type Grouping,
  groupings,
  type Property,
  properties,
  type ServerSearch,
  type Sort,
  sorts,
  type View,
  valuesOf,
} from "./browse"
import { serverStates, states } from "./server-types"

const propertyLabels: Record<Property, () => string> = {
  network: () => t("Network"),
  node: () => t("Node"),
  type: () => t("Type"),
  tag: () => t("Tag"),
}

/** The values of a property among the servers, with how many servers have each. */
function optionsOf(property: Property, servers: NodeServer[], facts: Facts) {
  const options = new Map<string, { label: string; count: number }>()
  for (const s of servers) {
    for (const [value, label] of valuesOf(property, s, facts)) {
      if (property === "tag" && !value) continue
      options.set(value, { label, count: (options.get(value)?.count ?? 0) + 1 })
    }
  }
  return [...options].map(([value, o]) => ({ value, ...o })).sort((a, b) => a.label.localeCompare(b.label, undefined, { numeric: true }))
}

/**
 * Searches, filters, sorts and groups the servers, switches between cards and a table and chooses
 * the columns of the table. The states show how many servers of the search have each.
 */
export function ServerToolbar({
  servers,
  search,
  onSearch,
  counts,
  total,
  facts,
  hidden,
  view,
  sorting,
  columns,
  onColumns,
  rows,
  views,
}: {
  servers: NodeServer[]
  search: ServerSearch
  onSearch: (change: Partial<ServerSearch>) => void
  counts: Record<ServerState, number>
  total: number
  facts: Facts
  /** Properties the list doesn't offer, e.g. the node on a node's page. */
  hidden: Property[]
  view: View
  sorting: Sorting<Sort>
  /** The columns chosen for the table, and a choice of others; undefined takes it back to the default ones. */
  columns: Column[]
  onColumns: (columns: Column[] | undefined) => void
  /** The servers listed, as CSV. */
  rows: () => Cell[][]
  /** The views the user saved, next to the search. */
  views?: ReactNode
}) {
  const filters = properties
    .filter((p) => !hidden.includes(p))
    .map((property) => ({ property, options: optionsOf(property, servers, facts) }))
    // Choosing among one type or node filters nothing; a single tag still tells tagged servers apart.
    .filter(({ property, options }) => search[property] || options.length > (property === "tag" ? 0 : 1))
  const active = filters.filter(({ property }) => search[property])
  const filtered = active.length > 0 || search.q || search.state
  const stateRadio = radios([undefined, ...states], search.state, (state) => onSearch({ state }))

  return (
    <div className="mb-5 space-y-3">
      <ListToolbar
        search={
          <>
            <SearchField label={t("Search servers")} value={search.q} onChange={(q) => onSearch({ q })} className="min-w-0 flex-1" />
            {filters.length > 0 && (
              <DropdownMenu>
                <DropdownMenuTrigger asChild>
                  <Button variant="outline">
                    <FunnelSimpleIcon />
                    {t("Filter")}
                    {active.length > 0 && (
                      <span className="rounded-sm bg-primary px-1.5 text-xs text-primary-foreground tabular-nums">{active.length}</span>
                    )}
                  </Button>
                </DropdownMenuTrigger>
                <DropdownMenuContent align="start" className="w-48">
                  {filters.map(({ property, options }) => (
                    <DropdownMenuSub key={property}>
                      <DropdownMenuSubTrigger>{propertyLabels[property]()}</DropdownMenuSubTrigger>
                      <DropdownMenuSubContent className="max-h-80 w-56 overflow-y-auto">
                        <DropdownMenuRadioGroup
                          value={search[property] ?? ""}
                          onValueChange={(value) => onSearch({ [property]: value || undefined })}
                        >
                          <DropdownMenuRadioItem value="">{t("All")}</DropdownMenuRadioItem>
                          {options.map((o) => (
                            <DropdownMenuRadioItem key={o.value} value={o.value}>
                              <span className="min-w-0 flex-1 truncate">{o.label}</span>
                              <span className="text-xs text-muted-foreground tabular-nums">{o.count}</span>
                            </DropdownMenuRadioItem>
                          ))}
                        </DropdownMenuRadioGroup>
                      </DropdownMenuSubContent>
                    </DropdownMenuSub>
                  ))}
                </DropdownMenuContent>
              </DropdownMenu>
            )}
            {views}
          </>
        }
      >
        <SortMenu sorting={sorting} sorts={sorts} />
        <MenuChoice
          icon={RowsIcon}
          label={t("Group by")}
          value={search.group ?? "none"}
          options={(Object.keys(groupings) as Grouping[])
            .filter((g) => g === "none" || g === search.group || filters.some((f) => f.property === g))
            .map((value) => ({ value, label: t(groupings[value]) }))}
          onChange={(group) => onSearch({ group: group === "none" ? undefined : group })}
        />
        <ViewSwitch value={view} onChange={(v) => onSearch({ view: v })} />
        {view === "table" && (
          <ColumnsMenu
            // The node only where servers of several nodes are listed, the network only once some server has one.
            offered={(Object.keys(columnLabels) as Column[]).filter(
              (c) => (c !== "node" || !hidden.includes("node")) && (c !== "network" || servers.some((s) => facts.network(s))),
            )}
            columns={columns}
            onChange={onColumns}
          />
        )}
        <CsvButton name="servers" rows={rows} />
      </ListToolbar>
      <div className="flex flex-wrap items-center gap-2">
        <div role="radiogroup" aria-label={t("State")} className="flex max-w-full gap-1 overflow-x-auto rounded-lg bg-muted p-0.5 ring-1 ring-border ring-inset [scrollbar-width:none]">
          {[undefined, ...states].map((state) => (
            <button
              key={state ?? "all"}
              {...stateRadio(state)}
              className={cn(
                "flex h-7 items-center gap-2 rounded-md px-2.5 text-xs font-medium whitespace-nowrap text-muted-foreground transition-colors outline-none hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring aria-checked:bg-card aria-checked:text-foreground aria-checked:shadow-sm",
                state && counts[state] === 0 && search.state !== state && "opacity-50",
              )}
            >
              {state && <StatusDot status={serverStates[state]} />}
              {state ? t(serverStates[state].label) : t("All")}
              <span className="font-normal tabular-nums">{state ? counts[state] : total}</span>
            </button>
          ))}
        </div>
        {active.map(({ property, options }) => (
          <FilterChip
            key={property}
            label={`${propertyLabels[property]()}: ${options.find((o) => o.value === search[property])?.label ?? search[property]}`}
            onRemove={() => onSearch({ [property]: undefined })}
          />
        ))}
        {filtered && (
          <Button
            variant="ghost"
            size="xs"
            className="text-muted-foreground"
            onClick={() => onSearch({ q: undefined, state: undefined, ...Object.fromEntries(properties.map((p) => [p, undefined])) })}
          >
            {t("Reset filters")}
          </Button>
        )}
      </div>
    </div>
  )
}

/** Chooses the columns of the table among those offered; it stays open for the next choice. */
function ColumnsMenu({
  offered,
  columns,
  onChange,
}: {
  offered: Column[]
  columns: Column[]
  onChange: (columns: Column[] | undefined) => void
}) {
  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button variant="outline" size="icon" aria-label={t("Columns")} title={t("Columns")}>
          <ColumnsIcon />
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="w-48">
        <DropdownMenuLabel>{t("Columns")}</DropdownMenuLabel>
        {offered.map((c) => (
          <DropdownMenuCheckboxItem
            key={c}
            checked={columns.includes(c)}
            onSelect={(e) => e.preventDefault()}
            onCheckedChange={(on) => onChange(on ? [...columns, c] : columns.filter((other) => other !== c))}
          >
            {t(columnLabels[c])}
          </DropdownMenuCheckboxItem>
        ))}
        <DropdownMenuSeparator />
        <DropdownMenuItem onSelect={() => onChange(undefined)}>{t("Default columns")}</DropdownMenuItem>
      </DropdownMenuContent>
    </DropdownMenu>
  )
}
