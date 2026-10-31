import {
  ArchiveIcon,
  ArrowCounterClockwiseIcon,
  ClockIcon,
  DotsThreeIcon,
  DownloadSimpleIcon,
  PlusIcon,
  PushPinIcon,
  PushPinSlashIcon,
  TagIcon,
  TrashIcon,
} from "@phosphor-icons/react"
import { useQuery } from "@tanstack/react-query"
import { getRouteApi, Link } from "@tanstack/react-router"
import { t } from "i18next"
import { type FormEvent, useState } from "react"
import { Trans } from "react-i18next"
import { toast } from "sonner"
import { Callout, ErrorCallout } from "@/components/callout"
import { ConfirmDialog } from "@/components/confirm-dialog"
import { EmptyState } from "@/components/empty-state"
import { guard } from "@/components/form-dialog"
import { IconTile } from "@/components/icon-tile"
import { Section } from "@/components/section"
import { Segmented } from "@/components/segmented"
import { Pill } from "@/components/status"
import { Button } from "@/components/ui/button"
import {
  Dialog,
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from "@/components/ui/dialog"
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuSeparator, DropdownMenuTrigger } from "@/components/ui/dropdown-menu"
import { Field, FieldContent, FieldDescription, FieldError, FieldGroup, FieldLabel, FieldLegend, FieldSet } from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { Skeleton } from "@/components/ui/skeleton"
import { Switch } from "@/components/ui/switch"
import { useAccess } from "@/features/access/use-access"
import { PathPicker, type Picked } from "@/features/files/path-picker"
import { nodeQuery } from "@/features/nodes/api"
import { useOperation } from "@/features/operations/use-operation"
import { describeSchedule } from "@/features/schedules/describe"
import { allServersQuery, type NodeServer, type Server, serverKey, useServer } from "@/features/servers/api"
import { serverType } from "@/features/servers/server-types"
import { formatBytes, formatDateTime } from "@/lib/format"
import {
  type Backup,
  backupFilesQuery,
  backupsQuery,
  defaultSelection,
  describeContent,
  downloadUrl,
  jobs,
  nothingSelected,
  pathsError,
  type Restore,
  useBackups,
} from "./api"
import { LocationField, SelectionField } from "./backup-fields"
import { ServerCopies } from "./copies"
import { UploadBackupDialog } from "./upload-backup-dialog"

const route = getRouteApi("/_app/nodes/$nodeId/servers/$serverId/backups")

/** The Backups tab of a server. */
export function ServerBackupsPage() {
  const { can } = useAccess()
  const { nodeId, serverId } = route.useParams()
  const { server } = useServer(nodeId, serverId)
  const { data: backups, isPending, error } = useQuery(backupsQuery(nodeId, serverId))
  const { data: jobList = [] } = useQuery({ ...jobs.coveringQuery(nodeId, serverId), enabled: can("backupjobs.view") })
  const { data: servers = [] } = useQuery(allServersQuery)
  const create = can("backups.create", nodeId, serverId)
  // An uploaded backup is there to be restored, and can bring any file, like the file manager.
  const upload = create && can("backups.restore", nodeId, serverId) && can("files.write", nodeId, serverId)
  const covering = jobList.filter((j) => j.enabled)

  if (!server || isPending) return <Skeleton className="h-64 rounded-xl" />
  if (error) return <ErrorCallout error={error} />
  const total = backups.reduce((sum, b) => sum + b.size, 0)
  // Other servers of the same kind, proxies or game servers, that the backups may be restored into.
  const others = servers.filter(
    (s) =>
      serverKey(s) !== serverKey({ nodeId, id: serverId }) &&
      serverType(s.type).proxy === serverType(server.type).proxy &&
      can("backups.restore", s.nodeId, s.id),
  )

  return (
    <>
      <Section
        title={t("{{count}} backups", { count: backups.length, defaultValue_one: "{{count}} backup" })}
        description={formatBytes(total)}
        className="mt-0"
        actions={create && <BackupActions nodeId={nodeId} server={server} upload={upload} />}
      >
        {can("backupjobs.view") && (
          <Callout tone={covering.length > 0 ? "info" : "neutral"} icon={ClockIcon} className="mb-4">
            {covering.length > 0 ? (
              <Trans
                i18nKey="Backed up by <jobs/>."
                components={{
                  jobs: (
                    <>
                      {covering.map((j, i) => (
                        <span key={j.id}>
                          {i > 0 && ", "}
                          <Link to="/backups/$jobId" params={{ jobId: j.id }} className="font-medium underline-offset-4 hover:underline">
                            {j.name}
                          </Link>{" "}
                          ({describeSchedule(j.schedule)})
                        </span>
                      ))}
                    </>
                  ),
                }}
              />
            ) : (
              <>
                {t("No backup job covers this server.")}
                {can("backupjobs.manage") && (
                  <>
                    {" "}
                    <Trans
                      i18nKey="<link>Create a job</link> to back it up on a schedule."
                      components={{ link: <Link to="/backups/new" className="font-medium underline-offset-4 hover:underline" /> }}
                    />
                  </>
                )}
              </>
            )}
          </Callout>
        )}
        {backups.length === 0 ? (
          <EmptyState icon={ArchiveIcon} tone="info" title={t("No backups yet")}>
            {create && <BackupActions nodeId={nodeId} server={server} upload={upload} />}
          </EmptyState>
        ) : (
          <ul className="surface divide-y rounded-xl">
            {backups.map((b) => (
              <BackupRow key={b.id} nodeId={nodeId} server={server} backup={b} others={others} />
            ))}
          </ul>
        )}
      </Section>
      <ServerCopies server={{ ...server, nodeId }} />
    </>
  )
}

/** Backs up the server now, and uploads a backup if allowed. */
function BackupActions({ nodeId, server, upload }: { nodeId: string; server: Server; upload: boolean }) {
  return (
    <div className="flex flex-wrap justify-end gap-2">
      {upload && <UploadBackupDialog nodeId={nodeId} server={server} />}
      <CreateBackupDialog nodeId={nodeId} server={server} />
    </div>
  )
}

function BackupRow({ nodeId, server, backup, others }: { nodeId: string; server: Server; backup: Backup; others: NodeServer[] }) {
  const { can } = useAccess()
  const { update, restore, remove } = useBackups(nodeId, server.id)
  const operation = useOperation()
  const [dialog, setDialog] = useState<"restore" | "label" | "delete">()
  const close = (open: boolean) => !open && setDialog(undefined)
  const created = formatDateTime(backup.createdAt)
  const change = can("backups.create", nodeId, server.id)
  // Letting its job delete it again is like deleting it.
  const release = can("backups.delete", nodeId, server.id)
  const restorable = can("backups.restore", nodeId, server.id) || others.length > 0

  const keep = (kept: boolean) =>
    update.mutate(
      { id: backup.id, kept },
      {
        onSuccess: () => toast.success(kept ? t("Its job keeps the backup") : t("Its job may delete the backup again")),
        onError: (e) => toast.error(e.message),
      },
    )

  const run = ({ name, ...input }: Omit<Restore, "id" | "onStart"> & { name: string }) =>
    operation.run((onStart) => restore.mutateAsync({ id: backup.id, ...input, onStart }), {
      title: t("Restoring {{name}}…", { name }),
      notify: true,
      done: ({ warning, snapshot }) => ({
        message: t("Restored the backup of {{time}}", { time: created }),
        description: warning ?? (snapshot && t("What it replaced is kept in the backup {{label}}.", { label: snapshot.label })),
        warning: !!warning,
      }),
    })

  return (
    <li className="flex flex-wrap items-center gap-3 px-4 py-3">
      <IconTile icon={ArchiveIcon} tone="info" size="sm" />
      <div className="min-w-0 flex-1">
        <p className="flex items-center gap-2 text-sm font-semibold">
          <span className="truncate">{backup.label || t("Backup")}</span>
          {backup.jobId && <Pill tone="info">{t("Scheduled")}</Pill>}
          {backup.kept && (
            <Pill tone="success">
              <PushPinIcon className="size-3" />
              {t("Kept")}
            </Pill>
          )}
          {backup.untrusted && (
            <span title={t("Not made on this node, e.g. uploaded. Restoring it checks its archive and keeps the server's secrets.")}>
              <Pill tone="neutral">{t("From elsewhere")}</Pill>
            </span>
          )}
        </p>
        <p className="truncate text-xs text-muted-foreground">
          {created} · {formatBytes(backup.size)} · {describeContent(backup)}
          {backup.location !== "default" && ` · ${backup.location}`}
        </p>
      </div>
      <div className="flex items-center gap-1.5">
        <Button asChild size="sm" variant="outline">
          <a href={downloadUrl(nodeId, server.id, backup.id)} download>
            <DownloadSimpleIcon />
            <span className="max-sm:sr-only">{t("Download")}</span>
          </a>
        </Button>
        {restorable && (
          <Button size="sm" variant="outline" disabled={restore.isPending} onClick={() => setDialog("restore")}>
            <ArrowCounterClockwiseIcon />
            <span className="max-sm:sr-only">{t("Restore")}</span>
          </Button>
        )}
        {(change || release) && (
          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <Button
                size="icon-sm"
                variant="ghost"
                className="text-muted-foreground"
                aria-label={t("More actions for the backup of {{time}}", { time: created })}
                title={t("More actions")}
              >
                <DotsThreeIcon weight="bold" />
              </Button>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end" className="w-56">
              {change && (
                <DropdownMenuItem onSelect={() => setDialog("label")}>
                  <TagIcon />
                  {t("Change label…")}
                </DropdownMenuItem>
              )}
              {/* Its job deletes it in time; backups made by hand are never deleted that way. */}
              {backup.jobId && !backup.kept && change && (
                <DropdownMenuItem disabled={update.isPending} onSelect={() => keep(true)}>
                  <PushPinIcon />
                  {t("Keep")}
                </DropdownMenuItem>
              )}
              {backup.jobId && backup.kept && release && (
                <DropdownMenuItem disabled={update.isPending} onSelect={() => keep(false)}>
                  <PushPinSlashIcon />
                  {t("Let its job delete it")}
                </DropdownMenuItem>
              )}
              {release && (
                <>
                  <DropdownMenuSeparator />
                  <DropdownMenuItem variant="destructive" disabled={remove.isPending} onSelect={() => setDialog("delete")}>
                    <TrashIcon />
                    {t("Delete…")}
                  </DropdownMenuItem>
                </>
              )}
            </DropdownMenuContent>
          </DropdownMenu>
        )}
      </div>
      {dialog === "restore" && (
        <RestoreDialog
          nodeId={nodeId}
          server={server}
          backup={backup}
          others={others}
          onClose={() => setDialog(undefined)}
          onRestore={run}
        />
      )}
      {dialog === "label" && <LabelDialog nodeId={nodeId} server={server} backup={backup} onClose={() => setDialog(undefined)} />}
      {dialog === "delete" && (
        <ConfirmDialog
          open
          onOpenChange={close}
          title={t("Delete the backup of {{time}}?", { time: created })}
          description={t("The backup is deleted from the node. This can't be undone.")}
          action={t("Delete backup")}
          destructive
          onConfirm={() =>
            remove.mutate(backup.id, { onSuccess: () => toast.success(t("Deleted the backup")), onError: (e) => toast.error(e.message) })
          }
        />
      )}
    </li>
  )
}

const here = "here"

/**
 * Chooses what of a backup is restored, all of it or files and folders in a browser of it, and where: into its server,
 * or into another one of the same kind, like a copy. What the restore replaces is backed up first unless turned off.
 */
function RestoreDialog({
  nodeId,
  server,
  backup,
  others,
  onClose,
  onRestore,
}: {
  nodeId: string
  server: Server
  backup: Backup
  others: NodeServer[]
  onClose: () => void
  onRestore: (input: Omit<Restore, "id" | "onStart"> & { name: string }) => void
}) {
  const { can } = useAccess()
  const own = can("backups.restore", nodeId, server.id)
  const [into, setInto] = useState(own || !others[0] ? here : serverKey(others[0]))
  const [part, setPart] = useState<"all" | "chosen">("all")
  const [folder, setFolder] = useState("")
  const [picked, setPicked] = useState<Picked[]>([])
  const [snapshotFirst, setSnapshotFirst] = useState(true)
  const target = others.find((s) => serverKey(s) === into)
  const name = target?.name ?? server.name
  const paths = part === "chosen" ? picked.map((p) => p.path) : []
  const error = pathsError(paths)

  function submit(event: FormEvent) {
    event.preventDefault()
    onClose()
    onRestore({ name, paths, snapshotFirst, into: target && { nodeId: target.nodeId, serverId: target.id } })
  }

  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent className="sm:max-w-2xl">
        <form onSubmit={submit} className="grid gap-6">
          <DialogHeader>
            <DialogTitle>{t("Restore the backup of {{time}}", { time: formatDateTime(backup.createdAt) })}</DialogTitle>
            <DialogDescription>{describeContent(backup)}</DialogDescription>
          </DialogHeader>
          <FieldGroup>
            {others.length > 0 && (
              <Field>
                <FieldLabel htmlFor="restore-into">{t("Restore into")}</FieldLabel>
                <Select value={into} onValueChange={setInto}>
                  <SelectTrigger id="restore-into" className="w-full">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    {own && <SelectItem value={here}>{t("{{name}}, its own server", { name: server.name })}</SelectItem>}
                    {others.map((s) => (
                      <SelectItem key={serverKey(s)} value={serverKey(s)}>
                        {s.name} <span className="text-muted-foreground">· {s.nodeName}</span>
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
                {target && (
                  <FieldDescription>
                    {t(
                      "Like a copy, {{name}} keeps its own secrets and those of its network, and gets no files with secrets of file sets.",
                      { name },
                    )}
                  </FieldDescription>
                )}
              </Field>
            )}
            <FieldSet>
              <FieldLegend variant="label">{t("What to restore")}</FieldLegend>
              <Segmented
                label={t("What to restore")}
                className="w-fit"
                value={part}
                onChange={setPart}
                options={[
                  { value: "all", label: t("All of it") },
                  { value: "chosen", label: t("Chosen files and folders") },
                ]}
              />
              {part === "chosen" && (
                <>
                  <PathPicker
                    server={{ nodeId, serverId: server.id }}
                    list={(path) => backupFilesQuery({ nodeId, serverId: server.id }, backup.id, path)}
                    folder={folder}
                    onFolder={setFolder}
                    picked={picked}
                    onPick={setPicked}
                  />
                  {error ? (
                    <FieldError>{error}</FieldError>
                  ) : (
                    <FieldDescription>{t("{{count}} chosen", { count: picked.length })}</FieldDescription>
                  )}
                </>
              )}
            </FieldSet>
            <Field orientation="horizontal">
              <Switch id="restore-snapshot" checked={snapshotFirst} onCheckedChange={setSnapshotFirst} />
              <FieldContent>
                <FieldLabel htmlFor="restore-snapshot">{t("Back up what is replaced first")}</FieldLabel>
                <FieldDescription>{t("As a backup made by hand, which undoes the restore if it was the wrong backup.")}</FieldDescription>
              </FieldContent>
            </Field>
          </FieldGroup>
          <p className="text-sm text-muted-foreground">
            {[
              part === "chosen"
                ? t("This replaces the chosen files and folders of {{name}} with their backed up state.", { name })
                : t("This replaces {{content}} of {{name}} with the backed up state; what was added since is removed.", {
                    content: backup.paths.includes(".") ? t("everything") : describeContent({ ...backup, exclude: [] }),
                    name,
                  }),
              (target ?? server).state !== "stopped" && t("The server stops while this happens and starts again afterwards."),
            ]
              .filter(Boolean)
              .join(" ")}
          </p>
          <DialogFooter>
            <DialogClose asChild>
              <Button variant="outline">{t("Cancel")}</Button>
            </DialogClose>
            <Button type="submit" variant="destructive" disabled={(part === "chosen" && picked.length === 0) || !!error}>
              <ArrowCounterClockwiseIcon />
              {t("Restore")}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

/** Changes the label of a backup. */
function LabelDialog({ nodeId, server, backup, onClose }: { nodeId: string; server: Server; backup: Backup; onClose: () => void }) {
  const [label, setLabel] = useState(backup.label)
  const { update } = useBackups(nodeId, server.id)

  function submit(event: FormEvent) {
    event.preventDefault()
    update.mutate({ id: backup.id, label: label.trim() }, { onSuccess: onClose })
  }

  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent {...guard(update.isPending)}>
        <form onSubmit={submit} className="grid gap-6">
          <DialogHeader>
            <DialogTitle>{t("Change label")}</DialogTitle>
            <DialogDescription>{t("The backup of {{time}}", { time: formatDateTime(backup.createdAt) })}</DialogDescription>
          </DialogHeader>
          <Field data-invalid={!!update.error}>
            <FieldLabel htmlFor="backup-new-label">{t("Label")}</FieldLabel>
            <Input
              id="backup-new-label"
              autoFocus
              maxLength={64}
              placeholder={t("Before the update")}
              value={label}
              onChange={(e) => setLabel(e.target.value)}
            />
            {update.error && <FieldError>{update.error.message}</FieldError>}
          </Field>
          <DialogFooter>
            <DialogClose asChild>
              <Button variant="outline" disabled={update.isPending}>
                {t("Cancel")}
              </Button>
            </DialogClose>
            <Button type="submit" disabled={update.isPending}>
              {t("Save")}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

/**
 * Backs up a server by hand. Once the master backs it up, the dialog closes and a notification follows the backup, as
 * large worlds take a while; a request that fails right away shows its error in the dialog.
 */
function CreateBackupDialog({ nodeId, server }: { nodeId: string; server: Server }) {
  const [open, setOpen] = useState(false)
  // The mutation lives here, as the button tells while the backup is made, also after the dialog closed.
  const { create } = useBackups(nodeId, server.id)
  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        setOpen(next)
        if (next) create.reset()
      }}
    >
      <DialogTrigger asChild>
        <Button disabled={create.isPending}>
          <PlusIcon />
          {create.isPending ? t("Backing up…") : t("Back up now")}
        </Button>
      </DialogTrigger>
      <DialogContent className="sm:max-w-2xl" {...guard(create.isPending)}>
        <CreateBackupForm nodeId={nodeId} server={server} create={create} onClose={() => setOpen(false)} />
      </DialogContent>
    </Dialog>
  )
}

/** What to back up and where, chosen anew each time the dialog opens. */
function CreateBackupForm({
  nodeId,
  server,
  create,
  onClose,
}: {
  nodeId: string
  server: Server
  create: ReturnType<typeof useBackups>["create"]
  onClose: () => void
}) {
  const [label, setLabel] = useState("")
  const [selection, setSelection] = useState(defaultSelection)
  const [location, setLocation] = useState("")
  const { data: node } = useQuery(nodeQuery(nodeId))
  const operation = useOperation()

  function submit(event: FormEvent) {
    event.preventDefault()
    const title = t("Backing up {{name}}…", { name: server.name })
    operation.run(
      (onStart) =>
        create.mutateAsync({
          label: label.trim(),
          selection,
          location,
          onStart: (op) => {
            onStart(op)
            operation.background(title, op.id)
            onClose()
          },
        }),
      {
        title,
        done: (b) => ({ message: t("Backed up {{name}} ({{size}})", { name: server.name, size: formatBytes(b.size) }) }),
        then: onClose,
      },
    )
  }

  return (
    <form onSubmit={submit} className="grid gap-6">
      <DialogHeader>
        <DialogTitle>{t("Back up {{name}}", { name: server.name })}</DialogTitle>
        <DialogDescription>
          {server.state === "stopped"
            ? t("The backup is kept on the node.")
            : t("The server saves its worlds first and keeps running. The backup is kept on the node.")}
        </DialogDescription>
      </DialogHeader>
      <FieldGroup>
        <Field>
          <FieldLabel htmlFor="backup-label">{t("Label")}</FieldLabel>
          <Input
            id="backup-label"
            maxLength={64}
            placeholder={t("Before the update")}
            value={label}
            onChange={(e) => setLabel(e.target.value)}
          />
        </Field>
        <SelectionField value={selection} server={{ nodeId, serverId: server.id }} onChange={setSelection} />
        <LocationField locations={node?.info?.storage.map((l) => l.name) ?? []} value={location} onChange={setLocation} />
      </FieldGroup>
      {create.error && <FieldError>{create.error.message}</FieldError>}
      <DialogFooter>
        <DialogClose asChild>
          <Button variant="outline" disabled={create.isPending}>
            {t("Cancel")}
          </Button>
        </DialogClose>
        <Button
          type="submit"
          disabled={nothingSelected(selection) || !!pathsError(selection.paths) || !!pathsError(selection.exclude ?? []) || create.isPending}
        >
          {create.isPending ? t("Backing up…") : t("Back up")}
        </Button>
      </DialogFooter>
    </form>
  )
}
