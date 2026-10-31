import { CaretDownIcon, CaretRightIcon, KeyIcon, LockKeyIcon, PaperPlaneTiltIcon, WarningCircleIcon } from "@phosphor-icons/react"
import { t } from "i18next"
import { useEffect, useState } from "react"
import { Callout, ErrorCallout } from "@/components/callout"
import { guard } from "@/components/form-dialog"
import { Segmented } from "@/components/segmented"
import { Pill, StatusDot } from "@/components/status"
import type { Tone } from "@/components/tone"
import { Button } from "@/components/ui/button"
import { Dialog, DialogClose, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { Skeleton } from "@/components/ui/skeleton"
import { Switch } from "@/components/ui/switch"
import { useAccess } from "@/features/access/use-access"
import type { ServerRef } from "@/features/networks/api"
import { OperationStatus } from "@/features/operations/operation-status"
import { mergeResults } from "@/features/operations/retry"
import { RetryButton } from "@/features/operations/retry-button"
import { useOperation } from "@/features/operations/use-operation"
import { type Action, type FileSet, type Preview, type PreviewFile, type PreviewServer, type Result, useApply, usePreview } from "./api"
import { DiffView } from "./diff-view"
import { actions } from "./labels"

const tones: Record<Action, Tone> = { unchanged: "neutral", created: "success", changed: "info", removed: "destructive", kept: "warning" }
const batches = ["1", "2", "5", "10"] as const

const nameOf = (s: { name?: string; serverId: string }) => s.name || s.serverId

/** Servers whose files change alike, so that their changes show once. */
interface Group {
  servers: PreviewServer[]
  files: PreviewFile[]
}

function groupsOf(preview: Preview): Group[] {
  const groups = new Map<string, Group>()
  for (const server of preview.servers) {
    if (server.error) continue
    const files = server.files.filter((f) => f.action !== "unchanged")
    if (files.length === 0) continue
    const key = JSON.stringify(files)
    const group = groups.get(key) ?? { servers: [], files }
    group.servers.push(server)
    groups.set(key, group)
  }
  return [...groups.values()]
}

/** Shows what applying the newest version of a set changes on each server, and applies it. */
export function ApplyDialog({ set, onClose }: { set: FileSet; onClose: () => void }) {
  const { can } = useAccess()
  const preview = usePreview(set.id)
  const apply = useApply(set.id)
  const operation = useOperation()
  const [restart, setRestart] = useState(false)
  const [batch, setBatch] = useState<(typeof batches)[number]>("1")
  const [results, setResults] = useState<Result[]>()
  const title = t("Apply {{name}}", { name: set.name })
  const { mutate } = preview
  useEffect(() => mutate(set.version), [mutate, set.version])

  const p = preview.data
  const groups = p ? groupsOf(p) : []
  const failing = p?.servers.filter((s) => s.error) ?? []
  const unchanged = p?.servers.filter((s) => !s.error && s.files.every((f) => f.action === "unchanged")) ?? []
  // Servers whose files don't change also record the newest state, e.g. one that has all files already.
  const touched = p?.servers.filter((s) => !s.error && (s.state !== "current" || s.files.some((f) => f.action !== "unchanged"))) ?? []
  const first = p?.servers.filter((s) => s.firstSecrets) ?? []
  const restarting = groups.flatMap((g) => g.servers).filter((s) => s.running)
  const mayRestart = restarting.every((s) => can("servers.restart", s.nodeId, s.serverId))

  /** Applies the set to all its servers, or again to those where it failed, whose results take the place of the earlier ones. */
  function start(retry?: { servers: ServerRef[]; earlier: Result[] }) {
    const body = { version: set.version, restart: restart && mayRestart, batch: Number(batch), servers: retry?.servers }
    operation.run((onStart) => apply.mutateAsync({ ...body, onStart }), {
      title,
      done: ({ results }) => {
        const failed = results.filter((r) => r.error).length
        return failed > 0
          ? { message: t("Applied {{name}}, but not on {{count}} servers", { name: set.name, count: failed, defaultValue_one: "Applied {{name}}, but not on {{count}} server" }), warning: true }
          : { message: t("Applied {{name}}", { name: set.name }) }
      },
      then: ({ results }) => setResults(mergeResults(retry?.earlier, results)),
    })
  }

  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent className="sm:max-w-4xl" {...guard(apply.isPending)}>
        {results ? (
          <Results
            title={title}
            results={results}
            onRetry={(servers) => {
              start({ servers, earlier: results })
              setResults(undefined)
            }}
          />
        ) : operation.live ? (
          <OperationStatus
            op={operation.live}
            title={title}
            onBackground={() => {
              operation.background(title)
              onClose()
            }}
            onBack={() => {
              operation.reset()
              apply.reset()
            }}
          />
        ) : (
          <div className="grid gap-6">
            <DialogHeader>
              <DialogTitle>{title}</DialogTitle>
              <DialogDescription>
                {t("What version {{version}} changes on each server. Files with secrets show placeholders instead of the values.", { version: set.version })}
              </DialogDescription>
            </DialogHeader>

            {preview.isPending || preview.isIdle ? (
              <div className="grid gap-3">
                <p className="text-sm text-muted-foreground">{t("Comparing the files of the servers…")}</p>
                <Skeleton className="h-32 rounded-xl" />
              </div>
            ) : preview.error ? (
              <ErrorCallout error={preview.error} retry={false} />
            ) : (
              <>
                {first.length > 0 && (
                  <Callout tone="warning" icon={KeyIcon} title={t("These servers get the secrets of the set for the first time")}>
                    {first.map(nameOf).join(", ")}
                  </Callout>
                )}
                {failing.length > 0 && (
                  <Callout tone="destructive" icon={WarningCircleIcon} title={t("The set can't be applied to these servers")}>
                    <ul className="grid gap-1">
                      {failing.map((s) => (
                        <li key={`${s.nodeId}/${s.serverId}`}>
                          <span className="font-medium">{nameOf(s)}</span>: {s.error}
                        </li>
                      ))}
                    </ul>
                  </Callout>
                )}
                {groups.length === 0 ? (
                  <p className="text-sm text-muted-foreground">{t("No server changes: they all have these files.")}</p>
                ) : (
                  groups.map((g) => <GroupChanges key={g.servers.map((s) => s.serverId).join()} group={g} contents={p!.contents} />)
                )}
                {unchanged.length > 0 && (
                  <p className="text-sm text-muted-foreground">{t("Unchanged: {{names}}", { names: unchanged.map(nameOf).join(", ") })}</p>
                )}
                {restarting.length > 0 && (
                  <div className="grid gap-3 rounded-xl p-4 ring-1 ring-border">
                    <label className="flex items-start gap-3 text-sm">
                      <Switch className="mt-0.5" checked={restart && mayRestart} disabled={!mayRestart} onCheckedChange={setRestart} />
                      <span className="space-y-0.5">
                        <span className="block font-medium">{t("Restart the running servers whose files change")}</span>
                        <span className="block text-muted-foreground">
                          {mayRestart
                            ? t("Most plugins only read their configuration when they start. The game servers of a network restart a few at a time, so that it stays open; others restart at once.")
                            : t("You need the permission to restart all of them for this.")}
                        </span>
                      </span>
                    </label>
                    {restart && mayRestart && (
                      <div className="flex flex-wrap items-center gap-3 pl-12 text-sm">
                        <span>{t("Servers of a network at a time")}</span>
                        <Segmented label={t("Servers of a network at a time")} value={batch} onChange={setBatch} options={batches.map((b) => ({ value: b, label: b }))} />
                      </div>
                    )}
                  </div>
                )}
              </>
            )}
            {apply.error && <ErrorCallout error={apply.error} retry={false} />}
            <DialogFooter>
              <DialogClose asChild>
                <Button variant="outline">{t("Cancel")}</Button>
              </DialogClose>
              <Button disabled={touched.length === 0 || apply.isPending} onClick={() => start()}>
                <PaperPlaneTiltIcon />
                {t("Apply to {{count}} servers", { count: touched.length, defaultValue_one: "Apply to {{count}} server" })}
              </Button>
            </DialogFooter>
          </div>
        )}
      </DialogContent>
    </Dialog>
  )
}

/** The changes of servers whose files change alike. */
function GroupChanges({ group, contents }: { group: Group; contents: Record<string, string> }) {
  return (
    <section className="grid gap-3 rounded-xl p-4 ring-1 ring-border">
      <div className="flex flex-wrap items-center gap-1.5">
        {group.servers.map((s) => (
          <Pill key={`${s.nodeId}/${s.serverId}`} tone={s.state === "left" ? "neutral" : "info"}>
            {s.running && <StatusDot status={{ tone: "success", label: "" }} />}
            {nameOf(s)}
            <span className="text-muted-foreground">· {s.nodeName}</span>
          </Pill>
        ))}
        {group.servers.some((s) => s.state === "left") && (
          <span className="text-xs text-muted-foreground">{t("no longer targets: they lose the files that didn't change on them")}</span>
        )}
      </div>
      <ul className="grid gap-2">
        {group.files.map((f) => (
          <FileChange key={f.path} file={f} contents={contents} />
        ))}
      </ul>
    </section>
  )
}

function FileChange({ file: f, contents }: { file: PreviewFile; contents: Record<string, string> }) {
  const [open, setOpen] = useState(false)
  const diffable = !f.unknown && (f.before !== undefined || f.after !== undefined) && f.action !== "kept"
  return (
    <li className="grid gap-2">
      <button
        type="button"
        disabled={!diffable}
        aria-expanded={diffable ? open : undefined}
        onClick={() => setOpen(!open)}
        className="flex flex-wrap items-center gap-2 rounded text-left outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:cursor-default"
      >
        {diffable ? open ? <CaretDownIcon className="size-4" /> : <CaretRightIcon className="size-4" /> : <span className="size-4" />}
        <span className="font-mono text-xs font-medium">{f.path}</span>
        <Pill tone={tones[f.action]}>{t(actions[f.action])}</Pill>
        {f.secret && (
          <span className="inline-flex items-center gap-1 text-xs text-warning">
            <LockKeyIcon className="size-3.5" weight="fill" />
            {t("holds secrets")}
          </span>
        )}
        {f.binary && <span className="text-xs text-muted-foreground">{t("binary file")}</span>}
        {f.changedOnServer && <span className="text-xs text-warning">{t("changed on the server since it was written")}</span>}
        {f.unknown && <span className="text-xs text-muted-foreground">{t("what the server has can't be shown")}</span>}
      </button>
      {open && diffable && (
        <DiffView
          before={f.before ? (contents[f.before] ?? "") : ""}
          after={f.after ? (contents[f.after] ?? "") : ""}
          filename={f.path}
          label={t("Changes of {{path}}", { path: f.path })}
        />
      )}
    </li>
  )
}

/** How applying ended on each server. */
function Results({ title, results, onRetry }: { title: string; results: Result[]; onRetry: (servers: ServerRef[]) => void }) {
  const pending = results.filter((r) => r.restart)
  return (
    <div className="grid gap-6">
      <DialogHeader>
        <DialogTitle>{title}</DialogTitle>
        <DialogDescription>
          {pending.length > 0
            ? t("Restart these servers so that their plugins load the new files: {{names}}", { names: pending.map(nameOf).join(", ") })
            : t("The servers have the new files.")}
        </DialogDescription>
      </DialogHeader>
      <ul className="grid gap-2">
        {results.map((r) => {
          const changed = r.changes.filter((c) => c.action !== "unchanged").length
          return (
            <li key={`${r.nodeId}/${r.serverId}`} className="flex flex-wrap items-center gap-x-3 gap-y-1 rounded-lg px-3 py-2 text-sm ring-1 ring-border">
              <span className="font-medium">{nameOf(r)}</span>
              <span className="text-muted-foreground">{r.nodeName}</span>
              {r.error ? (
                <span className="text-destructive">{r.error}</span>
              ) : (
                <span className="text-muted-foreground">
                  {t("{{count}} files changed", { count: changed, defaultValue_one: "{{count}} file changed" })}
                </span>
              )}
              {r.restarted && <Pill tone="success">{t("Restarted")}</Pill>}
              {r.restart && <Pill tone="warning">{t("Needs a restart")}</Pill>}
            </li>
          )
        })}
      </ul>
      <DialogFooter>
        <RetryButton results={results} onRetry={onRetry} />
        <DialogClose asChild>
          <Button>{t("Close")}</Button>
        </DialogClose>
      </DialogFooter>
    </div>
  )
}
