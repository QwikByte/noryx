import { CheckCircleIcon, ClockCounterClockwiseIcon, FilesIcon, PaperPlaneTiltIcon, TrashIcon, UsersThreeIcon, WarningIcon } from "@phosphor-icons/react"
import { useQuery } from "@tanstack/react-query"
import { getRouteApi, useBlocker, useNavigate } from "@tanstack/react-router"
import { t } from "i18next"
import { useState } from "react"
import { toast } from "sonner"
import { BackLink } from "@/components/back-link"
import { ErrorCallout } from "@/components/callout"
import { ConfirmDialog } from "@/components/confirm-dialog"
import { PageHeader } from "@/components/page-header"
import { usePageName } from "@/components/page-title"
import { Pill, StatusDot } from "@/components/status"
import { Button } from "@/components/ui/button"
import { Skeleton } from "@/components/ui/skeleton"
import { useAccess } from "@/features/access/use-access"
import { msg } from "@/lib/i18n"
import { errorsAt, formError, useDraft } from "@/lib/use-draft"
import {
  type FileSet,
  fileSetQuery,
  type ServerStatus,
  type SetInput,
  type State,
  statusQuery,
  useDeleteFileSet,
  usedSecrets,
  usedVariables,
  useSaveFileSet,
} from "./api"
import { ApplyDialog } from "./apply-dialog"
import { HistoryTab } from "./history-tab"
import { states } from "./labels"
import { ServersTab } from "./servers-tab"
import { Workspace } from "./workspace"

const route = getRouteApi("/_app/filesets/$fileSetId")

const draftOf = ({ name, description, files, targets, variables, version }: FileSet): SetInput => ({ name, description, files, targets, variables, version })

const tabs = [
  { id: "files", label: msg("Files"), icon: FilesIcon },
  { id: "servers", label: msg("Servers"), icon: UsersThreeIcon },
  { id: "history", label: msg("History"), icon: ClockCounterClockwiseIcon },
] as const

// The states of servers that applying the set changes.
const pending: State[] = ["outdated", "changed", "missing", "left"]

// The fields of a set that the Files tab shows what the master refused about.
const fields = ["name", "description", "targets", "variables"]

type Tab = (typeof tabs)[number]["id"]

export function FileSetPage() {
  const { fileSetId } = route.useParams()
  const { data: set, isPending, error } = useQuery(fileSetQuery(fileSetId))
  usePageName(set?.name)
  return (
    <>
      <BackLink to="/filesets">{t("File sets")}</BackLink>
      {isPending ? <Skeleton className="h-96 rounded-xl" /> : error ? <ErrorCallout error={error} /> : <SetEditor key={set.id} set={set} />}
    </>
  )
}

/** A set with its files and settings as they are edited, which saving stores as a new version. */
function SetEditor({ set }: { set: FileSet }) {
  const editable = useAccess().can("filesets.manage")
  // Without changes of its own, the draft follows what others save.
  const { value: draft, dirty, set: change, reset } = useDraft(draftOf(set))
  // Saving or discarding loads the stored set into the editors again.
  const [generation, setGeneration] = useState(0)
  const [tab, setTab] = useState<Tab>("files")
  const [selected, setSelected] = useState<string>()
  const [applying, setApplying] = useState<FileSet>()
  const { data: statuses } = useQuery(statusQuery(set.id))
  const save = useSaveFileSet(set.id)
  const remove = useDeleteFileSet()
  const navigate = useNavigate()
  const blocker = useBlocker({ shouldBlockFn: () => dirty && !save.isPending, enableBeforeUnload: () => dirty, withResolver: true })

  // Without changes of its own, the editors load what others save too.
  const [shown, setShown] = useState(set)
  if (set !== shown && !dirty) {
    setShown(set)
    setGeneration((g) => g + 1)
  }
  // A refused change shows at the field it is about on the Files tab, and else in the bar of unsaved changes.
  const refused = formError(save.error, tab === "files" ? fields : [])

  // Saving stores a new version, which the draft follows then, and opens the preview of applying it if asked to.
  const store = (apply = false) =>
    save.mutate(draft, {
      onSuccess: (saved) => {
        discard()
        if (apply) setApplying(saved)
        else toast.success(t("Saved {{name}}", { name: saved.name }))
      },
    })
  const discard = () => {
    reset()
    setGeneration((g) => g + 1)
  }

  return (
    <>
      <PageHeader
        icon={FilesIcon}
        tone="info"
        title={set.name}
        description={set.description || undefined}
        badge={<Pill tone="neutral">{t("Version {{version}}", { version: set.version })}</Pill>}
        actions={
          editable && (
            <ConfirmDialog
              trigger={
                <Button variant="outline" className="text-destructive hover:bg-destructive/10 hover:text-destructive">
                  <TrashIcon />
                  {t("Delete")}
                </Button>
              }
              title={t("Delete {{name}}?", { name: set.name })}
              description={t("The servers lose the files of the set that hold secrets. Its other files stay on the servers as they are.")}
              action={t("Delete file set")}
              destructive
              onConfirm={() =>
                remove.mutate(set.id, {
                  onSuccess: () => {
                    toast.success(t("Deleted {{name}}", { name: set.name }))
                    void navigate({ to: "/filesets", ignoreBlocker: true })
                  },
                  onError: (e) => toast.error(e.message),
                })
              }
            />
          )
        }
      />
      <StatusBar set={set} draft={draft} statuses={statuses} dirty={dirty} editable={editable} onApply={() => setApplying(set)} onServers={() => setTab("servers")} />
      <div role="tablist" aria-label={t("Parts of the file set")} className="mb-6 flex max-w-full gap-1 overflow-x-auto rounded-xl bg-muted/80 p-1 sm:w-fit">
        {tabs.map(({ id, label, icon: Icon }) => {
          const count = { files: draft.files.length, servers: statuses?.length, history: set.versions.length }[id]
          return (
            <button
              key={id}
              type="button"
              role="tab"
              aria-selected={tab === id}
              onClick={() => setTab(id)}
              className="flex items-center gap-2 rounded-lg px-3.5 py-1.5 text-sm font-medium whitespace-nowrap text-muted-foreground transition-colors outline-none hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring aria-selected:bg-card aria-selected:text-foreground aria-selected:shadow-sm"
            >
              <Icon className="size-4" />
              {t(label)}
              {count !== undefined && <span className="text-xs text-muted-foreground tabular-nums">{count}</span>}
            </button>
          )
        })}
      </div>
      {tab === "files" && (
        <Workspace
          key={generation}
          set={set}
          draft={draft}
          editable={editable}
          selected={selected}
          onSelect={setSelected}
          onChange={change}
          errorAt={errorsAt(save.error)}
        />
      )}
      {tab === "servers" && <ServersTab set={set} />}
      {tab === "history" && (
        <HistoryTab
          set={set}
          dirty={dirty}
          editable={editable}
          onRestore={(files) => {
            change({ files })
            setGeneration((g) => g + 1)
            setTab("files")
          }}
        />
      )}

      {dirty && (
        <div className="sticky bottom-4 z-10 mt-10 flex flex-wrap items-center justify-between gap-3 rounded-xl bg-popover/90 px-4 py-3 shadow-2xl ring-1 ring-foreground/10 backdrop-blur-xl">
          <div className="min-w-0 space-y-0.5">
            <p className="flex items-center gap-2.5 text-sm font-medium">
              <span aria-hidden className="size-2 rounded-full bg-warning" />
              {t("Unsaved changes")}
            </p>
            <p className={refused ? "text-xs text-destructive" : "text-xs text-muted-foreground"}>
              {refused ?? t("Saving creates a new version but doesn't change any servers until the set is applied.")}
            </p>
          </div>
          <div className="flex gap-2">
            <Button variant="ghost" disabled={save.isPending} onClick={discard}>
              {t("Discard")}
            </Button>
            <Button variant={draft.targets.length > 0 ? "outline" : "default"} disabled={save.isPending} onClick={() => store()}>
              {t("Save")}
            </Button>
            {draft.targets.length > 0 && (
              <Button disabled={save.isPending} onClick={() => store(true)}>
                <PaperPlaneTiltIcon />
                {t("Save and apply…")}
              </Button>
            )}
          </div>
        </div>
      )}
      {applying && <ApplyDialog set={applying} onClose={() => setApplying(undefined)} />}
      <ConfirmDialog
        open={blocker.status === "blocked"}
        onOpenChange={(open) => !open && blocker.reset?.()}
        title={t("Discard your changes?")}
        description={t("Your changes to the file set haven't been saved.")}
        action={t("Discard changes")}
        destructive
        onConfirm={() => blocker.proceed?.()}
      />
    </>
  )
}

/** What a set needs: servers whose files are behind, secrets and variables without a value, a target. Applying is its main action. */
function StatusBar({
  set,
  draft,
  statuses,
  dirty,
  editable,
  onApply,
  onServers,
}: {
  set: FileSet
  draft: SetInput
  statuses?: ServerStatus[]
  dirty: boolean
  editable: boolean
  onApply: () => void
  onServers: () => void
}) {
  const counts = new Map<State, number>()
  for (const s of statuses ?? []) counts.set(s.state, (counts.get(s.state) ?? 0) + 1)
  const behind = statuses?.filter((s) => pending.includes(s.state)).length ?? 0
  const missing = usedSecrets(draft.files).filter((name) => !set.secrets.some((s) => s.name === name))
  const valueless = usedVariables(draft.files).filter((name) => !draft.variables.some((v) => v.name === name && v.values.length > 0))
  const problems = [
    draft.targets.length === 0 && t("The set doesn't target any servers yet: add a target."),
    draft.files.length === 0 && t("The set has no files yet."),
    missing.length > 0 && t("Secrets without a value: {{names}}", { names: missing.join(", ") }),
    valueless.length > 0 && t("Variables without a value: {{names}}", { names: valueless.join(", ") }),
  ].filter((p): p is string => Boolean(p))

  return (
    <div className="surface mb-6 flex flex-wrap items-center gap-x-4 gap-y-3 rounded-xl px-4 py-3">
      <div className="flex min-w-0 flex-1 flex-wrap items-center gap-2">
        {!statuses ? (
          <span className="text-sm text-muted-foreground">{t("Checking the servers…")}</span>
        ) : statuses.length === 0 ? (
          draft.targets.length > 0 && <span className="text-sm text-muted-foreground">{t("The targets have no servers yet.")}</span>
        ) : behind === 0 ? (
          <span className="inline-flex items-center gap-1.5 text-sm font-medium text-success">
            <CheckCircleIcon className="size-4" weight="fill" />
            {t("All {{count}} servers are up to date", { count: statuses.length, defaultValue_one: "The server is up to date" })}
          </span>
        ) : (
          (Object.keys(states) as State[])
            .filter((s) => counts.has(s))
            .map((s) => (
              <button key={s} type="button" onClick={onServers} title={t(states[s].description)} className="rounded-full outline-none focus-visible:ring-2 focus-visible:ring-ring">
                <Pill tone={states[s].tone}>
                  <StatusDot status={states[s]} />
                  <span className="tabular-nums">{counts.get(s)}</span> {t(states[s].label)}
                </Pill>
              </button>
            ))
        )}
        {problems.map((p) => (
          <span key={p} className="inline-flex items-center gap-1.5 text-sm text-warning">
            <WarningIcon className="size-4 shrink-0" weight="fill" />
            {p}
          </span>
        ))}
      </div>
      {editable && !dirty && behind > 0 && (
        <Button onClick={onApply}>
          <PaperPlaneTiltIcon />
          {t("Apply to {{count}} servers…", { count: behind, defaultValue_one: "Apply to {{count}} server…" })}
        </Button>
      )}
    </div>
  )
}
