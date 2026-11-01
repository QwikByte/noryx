import { ArrowCounterClockwiseIcon, CloudArrowUpIcon, DotsThreeIcon, HardDrivesIcon, PushPinIcon, TrashIcon } from "@phosphor-icons/react"
import { useQuery } from "@tanstack/react-query"
import { t } from "i18next"
import { type FormEvent, useState } from "react"
import { toast } from "sonner"
import { ConfirmDialog } from "@/components/confirm-dialog"
import { IconTile } from "@/components/icon-tile"
import { Section } from "@/components/section"
import { Pill } from "@/components/status"
import { Button } from "@/components/ui/button"
import { Dialog, DialogClose, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from "@/components/ui/dropdown-menu"
import { Field, FieldContent, FieldDescription, FieldGroup, FieldLabel } from "@/components/ui/field"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { Switch } from "@/components/ui/switch"
import { useAccess } from "@/features/access/use-access"
import type { ServerFiles } from "@/features/files/api"
import { useOperation } from "@/features/operations/use-operation"
import { allServersQuery, type NodeServer, serverKey } from "@/features/servers/api"
import { isBedrock, serverType } from "@/features/servers/server-types"
import { formatBytes, formatDateTime } from "@/lib/format"
import { type Copy, copiesQuery, describeContent, useCopies } from "./api"

/** A server that copies can be restored into. */
type Target = Pick<NodeServer, "nodeId" | "id" | "name" | "state"> & { nodeName?: string }

/** The servers that the user may restore backups of a kind into: proxies, Java or Bedrock servers. */
function useTargets({ proxy, bedrock }: Pick<Copy, "proxy" | "bedrock">): NodeServer[] {
  const { can } = useAccess()
  const { data: servers = [] } = useQuery(allServersQuery)
  return servers.filter((s) => serverType(s.type).proxy === proxy && isBedrock(s.type) === bedrock && can("backups.restore", s.nodeId, s.id))
}

/** The copies of the backups of a server, after its backups, which restore into it or another server of its kind. */
export function ServerCopies({ server }: { server: Target }) {
  const own: ServerFiles = { nodeId: server.nodeId, serverId: server.id }
  const { data: copies = [] } = useQuery(copiesQuery(own))
  if (copies.length === 0) return null
  return (
    <Section
      title={t("{{count}} copies", { count: copies.length, defaultValue_one: "{{count}} copy" })}
      description={t("Backup jobs keep them away from the node, without the server's secrets, so that the server can be restored if the node is lost.")}
    >
      <CopyList copies={copies} routes={own} own={server} />
    </Section>
  )
}

/** The copies of servers that the panel doesn't find: deleted ones, and those on nodes that are offline or removed. */
export function GoneCopies() {
  const { can } = useAccess()
  const enabled = can("backups.view")
  const { data: copies = [] } = useQuery({ ...copiesQuery(), enabled })
  const { data: servers } = useQuery({ ...allServersQuery, enabled })
  if (!servers) return null
  const known = new Set(servers.map((s) => s.id))
  const gone = new Map<string, Copy[]>()
  for (const c of copies) {
    if (!known.has(c.serverId)) gone.set(c.serverId, [...(gone.get(c.serverId) ?? []), c])
  }
  if (gone.size === 0) return null
  return (
    <Section
      title={t("Copies of servers that are gone")}
      description={t("Their servers were deleted, or their nodes are offline or removed. Restore them into another server of the same kind, e.g. a new one.")}
    >
      <div className="space-y-6">
        {[...gone.values()].map((list) => (
          <div key={list[0].serverId} className="space-y-2">
            <h3 className="text-sm font-semibold">
              {list[0].serverName} <span className="font-normal text-muted-foreground">· {list[0].nodeName}</span>
            </h3>
            <CopyList copies={list} />
          </div>
        ))}
      </div>
    </Section>
  )
}

/** Copies of one server, with the routes of the server they are of if it is there, otherwise those of all copies. */
function CopyList({ copies, routes, own }: { copies: Copy[]; routes?: ServerFiles; own?: Target }) {
  const others = useTargets(copies[0]).filter((s) => !own || serverKey(s) !== serverKey(own))
  return (
    <ul className="surface divide-y rounded-xl">
      {copies.map((c) => (
        <CopyRow key={c.id} copy={c} routes={routes} own={own} others={others} />
      ))}
    </ul>
  )
}

function CopyRow({ copy, routes, own, others }: { copy: Copy; routes?: ServerFiles; own?: Target; others: Target[] }) {
  const { can } = useAccess()
  const { restore, remove } = useCopies(routes)
  const operation = useOperation()
  const [dialog, setDialog] = useState<"restore" | "delete">()
  const created = formatDateTime(copy.createdAt)
  const targets = [...(own && can("backups.restore", own.nodeId, own.id) ? [own] : []), ...others]
  const deletable = routes ? can("backups.delete", routes.nodeId, routes.serverId) : can("backups.delete")

  const run = (into: Target, snapshotFirst: boolean) =>
    operation.run((onStart) => restore.mutateAsync({ id: copy.id, into: { nodeId: into.nodeId, serverId: into.id }, snapshotFirst, onStart }), {
      title: t("Restoring {{name}}…", { name: into.name }),
      notify: true,
      done: ({ warning, snapshot }) => ({
        message: t("Restored the copy of {{time}} into {{name}}", { time: created, name: into.name }),
        description: warning ?? (snapshot && t("What it replaced is kept in the backup {{label}}.", { label: snapshot.label })),
        warning: !!warning,
      }),
    })

  return (
    <li className="flex flex-wrap items-center gap-3 px-4 py-3">
      <IconTile icon={copy.storageId ? CloudArrowUpIcon : HardDrivesIcon} tone="info" size="sm" />
      <div className="min-w-0 flex-1">
        <p className="flex items-center gap-2 text-sm font-semibold">
          <span className="truncate">{copy.label || t("Backup")}</span>
          {copy.kept && (
            <Pill tone="success">
              <PushPinIcon className="size-3" />
              {t("Kept")}
            </Pill>
          )}
        </p>
        <p className="truncate text-xs text-muted-foreground">
          {t("{{time}} · in {{where}} · {{size}} · {{content}}", {
            time: created,
            size: formatBytes(copy.size),
            content: describeContent(copy),
            where: copy.where,
          })}
        </p>
      </div>
      <div className="flex items-center gap-1.5">
        {targets.length > 0 && (
          <Button size="sm" variant="outline" disabled={restore.isPending} onClick={() => setDialog("restore")}>
            <ArrowCounterClockwiseIcon />
            <span className="max-sm:sr-only">{t("Restore")}</span>
          </Button>
        )}
        {deletable && (
          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <Button
                size="icon-sm"
                variant="ghost"
                className="text-muted-foreground"
                aria-label={t("More actions for the copy of {{time}}", { time: created })}
                title={t("More actions")}
              >
                <DotsThreeIcon weight="bold" />
              </Button>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end" className="w-48">
              <DropdownMenuItem variant="destructive" disabled={remove.isPending} onSelect={() => setDialog("delete")}>
                <TrashIcon />
                {t("Delete…")}
              </DropdownMenuItem>
            </DropdownMenuContent>
          </DropdownMenu>
        )}
      </div>
      {dialog === "restore" && <RestoreCopyDialog copy={copy} targets={targets} onClose={() => setDialog(undefined)} onRestore={run} />}
      {dialog === "delete" && (
        <ConfirmDialog
          open
          onOpenChange={(open) => !open && setDialog(undefined)}
          title={t("Delete the copy of {{time}}?", { time: created })}
          description={t("The copy is deleted from {{where}}. The backup on the node stays, if it is still there. This can't be undone.", {
            where: copy.where,
          })}
          action={t("Delete copy")}
          destructive
          onConfirm={() =>
            remove.mutate(copy.id, { onSuccess: () => toast.success(t("Deleted the copy")), onError: (e) => toast.error(e.message) })
          }
        />
      )}
    </li>
  )
}

/**
 * Chooses the server that a copy is restored into, the one it is of or another one of its kind, and whether what the
 * restore replaces is backed up first. All of the copy is restored, as it can't be looked into.
 */
function RestoreCopyDialog({
  copy,
  targets,
  onClose,
  onRestore,
}: {
  copy: Copy
  targets: Target[]
  onClose: () => void
  onRestore: (into: Target, snapshotFirst: boolean) => void
}) {
  const [into, setInto] = useState(serverKey(targets[0]))
  const [snapshotFirst, setSnapshotFirst] = useState(true)
  const target = targets.find((s) => serverKey(s) === into) ?? targets[0]
  const other = target.id !== copy.serverId

  function submit(event: FormEvent) {
    event.preventDefault()
    onClose()
    onRestore(target, snapshotFirst)
  }

  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent className="sm:max-w-xl">
        <form onSubmit={submit} className="grid gap-6">
          <DialogHeader>
            <DialogTitle>{t("Restore the copy of {{time}}", { time: formatDateTime(copy.createdAt) })}</DialogTitle>
            <DialogDescription>
              {t("{{content}} of {{name}}, in {{where}}", { content: describeContent(copy), name: copy.serverName, where: copy.where })}
            </DialogDescription>
          </DialogHeader>
          <FieldGroup>
            <Field>
              <FieldLabel htmlFor="copy-into">{t("Restore into")}</FieldLabel>
              <Select value={into} onValueChange={setInto}>
                <SelectTrigger id="copy-into" className="w-full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {targets.map((s) => (
                    <SelectItem key={serverKey(s)} value={serverKey(s)}>
                      {s.id === copy.serverId ? t("{{name}}, its own server", { name: s.name }) : s.name}
                      {s.nodeName && <span className="text-muted-foreground"> · {s.nodeName}</span>}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
              <FieldDescription>
                {other
                  ? t("Like a copy, {{name}} keeps its own secrets and those of its network, and gets no files with secrets of file sets.", {
                      name: target.name,
                    })
                  : t("The server keeps its secrets and those of its network, which the copy leaves out.")}
              </FieldDescription>
            </Field>
            <Field orientation="horizontal">
              <Switch id="copy-snapshot" checked={snapshotFirst} onCheckedChange={setSnapshotFirst} />
              <FieldContent>
                <FieldLabel htmlFor="copy-snapshot">{t("Back up what is replaced first")}</FieldLabel>
                <FieldDescription>{t("As a backup made by hand, which undoes the restore if it was the wrong backup.")}</FieldDescription>
              </FieldContent>
            </Field>
          </FieldGroup>
          <p className="text-sm text-muted-foreground">
            {[
              t("The master fetches the copy from {{where}} and replaces {{content}} of {{name}} with it; what was added since is removed.", {
                where: copy.where,
                content: copy.paths.includes(".") ? t("everything") : describeContent({ ...copy, exclude: [] }),
                name: target.name,
              }),
              target.state !== "stopped" && t("The server stops while this happens and starts again afterwards."),
            ]
              .filter(Boolean)
              .join(" ")}
          </p>
          <DialogFooter>
            <DialogClose asChild>
              <Button variant="outline">{t("Cancel")}</Button>
            </DialogClose>
            <Button type="submit" variant="destructive">
              <ArrowCounterClockwiseIcon />
              {t("Restore")}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
