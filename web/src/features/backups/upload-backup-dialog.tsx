import { UploadSimpleIcon } from "@phosphor-icons/react"
import { useQuery, useQueryClient } from "@tanstack/react-query"
import { t } from "i18next"
import { type FormEvent, useState } from "react"
import { toast } from "sonner"
import { FormDialogContent, useBusy } from "@/components/form-dialog"
import { UploadProgress } from "@/components/upload-progress"
import { Button } from "@/components/ui/button"
import { Dialog, DialogClose, DialogDescription, DialogFooter, DialogHeader, DialogTitle, DialogTrigger } from "@/components/ui/dialog"
import { Field, FieldDescription, FieldError, FieldGroup, FieldLabel } from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import { nodeQuery } from "@/features/nodes/api"
import type { Server } from "@/features/servers/api"
import { formatBytes } from "@/lib/format"
import { useUpload } from "@/lib/use-upload"
import { backupsQuery, uploadBackup } from "./api"
import { LocationField } from "./backup-fields"

/** Uploads a ZIP archive as a backup of a server, e.g. one downloaded before, to restore it like any other. */
export function UploadBackupDialog({ nodeId, server }: { nodeId: string; server: Server }) {
  const [open, setOpen] = useState(false)
  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogTrigger asChild>
        <Button variant="outline">
          <UploadSimpleIcon />
          {t("Upload")}
        </Button>
      </DialogTrigger>
      <FormDialogContent className="sm:max-w-lg">
        <UploadBackupForm nodeId={nodeId} server={server} onClose={() => setOpen(false)} />
      </FormDialogContent>
    </Dialog>
  )
}

/** The archive to upload, chosen anew each time the dialog opens. */
function UploadBackupForm({ nodeId, server, onClose }: { nodeId: string; server: Server; onClose: () => void }) {
  const [file, setFile] = useState<File>()
  const [label, setLabel] = useState("")
  const [location, setLocation] = useState("")
  const [error, setError] = useState<string>()
  const upload = useUpload()
  const queryClient = useQueryClient()
  const { data: node } = useQuery(nodeQuery(nodeId))
  useBusy(upload.uploading)

  function choose(chosen?: File) {
    setFile(chosen)
    // The label of a backup has up to 64 characters.
    if (chosen && !label) setLabel([...chosen.name.replace(/\.zip$/i, "")].slice(0, 64).join(""))
  }

  async function submit(event: FormEvent) {
    event.preventDefault()
    if (!file) return
    setError(undefined)
    try {
      const b = await upload.run((onProgress, signal) => uploadBackup(nodeId, server.id, file, { label: label.trim(), location, onProgress, signal }))
      if (!b) return
      void queryClient.invalidateQueries({ queryKey: backupsQuery(nodeId, server.id).queryKey })
      toast.success(t("Uploaded {{name}} as a backup", { name: file.name }), { description: formatBytes(b.size) })
      onClose()
    } catch (e) {
      setError((e as Error).message)
    }
  }

  return (
    <form onSubmit={submit} className="grid gap-6">
      <DialogHeader>
        <DialogTitle>{t("Upload a backup of {{name}}", { name: server.name })}</DialogTitle>
        <DialogDescription>
          {t(
            "A ZIP archive of files and folders of the server, e.g. a backup downloaded before. Restoring it replaces the files and folders at its top, and the server keeps its secrets and how it takes part in its network.",
          )}
        </DialogDescription>
      </DialogHeader>
      <FieldGroup>
        <Field>
          <FieldLabel htmlFor="upload-backup-archive">{t("Archive")}</FieldLabel>
          <Input
            id="upload-backup-archive"
            type="file"
            accept=".zip,application/zip"
            required
            disabled={upload.uploading}
            onChange={(e) => choose(e.target.files?.[0])}
          />
          <FieldDescription>
            {file
              ? formatBytes(file.size)
              : t("Archives with links, paths outside the server's folder or too much data are refused. Up to 16 GB.")}
          </FieldDescription>
        </Field>
        <Field>
          <FieldLabel htmlFor="upload-backup-label">{t("Label")}</FieldLabel>
          <Input
            id="upload-backup-label"
            maxLength={64}
            placeholder={t("Before the update")}
            disabled={upload.uploading}
            value={label}
            onChange={(e) => setLabel(e.target.value)}
          />
        </Field>
        <LocationField locations={node?.info?.storage.map((l) => l.name) ?? []} value={location} onChange={setLocation} />
      </FieldGroup>
      {upload.progress !== undefined && <UploadProgress progress={upload.progress} done={t("Checking the archive…")} />}
      {error && <FieldError>{error}</FieldError>}
      <DialogFooter>
        {upload.uploading ? (
          <Button type="button" variant="outline" onClick={upload.cancel}>
            {t("Cancel upload")}
          </Button>
        ) : (
          <DialogClose asChild>
            <Button variant="outline">{t("Cancel")}</Button>
          </DialogClose>
        )}
        <Button type="submit" disabled={!file || upload.uploading}>
          <UploadSimpleIcon />
          {upload.uploading ? t("Uploading…") : t("Upload")}
        </Button>
      </DialogFooter>
    </form>
  )
}
