import { CalendarCheckIcon, WarningCircleIcon } from "@phosphor-icons/react"
import { Link } from "@tanstack/react-router"
import { t } from "i18next"
import { AnimatePresence, motion } from "motion/react"
import { IconTile } from "@/components/icon-tile"
import { useAccess } from "@/features/access/use-access"
import { describeSchedule } from "@/features/schedules/describe"
import { Ago } from "@/components/ago"
import { rise } from "@/lib/motion"
import { useNow } from "@/lib/use-now"
import { cn } from "@/lib/utils"
import { row } from "./lists"
import type { WidgetProps } from "./options"
import { Calm, Panel } from "./panel"
import { failed, useAutomationTasks } from "./tasks"

/** The backup jobs and schedules whose last run failed, then the next ones to run. */
export function Schedules({ title, options }: WidgetProps) {
  const access = useAccess()
  const tasks = useAutomationTasks()
  // Renders again now and then, so that the times stay current.
  const now = useNow(true, 30_000)
  const failing = tasks.filter((a) => failed(a.task))
  const next = tasks
    .filter((a) => a.task.nextRun && !failing.includes(a))
    .sort((a, b) => Date.parse(a.task.nextRun!) - Date.parse(b.task.nextRun!))
    .slice(0, Number(options.count))
  return (
    <Panel
      title={title}
      count={failing.length}
      more={{ to: access.can("backupjobs.view") ? "/backups" : "/policies", label: t("Open automation") }}
    >
      {failing.length + next.length === 0 ? (
        <Calm icon={CalendarCheckIcon} tone="warning">
          {t("Nothing is scheduled.")}
        </Calm>
      ) : (
        <ul className="divide-y">
          <AnimatePresence initial={false}>
            {[...failing, ...next].map(({ task, link, icon, tone }, i) => {
              const error = failed(task) && task.lastRun?.error
              return (
                <motion.li key={`${link.to}/${task.id}`} layout {...rise(i)} exit={{ opacity: 0, x: 16 }}>
                  <Link {...link} className={cn("flex items-center gap-3 px-4 py-2.5", row)}>
                    <IconTile icon={error ? WarningCircleIcon : icon} tone={error ? "destructive" : tone} size="sm" />
                    <span className="min-w-0 flex-1">
                      <span className="block truncate text-sm font-medium">{task.name}</span>
                      <span className={cn("block truncate text-xs", error ? "text-destructive" : "text-muted-foreground")}>
                        {error ? t("The last run failed: {{error}}", { error }) : describeSchedule(task.schedule)}
                      </span>
                    </span>
                    <span className="shrink-0 text-xs whitespace-nowrap text-muted-foreground">
                      {task.running
                        ? t("Running")
                        : task.nextRun && <Ago time={task.nextRun} now={now} />}
                    </span>
                  </Link>
                </motion.li>
              )
            })}
          </AnimatePresence>
        </ul>
      )}
    </Panel>
  )
}
