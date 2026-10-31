import { ArchiveIcon, CubeIcon, type Icon } from "@phosphor-icons/react"
import { useQuery } from "@tanstack/react-query"
import type { Tone } from "@/components/tone"
import { useAccess } from "@/features/access/use-access"
import { jobs, masterJobs } from "@/features/backups/api"
import { actions, policies } from "@/features/policies/api"
import type { Task } from "@/features/schedules/api"

/** A backup job, the backup job of the master or a schedule, with its page and how it looks. */
export interface AutomationTask {
  task: Task<unknown>
  link:
    | { to: "/backups/$jobId"; params: { jobId: string } }
    | { to: "/backups/master" }
    | { to: "/policies/$policyId"; params: { policyId: string } }
  icon: Icon
  tone: Tone
}

/** The backup jobs and schedules the user may see, and the backup job of the master for administrators. */
export function useAutomationTasks(): AutomationTask[] {
  const access = useAccess()
  const { data: backupJobs = [] } = useQuery({ ...jobs.tasksQuery, enabled: access.can("backupjobs.view") })
  const { data: masterBackups = [] } = useQuery({ ...masterJobs.tasksQuery, enabled: access.admin })
  const { data: schedules = [] } = useQuery({ ...policies.tasksQuery, enabled: access.can("policies.view") })
  return [
    ...masterBackups.map((task) => ({ task, link: { to: "/backups/master" } as const, icon: CubeIcon, tone: "violet" as const })),
    ...backupJobs.map((task) => ({
      task,
      link: { to: "/backups/$jobId", params: { jobId: task.id } } as const,
      icon: ArchiveIcon,
      tone: "info" as const,
    })),
    ...schedules.map((task) => ({
      task,
      link: { to: "/policies/$policyId", params: { policyId: task.id } } as const,
      icon: actions[task.settings.action].icon,
      tone: "warning" as const,
    })),
  ]
}

/** Whether the last run of a task that still runs on its schedule failed; paused tasks are left alone. */
export const failed = (task: Task<unknown>) => task.enabled && !!task.lastRun?.error
