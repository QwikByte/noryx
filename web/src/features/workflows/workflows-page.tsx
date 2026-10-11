import { CaretDownIcon, FlowArrowIcon, LightningIcon, PencilSimpleIcon, PlayIcon, PlusIcon, SparkleIcon, TrashIcon } from "@phosphor-icons/react"
import { useQuery } from "@tanstack/react-query"
import { getRouteApi, Link } from "@tanstack/react-router"
import { t } from "i18next"
import { useState } from "react"
import { toast } from "sonner"
import { Callout, ErrorCallout } from "@/components/callout"
import { Chip } from "@/components/chip"
import { ConfirmDialog } from "@/components/confirm-dialog"
import { EmptyState } from "@/components/empty-state"
import { TabIntro } from "@/components/hub-layout"
import { IconTile } from "@/components/icon-tile"
import { NoMatch, SearchField } from "@/components/list-toolbar"
import { StatusBadge } from "@/components/status"
import { Button } from "@/components/ui/button"
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuLabel, DropdownMenuTrigger } from "@/components/ui/dropdown-menu"
import { Skeleton } from "@/components/ui/skeleton"
import { useAccess } from "@/features/access/use-access"
import { formatDateTime } from "@/lib/format"
import { matches } from "@/lib/search"
import { useDeleteWorkflow, useRunWorkflow, type Workflow, workflowsQuery } from "./api"
import { describeTrigger, triggers } from "./catalog"
import { examples } from "./examples"
import { RunNowDialog } from "./runs"
import { workflowStatus } from "./status"
import { walk } from "./tree"

const route = getRouteApi("/_app/_automation/workflows")

/** Creates a workflow, empty or from an example. */
function NewWorkflow() {
  return (
    <div className="flex">
      <Button asChild className="rounded-r-none">
        <Link to="/workflows/new">
          <PlusIcon />
          {t("New workflow")}
        </Link>
      </Button>
      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <Button className="rounded-l-none border-l border-primary-foreground/20 px-2" aria-label={t("Start from an example")}>
            <CaretDownIcon />
          </Button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="end" className="w-80">
          <DropdownMenuLabel>{t("Start from an example")}</DropdownMenuLabel>
          {examples.map((e, i) => (
            <DropdownMenuItem key={e.name} asChild className="items-start">
              <Link to="/workflows/new" search={{ example: i }}>
                <e.icon className="mt-0.5 text-warning" />
                <span className="grid">
                  <span className="font-medium">{t(e.name)}</span>
                  <span className="text-xs text-muted-foreground">{t(e.description)}</span>
                </span>
              </Link>
            </DropdownMenuItem>
          ))}
        </DropdownMenuContent>
      </DropdownMenu>
    </div>
  )
}

/** The workflows, searched by the address, or examples to start from while there are none. */
export function WorkflowsPage() {
  const manage = useAccess().can("workflows.manage")
  const { data: list, isPending, error } = useQuery(workflowsQuery)
  const { q } = route.useSearch()
  const navigate = route.useNavigate()
  const shown = (list ?? []).filter((w) => matches(q, w.name, w.description, ...w.triggers.map(describeTrigger)))
  return (
    <>
      <TabIntro actions={manage && <NewWorkflow />}>
        {t(
          "Chain actions with conditions, loops and variables, started by times, the log, players, measures or other systems. Each step can use what the trigger and the steps before told.",
        )}
      </TabIntro>
      {isPending ? (
        <div className="grid gap-4 md:grid-cols-2">
          {[0, 1].map((i) => (
            <Skeleton key={i} className="h-56 rounded-xl" />
          ))}
        </div>
      ) : error ? (
        <ErrorCallout error={error} />
      ) : list.length === 0 ? (
        <>
          <EmptyState icon={FlowArrowIcon} tone="warning" title={t("No workflows yet")} description={manage && t("Start with an empty one, or with an example below.")}>
            {manage && (
              <Button asChild variant="outline">
                <Link to="/workflows/new">
                  <SparkleIcon />
                  {t("Start empty")}
                </Link>
              </Button>
            )}
          </EmptyState>
          {manage && (
            <section aria-label={t("Examples")} className="mt-6 grid gap-3 sm:grid-cols-2 xl:grid-cols-3">
              {examples.map((e, i) => (
                <Link
                  key={e.name}
                  to="/workflows/new"
                  search={{ example: i }}
                  className="surface lift flex items-start gap-3 rounded-xl p-4 text-left outline-none focus-visible:ring-2 focus-visible:ring-ring"
                >
                  <IconTile icon={e.icon} tone="warning" size="sm" />
                  <span className="grid gap-0.5">
                    <span className="text-sm font-semibold">{t(e.name)}</span>
                    <span className="text-xs text-muted-foreground">{t(e.description)}</span>
                  </span>
                </Link>
              ))}
            </section>
          )}
        </>
      ) : (
        <>
          <SearchField
            label={t("Search workflows")}
            value={q}
            onChange={(q) => navigate({ search: { q }, replace: true })}
            className="mb-5 sm:max-w-xs"
          />
          {shown.length === 0 ? (
            <NoMatch>{t("Nothing matches your search.")}</NoMatch>
          ) : (
            <ul className="grid gap-4 md:grid-cols-2">
              {shown.map((w) => (
                <WorkflowCard key={w.id} workflow={w} manage={manage} />
              ))}
            </ul>
          )}
        </>
      )}
    </>
  )
}

function WorkflowCard({ workflow: w, manage }: { workflow: Workflow; manage: boolean }) {
  const remove = useDeleteWorkflow()
  const run = useRunWorkflow(w.id)
  const [asking, setAsking] = useState(false)
  const icon = w.triggers[0] ? triggers[w.triggers[0].kind].icon : LightningIcon
  const steps = walk(w.steps).length
  const start = () => {
    if (w.params.length > 0) return setAsking(true)
    run.mutate({}, { onSuccess: () => toast.success(t("Started {{name}}", { name: w.name })), onError: (e) => toast.error(e.message) })
  }
  return (
    <li className="surface flex min-w-0 flex-col gap-4 rounded-xl p-5">
      <div className="flex items-start gap-3">
        <IconTile icon={icon} tone="warning" />
        <div className="min-w-0 flex-1">
          <Link to="/workflows/$workflowId" params={{ workflowId: w.id }} className="block truncate font-semibold hover:underline">
            {w.name}
          </Link>
          <p className="truncate text-xs text-muted-foreground">{w.description || t("{{count}} steps", { count: steps, defaultValue_one: "1 step" })}</p>
        </div>
        <StatusBadge status={workflowStatus(w)} />
      </div>
      <div className="flex flex-wrap gap-1.5">
        {w.triggers.length === 0 && <Chip icon={PlayIcon}>{t("Started by hand")}</Chip>}
        {w.triggers.map((tr, i) => (
          // Triggers have no identity of their own and don't change here.
          <Chip key={i} icon={triggers[tr.kind].icon} className="max-w-full font-normal">
            <span className="truncate">{describeTrigger(tr)}</span>
          </Chip>
        ))}
        {w.description && <Chip className="font-normal">{t("{{count}} steps", { count: steps, defaultValue_one: "1 step" })}</Chip>}
      </div>
      {w.lastRun?.outcome === "failed" && (
        <Callout tone="destructive" title={t("The last run failed")} className="py-3">
          <span className="line-clamp-3 break-words">{w.lastRun.error}</span>
        </Callout>
      )}
      <p className="text-xs text-muted-foreground">
        {w.lastRun ? t("Last run {{time}}", { time: formatDateTime(w.lastRun.startedAt) }) : t("Never run")}
        {w.nextRun && ` · ${t("next {{time}}", { time: formatDateTime(w.nextRun) })}`}
      </p>
      <div className="mt-auto flex flex-wrap items-center gap-2 border-t pt-4" hidden={!manage}>
        <Button size="sm" disabled={run.isPending} onClick={start}>
          <PlayIcon />
          {t("Run now")}
        </Button>
        <Button asChild size="sm" variant="outline">
          <Link to="/workflows/$workflowId" params={{ workflowId: w.id }}>
            <PencilSimpleIcon />
            {t("Edit")}
          </Link>
        </Button>
        <ConfirmDialog
          trigger={
            <Button
              size="icon-sm"
              variant="ghost"
              aria-label={t("Delete {{name}}", { name: w.name })}
              title={t("Delete")}
              className="ml-auto text-muted-foreground hover:bg-destructive/10 hover:text-destructive"
            >
              <TrashIcon />
            </Button>
          }
          title={t("Delete {{name}}?", { name: w.name })}
          description={t("It no longer runs, and its runs in progress are cancelled.")}
          action={t("Delete")}
          destructive
          onConfirm={() =>
            remove.mutate(w.id, {
              onSuccess: () => toast.success(t("Deleted {{name}}", { name: w.name })),
              onError: (e) => toast.error(e.message),
            })
          }
        />
      </div>
      <RunNowDialog workflow={w} open={asking} onOpenChange={setAsking} />
    </li>
  )
}
