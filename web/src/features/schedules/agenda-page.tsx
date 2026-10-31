import { CalendarDotsIcon, FlowArrowIcon, type Icon, SpinnerGapIcon } from "@phosphor-icons/react"
import { useQuery } from "@tanstack/react-query"
import { Link } from "@tanstack/react-router"
import { t } from "i18next"
import { ErrorCallout } from "@/components/callout"
import { EmptyState } from "@/components/empty-state"
import { TabIntro } from "@/components/hub-layout"
import { IconTile } from "@/components/icon-tile"
import { Section } from "@/components/section"
import type { Tone } from "@/components/tone"
import { Skeleton } from "@/components/ui/skeleton"
import { useAccess } from "@/features/access/use-access"
import { jobs, masterJobs } from "@/features/backups/api"
import { type AutomationTask, useAutomationTasks } from "@/features/dashboard/tasks"
import { policies } from "@/features/policies/api"
import { upcomingQuery, type Workflow, workflowsQuery } from "@/features/workflows/api"
import { dayOf, formatTime } from "@/lib/format"
import { locale, msg } from "@/lib/i18n"
import { today } from "./describe"

/** A backup job, schedule or workflow, with its page and how it looks. */
interface Item {
  id: string
  name: string
  running: boolean
  kind: string
  link: AutomationTask["link"] | { to: "/workflows/$workflowId"; params: { workflowId: string } }
  icon: Icon
  tone: Tone
}

/** An item with the times it runs on one day. */
interface Entry extends Item {
  times: string[]
}

const kinds: Record<AutomationTask["link"]["to"], string> = {
  "/backups/$jobId": msg("Backup job"),
  "/backups/master": msg("Backup of the master"),
  "/policies/$policyId": msg("Schedule"),
}

const fromTask = (a: AutomationTask): Item => ({
  ...a,
  id: a.task.id,
  name: a.task.name,
  running: a.task.running,
  kind: t(kinds[a.link.to]),
})

const fromWorkflow = (w: Workflow): Item => ({
  id: w.id,
  name: w.name,
  running: w.running > 0,
  kind: t("Workflow"),
  link: { to: "/workflows/$workflowId", params: { workflowId: w.id } },
  icon: FlowArrowIcon,
  tone: "warning",
})

/** The days of the next week that items run on, in the panel's time zone, with the items of each in order. */
function byDay(items: Item[], upcoming: { id: string; at: string }[]) {
  const days = new Map<string, Map<string, Entry>>()
  for (const { id, at } of upcoming.toSorted((a, b) => Date.parse(a.at) - Date.parse(b.at))) {
    const task = items.find((a) => a.id === id)
    if (!task) continue
    const day = dayOf(at)
    if (!days.has(day)) days.set(day, new Map())
    const entries = days.get(day)!
    if (!entries.has(id)) entries.set(id, { ...task, times: [] })
    entries.get(id)!.times.push(at)
  }
  return [...days].map(([day, entries]) => ({ day, entries: [...entries.values()] }))
}

function dayTitle(day: string) {
  const date = Date.parse(`${day}T12:00:00Z`)
  const name = new Intl.DateTimeFormat(locale, { weekday: "long", day: "numeric", month: "long", timeZone: "UTC" }).format(date)
  if (day === today()) return t("Today, {{date}}", { date: name })
  const tomorrow = dayOf(Date.now() + 86_400_000)
  return day === tomorrow ? t("Tomorrow, {{date}}", { date: name }) : name
}

const time = (at: string) => formatTime(at, { timeStyle: "short" })

/** The times of a task on a day, e.g. "04:00 · 16:00", or how often it runs if that is often. */
const describeTimes = (times: string[]) =>
  times.length > 3
    ? t("{{count}} times, {{first}} to {{last}}", { count: times.length, first: time(times[0]), last: time(times[times.length - 1]) })
    : times.map(time).join(" · ")

/** The Agenda tab of the automation: what runs now and what runs in the next 7 days. */
export function AgendaPage() {
  const access = useAccess()
  const [canJobs, canPolicies, canWorkflows] = [access.can("backupjobs.view"), access.can("policies.view"), access.can("workflows.view")]
  const tasks = useAutomationTasks()
  const flows = useQuery({ ...workflowsQuery, enabled: canWorkflows })
  const upcoming = [
    useQuery({ ...jobs.upcomingQuery, enabled: canJobs }),
    useQuery({ ...masterJobs.upcomingQuery, enabled: access.admin }),
    useQuery({ ...policies.upcomingQuery, enabled: canPolicies }),
    useQuery({ ...upcomingQuery, enabled: canWorkflows }),
  ]
  // The tasks name the runs, so the agenda waits for them too.
  const queries = [
    ...upcoming,
    flows,
    useQuery({ ...jobs.tasksQuery, enabled: canJobs }),
    useQuery({ ...masterJobs.tasksQuery, enabled: access.admin }),
    useQuery({ ...policies.tasksQuery, enabled: canPolicies }),
  ]
  const error = queries.find((q) => q.error)?.error
  const items = [...tasks.map(fromTask), ...(flows.data ?? []).map(fromWorkflow)]
  const running = items.filter((a) => a.running)
  const days = byDay(
    items,
    upcoming.flatMap((q) => q.data ?? []),
  )
  return (
    <>
      <TabIntro>{t("What backup jobs, schedules and workflows do in the next 7 days, in your time zone. Paused ones are left out.")}</TabIntro>
      {error ? (
        <ErrorCallout error={error} />
      ) : queries.some((q) => q.isLoading) ? (
        <Skeleton className="h-60 rounded-xl" />
      ) : running.length + days.length === 0 ? (
        <EmptyState icon={CalendarDotsIcon} tone="warning" title={t("Nothing runs in the next 7 days")} />
      ) : (
        <div className="grid gap-8">
          {running.length > 0 && <Day title={t("Running now")} entries={running.map((a) => ({ ...a, times: [] }))} />}
          {days.map(({ day, entries }) => (
            <Day key={day} title={dayTitle(day)} entries={entries} />
          ))}
        </div>
      )}
    </>
  )
}

function Day({ title, entries }: { title: string; entries: Entry[] }) {
  return (
    <Section title={title} className="mt-0">
      <ul className="surface divide-y rounded-xl">
        {entries.map(({ id, name, kind, link, icon, tone, times }) => (
          <li key={id} className="flex items-center gap-3 px-4 py-2.5">
            <IconTile icon={icon} tone={tone} size="sm" />
            <div className="min-w-0 flex-1">
              <Link {...link} className="block truncate text-sm font-medium hover:underline">
                {name}
              </Link>
              <span className="block truncate text-xs text-muted-foreground">{kind}</span>
            </div>
            <span className="max-w-36 text-right font-mono text-xs tabular-nums sm:max-w-none sm:text-sm">
              {times.length > 0 ? (
                describeTimes(times)
              ) : (
                <SpinnerGapIcon aria-hidden className="size-4 animate-spin text-muted-foreground" />
              )}
            </span>
          </li>
        ))}
      </ul>
    </Section>
  )
}
