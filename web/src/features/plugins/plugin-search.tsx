import {
  ArrowsDownUpIcon,
  CaretDownIcon,
  ClockCounterClockwiseIcon,
  DownloadSimpleIcon,
  HeartIcon,
  MagnifyingGlassIcon,
  SquaresFourIcon,
  UsersThreeIcon,
} from "@phosphor-icons/react"
import { useInfiniteQuery, useQuery } from "@tanstack/react-query"
import { t } from "i18next"
import { type ReactNode, useState } from "react"
import { Choice } from "@/components/choice"
import { ErrorCallout } from "@/components/callout"
import { Chip } from "@/components/chip"
import { FilterChip } from "@/components/filter-chip"
import { Segmented } from "@/components/segmented"
import { Button } from "@/components/ui/button"
import { DropdownMenu, DropdownMenuCheckboxItem, DropdownMenuContent, DropdownMenuTrigger } from "@/components/ui/dropdown-menu"
import { InputGroup, InputGroupAddon, InputGroupInput } from "@/components/ui/input-group"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { Skeleton } from "@/components/ui/skeleton"
import { Switch } from "@/components/ui/switch"
import { useChoice } from "@/features/preferences/api"
import { serverType, serverTypes } from "@/features/servers/server-types"
import { formatDate } from "@/lib/format"
import { locale, msg } from "@/lib/i18n"
import { useDebounced } from "@/lib/use-debounced"
import {
  gameVersionsQuery,
  type Kind,
  onHangar,
  projectUrl,
  type Search,
  type SearchHit,
  type Sort,
  searchQuery,
} from "./api"
import { categories } from "./categories"
import { PluginIcon } from "./plugin-icon"

const numbers = new Intl.NumberFormat(locale)
const compact = new Intl.NumberFormat(locale, { notation: "compact" })

const sorts: Record<Sort, string> = {
  relevance: msg("Relevance"),
  downloads: msg("Most downloads"),
  follows: msg("Most followers"),
  newest: msg("Newest"),
  updated: msg("Recently updated"),
}

const modLoaders = serverTypes.flatMap((s) => (s.addons?.kind === "mods" ? s.addons.loaders : []))

type Filters = Omit<Search, "query" | "kind" | "sort" | "type">

/**
 * Searches Modrinth for plugins and mods, in categories and sorted, or Hangar for plugins. A given server type and Minecraft version are
 * fixed, otherwise they can be chosen, among the software of a kind if given. The source, the sort and the software of
 * each kind chosen last apply again. Without a query, the most downloaded ones come first.
 */
export function PluginSearch({
  kind,
  type,
  version,
  action,
  autoFocus,
}: {
  kind?: Kind
  type?: string
  version?: string
  action: (hit: SearchHit) => ReactNode
  autoFocus?: boolean
}) {
  const [source, setSource] = useChoice("pluginSource", "modrinth")
  const [sort, setSort] = useChoice("pluginSort", "relevance")
  const [softwareChoice, chooseSoftware] = useChoice(kind === "mods" ? "modType" : "pluginType", "")
  // Hangar has no mods, and plugins only of some software, e.g. not of Folia.
  const sources = kind !== "mods" && (type === undefined || onHangar(type))
  const hangar = sources && source === "hangar"
  const choices = serverTypes.filter((s) => s.addons && (!kind || s.addons.kind === kind) && (!hangar || onHangar(s.value)))
  const [input, setInput] = useState("")
  const query = useDebounced(input.trim())
  const [filters, setFilters] = useState<Filters>({ categories: [], serverOnly: false })
  const set = (change: Partial<Filters>) => setFilters((f) => ({ ...f, ...change }))
  // Without a choice of software, or one that the source lacks, the first is searched, the most common one.
  const chosen = softwareChoice === "all" ? undefined : (choices.find((s) => s.value === softwareChoice) ?? choices[0]).value
  const software = type ?? chosen
  // Players may have to install mods too, never plugins.
  const mods = !hangar && (software ? serverType(software).addons?.kind === "mods" : kind !== "plugins")
  const search: Search = {
    ...filters,
    source: hangar ? "hangar" : "modrinth",
    query,
    kind,
    sort,
    type: software,
    version: version ?? filters.version,
    // Hangar's categories differ, and it has no mods.
    categories: hangar ? [] : filters.categories,
    serverOnly: filters.serverOnly && mods,
  }

  const { data: releases = [] } = useQuery({ ...gameVersionsQuery, enabled: version === undefined })
  const { data, error, isPending, fetchNextPage, hasNextPage, isFetchingNextPage } = useInfiniteQuery(searchQuery(search))
  const hits = data?.pages.flatMap((p) => p.hits) ?? []
  const total = data?.pages[0]?.total ?? 0
  const narrowed = filters.categories.length > 0 || search.serverOnly || (version === undefined && !!filters.version)
  const toggle = (category: string, on: boolean) =>
    set({ categories: on ? [...filters.categories, category] : filters.categories.filter((c) => c !== category) })

  return (
    <div className="grid grid-cols-1 gap-4">
      <div className="space-y-3">
        <div className="flex flex-wrap gap-2">
          <InputGroup className="min-w-56 flex-1">
            <InputGroupAddon>
              <MagnifyingGlassIcon />
            </InputGroupAddon>
            <InputGroupInput
              type="search"
              placeholder={hangar ? t("Search Hangar, e.g. Chunky") : t("Search Modrinth, e.g. LuckPerms")}
              aria-label={t("Search plugins and mods")}
              autoFocus={autoFocus}
              value={input}
              onChange={(e) => setInput(e.target.value)}
            />
          </InputGroup>
          {sources && (
            <Segmented
              label={t("Source")}
              value={hangar ? "hangar" : "modrinth"}
              options={[
                { value: "modrinth", label: "Modrinth" },
                { value: "hangar", label: "Hangar" },
              ]}
              onChange={setSource}
              className="self-center"
            />
          )}
          <Select value={sort} onValueChange={(sort) => setSort(sort as Sort)}>
            <SelectTrigger aria-label={t("Sort")} className="max-sm:flex-1">
              <ArrowsDownUpIcon className="text-muted-foreground" />
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {Object.entries(sorts).map(([value, label]) => (
                <SelectItem key={value} value={value}>
                  {t(label)}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          {type === undefined && (
            <Choice
              label={t("Software")}
              value={chosen}
              onChange={(type) => chooseSoftware(type ?? "all")}
              everything={kind === "mods" ? t("All mod loaders") : kind === "plugins" ? t("All plugin software") : t("All software")}
            >
              {choices.map((s) => (
                <SelectItem key={s.value} value={s.value}>
                  {s.label}
                </SelectItem>
              ))}
            </Choice>
          )}
          {/* Proxies run plugins of any Minecraft version. */}
          {version === undefined && !(software && serverType(software).proxy) && (
            <Choice
              label={t("Minecraft version")}
              value={filters.version}
              onChange={(version) => set({ version })}
              everything={t("All Minecraft versions")}
            >
              {releases.map((r) => (
                <SelectItem key={r} value={r}>
                  {t("Minecraft {{version}}", { version: r })}
                </SelectItem>
              ))}
            </Choice>
          )}
          {!hangar && (
            <DropdownMenu>
              <DropdownMenuTrigger asChild>
                <Button variant="outline" className="font-normal max-sm:flex-1">
                  <SquaresFourIcon className="text-muted-foreground" />
                  {t("Categories")}
                  {filters.categories.length > 0 && (
                    <span className="rounded-full bg-primary px-1.5 text-xs text-primary-foreground">{filters.categories.length}</span>
                  )}
                  <CaretDownIcon className="text-muted-foreground" />
                </Button>
              </DropdownMenuTrigger>
              <DropdownMenuContent className="max-h-80 w-56">
                {Object.entries(categories).map(([value, { label, icon: Icon }]) => (
                  <DropdownMenuCheckboxItem
                    key={value}
                    checked={filters.categories.includes(value)}
                    onCheckedChange={(on) => toggle(value, on)}
                    onSelect={(e) => e.preventDefault()}
                  >
                    <Icon className="text-muted-foreground" />
                    {t(label)}
                  </DropdownMenuCheckboxItem>
                ))}
              </DropdownMenuContent>
            </DropdownMenu>
          )}
          {mods && (
            <label className="flex h-9 cursor-pointer items-center gap-2 px-1 text-sm" title={t("Only mods that players don't have to install")}>
              <Switch checked={filters.serverOnly} onCheckedChange={(serverOnly) => set({ serverOnly })} />
              {t("Server-side only")}
            </label>
          )}
        </div>
        {narrowed && (
          <div className="flex flex-wrap items-center gap-2">
            {filters.categories.map((c) => (
              <FilterChip key={c} label={t(categories[c].label)} onRemove={() => toggle(c, false)} />
            ))}
            <Button variant="ghost" size="sm" onClick={() => set({ categories: [], serverOnly: false, version: undefined })}>
              {t("Clear filters")}
            </Button>
          </div>
        )}
      </div>
      {isPending ? (
        <div className="grid gap-2">
          {[0, 1, 2].map((i) => (
            <Skeleton key={i} className="h-20 rounded-xl" />
          ))}
        </div>
      ) : error ? (
        <ErrorCallout error={error} />
      ) : hits.length === 0 ? (
        <p className="py-6 text-center text-sm text-muted-foreground">{t("Nothing found. Try another search or fewer filters.")}</p>
      ) : (
        <div className="space-y-2">
          <p className="text-xs text-muted-foreground">
            {t("{{total}} results", { count: total, total: numbers.format(total), defaultValue_one: "{{total}} result" })}
          </p>
          <ul className="grid grid-cols-1 gap-2">
            {hits.map((hit) => (
              <Hit key={hit.id} hit={hit} loaders={!software} action={action(hit)} />
            ))}
          </ul>
        </div>
      )}
      {hasNextPage && (
        <Button variant="outline" className="justify-self-center" disabled={isFetchingNextPage} onClick={() => fetchNextPage()}>
          {isFetchingNextPage ? t("Loading…") : t("Show more")}
        </Button>
      )}
    </div>
  )
}

/** A project found on Modrinth; with loaders, it tells what it runs on. */
function Hit({ hit, loaders, action }: { hit: SearchHit; loaders: boolean; action: ReactNode }) {
  const clientToo = hit.clientSide === "required" && hit.loaders.some((l) => modLoaders.includes(l))
  return (
    // On small screens, the action goes below the project, which keeps the room of its facts.
    <li className="grid grid-cols-[auto_minmax(0,1fr)] items-start gap-x-3 gap-y-2.5 rounded-xl p-3 ring-1 ring-border transition-colors hover:bg-muted/50 sm:grid-cols-[auto_minmax(0,1fr)_auto]">
      <PluginIcon src={hit.icon} />
      <div className="min-w-0 space-y-1">
        <p className="truncate text-sm font-semibold">
          <a href={projectUrl(hit)} target="_blank" rel="noreferrer" className="hover:underline">
            {hit.title}
          </a>
          <span className="font-normal text-muted-foreground"> {t("by {{author}}", { author: hit.author })}</span>
        </p>
        <p className="line-clamp-2 text-xs text-muted-foreground">{hit.description}</p>
        <div className="flex flex-wrap gap-1.5 pt-1">
          <Chip icon={DownloadSimpleIcon}>
            <span className="sr-only">{t("Downloads")}</span>
            {compact.format(hit.downloads)}
          </Chip>
          <Chip icon={HeartIcon}>
            <span className="sr-only">{t("Followers")}</span>
            {compact.format(hit.follows)}
          </Chip>
          <Chip icon={ClockCounterClockwiseIcon}>{t("Updated {{date}}", { date: formatDate(hit.updated) })}</Chip>
          {clientToo && (
            <Chip icon={UsersThreeIcon} className="bg-warning/10 text-warning">
              {t("Players need it too")}
            </Chip>
          )}
          {hit.categories.slice(0, 3).flatMap((c) => {
            const category = categories[c]
            return category ? [<Chip key={c} icon={category.icon} className="font-normal">{t(category.label)}</Chip>] : []
          })}
          {loaders &&
            hit.loaders.slice(0, 5).map((l) => (
              <Chip key={l} className="font-normal capitalize">
                {l}
              </Chip>
            ))}
        </div>
      </div>
      <div className="col-start-2 sm:col-start-auto sm:self-center">{action}</div>
    </li>
  )
}
