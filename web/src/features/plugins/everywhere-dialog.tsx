import { ArrowCircleUpIcon, CheckCircleIcon, TrashIcon, WarningCircleIcon } from "@phosphor-icons/react"
import { t } from "i18next"
import { useState } from "react"
import { Callout, ErrorCallout } from "@/components/callout"
import { guard } from "@/components/form-dialog"
import { Button } from "@/components/ui/button"
import { Dialog, DialogClose, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { useAccess } from "@/features/access/use-access"
import type { ServerRef } from "@/features/networks/api"
import { key } from "@/features/networks/servers"
import { OperationStatus } from "@/features/operations/operation-status"
import { mergeResults } from "@/features/operations/retry"
import { RetryButton } from "@/features/operations/retry-button"
import { useOperation } from "@/features/operations/use-operation"
import type { NodeServer } from "@/features/servers/api"
import { type Everywhere, type InstalledOn, type InstallResult, usePluginsEverywhere } from "./api"
import { ChannelPill } from "./channel-pill"
import { RestartOption, RestartPill } from "./restart-option"
import { useRestart } from "./use-restart"

export type EverywhereAction = "update" | "remove"

/**
 * Updates or removes a project on all servers that have it, as an operation that tells how it went on each, and can
 * restart the running ones afterwards. Servers whose plugins the user may not manage are left out and named; an update
 * leaves out the servers that keep the project at its version, and turned-off files.
 */
export function EverywhereDialog({
  action,
  entry,
  servers,
  onClose,
}: {
  action: EverywhereAction
  entry: Everywhere["projects"][number]
  /** The servers the user sees, by key. */
  servers: Map<string, NodeServer>
  onClose: () => void
}) {
  const { can } = useAccess()
  const mutation = usePluginsEverywhere(action)
  const operation = useOperation()
  const [results, setResults] = useState<InstallResult[]>()
  const nameOf = (ref: ServerRef) => servers.get(key(ref))?.name ?? ref.serverId
  const names = (refs: ServerRef[]) => refs.map(nameOf).join(", ")
  const update = action === "update"
  const name = entry.project.title

  // A server can have more than one file of a project, but is named once.
  const unique = (files: InstalledOn[]): ServerRef[] => [...new Map(files.map((s) => [key(s), { nodeId: s.nodeId, serverId: s.serverId }])).values()]
  const affected = update ? entry.servers.filter((s) => s.update && !s.disabled) : entry.servers
  const manageable = affected.filter((s) => can("plugins.manage", s.nodeId, s.serverId))
  const allowed = unique(manageable)
  const denied = unique(affected.filter((s) => !manageable.includes(s)))
  const pinned = unique(update ? manageable.filter((s) => s.pinned) : [])
  const changing = unique(manageable.filter((s) => !(update && s.pinned)))
  const restart = useRestart(unique(manageable.filter((s) => !(update && s.pinned) && !s.disabled && servers.get(key(s))?.state === "running")))
  const needed = update ? [] : manageable.filter((s) => s.requiredBy?.length)
  const title = update ? t("Update {{name}} everywhere", { name }) : t("Remove {{name}} everywhere", { name })

  function start(refs: ServerRef[], earlier?: InstallResult[]) {
    operation.run((onStart) => mutation.mutateAsync({ project: entry.project.id, servers: refs, ...restart.request, onStart }), {
      title,
      done: (results) => {
        const failed = results.filter((r) => r.error).length
        if (failed > 0)
          return {
            message: update
              ? t("Updated {{name}}, but not on {{count}} servers", { name, count: failed, defaultValue_one: "Updated {{name}}, but not on {{count}} server" })
              : t("Removed {{name}}, but not from {{count}} servers", { name, count: failed, defaultValue_one: "Removed {{name}}, but not from {{count}} server" }),
            warning: true,
          }
        return { message: update ? t("Updated {{name}}", { name }) : t("Removed {{name}}", { name }) }
      },
      then: (results) => setResults(mergeResults(earlier, results)),
    })
  }

  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent className="sm:max-w-xl" {...guard(mutation.isPending)}>
        {results ? (
          <div className="grid gap-6">
            <DialogHeader>
              <DialogTitle>{title}</DialogTitle>
              <DialogDescription>
                {results.some((r) => r.restart) ? t("Restart the servers that need it, so that they load the change.") : t("How it went on each server.")}
              </DialogDescription>
            </DialogHeader>
            <ul className="grid max-h-[50vh] gap-2 overflow-y-auto">
              {results.map((r) => (
                <li key={key(r)} className="flex items-start gap-3 rounded-lg px-3 py-2 text-sm ring-1 ring-border">
                  {r.error ? (
                    <WarningCircleIcon className="mt-0.5 size-4 shrink-0 text-destructive" weight="fill" />
                  ) : (
                    <CheckCircleIcon className="mt-0.5 size-4 shrink-0 text-success" weight="fill" />
                  )}
                  <span className="min-w-0 flex-1">
                    <span className="flex flex-wrap items-center gap-2 font-medium">
                      {nameOf(r)}
                      <span className="font-normal text-muted-foreground">{servers.get(key(r))?.nodeName}</span>
                      <RestartPill result={r} />
                    </span>
                    <span className="flex flex-wrap items-center gap-x-2 gap-y-1 text-xs text-muted-foreground">
                      {r.error ||
                        (r.pinned?.length
                          ? t("Kept at its version")
                          : update
                            ? r.installed.length === 0
                              ? t("Nothing to update")
                              : r.installed.map((f) => (
                                  <span key={f.fileName} className="inline-flex items-center gap-1.5">
                                    {f.fileName}
                                    <ChannelPill channel={f.channel} />
                                  </span>
                                ))
                            : r.removed?.join(", ") || t("Nothing to remove"))}
                    </span>
                  </span>
                </li>
              ))}
            </ul>
            <DialogFooter>
              <RetryButton
                results={results}
                onRetry={(failed) => {
                  start(failed, results)
                  setResults(undefined)
                }}
              />
              <DialogClose asChild>
                <Button>{t("Close")}</Button>
              </DialogClose>
            </DialogFooter>
          </div>
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
              mutation.reset()
            }}
          />
        ) : (
          <div className="grid gap-4">
            <DialogHeader>
              <DialogTitle>{title}</DialogTitle>
              <DialogDescription>
                {update
                  ? t("Each server gets the newest version that suits it in the channel it takes {{name}} in, with what it requires.", { name })
                  : t("Its files are deleted, turned-off ones too. Their configuration in the servers' folders is kept.")}
              </DialogDescription>
            </DialogHeader>
            <p className="text-sm">
              <span className="font-medium">
                {t("{{count}} servers:", { count: changing.length, defaultValue_one: "{{count}} server:" })}
              </span>{" "}
              <span className="text-muted-foreground">{names(changing) || "–"}</span>
            </p>
            {pinned.length > 0 && <Callout tone="info">{t("Left out, as they keep {{name}} at its version: {{names}}", { name, names: names(pinned) })}</Callout>}
            {denied.length > 0 && <Callout tone="warning">{t("Left out, as you may not manage their plugins: {{names}}", { names: names(denied) })}</Callout>}
            {needed.length > 0 && (
              <Callout tone="warning" title={t("Other plugins need {{name}}", { name })}>
                <ul className="grid gap-1">
                  {needed.map((s) => (
                    <li key={key(s)}>
                      <span className="font-medium">{nameOf(s)}</span>: {s.requiredBy?.join(", ")}
                    </li>
                  ))}
                </ul>
              </Callout>
            )}
            <RestartOption restart={restart} />
            {mutation.error && <ErrorCallout error={mutation.error} retry={false} />}
            <DialogFooter>
              <DialogClose asChild>
                <Button variant="outline">{t("Cancel")}</Button>
              </DialogClose>
              <Button
                variant={update ? "default" : "destructive"}
                disabled={changing.length === 0 || mutation.isPending}
                onClick={() => start(allowed)}
              >
                {update ? <ArrowCircleUpIcon /> : <TrashIcon />}
                {update
                  ? t("Update on {{count}} servers", { count: changing.length, defaultValue_one: "Update on {{count}} server" })
                  : t("Remove from {{count}} servers", { count: changing.length, defaultValue_one: "Remove from {{count}} server" })}
              </Button>
            </DialogFooter>
          </div>
        )}
      </DialogContent>
    </Dialog>
  )
}
