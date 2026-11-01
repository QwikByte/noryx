import { ArchiveIcon } from "@phosphor-icons/react"
import { useQuery } from "@tanstack/react-query"
import { getRouteApi, useNavigate } from "@tanstack/react-router"
import { t } from "i18next"
import { toast } from "sonner"
import { BackLink } from "@/components/back-link"
import { Callout, ErrorCallout } from "@/components/callout"
import { FormSection } from "@/components/form-section"
import { PageHeader } from "@/components/page-header"
import { usePageName } from "@/components/page-title"
import { Skeleton } from "@/components/ui/skeleton"
import { useAccess } from "@/features/access/use-access"
import { DatastoresField } from "@/features/datastores/datastores-field"
import { nodesQuery } from "@/features/nodes/api"
import type { TaskInput } from "@/features/schedules/api"
import { RunHistory } from "@/features/schedules/run-history"
import { TaskForm } from "@/features/schedules/task-form"
import { emptyJob, type JobSettings, jobs } from "./api"
import { LocationField, RetentionField, SelectionField } from "./backup-fields"
import { CopyField } from "./storages"

const route = getRouteApi("/_app/backups/$jobId")

export function BackupJobPage() {
  const manage = useAccess().can("backupjobs.manage")
  const { jobId } = route.useParams()
  const { data: job, isPending, error } = useQuery(jobs.taskQuery(jobId))
  usePageName(job?.name)
  const save = jobs.useSaveTask(jobId)
  return (
    <>
      <BackLink to="/backups">{t("Backups")}</BackLink>
      {isPending ? (
        <Skeleton className="h-96 rounded-xl" />
      ) : error ? (
        <ErrorCallout error={error} />
      ) : (
        <>
          <PageHeader icon={ArchiveIcon} tone="info" title={job.name} />
          {job.settings.copy && (
            <Callout tone={job.savedBy ? "info" : "warning"} className="mb-6">
              {job.savedBy
                ? t("It copies backups with the permissions of {{user}}, who saved it last.", { user: job.savedBy })
                : t("The user who saved it last was deleted or disabled, so it can't copy backups. Save it again.")}
            </Callout>
          )}
          {/* Without the permission to manage jobs, the job is only shown. */}
          <fieldset disabled={!manage} className="contents">
            <JobForm
              // Remounting on save resets the form to what was stored.
              key={JSON.stringify(job)}
              initial={{ name: job.name, enabled: job.enabled, schedule: job.schedule, targets: job.targets, settings: job.settings }}
              submitLabel={t("Save job")}
              save={save}
              onSaved={(j) => toast.success(t("Saved {{name}}", { name: j.name }))}
            />
          </fieldset>
          <RunHistory taskApi={jobs} id={jobId} />
        </>
      )}
    </>
  )
}

export function NewBackupJobPage() {
  const save = jobs.useSaveTask()
  usePageName(t("New backup job"))
  const navigate = useNavigate()
  return (
    <>
      <BackLink to="/backups">{t("Backups")}</BackLink>
      <PageHeader icon={ArchiveIcon} tone="info" title={t("New backup job")} />
      <JobForm
        initial={emptyJob}
        submitLabel={t("Create job")}
        save={save}
        onSaved={(j) => {
          toast.success(t("Created {{name}}", { name: j.name }))
          void navigate({ to: "/backups", ignoreBlocker: true })
        }}
      />
    </>
  )
}

function JobForm({
  initial,
  submitLabel,
  save,
  onSaved,
}: {
  initial: TaskInput<JobSettings>
  submitLabel: string
  save: ReturnType<typeof jobs.useSaveTask>
  onSaved: (job: { name: string }) => void
}) {
  const { data: nodes = [] } = useQuery(nodesQuery)
  const locations = nodes.flatMap((n) => n.info?.storage.map((l) => l.name) ?? [])
  return (
    <TaskForm
      initial={initial}
      kind="job"
      submitLabel={submitLabel}
      pending={save.isPending}
      error={save.error}
      onSubmit={(input) => save.mutate(input, { onSuccess: onSaved })}
      // A job that only backs up datastores needs no servers.
      targetsOptional={(settings) => settings.datastores.length > 0}
    >
      {(settings, set) => (
        <FormSection title={t("Backups")}>
          <SelectionField value={settings.selection} onChange={(selection) => set({ selection })} />
          <DatastoresField value={settings.datastores} onChange={(datastores) => set({ datastores })} />
          <LocationField locations={locations} value={settings.location} onChange={(location) => set({ location })} />
          <RetentionField value={settings} onChange={set} />
          <CopyField value={settings.copy} onChange={(copy) => set({ copy })} />
        </FormSection>
      )}
    </TaskForm>
  )
}
