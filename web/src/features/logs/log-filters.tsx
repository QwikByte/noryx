import { MagnifyingGlassIcon } from "@phosphor-icons/react"
import { useQuery } from "@tanstack/react-query"
import { t } from "i18next"
import { useEffect, useRef, useState } from "react"
import { Choice } from "@/components/choice"
import { FilterChip } from "@/components/filter-chip"
import { Button } from "@/components/ui/button"
import { InputGroup, InputGroupAddon, InputGroupInput } from "@/components/ui/input-group"
import { SelectItem } from "@/components/ui/select"
import { nodesQuery } from "@/features/nodes/api"
import { useSettings } from "@/features/preferences/api"
import { serversQuery } from "@/features/servers/api"
import { msg } from "@/lib/i18n"
import { categories } from "./meta"
import { type LogSearch, ranges, timeOf } from "./search"
import { fromWallClock, toWallClock } from "@/lib/format"

const levelLabels = { info: msg("Info and above"), warn: msg("Warnings and errors"), error: msg("Errors only") }

/**
 * The filters of the log page in one row, and the narrower ones as removable chips below it. The user's default level
 * shows as a chip while it applies, and a level chosen here can become the default.
 */
export function LogFilters({ search, onChange }: { search: LogSearch; onChange: (change: Partial<LogSearch>) => void }) {
  const [text, setText] = useState(search.search ?? "")
  // A time from one to another, also while both its ends are empty.
  const [custom, setCustom] = useState(!!(search.from || search.to))
  const typing = useRef(0)
  const { settings, change } = useSettings()
  const { data: nodes = [] } = useQuery(nodesQuery)
  const { data: servers } = useQuery({ ...serversQuery(search.node ?? ""), enabled: !!search.node && !!search.server })
  useEffect(() => () => clearTimeout(typing.current), [])

  // The search applies once typing pauses.
  function type(value: string) {
    setText(value)
    clearTimeout(typing.current)
    typing.current = window.setTimeout(() => onChange({ search: value.trim() || undefined }), 300)
  }

  const node = nodes.find((n) => n.id === search.node)
  const fixed = custom || !!(search.from || search.to)
  // All levels are those from debug on, which the address names while the default level would apply otherwise.
  const level = search.level === "debug" ? undefined : (search.level ?? settings.logLevel)
  const chips: [string, Partial<LogSearch>][] = []
  if (!search.level && settings.logLevel)
    chips.push([t("Default level: {{level}}", { level: t(levelLabels[settings.logLevel]) }), { level: "debug" }])
  if (search.user) chips.push([t("User {{user}}", { user: search.user }), { user: undefined }])
  if (search.server)
    chips.push([
      t("Server {{server}}", { server: servers?.find((srv) => srv.id === search.server)?.name ?? search.server }),
      { server: undefined },
    ])
  const filtered = Object.values(search).some(Boolean)

  return (
    <div className="mb-6 space-y-3">
      <div className="flex flex-wrap gap-2">
        <InputGroup className="min-w-56 flex-1">
          <InputGroupAddon>
            <MagnifyingGlassIcon />
          </InputGroupAddon>
          <InputGroupInput
            type="search"
            placeholder={t("Search messages, users, servers, IPs…")}
            aria-label={t("Search the log")}
            value={text}
            onChange={(e) => type(e.target.value)}
          />
        </InputGroup>
        <Choice
          label={t("Time")}
          value={fixed ? "custom" : search.range}
          onChange={(range) => {
            setCustom(range === "custom")
            // A range up to now becomes one from its start on.
            onChange(
              range === "custom"
                ? { range: undefined, from: timeOf(search).since }
                : { range: range as LogSearch["range"], from: undefined, to: undefined },
            )
          }}
          everything={t("All time")}
        >
          {Object.entries(ranges).map(([value, { label }]) => (
            <SelectItem key={value} value={value}>
              {t(label)}
            </SelectItem>
          ))}
          <SelectItem value="custom">{t("From … until …")}</SelectItem>
        </Choice>
        {fixed && (
          <>
            <TimeField label={t("From")} value={search.from} max={search.to} onChange={(from) => onChange({ from })} />
            <TimeField label={t("Until")} value={search.to} min={search.from} onChange={(to) => onChange({ to })} />
          </>
        )}
        <Choice
          label={t("Level")}
          value={level}
          onChange={(level) => onChange({ level: (level ?? (settings.logLevel && "debug")) as LogSearch["level"] })}
          everything={t("All levels")}
        >
          {Object.entries(levelLabels).map(([value, label]) => (
            <SelectItem key={value} value={value}>
              {t(label)}
            </SelectItem>
          ))}
        </Choice>
        <Choice
          label={t("Category")}
          value={search.category}
          onChange={(category) => onChange({ category })}
          everything={t("All categories")}
        >
          {Object.entries(categories).map(([value, label]) => (
            <SelectItem key={value} value={value}>
              {t(label)}
            </SelectItem>
          ))}
        </Choice>
        <Choice
          label={t("Source")}
          value={search.source}
          onChange={(source) => onChange({ source: source as LogSearch["source"] })}
          everything={t("Master and agents")}
        >
          <SelectItem value="master">{t("Master")}</SelectItem>
          <SelectItem value="agent">{t("Agents")}</SelectItem>
        </Choice>
        {nodes.length > 0 && (
          <Choice
            label={t("Node")}
            value={search.node}
            onChange={(id) => onChange({ node: id, server: undefined })}
            everything={t("All nodes")}
          >
            {search.node && !node && <SelectItem value={search.node}>{t("Removed node")}</SelectItem>}
            {nodes.map((n) => (
              <SelectItem key={n.id} value={n.id}>
                {n.name}
              </SelectItem>
            ))}
          </Choice>
        )}
      </div>
      {(chips.length > 0 || filtered) && (
        <div className="flex flex-wrap items-center gap-2">
          {chips.map(([label, change]) => (
            <FilterChip key={label} label={label} onRemove={() => onChange(change)} />
          ))}
          {search.level && level !== settings.logLevel && (
            <Button
              variant="ghost"
              size="sm"
              onClick={() => {
                change({ logLevel: level ?? null })
                onChange({ level: undefined })
              }}
            >
              {level ? t("Show this level by default") : t("Show all levels by default")}
            </Button>
          )}
          {filtered && (
            <Button
              variant="ghost"
              size="sm"
              onClick={() => {
                clearTimeout(typing.current)
                setText("")
                setCustom(false)
                onChange(Object.fromEntries(Object.keys(search).map((key) => [key, undefined])))
              }}
            >
              {t("Clear filters")}
            </Button>
          )}
        </div>
      )}
    </div>
  )
}

/** An end of the time from one to another, as a date and time in the user's time zone; empty leaves it open. */
function TimeField({
  label,
  value,
  min,
  max,
  onChange,
}: {
  label: string
  value?: string
  min?: string
  max?: string
  onChange: (value: string | undefined) => void
}) {
  return (
    <InputGroup className="w-auto max-sm:w-full">
      <InputGroupAddon>{label}</InputGroupAddon>
      <InputGroupInput
        type="datetime-local"
        aria-label={label}
        value={value ? toWallClock(value) : ""}
        min={min && toWallClock(min)}
        max={max && toWallClock(max)}
        onChange={(e) => onChange(e.target.value ? fromWallClock(e.target.value).toISOString() : undefined)}
      />
    </InputGroup>
  )
}
