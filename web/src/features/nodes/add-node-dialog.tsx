import { PlusIcon } from "@phosphor-icons/react"
import { t } from "i18next"
import { type FormEvent, type ReactElement, useState } from "react"
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
import { Field, FieldDescription, FieldError, FieldGroup, FieldLabel } from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import { useCreateNode } from "./api"
import { EnrollSteps } from "./enroll-steps"

const empty = { name: "", address: "" }

/**
 * trigger replaces the button that opens the dialog; with open, it opens without one, e.g. from
 * the palette, and onOpenChange tells when it closes.
 */
export function AddNodeDialog({
  trigger,
  open: shown,
  onOpenChange: onShownChange,
}: {
  trigger?: ReactElement
  open?: boolean
  onOpenChange?: (open: boolean) => void
}) {
  const [ownOpen, setOpen] = useState(false)
  const open = shown ?? ownOpen

  function onOpenChange(next: boolean) {
    setOpen(next)
    onShownChange?.(next)
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      {shown === undefined && (
        <DialogTrigger asChild>
          {trigger ?? (
            <Button>
              <PlusIcon />
              {t("Add node")}
            </Button>
          )}
        </DialogTrigger>
      )}
      <DialogContent className="sm:max-w-lg">
        <AddNodeForm />
      </DialogContent>
    </Dialog>
  )
}

/** The node to add, created anew each time the dialog opens, and then how to connect it. */
function AddNodeForm() {
  const [form, setForm] = useState(empty)
  const create = useCreateNode()

  function submit(event: FormEvent) {
    event.preventDefault()
    create.mutate(form)
  }

  return create.data ? (
    <>
      <DialogHeader>
        <DialogTitle>{t("Connect {{name}}", { name: create.data.node.name })}</DialogTitle>
        <DialogDescription>{t("The node was added. Connect its agent to start hosting servers.")}</DialogDescription>
      </DialogHeader>
      <EnrollSteps token={create.data} />
      <DialogFooter>
        <DialogClose asChild>
          <Button>{t("Done")}</Button>
        </DialogClose>
      </DialogFooter>
    </>
  ) : (
    <form onSubmit={submit} className="grid gap-6">
      <DialogHeader>
        <DialogTitle>{t("Add node")}</DialogTitle>
        <DialogDescription>{t("Register a machine that will run Minecraft servers.")}</DialogDescription>
      </DialogHeader>
      <FieldGroup>
        <Field>
          <FieldLabel htmlFor="node-name">{t("Name")}</FieldLabel>
          <Input
            id="node-name"
            placeholder={t("Frankfurt 1")}
            required
            value={form.name}
            onChange={(e) => setForm({ ...form, name: e.target.value })}
          />
        </Field>
        <Field>
          <FieldLabel htmlFor="node-address">{t("Agent address")}</FieldLabel>
          <Input
            id="node-address"
            // i18next-instrument-ignore-next-line: an example of what to enter
            placeholder="203.0.113.10:7443"
            className="font-mono"
            required
            value={form.address}
            onChange={(e) => setForm({ ...form, address: e.target.value })}
          />
          <FieldDescription>
            {t("Host and port the master uses to reach the agent. The agent listens on port 7443.")}
          </FieldDescription>
        </Field>
        {create.error && <FieldError>{create.error.message}</FieldError>}
      </FieldGroup>
      <DialogFooter>
        <DialogClose asChild>
          <Button variant="outline">{t("Cancel")}</Button>
        </DialogClose>
        <Button type="submit" disabled={create.isPending}>
          {create.isPending ? t("Adding…") : t("Add node")}
        </Button>
      </DialogFooter>
    </form>
  )
}
