import { GearIcon } from "@phosphor-icons/react"
import { t } from "i18next"
import { type FormEvent, type ReactNode, useState } from "react"
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
import { Field, FieldDescription, FieldError, FieldGroup, FieldLabel } from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { formatBytes } from "@/lib/format"
import { AgentFields } from "./agent-fields"
import { type Node, useUpdateNode } from "./api"
import { limitsForm, limitsOf } from "./limits"
import { LimitsFields } from "./limits-fields"

function formOf(node: Node) {
  const { logFileMb, logFiles, keepFreeGb, statsIntervalSeconds } = node
  return {
    name: node.name,
    address: node.address ?? "",
    defaultStorage: node.defaultStorage,
    ...limitsForm(node),
    agent: { logFileMb, logFiles, keepFreeGb, statsIntervalSeconds },
  }
}

/** Settings of a node; the trigger is a button unless one is given, e.g. an icon button. */
export function NodeSettingsDialog({ node, trigger }: { node: Node; trigger?: ReactNode }) {
  const [open, setOpen] = useState(false)
  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogTrigger asChild>
        {trigger ?? (
          <Button variant="outline">
            <GearIcon />
            {t("Settings")}
          </Button>
        )}
      </DialogTrigger>
      <DialogContent className="sm:max-w-lg">
        <NodeSettingsForm node={node} onClose={() => setOpen(false)} />
      </DialogContent>
    </Dialog>
  )
}

/** The settings as the node has them each time the dialog opens. */
function NodeSettingsForm({ node, onClose }: { node: Node; onClose: () => void }) {
  const [form, setForm] = useState(() => formOf(node))
  const update = useUpdateNode(node.id)
  const locations = node.info?.storage ?? []
  const memoryMb = node.info?.memoryBytes ? Math.floor(node.info.memoryBytes / 1024 ** 2) : undefined
  const set = (change: Partial<typeof form>) => setForm({ ...form, ...change })

  function submit(event: FormEvent) {
    event.preventDefault()
    update.mutate(
      { name: form.name, address: form.address, defaultStorage: form.defaultStorage, ...limitsOf(form), ...form.agent },
      {
        onSuccess: ({ warning }) => {
          ;(warning ? toast.warning : toast.success)(t("Saved the settings of {{name}}", { name: form.name }), { description: warning })
          onClose()
        },
      },
    )
  }

  return (
    <form onSubmit={submit} className="grid gap-6">
      <DialogHeader>
        <DialogTitle>{t("Settings of {{name}}", { name: node.name })}</DialogTitle>
        <DialogDescription>{t("The limits apply when servers are created or changed.")}</DialogDescription>
      </DialogHeader>
      <FieldGroup>
        <div className="grid gap-4 sm:grid-cols-2">
          <Field>
            <FieldLabel htmlFor="node-settings-name">{t("Name")}</FieldLabel>
            <Input id="node-settings-name" required maxLength={64} value={form.name} onChange={(e) => set({ name: e.target.value })} />
          </Field>
          <Field>
            <FieldLabel htmlFor="node-settings-address">{t("Agent address")}</FieldLabel>
            <Input
              id="node-settings-address"
              required
              className="font-mono"
              value={form.address}
              onChange={(e) => set({ address: e.target.value })}
            />
          </Field>
        </div>
        {locations.length > 0 && (
          <Field>
            <FieldLabel htmlFor="node-settings-storage">{t("Default storage")}</FieldLabel>
            <Select value={form.defaultStorage} onValueChange={(defaultStorage) => set({ defaultStorage })}>
              <SelectTrigger id="node-settings-storage" className="w-full">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {locations.map((l) => (
                  <SelectItem key={l.name} value={l.name}>
                    {l.name}
                    <span className="text-muted-foreground">{t("{{size}} free", { size: formatBytes(l.freeBytes) })}</span>
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
            <FieldDescription>{t("Preselected when you create a server on this node.")}</FieldDescription>
          </Field>
        )}
        <LimitsFields id="node-settings" form={form} onChange={set} memoryMb={memoryMb} />
        <AgentFields id="node-settings" value={form.agent} onChange={(agent) => set({ agent })} />
        {update.error && <FieldError>{update.error.message}</FieldError>}
      </FieldGroup>
      <DialogFooter>
        <DialogClose asChild>
          <Button variant="outline">{t("Cancel")}</Button>
        </DialogClose>
        <Button type="submit" disabled={update.isPending}>
          {update.isPending ? t("Saving…") : t("Save")}
        </Button>
      </DialogFooter>
    </form>
  )
}
