import { WarningIcon } from "@phosphor-icons/react"
import { useQuery } from "@tanstack/react-query"
import { t } from "i18next"
import { type FormEvent, type ReactElement, useState } from "react"
import { Callout } from "@/components/callout"
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
import { Field, FieldContent, FieldDescription, FieldGroup, FieldLabel } from "@/components/ui/field"
import { Switch } from "@/components/ui/switch"
import { useAccess } from "@/features/access/use-access"
import { defaultSelection, nothingSelected, pathsError } from "@/features/backups/api"
import { LocationField, SelectionField } from "@/features/backups/backup-fields"
import { nodeQuery } from "@/features/nodes/api"
import type { Server } from "@/features/servers/api"
import type { ModpackBackup, ModpackVersion } from "./api"

interface Update {
  nodeId: string
  server: Server
  target: ModpackVersion
  /** The target is older than the version the server has. */
  older: boolean
  action: string
  onUpdate: (target: ModpackVersion, backup: ModpackBackup) => void
}

/** Asks before moving a server to another version of its modpack, and what to back up first and where, or to confirm no backup. */
export function UpdateModpackDialog({
  trigger,
  target,
  ...update
}: Omit<Update, "target"> & { target?: ModpackVersion; trigger: ReactElement }) {
  const [open, setOpen] = useState(false)
  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogTrigger asChild>{trigger}</DialogTrigger>
      <DialogContent className="sm:max-w-2xl">
        {target && <UpdateForm {...update} target={target} onClose={() => setOpen(false)} />}
      </DialogContent>
    </Dialog>
  )
}

/** The form, created anew each time the dialog opens, with the backup as by default. */
function UpdateForm({ nodeId, server, target, older, action, onUpdate, onClose }: Update & { onClose: () => void }) {
  const { can } = useAccess()
  // Choosing what to back up, and where, needs the permission to back up; others get the backup the master makes by default.
  const choose = can("backups.create", nodeId, server.id)
  const { data: node } = useQuery({ ...nodeQuery(nodeId), enabled: choose })
  const [backUp, setBackUp] = useState(true)
  const [selection, setSelection] = useState(defaultSelection)
  const [location, setLocation] = useState("")
  const [confirmed, setConfirmed] = useState(false)
  const invalid = backUp
    ? choose && (nothingSelected(selection) || !!pathsError(selection.paths) || !!pathsError(selection.exclude ?? []))
    : !confirmed

  function submit(event: FormEvent) {
    event.preventDefault()
    onClose()
    onUpdate(target, !backUp ? { noBackup: true } : choose ? { backup: { selection, location } } : {})
  }

  return (
    <form onSubmit={submit} className="grid gap-6">
      <DialogHeader>
        <DialogTitle>{t("Move {{name}} to version {{number}}?", { name: server.name, number: target.number })}</DialogTitle>
        <DialogDescription>
          {[
            server.state === "stopped"
              ? t("Its mods and files change, and its Minecraft and loader version if the modpack's do.")
              : t(
                  "The server stops, its mods and files change, and its Minecraft and loader version if the modpack's do. Then it starts again.",
                ),
            t("Files that changed on the server since the modpack wrote them stay as they are, and are listed afterwards."),
          ].join(" ")}
        </DialogDescription>
      </DialogHeader>
      {older && (
        <Callout tone="warning" icon={WarningIcon}>
          {t("Worlds can't be opened by older Minecraft versions; back them up to keep them as they are.")}
        </Callout>
      )}
      <FieldGroup>
        <Field orientation="horizontal">
          <Switch id="modpack-backup" checked={backUp} onCheckedChange={setBackUp} />
          <FieldContent>
            <FieldLabel htmlFor="modpack-backup">{t("Back up first")}</FieldLabel>
            <FieldDescription>
              {choose
                ? t(
                    "A backup made by hand, which brings the server back as it was if the new version doesn't work out. It always has the folders with the modpack's files.",
                  )
                : t(
                    "A backup made by hand of the worlds, mods and configuration and the folders with the modpack's files, in the default storage location.",
                  )}
            </FieldDescription>
          </FieldContent>
        </Field>
        {backUp && choose && (
          <>
            <SelectionField value={selection} server={{ nodeId, serverId: server.id }} onChange={setSelection} />
            <LocationField locations={node?.info?.storage.map((l) => l.name) ?? []} value={location} onChange={setLocation} />
          </>
        )}
        {!backUp && (
          <Field orientation="horizontal">
            <Checkbox id="modpack-no-backup" checked={confirmed} onCheckedChange={(on) => setConfirmed(on === true)} />
            <FieldContent>
              <FieldLabel htmlFor="modpack-no-backup">{t("Update without a backup")}</FieldLabel>
              <FieldDescription>
                {t(
                  "Nothing brings the server back as it was if the new version doesn't work out, unless another backup has it, e.g. one of a backup job.",
                )}
              </FieldDescription>
            </FieldContent>
          </Field>
        )}
      </FieldGroup>
      <DialogFooter>
        <DialogClose asChild>
          <Button variant="outline">{t("Cancel")}</Button>
        </DialogClose>
        <Button type="submit" variant={backUp ? "default" : "destructive"} disabled={invalid}>
          {action}
        </Button>
      </DialogFooter>
    </form>
  )
}
