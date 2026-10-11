import { ArchiveIcon, PencilSimpleIcon, PlusIcon } from "@phosphor-icons/react"
import { useQuery } from "@tanstack/react-query"
import { getRouteApi, Link } from "@tanstack/react-router"
import { t } from "i18next"
import { ErrorCallout } from "@/components/callout"
import { EmptyState } from "@/components/empty-state"
import { TabIntro } from "@/components/hub-layout"
import { NoMatch, SearchField } from "@/components/list-toolbar"
import { Button } from "@/components/ui/button"
import { Skeleton } from "@/components/ui/skeleton"
import { useAccess } from "@/features/access/use-access"
import { nodesQuery } from "@/features/nodes/api"
import type { Task } from "@/features/schedules/api"
import { TaskCard } from "@/features/schedules/task-card"
import { matches } from "@/lib/search"
import { type CopyTo, describeRetention, describeSelection, type JobSettings, jobs, nothingSelected, storagesQuery } from "./api"
import { GoneCopies } from "./copies"
import { StoragesSection } from "./storages"

const route = getRouteApi("/_app/_automation/backups")

function NewJob() {
  return (
    <Button asChild>
      <Link to="/backups/new">
        <PlusIcon />
        {t("New backup job")}
      </Link>
    </Button>
  )
}

/** The backup jobs, searched by the address. */
export function BackupJobsPage() {
  const manage = useAccess().can("backupjobs.manage")
  const { data: list, isPending, error } = useQuery(jobs.tasksQuery)
  const { data: storages = [] } = useQuery(storagesQuery)
  const { data: nodes = [] } = useQuery(nodesQuery)
  const { q } = route.useSearch()
  const navigate = route.useNavigate()
  /** Where a job copies its backups to, by name. */
  const copiesTo = ({ storage, node }: CopyTo) =>
    t("copies to {{where}}", { where: (storage ? storages.find((s) => s.id === storage) : nodes.find((n) => n.id === node))?.name ?? "…" })
  /** What a job backs up, how many backups it keeps and where it copies them, which the search finds too. */
  const summary = (job: Task<JobSettings>) =>
    [
      !nothingSelected(job.settings.selection) && describeSelection(job.settings.selection),
      job.settings.datastores?.length &&
        t("{{count}} datastores", { count: job.settings.datastores.length, defaultValue_one: "{{count}} datastore" }),
      describeRetention(job.settings),
      job.settings.copy && copiesTo(job.settings.copy),
    ]
      .filter(Boolean)
      .join(" · ")
  const shown = (list ?? []).filter((job) => matches(q, job.name, summary(job)))
  return (
    <>
      <TabIntro actions={manage && <NewJob />}>
        {t("Back up servers and databases on a schedule. Each job keeps as many backups as you choose.")}
      </TabIntro>
      {isPending ? (
        <div className="grid gap-4 md:grid-cols-2">
          {[0, 1].map((i) => (
            <Skeleton key={i} className="h-60 rounded-xl" />
          ))}
        </div>
      ) : error ? (
        <ErrorCallout error={error} />
      ) : list.length === 0 ? (
        <EmptyState icon={ArchiveIcon} tone="info" title={t("No backup jobs yet")}>
          {manage && <NewJob />}
        </EmptyState>
      ) : (
        <>
          <SearchField
            label={t("Search backup jobs")}
            value={q}
            onChange={(q) => navigate({ search: { q }, replace: true })}
            className="mb-5 sm:max-w-xs"
          />
          {shown.length === 0 ? (
            <NoMatch>{t("Nothing matches your search.")}</NoMatch>
          ) : (
            <ul className="grid gap-4 md:grid-cols-2">
              {shown.map((job) => (
                <TaskCard
                  manage={manage}
                  key={job.id}
                  task={job}
                  taskApi={jobs}
                  icon={ArchiveIcon}
                  tone="info"
                  title={
                    <Link to="/backups/$jobId" params={{ jobId: job.id }} className="block truncate font-semibold hover:underline">
                      {job.name}
                    </Link>
                  }
                  summary={summary(job)}
                  edit={
                    <Button asChild size="sm" variant="outline">
                      <Link to="/backups/$jobId" params={{ jobId: job.id }}>
                        <PencilSimpleIcon />
                        {t("Edit")}
                      </Link>
                    </Button>
                  }
                />
              ))}
            </ul>
          )}
        </>
      )}
      <StoragesSection />
      <GoneCopies />
    </>
  )
}
