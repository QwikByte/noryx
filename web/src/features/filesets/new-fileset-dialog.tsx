import { FilesIcon } from "@phosphor-icons/react"
import { useNavigate } from "@tanstack/react-router"
import { t } from "i18next"
import { type FormEvent, type ReactElement, useState } from "react"
import { toast } from "sonner"
import { Button } from "@/components/ui/button"
import { Dialog, DialogClose, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle, DialogTrigger } from "@/components/ui/dialog"
import { Field, FieldError, FieldLabel } from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import { Textarea } from "@/components/ui/textarea"
import { useSaveFileSet } from "./api"

// An example in an empty field, which needs no translation.
const exampleName = "LuckPerms"

/** Creates an empty set and opens it, to add its files and targets there. */
export function NewFileSetDialog({ trigger }: { trigger: ReactElement }) {
  return (
    <Dialog>
      <DialogTrigger asChild>{trigger}</DialogTrigger>
      <DialogContent className="sm:max-w-lg">
        <NewFileSetForm />
      </DialogContent>
    </Dialog>
  )
}

/** The name and description of the set, entered anew each time the dialog opens. */
function NewFileSetForm() {
  const [name, setName] = useState("")
  const [description, setDescription] = useState("")
  const save = useSaveFileSet()
  const navigate = useNavigate()

  function submit(event: FormEvent) {
    event.preventDefault()
    save.mutate(
      { name, description, files: [], targets: [], variables: [], version: 0 },
      {
        onSuccess: (set) => {
          toast.success(t("Created {{name}}", { name: set.name }))
          void navigate({ to: "/filesets/$fileSetId", params: { fileSetId: set.id } })
        },
      },
    )
  }

  return (
    <form onSubmit={submit} className="grid gap-6">
      <DialogHeader>
        <DialogTitle>{t("New file set")}</DialogTitle>
        <DialogDescription>{t("Saving a set doesn't change any servers. Its files reach the servers when it is applied.")}</DialogDescription>
      </DialogHeader>
      <Field>
        <FieldLabel htmlFor="fileset-name">{t("Name")}</FieldLabel>
        <Input id="fileset-name" autoFocus required maxLength={64} placeholder={exampleName} value={name} onChange={(e) => setName(e.target.value)} />
      </Field>
      <Field>
        <FieldLabel htmlFor="fileset-description">{t("Description")}</FieldLabel>
        <Textarea id="fileset-description" maxLength={500} rows={2} value={description} onChange={(e) => setDescription(e.target.value)} />
        {save.error && <FieldError>{save.error.message}</FieldError>}
      </Field>
      <DialogFooter>
        <DialogClose asChild>
          <Button variant="outline">{t("Cancel")}</Button>
        </DialogClose>
        <Button type="submit" disabled={save.isPending || !name.trim()}>
          <FilesIcon />
          {t("Create file set")}
        </Button>
      </DialogFooter>
    </form>
  )
}
