import { DownloadSimpleIcon, ScrollIcon } from "@phosphor-icons/react"
import { getRouteApi } from "@tanstack/react-router"
import { t } from "i18next"
import { useMemo, useState } from "react"
import { EmptyState } from "@/components/empty-state"
import { PageHeader } from "@/components/page-header"
import { Section } from "@/components/section"
import { Button } from "@/components/ui/button"
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from "@/components/ui/dropdown-menu"
import { useAccess } from "@/features/access/use-access"
import { useCsvFormat, useSettings } from "@/features/preferences/api"
import { cn } from "@/lib/utils"
import { exportUrl, type LogFilter, useLiveLogs } from "./api"
import { LogOverview } from "./log-chart"
import { LogFilters } from "./log-filters"
import { LogList } from "./log-list"
import { timeOf } from "./search"

const route = getRouteApi("/_app/logs")

/**
 * The log of the master and its agents, filtered by the address, and by the user's default level where it names none,
 * with new entries streaming in.
 */
export function LogsPage() {
  const search = route.useSearch()
  const navigate = route.useNavigate()
  const [live, setLive] = useState(true)
  const csvFormat = useCsvFormat()
  const { logLevel } = useSettings().settings
  const { range, from, to, ...rest } = search
  const time = useMemo(() => timeOf({ range, from, to }), [range, from, to])
  const filter: LogFilter = { ...rest, level: search.level ?? logLevel, ...time }
  const update = (change: Partial<typeof search>) => void navigate({ search: (prev) => ({ ...prev, ...change }), replace: true })
  // A time with an end gets no new entries.
  const streaming = live && !to
  // Opened by its address without the permission, the page would ask for the log again and again.
  const allowed = useAccess().canSomewhere("logs.view")
  const down = useLiveLogs(filter, streaming && allowed)
  if (!allowed) {
    return (
      <>
        <PageHeader icon={ScrollIcon} tone="violet" title={t("Logs")} />
        <EmptyState icon={ScrollIcon} tone="neutral" title={t("Nothing to see here")} description={t("Your groups don't include the permission to see the log.")} />
      </>
    )
  }

  return (
    <>
      <PageHeader
        icon={ScrollIcon}
        tone="violet"
        title={t("Logs")}
        actions={
          <>
            <Button variant="outline" aria-pressed={live} onClick={() => setLive(!live)} disabled={!!to}>
              <span
                aria-hidden
                className={cn("size-2 rounded-full", !streaming ? "bg-muted-foreground/50" : down ? "animate-pulse bg-warning" : "animate-pulse bg-success")}
              />
              {!streaming ? t("Paused") : down ? t("Reconnecting…") : t("Live")}
            </Button>
            <DropdownMenu>
              <DropdownMenuTrigger asChild>
                <Button variant="outline">
                  <DownloadSimpleIcon />
                  {t("Export")}
                </Button>
              </DropdownMenuTrigger>
              <DropdownMenuContent align="end" className="w-56">
                <DropdownMenuItem asChild>
                  <a href={exportUrl(filter, csvFormat)} download>
                    {t("CSV for spreadsheets")}
                  </a>
                </DropdownMenuItem>
                <DropdownMenuItem asChild>
                  <a href={exportUrl(filter, "jsonl")} download>
                    {t("JSON lines for log tools")}
                  </a>
                </DropdownMenuItem>
              </DropdownMenuContent>
            </DropdownMenu>
          </>
        }
      />
      <LogFilters search={search} onChange={update} />
      <LogOverview
        filter={filter}
        onSelectHour={(start) =>
          update({ range: undefined, from: start.toISOString(), to: new Date(start.getTime() + 3_600_000).toISOString() })
        }
      />
      <Section title={t("Entries")} className="mt-8">
        <LogList filter={filter} live={streaming} onFilter={update} />
      </Section>
    </>
  )
}
