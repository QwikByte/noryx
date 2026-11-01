import { BellIcon, BellRingingIcon, BellZIcon } from "@phosphor-icons/react"
import { queryOptions, useQuery, useQueryClient } from "@tanstack/react-query"
import { Link, useNavigate } from "@tanstack/react-router"
import { t } from "i18next"
import { useState } from "react"
import { toast } from "sonner"
import { Button } from "@/components/ui/button"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuSub,
  DropdownMenuSubContent,
  DropdownMenuSubTrigger,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { meQuery } from "@/features/auth/api"
import { notifyDesktop } from "@/features/notify/desktop"
import { popsUp, quiet, quietHours, useAlerts, usePinned } from "@/features/preferences/api"
import { formatDateTime, formatDuration, formatTime } from "@/lib/format"
import { useNow } from "@/lib/use-now"
import { cn } from "@/lib/utils"
import { api } from "@/lib/api"
import { filterQuery, type LogEntry, type LogFilter, useLogStream } from "./api"
import { formatEntryTime, levels } from "./meta"

const problems: LogFilter = { level: "warn" }
const shown = 8
const seenKey = "noryx-logs-seen"

const problemsQuery = queryOptions({
  queryKey: ["logs", "problems"],
  queryFn: () => api<LogEntry[]>(`/logs?${filterQuery(problems, { limit: "20" })}`),
})

// The last seen entry is a convenience of this browser; without storage, all count as new.
function readSeen() {
  try {
    return Number(localStorage.getItem(seenKey)) || 0
  } catch {
    return 0
  }
}

function writeSeen(id: number) {
  try {
    localStorage.setItem(seenKey, String(id))
  } catch {
    // nothing to remember then
  }
}

const about = (e: LogEntry) =>
  [e.serverName ?? e.nodeName, e.user && t("by {{user}}", { user: e.user }), e.attrs.err].filter(Boolean).join(" · ")

/**
 * The bell with the latest warnings and errors and how many are new. New ones also show up
 * as toasts, except those of the user's own actions, which the panel reported already, and as
 * notifications of the operating system while the tab is in the background, if the user turned them on;
 * the user chooses which of them do, and can keep all of them from popping up for a while.
 */
export function LogAlerts() {
  const { data: me } = useQuery(meQuery)
  const { data: entries = [] } = useQuery(problemsQuery)
  const queryClient = useQueryClient()
  const navigate = useNavigate()
  const [seen, setSeen] = useState(readSeen)
  const { alerts, change } = useAlerts()
  const { pinned } = usePinned()
  // Renders again while paused, so that the bell rings again once the pause ends.
  const quietNow = quiet(alerts, useNow(quiet(alerts), 30_000))

  useLogStream(problems, (entry) => {
    void queryClient.invalidateQueries({ queryKey: problemsQuery.queryKey })
    if (entry.user === me?.username || !popsUp(entry, alerts, pinned)) return
    const show = () => void navigate({ to: "/logs", search: { level: "warn" } })
    const notify = entry.level === "error" ? toast.error : toast.warning
    notify(entry.message, { description: about(entry), action: { label: t("Show"), onClick: show } })
    notifyDesktop(entry, about(entry), show)
  })

  const unread = entries.filter((e) => e.id > seen).length
  const label = unread > 0 ? t("{{number}} new warnings and errors", { number: unread === 20 ? "20+" : unread }) : t("Warnings and errors")
  // Closing the menu marks what it showed as seen.
  const markSeen = (open: boolean) => {
    if (open || !entries[0]) return
    setSeen(entries[0].id)
    writeSeen(entries[0].id)
  }

  return (
    <DropdownMenu onOpenChange={markSeen}>
      <DropdownMenuTrigger asChild>
        <Button variant="ghost" size="icon-sm" aria-label={label} title={label} className="relative shrink-0 text-muted-foreground">
          {quietNow ? <BellZIcon /> : unread > 0 ? <BellRingingIcon className="text-foreground" /> : <BellIcon />}
          {unread > 0 && (
            <span
              aria-hidden
              className="absolute -top-0.5 -right-0.5 grid h-4 min-w-4 place-items-center rounded-full bg-destructive px-1 text-[0.625rem] font-bold text-white tabular-nums"
            >
              {unread === 20 ? "20+" : unread}
            </span>
          )}
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="w-80">
        <DropdownMenuLabel>{t("Warnings and errors")}</DropdownMenuLabel>
        <DropdownMenuSeparator />
        {entries.length === 0 && <p className="px-2 py-4 text-center text-sm text-muted-foreground">{t("Nothing has gone wrong lately.")}</p>}
        {entries.slice(0, shown).map((e) => {
          const { icon: Icon, tone, label } = levels[e.level]
          return (
            <DropdownMenuItem key={e.id} asChild className={cn("items-start gap-2.5", e.id > seen && "bg-muted/60")}>
              <Link to="/logs" search={{ level: "warn" }}>
                <Icon
                  aria-label={t(label)}
                 
                  className={cn("mt-0.5", tone === "destructive" ? "text-destructive" : "text-warning")}
                />
                <span className="min-w-0 flex-1">
                  <span className="block truncate font-medium">{e.message}</span>
                  <span className="block truncate text-xs text-muted-foreground">{about(e) || formatEntryTime(e.time)}</span>
                </span>
                <time dateTime={e.time} className="shrink-0 text-[0.6875rem] text-muted-foreground tabular-nums">
                  {formatTime(e.time, { timeStyle: "short" })}
                </time>
              </Link>
            </DropdownMenuItem>
          )
        })}
        <DropdownMenuSeparator />
        <DropdownMenuItem asChild>
          <Link to="/logs">{t("Open the log")}</Link>
        </DropdownMenuItem>
        {quietNow ? (
          <DropdownMenuItem onSelect={() => change({ quietUntil: undefined })}>
            {t("Pop up again (paused until {{time}})", { time: formatDateTime(alerts.quietUntil!) })}
          </DropdownMenuItem>
        ) : (
          <DropdownMenuSub>
            <DropdownMenuSubTrigger>{t("Pause pop-ups")}</DropdownMenuSubTrigger>
            <DropdownMenuSubContent>
              {quietHours.map((hours) => (
                <DropdownMenuItem key={hours} onSelect={() => change({ quietUntil: new Date(Date.now() + hours * 3_600_000).toISOString() })}>
                  {t("For {{duration}}", { duration: formatDuration(hours * 3_600_000) })}
                </DropdownMenuItem>
              ))}
            </DropdownMenuSubContent>
          </DropdownMenuSub>
        )}
        <DropdownMenuItem asChild>
          <Link to="/account" hash="notifications">
            {t("Choose what pops up")}
          </Link>
        </DropdownMenuItem>
      </DropdownMenuContent>
    </DropdownMenu>
  )
}
