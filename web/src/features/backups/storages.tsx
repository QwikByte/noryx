import { CloudArrowUpIcon, PencilSimpleIcon, PlusIcon, ShieldCheckIcon, TrashIcon, WarningIcon } from "@phosphor-icons/react"
import { useQuery } from "@tanstack/react-query"
import { t } from "i18next"
import { type FormEvent, type ReactElement, type ReactNode, useState } from "react"
import { toast } from "sonner"
import { Callout } from "@/components/callout"
import { ConfirmDialog } from "@/components/confirm-dialog"
import { FormDialogContent, useBusy } from "@/components/form-dialog"
import { IconTile } from "@/components/icon-tile"
import { Section } from "@/components/section"
import { Pill } from "@/components/status"
import { Button } from "@/components/ui/button"
import {
  Dialog,
  DialogClose,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from "@/components/ui/dialog"
import { Field, FieldContent, FieldDescription, FieldError, FieldGroup, FieldLabel } from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import { Select, SelectContent, SelectGroup, SelectItem, SelectLabel, SelectTrigger, SelectValue } from "@/components/ui/select"
import { Switch } from "@/components/ui/switch"
import { useAccess } from "@/features/access/use-access"
import { nodesQuery } from "@/features/nodes/api"
import { type CopyTo, type Storage, type StorageSettings, storagesQuery, useStorages } from "./api"

/** Whether the user may add, change and delete storages and let jobs copy: copies take the backups of all servers away. */
function useManageCopies() {
  const { can } = useAccess()
  return can("backupjobs.manage") && can("backups.view")
}

/** The S3-compatible storages that jobs copy backups to, on the backup jobs' tab, where they are set up. */
export function StoragesSection() {
  const manage = useManageCopies()
  const { data: storages = [] } = useQuery(storagesQuery)
  if (storages.length === 0 && !manage) return null
  return (
    <Section
      title={t("Storage for copies")}
      description={t(
        "S3-compatible storage, such as AWS S3, Backblaze B2 or MinIO, that jobs copy their backups to, so that servers can be restored when their node is lost.",
      )}
      actions={
        manage && (
          <StorageDialog
            trigger={
              <Button variant="outline">
                <PlusIcon />
                {t("Add storage")}
              </Button>
            }
          />
        )
      }
    >
      <Callout tone="warning" icon={WarningIcon} className="mb-4">
        {t(
          "The master doesn't encrypt the copies. Whoever runs the storage can read them: worlds, plugins and their settings, which can hold passwords. The secrets of the servers, such as their console passwords, the secrets of their networks and of file sets, are left out.",
        )}
      </Callout>
      {storages.length === 0 ? (
        <p className="text-sm text-muted-foreground">{t("No storage yet. Jobs can copy their backups to another node meanwhile.")}</p>
      ) : (
        <ul className="grid gap-4 md:grid-cols-2">
          {storages.map((s) => (
            <StorageCard key={s.id} storage={s} manage={manage} />
          ))}
        </ul>
      )}
    </Section>
  )
}

function StorageCard({ storage, manage }: { storage: Storage; manage: boolean }) {
  const { remove } = useStorages()
  return (
    <li className="surface flex min-w-0 items-start gap-3 rounded-xl p-4">
      <IconTile icon={CloudArrowUpIcon} tone="info" />
      <div className="min-w-0 flex-1 space-y-1">
        <p className="truncate font-semibold">{storage.name}</p>
        <p className="truncate font-mono text-xs text-muted-foreground">
          {storage.bucket}
          {storage.prefix && `/${storage.prefix}`} · {storage.endpoint}
        </p>
        <div className="flex flex-wrap gap-1.5 pt-1">
          <Pill tone="neutral">{t("{{count}} copies", { count: storage.copies, defaultValue_one: "{{count}} copy" })}</Pill>
          {storage.encrypt ? (
            <Pill tone="success">
              <ShieldCheckIcon className="size-3" />
              {t("Encrypted by the storage")}
            </Pill>
          ) : (
            <Pill tone="warning">{t("Not encrypted by the storage")}</Pill>
          )}
        </div>
      </div>
      {manage && (
        <div className="flex shrink-0 gap-1">
          <StorageDialog
            storage={storage}
            trigger={
              <Button size="icon-sm" variant="ghost" aria-label={t("Change {{name}}", { name: storage.name })} title={t("Change")}>
                <PencilSimpleIcon />
              </Button>
            }
          />
          <ConfirmDialog
            trigger={
              <Button size="icon-sm" variant="ghost" aria-label={t("Delete {{name}}", { name: storage.name })} title={t("Delete")}>
                <TrashIcon />
              </Button>
            }
            title={t("Delete {{name}}?", { name: storage.name })}
            description={t("The panel forgets the copies it holds, which stay in the bucket. Jobs that copy there have to copy elsewhere first.")}
            action={t("Delete storage")}
            destructive
            onConfirm={() =>
              remove.mutate(storage.id, {
                onSuccess: () => toast.success(t("Deleted {{name}}", { name: storage.name })),
                onError: (e) => toast.error(e.message),
              })
            }
          />
        </div>
      )}
    </li>
  )
}

const blank: StorageSettings = { name: "", endpoint: "", region: "", bucket: "", prefix: "", accessKey: "", pathStyle: false, encrypt: true }

/** Adds a storage, or changes one, whose secret key stays unless another one is entered. */
function StorageDialog({ storage, trigger }: { storage?: Storage; trigger: ReactElement }) {
  const [open, setOpen] = useState(false)
  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogTrigger asChild>{trigger}</DialogTrigger>
      <FormDialogContent className="sm:max-w-2xl">
        <StorageForm storage={storage} onClose={() => setOpen(false)} />
      </FormDialogContent>
    </Dialog>
  )
}

/** The storage as it is each time the dialog opens, or a new one, without its secret key. */
function StorageForm({ storage, onClose }: { storage?: Storage; onClose: () => void }) {
  const [form, setForm] = useState<StorageSettings>(storage ?? blank)
  const [secretKey, setSecretKey] = useState("")
  const { save } = useStorages()
  const set = (change: Partial<StorageSettings>) => setForm((f) => ({ ...f, ...change }))
  useBusy(save.isPending)

  function submit(event: FormEvent) {
    event.preventDefault()
    save.mutate(
      { ...form, id: storage?.id, secretKey },
      {
        onSuccess: (s) => {
          toast.success(storage ? t("Saved {{name}}", { name: s.name }) : t("Added {{name}}", { name: s.name }))
          onClose()
        },
      },
    )
  }

  const text = (key: "name" | "endpoint" | "region" | "bucket" | "prefix" | "accessKey", label: string, placeholder: string, mono = true) => (
    <Field>
      <FieldLabel htmlFor={`storage-${key}`}>{label}</FieldLabel>
      <Input
        id={`storage-${key}`}
        className={mono ? "font-mono" : undefined}
        placeholder={placeholder}
        autoComplete="off"
        spellCheck={false}
        value={form[key]}
        onChange={(e) => set({ [key]: e.target.value })}
      />
    </Field>
  )

  return (
    <form onSubmit={submit} className="grid gap-6">
      <DialogHeader>
        <DialogTitle>{storage ? t("Change {{name}}", { name: storage.name }) : t("Add storage for copies")}</DialogTitle>
        <DialogDescription>
          {t("The master only connects over HTTPS, and checks that it can store and delete an object there before it saves the storage.")}
        </DialogDescription>
      </DialogHeader>
      <FieldGroup>
        {text("name", t("Name"), t("Offsite"), false)}
        <div className="grid gap-4 sm:grid-cols-[2fr_1fr]">
          {text("endpoint", t("Endpoint"), "s3.eu-central-1.amazonaws.com")}
          {text("region", t("Region"), "eu-central-1")}
        </div>
        <div className="grid gap-4 sm:grid-cols-2">
          {text("bucket", t("Bucket"), "noryx-backups")}
          {text("prefix", t("Folder (optional)"), "noryx")}
        </div>
        <div className="grid gap-4 sm:grid-cols-2">
          {text("accessKey", t("Access key"), "AKIA…")}
          <Field>
            <FieldLabel htmlFor="storage-secret">{t("Secret key")}</FieldLabel>
            <Input
              id="storage-secret"
              type="password"
              className="font-mono"
              autoComplete="new-password"
              placeholder={storage ? t("Unchanged") : undefined}
              value={secretKey}
              onChange={(e) => setSecretKey(e.target.value)}
            />
            {storage && <FieldDescription>{t("It is never shown. Enter one only to change it.")}</FieldDescription>}
          </Field>
        </div>
        <FieldDescription>{t("The key only needs to put, get and delete objects in the bucket.")}</FieldDescription>
        <Field orientation="horizontal">
          <Switch id="storage-encrypt" checked={form.encrypt} onCheckedChange={(encrypt) => set({ encrypt })} />
          <FieldContent>
            <FieldLabel htmlFor="storage-encrypt">{t("Ask the storage to encrypt the copies")}</FieldLabel>
            <FieldDescription>
              {t("With its own keys (SSE-S3), which protects them on its disks but not from its provider. Turn it off for storage that refuses it.")}
            </FieldDescription>
          </FieldContent>
        </Field>
        <Field orientation="horizontal">
          <Switch id="storage-path-style" checked={form.pathStyle} onCheckedChange={(pathStyle) => set({ pathStyle })} />
          <FieldContent>
            <FieldLabel htmlFor="storage-path-style">{t("Path style")}</FieldLabel>
            <FieldDescription>{t("Addresses the bucket in the path rather than in the host name, e.g. for MinIO.")}</FieldDescription>
          </FieldContent>
        </Field>
      </FieldGroup>
      {save.error && <FieldError>{save.error.message}</FieldError>}
      <DialogFooter>
        <DialogClose asChild>
          <Button variant="outline" disabled={save.isPending}>
            {t("Cancel")}
          </Button>
        </DialogClose>
        <Button type="submit" disabled={save.isPending || (!storage && !secretKey)}>
          {save.isPending ? t("Checking…") : storage ? t("Save") : t("Add storage")}
        </Button>
      </DialogFooter>
    </form>
  )
}

const none = "none"

/** Chooses where a job copies its backups to: nowhere, a storage, or another node in one of its storage locations. */
export function CopyField({ value, onChange }: { value?: CopyTo; onChange: (copy?: CopyTo) => void }) {
  const manage = useManageCopies()
  return (
    <PlaceField id="job-copy" label={t("Copy to")} value={value} onChange={onChange} disabled={!manage}>
      {t(
        "Each backup of a server is copied away from its node, without the server's secrets, so that the server can be restored if its node is lost. The job keeps as many copies as backups. Storage is set up below the jobs on the Backups tab of the Automation. Copying needs the permission to see the backups of all servers.",
      )}
    </PlaceField>
  )
}

/**
 * Chooses a storage, or another node in one of its storage locations, e.g. for copies of backups; nowhere too unless
 * the place is required.
 */
export function PlaceField({
  id,
  label,
  value,
  onChange,
  required = false,
  disabled = false,
  children,
}: {
  id: string
  label: string
  value?: CopyTo
  onChange: (place?: CopyTo) => void
  required?: boolean
  disabled?: boolean
  /** Describes what goes there. */
  children: ReactNode
}) {
  const { data: storages = [] } = useQuery(storagesQuery)
  const { data: nodes = [] } = useQuery(nodesQuery)
  const selected = value?.storage ? `storage:${value.storage}` : value?.node ? `node:${value.node}` : required ? "" : none
  const node = nodes.find((n) => n.id === value?.node)
  const locations = [...new Set(["default", ...(node?.info?.storage.map((l) => l.name) ?? []), value?.location || "default"])]

  function choose(v: string) {
    const [kind, id] = v.split(":")
    onChange(kind === "storage" ? { storage: id } : kind === "node" ? { node: id } : undefined)
  }

  return (
    <Field>
      <FieldLabel htmlFor={id}>{label}</FieldLabel>
      <div className="flex flex-wrap gap-3">
        <Select value={selected} onValueChange={choose} disabled={disabled}>
          <SelectTrigger id={id} className="w-full sm:w-64">
            <SelectValue placeholder={t("Choose a storage or node")} />
          </SelectTrigger>
          <SelectContent>
            {!required && <SelectItem value={none}>{t("Nowhere")}</SelectItem>}
            {storages.length > 0 && (
              <SelectGroup>
                <SelectLabel>{t("S3 storage")}</SelectLabel>
                {storages.map((s) => (
                  <SelectItem key={s.id} value={`storage:${s.id}`}>
                    {s.name}
                  </SelectItem>
                ))}
              </SelectGroup>
            )}
            <SelectGroup>
              <SelectLabel>{t("Another node")}</SelectLabel>
              {nodes.map((n) => (
                <SelectItem key={n.id} value={`node:${n.id}`}>
                  {n.name}
                </SelectItem>
              ))}
            </SelectGroup>
          </SelectContent>
        </Select>
        {value?.node && (
          <Select value={value.location || "default"} onValueChange={(l) => onChange({ node: value.node, location: l === "default" ? "" : l })}>
            <SelectTrigger aria-label={t("Storage location")} className="w-full sm:w-48">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {locations.map((l) => (
                <SelectItem key={l} value={l}>
                  {l}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        )}
      </div>
      <FieldDescription>{children}</FieldDescription>
    </Field>
  )
}
