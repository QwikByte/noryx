import { FileArrowUpIcon, UploadSimpleIcon } from "@phosphor-icons/react"
import { useNavigate } from "@tanstack/react-router"
import { t } from "i18next"
import { type FormEvent, useRef, useState } from "react"
import { toast } from "sonner"
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
import { Field, FieldError, FieldGroup, FieldLabel } from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import { maxFileBytes, useImportTemplate } from "./api"

/** A file as exported by a master: the template is checked there, only its name is changed here. */
interface TemplateFile {
  template?: { name?: unknown }
}

/** Imports a template from a file exported by Noryx, e.g. on another master, under a name of choice. */
export function ImportTemplateDialog() {
  const [open, setOpen] = useState(false)
  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogTrigger asChild>
        <Button variant="outline">
          <UploadSimpleIcon />
          {t("Import")}
        </Button>
      </DialogTrigger>
      <DialogContent className="sm:max-w-lg">
        <ImportTemplateForm onClose={() => setOpen(false)} />
      </DialogContent>
    </Dialog>
  )
}

/** The file to import, chosen anew each time the dialog opens. */
function ImportTemplateForm({ onClose }: { onClose: () => void }) {
  const [file, setFile] = useState<{ name: string; content: TemplateFile }>()
  const [name, setName] = useState("")
  const [error, setError] = useState<string>()
  const picker = useRef<HTMLInputElement>(null)
  const save = useImportTemplate()
  const navigate = useNavigate()

  async function choose(chosen: File) {
    setError(undefined)
    save.reset()
    if (chosen.size > maxFileBytes) return setError(t("Template files have up to 1 MiB."))
    try {
      const content = JSON.parse(await chosen.text()) as TemplateFile
      setFile({ name: chosen.name, content })
      setName(typeof content?.template?.name === "string" ? content.template.name : "")
    } catch {
      setFile(undefined)
      setError(t("This isn't a template exported from Noryx."))
    }
  }

  function submit(event: FormEvent) {
    event.preventDefault()
    if (!file) return
    save.mutate(
      { ...file.content, template: { ...file.content.template, name: name.trim() } },
      {
        onSuccess: (template) => {
          toast.success(t("Imported {{name}}", { name: template.name }))
          onClose()
          void navigate({ to: "/templates/$templateId", params: { templateId: template.id } })
        },
      },
    )
  }

  return (
    <form onSubmit={submit} className="grid gap-6">
      <DialogHeader>
        <DialogTitle>{t("Import template")}</DialogTitle>
        <DialogDescription>
          {t("Choose a template exported from Noryx, e.g. on another master. It is checked like a template saved here, and its plugins are looked up again.")}
        </DialogDescription>
      </DialogHeader>
      <FieldGroup>
        <Field>
          <Button type="button" variant="outline" className="w-full justify-start" onClick={() => picker.current?.click()}>
            <FileArrowUpIcon />
            <span className="truncate">{file ? file.name : t("Choose file")}</span>
          </Button>
          <input
            ref={picker}
            type="file"
            accept=".json,application/json"
            hidden
            onChange={(e) => {
              const chosen = e.target.files?.[0]
              if (chosen) void choose(chosen)
              e.target.value = ""
            }}
          />
        </Field>
        {file && (
          <Field>
            <FieldLabel htmlFor="import-name">{t("Name")}</FieldLabel>
            <Input id="import-name" required maxLength={64} value={name} onChange={(e) => setName(e.target.value)} />
          </Field>
        )}
        {(error ?? save.error) && <FieldError>{error ?? save.error?.message}</FieldError>}
      </FieldGroup>
      <DialogFooter>
        <DialogClose asChild>
          <Button variant="outline">{t("Cancel")}</Button>
        </DialogClose>
        <Button type="submit" disabled={!file || save.isPending}>
          {save.isPending ? t("Importing…") : t("Import template")}
        </Button>
      </DialogFooter>
    </form>
  )
}
