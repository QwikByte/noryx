import { ArchiveIcon, ArrowCounterClockwiseIcon, FileArrowUpIcon, UploadSimpleIcon } from "@phosphor-icons/react"
import { t } from "i18next"
import { type FormEvent, useEffect, useId, useRef, useState } from "react"
import { Trans } from "react-i18next"
import { toast } from "sonner"
import { FormDialogContent, guard, useBusy } from "@/components/form-dialog"
import { Button } from "@/components/ui/button"
import { Checkbox } from "@/components/ui/checkbox"
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
import { Field, FieldContent, FieldDescription, FieldError, FieldGroup, FieldLabel, FieldLegend, FieldSet, FieldTitle } from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import { Progress } from "@/components/ui/progress"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { useOperation } from "@/features/operations/use-operation"
import { formatBytes, formatDateTime } from "@/lib/format"
import { type Datastore, type Dump, maxUpload, useDatastore } from "./api"

/**
 * Backs up databases of a datastore by hand, all at first. Once the master dumps them, the dialog closes and a
 * notification follows the backup; a request that fails right away shows its error in the dialog.
 */
export function BackUpDialog({ datastore: ds }: { datastore: Datastore }) {
  // Databases the datastore lacks can't be dumped.
  const names = ds.databases.map((db) => db.name).filter((name) => !ds.missing.includes(name))
  const [open, setOpen] = useState(false)
  const { dump } = useDatastore(ds.id)

  function onOpenChange(next: boolean) {
    setOpen(next)
    if (next) dump.reset()
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogTrigger asChild>
        <Button size="sm" variant="outline" disabled={dump.isPending || ds.state !== "running" || names.length === 0}>
          <ArchiveIcon />
          {dump.isPending ? t("Backing up…") : t("Back up now")}
        </Button>
      </DialogTrigger>
      <DialogContent className="sm:max-w-lg" {...guard(dump.isPending)}>
        <BackUpForm datastore={ds} names={names} dump={dump} onClose={() => setOpen(false)} />
      </DialogContent>
    </Dialog>
  )
}

/** Chooses a label and databases, at first all, and backs them up in an operation. */
function BackUpForm({
  datastore: ds,
  names,
  dump,
  onClose,
}: {
  datastore: Datastore
  names: string[]
  dump: ReturnType<typeof useDatastore>["dump"]
  onClose: () => void
}) {
  const [label, setLabel] = useState("")
  const [databases, setDatabases] = useState(names)
  const chosen = databases.filter((name) => names.includes(name))
  const operation = useOperation()

  function submit(event: FormEvent) {
    event.preventDefault()
    const title = t("Backing up {{name}}…", { name: ds.name })
    operation.run(
      (onStart) =>
        dump.mutateAsync({
          label: label.trim(),
          databases: chosen,
          onStart: (op) => {
            onStart(op)
            operation.background(title, op.id)
            onClose()
          },
        }),
      {
        title,
        done: (d) => ({ message: t("Backed up {{name}} ({{size}})", { name: ds.name, size: formatBytes(d.size) }) }),
        then: onClose,
      },
    )
  }

  return (
    <form onSubmit={submit} className="grid gap-6">
      <DialogHeader>
        <DialogTitle>{t("Back up {{name}}", { name: ds.name })}</DialogTitle>
        <DialogDescription>
          {t("The databases are dumped while the datastore keeps running. The backup is kept on {{node}}.", { node: ds.nodeName })}
        </DialogDescription>
      </DialogHeader>
      <FieldGroup>
        <Field>
          <FieldLabel htmlFor="dump-label">{t("Label")}</FieldLabel>
          <Input
            id="dump-label"
            maxLength={64}
            placeholder={t("Before the update")}
            value={label}
            onChange={(e) => setLabel(e.target.value)}
          />
        </Field>
        <DatabasesField legend={t("Databases to back up")} names={names} value={chosen} onChange={setDatabases} />
      </FieldGroup>
      {dump.error && <FieldError>{dump.error.message}</FieldError>}
      <DialogFooter>
        <DialogClose asChild>
          <Button variant="outline" disabled={dump.isPending}>
            {t("Cancel")}
          </Button>
        </DialogClose>
        <Button type="submit" disabled={chosen.length === 0 || dump.isPending}>
          {dump.isPending ? t("Backing up…") : t("Back up")}
        </Button>
      </DialogFooter>
    </form>
  )
}

/**
 * Restores the chosen databases of a dump; one that was dropped since has to be added again first. Once the master
 * restores them, the dialog closes and a notification follows.
 */
export function RestoreDialog({ datastore: ds, dump: d }: { datastore: Datastore; dump: Dump }) {
  const [open, setOpen] = useState(false)
  const { restore } = useDatastore(ds.id)

  function onOpenChange(next: boolean) {
    setOpen(next)
    if (next) restore.reset()
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogTrigger asChild>
        <Button size="sm" variant="outline" disabled={restore.isPending}>
          <ArrowCounterClockwiseIcon />
          <span className="max-sm:sr-only">{t("Restore")}</span>
        </Button>
      </DialogTrigger>
      <DialogContent className="sm:max-w-lg" {...guard(restore.isPending)}>
        <RestoreForm
          datastore={ds}
          dump={d}
          restore={restore}
          title={t("Restore the backup of {{time}}?", { time: formatDateTime(d.createdAt) })}
          onClose={() => setOpen(false)}
        />
      </DialogContent>
    </Dialog>
  )
}

/** Chooses databases of a dump, at first all that the datastore has, and restores them in an operation. */
function RestoreForm({
  datastore: ds,
  dump: d,
  restore,
  title,
  cancel = t("Cancel"),
  onClose,
}: {
  datastore: Datastore
  dump: Dump
  restore: ReturnType<typeof useDatastore>["restore"]
  title: string
  cancel?: string
  onClose: () => void
}) {
  const known = (name: string) => ds.databases.some((db) => db.name === name)
  const [databases, setDatabases] = useState(() => d.databases.filter(known))
  const chosen = databases.filter(known)
  const operation = useOperation()

  function submit(event: FormEvent) {
    event.preventDefault()
    const running = t("Restoring {{name}}…", { name: ds.name })
    operation.run(
      (onStart) =>
        restore.mutateAsync({
          dump: d.id,
          databases: chosen,
          onStart: (op) => {
            onStart(op)
            operation.background(running, op.id)
            onClose()
          },
        }),
      {
        title: running,
        done: () => ({ message: t("Restored the backup of {{time}}", { time: formatDateTime(d.createdAt) }) }),
        then: onClose,
      },
    )
  }

  return (
    <form onSubmit={submit} className="grid gap-6">
      <DialogHeader>
        <DialogTitle>{title}</DialogTitle>
        <DialogDescription>
          {t(
            "The chosen databases get their backed up state; what was added since is lost. The plugins that use them lose their connection meanwhile, so stop their servers first.",
          )}
        </DialogDescription>
      </DialogHeader>
      <DatabasesField
        legend={t("Databases to restore")}
        names={d.databases}
        value={chosen}
        onChange={setDatabases}
        unavailable={(name) => (known(name) ? undefined : t("Add the database to restore it."))}
      />
      {restore.error && <FieldError>{restore.error.message}</FieldError>}
      <DialogFooter>
        <DialogClose asChild>
          <Button variant="outline" disabled={restore.isPending}>
            {cancel}
          </Button>
        </DialogClose>
        <Button type="submit" variant="destructive" disabled={chosen.length === 0 || restore.isPending}>
          {restore.isPending ? t("Restoring…") : t("Restore")}
        </Button>
      </DialogFooter>
    </form>
  )
}

/**
 * Uploads a dump made elsewhere, e.g. of a database that moves to Noryx, and offers to restore it right away: a ZIP
 * archive with a <database>.sql for each database, or the SQL file of the database chosen. Closing the dialog cancels
 * the upload.
 */
export function UploadDialog({ datastore: ds }: { datastore: Datastore }) {
  const [open, setOpen] = useState(false)
  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogTrigger asChild>
        <Button size="sm" variant="outline">
          <UploadSimpleIcon />
          {t("Upload")}
        </Button>
      </DialogTrigger>
      <FormDialogContent className="sm:max-w-lg">
        <UploadForm datastore={ds} onClose={() => setOpen(false)} />
      </FormDialogContent>
    </Dialog>
  )
}

/** The file to upload, chosen anew each time the dialog opens, and then the restore it offers. */
function UploadForm({ datastore: ds, onClose }: { datastore: Datastore; onClose: () => void }) {
  const names = ds.databases.map((db) => db.name)
  const [file, setFile] = useState<File>()
  const [database, setDatabase] = useState("")
  const [label, setLabel] = useState("")
  const [progress, setProgress] = useState(0)
  const [uploaded, setUploaded] = useState<Dump>()
  const picker = useRef<HTMLInputElement>(null)
  const abort = useRef<AbortController>(null)
  const { upload, restore } = useDatastore(ds.id)
  const sql = !!file?.name.toLowerCase().endsWith(".sql")
  const tooLarge = !!file && file.size > maxUpload
  useBusy(restore.isPending)
  // Closing the dialog cancels the upload.
  useEffect(() => () => abort.current?.abort(), [])

  function choose(chosen: File) {
    upload.reset()
    setFile(chosen)
    const base = chosen.name.replace(/\.sql$/i, "").toLowerCase()
    setDatabase(names.includes(base) ? base : (names[0] ?? ""))
  }

  function submit(event: FormEvent) {
    event.preventDefault()
    if (!file) return
    abort.current = new AbortController()
    setProgress(0)
    upload.mutate(
      {
        file,
        database: sql ? database : undefined,
        label: label.trim() || file.name.slice(0, 64),
        onProgress: setProgress,
        signal: abort.current.signal,
      },
      {
        onSuccess: (dump) => {
          toast.success(t("Uploaded {{name}}", { name: file.name }))
          setUploaded(dump)
        },
      },
    )
  }

  return uploaded && file ? (
    <RestoreForm
      datastore={ds}
      dump={uploaded}
      restore={restore}
      title={t("Restore {{name}} now?", { name: file.name })}
      cancel={t("Later")}
      onClose={onClose}
    />
  ) : (
    <form onSubmit={submit} className="grid gap-6">
      <DialogHeader>
        <DialogTitle>{t("Upload a backup")}</DialogTitle>
        <DialogDescription>
          {t(
            "A dump made elsewhere, e.g. of a database that moves to Noryx: a ZIP archive with a <database>.sql for each database, or the SQL file of one. It is kept like the backups made here, and restoring it loads it as the database's user.",
          )}
        </DialogDescription>
      </DialogHeader>
      <FieldGroup>
        <Field data-invalid={tooLarge || undefined}>
          <Button
            type="button"
            variant="outline"
            className="w-full justify-start"
            disabled={upload.isPending}
            onClick={() => picker.current?.click()}
          >
            <FileArrowUpIcon />
            <span className="truncate">{file ? `${file.name} · ${formatBytes(file.size)}` : t("Choose a .zip or .sql file")}</span>
          </Button>
          <input
            ref={picker}
            type="file"
            accept=".zip,.sql,application/zip,application/sql"
            hidden
            onChange={(e) => {
              const chosen = e.target.files?.[0]
              if (chosen) choose(chosen)
              e.target.value = ""
            }}
          />
          {tooLarge && <FieldError>{t("A backup can have up to {{size}}.", { size: formatBytes(maxUpload) })}</FieldError>}
        </Field>
        {sql && (
          <Field>
            <FieldLabel htmlFor="upload-database">{t("Database")}</FieldLabel>
            <Select value={database} onValueChange={setDatabase} disabled={upload.isPending || names.length === 0}>
              <SelectTrigger id="upload-database" className="w-full font-mono">
                <SelectValue placeholder={t("Choose a database")} />
              </SelectTrigger>
              <SelectContent>
                {names.map((name) => (
                  <SelectItem key={name} value={name} className="font-mono">
                    {name}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
            {names.length === 0 ? (
              <FieldError>{t("Add the database first.")}</FieldError>
            ) : (
              <FieldDescription>{t("Restoring the backup loads the SQL into this database.")}</FieldDescription>
            )}
          </Field>
        )}
        <Field>
          <FieldLabel htmlFor="upload-label">{t("Label")}</FieldLabel>
          <Input
            id="upload-label"
            maxLength={64}
            placeholder={file?.name.slice(0, 64) ?? t("From the old server")}
            disabled={upload.isPending}
            value={label}
            onChange={(e) => setLabel(e.target.value)}
          />
        </Field>
        <FieldDescription>
          <Trans
            i18nKey="Dump each database on its own, without its owner and privileges, e.g. with <mariadb/> or <postgres/>. Statements that need more rights than the database's user has, e.g. to create or use another database, fail the restore, and commands of the client itself, such as <shell/> or <connect/>, are refused."
            components={{
              mariadb: <code className="font-mono text-xs">mariadb-dump luckperms</code>,
              postgres: <code className="font-mono text-xs">pg_dump --no-owner --no-privileges luckperms</code>,
              shell: <code className="font-mono text-xs">\!</code>,
              connect: <code className="font-mono text-xs">\connect</code>,
            }}
          />
        </FieldDescription>
      </FieldGroup>
      {upload.isPending && <Progress value={progress * 100} aria-label={t("Upload of {{name}}", { name: file?.name })} />}
      {upload.error && upload.error.name !== "AbortError" && <FieldError>{upload.error.message}</FieldError>}
      <DialogFooter>
        <DialogClose asChild>
          <Button variant="outline">{t("Cancel")}</Button>
        </DialogClose>
        <Button type="submit" disabled={!file || tooLarge || (sql && !database) || upload.isPending}>
          {upload.isPending ? t("Uploading…") : t("Upload")}
        </Button>
      </DialogFooter>
    </form>
  )
}

/** Chooses databases; one that can't be chosen tells why. */
function DatabasesField({
  legend,
  names,
  value,
  onChange,
  unavailable,
}: {
  legend: string
  names: string[]
  value: string[]
  onChange: (names: string[]) => void
  unavailable?: (name: string) => string | undefined
}) {
  const id = useId()
  return (
    <FieldSet>
      <FieldLegend variant="label">{legend}</FieldLegend>
      <div className="grid gap-2 sm:grid-cols-2">
        {names.map((name) => {
          const why = unavailable?.(name)
          return (
            <FieldLabel key={name} htmlFor={`${id}-${name}`}>
              <Field orientation="horizontal" data-disabled={!!why}>
                <Checkbox
                  id={`${id}-${name}`}
                  checked={value.includes(name)}
                  disabled={!!why}
                  onCheckedChange={(on) => onChange(on === true ? [...value, name] : value.filter((other) => other !== name))}
                />
                <FieldContent>
                  <FieldTitle>
                    <span className="font-mono">{name}</span>
                  </FieldTitle>
                  {why && <FieldDescription>{why}</FieldDescription>}
                </FieldContent>
              </Field>
            </FieldLabel>
          )
        })}
      </div>
    </FieldSet>
  )
}
