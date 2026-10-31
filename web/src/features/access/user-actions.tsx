import { KeyIcon, PencilSimpleIcon, ShieldSlashIcon, TrashIcon } from "@phosphor-icons/react"
import { t } from "i18next"
import { type FormEvent, useState } from "react"
import { toast } from "sonner"
import { ConfirmDialog } from "@/components/confirm-dialog"
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
import { Field, FieldContent, FieldDescription, FieldError, FieldGroup, FieldLabel, FieldLegend, FieldSet } from "@/components/ui/field"
import { Switch } from "@/components/ui/switch"
import { type Group, type User, useDeleteUser, useNewSetupLink, useResetMfa, useUpdateUser } from "./api"
import { GroupPicker } from "./group-picker"
import { SetupLinkView } from "./setup-link-view"

/**
 * Edits, resets and deletes a user. The own account can't be disabled or deleted, and its
 * two-factor authentication is turned off on the account page.
 */
export function UserActions({ user, groups, self }: { user: User; groups: Group[]; self: boolean }) {
  const remove = useDeleteUser()
  const resetMfa = useResetMfa(user.id)
  return (
    <span className="flex justify-end gap-1">
      <EditUserDialog user={user} groups={groups} self={self} />
      <SetupLinkDialog user={user} />
      {user.mfa && !self && (
        <ConfirmDialog
          trigger={
            <Button
              variant="ghost"
              size="icon-sm"
              aria-label={t("Turn off two-factor authentication for {{name}}", { name: user.username })}
              title={t("Turn off two-factor authentication")}
            >
              <ShieldSlashIcon />
            </Button>
          }
          title={t("Turn off two-factor authentication for {{name}}?", { name: user.username })}
          description={t(
            "Only do this if {{name}} lost the authenticator app and the recovery codes, and you are sure you are talking to them. Then their password alone signs them in until they set it up again.",
            { name: user.username },
          )}
          action={t("Turn off")}
          destructive
          onConfirm={() =>
            resetMfa.mutate(undefined, {
              onSuccess: () => toast.success(t("Turned off two-factor authentication for {{name}}", { name: user.username })),
              onError: (e) => toast.error(e.message),
            })
          }
        />
      )}
      {!self && (
        <ConfirmDialog
          trigger={
            <Button variant="ghost" size="icon-sm" aria-label={t("Delete {{name}}", { name: user.username })} title={t("Delete")}>
              <TrashIcon />
            </Button>
          }
          title={t("Delete {{name}}?", { name: user.username })}
          description={t("The user is signed out and can't sign in anymore. Disable the user instead to keep the account.")}
          action={t("Delete user")}
          destructive
          onConfirm={() =>
            remove.mutate(user.id, {
              onSuccess: () => toast.success(t("Deleted {{name}}", { name: user.username })),
              onError: (e) => toast.error(e.message),
            })
          }
        />
      )}
    </span>
  )
}

function EditUserDialog({ user, groups, self }: { user: User; groups: Group[]; self: boolean }) {
  const [open, setOpen] = useState(false)
  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogTrigger asChild>
        <Button variant="ghost" size="icon-sm" aria-label={t("Edit {{name}}", { name: user.username })} title={t("Edit")}>
          <PencilSimpleIcon />
        </Button>
      </DialogTrigger>
      <DialogContent className="sm:max-w-lg">
        <EditUserForm user={user} groups={groups} self={self} onClose={() => setOpen(false)} />
      </DialogContent>
    </Dialog>
  )
}

/** The groups of the user and whether it is disabled, as it has them each time the dialog opens. */
function EditUserForm({ user, groups, self, onClose }: { user: User; groups: Group[]; self: boolean; onClose: () => void }) {
  const [form, setForm] = useState({ disabled: user.disabled, groups: user.groups })
  const update = useUpdateUser(user.id)

  function submit(event: FormEvent) {
    event.preventDefault()
    update.mutate(form, {
      onSuccess: () => {
        toast.success(t("Saved {{name}}", { name: user.username }))
        onClose()
      },
    })
  }

  return (
    <form onSubmit={submit} className="grid gap-6">
      <DialogHeader>
        <DialogTitle>{user.username}</DialogTitle>
        <DialogDescription>{t("Changes apply right away, even to sessions in progress.")}</DialogDescription>
      </DialogHeader>
      <FieldGroup>
        <FieldSet>
          <FieldLegend variant="label">{t("Groups")}</FieldLegend>
          <GroupPicker groups={groups} value={form.groups} onChange={(groups) => setForm({ ...form, groups })} />
        </FieldSet>
        {!self && (
          <Field orientation="horizontal">
            <Switch id="user-disabled" checked={form.disabled} onCheckedChange={(disabled) => setForm({ ...form, disabled })} />
            <FieldContent>
              <FieldLabel htmlFor="user-disabled">{t("Disabled")}</FieldLabel>
              <FieldDescription>{t("The user is signed out and can't sign in until enabled again.")}</FieldDescription>
            </FieldContent>
          </Field>
        )}
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

/** Creates a new setup link, e.g. for a user who forgot the password. */
function SetupLinkDialog({ user }: { user: User }) {
  const create = useNewSetupLink(user.id)
  const [open, setOpen] = useState(false)
  return (
    <>
      <Button
        variant="ghost"
        size="icon-sm"
        aria-label={t("New setup link for {{name}}", { name: user.username })}
        title={user.passwordSet ? t("Reset password") : t("New setup link")}
        disabled={create.isPending || user.disabled}
        onClick={() => create.mutate(undefined, { onSuccess: () => setOpen(true), onError: (e) => toast.error(e.message) })}
      >
        <KeyIcon />
      </Button>
      <Dialog open={open} onOpenChange={setOpen}>
        <DialogContent className="sm:max-w-lg">
          <DialogHeader>
            <DialogTitle>{t("Setup link for {{name}}", { name: user.username })}</DialogTitle>
            <DialogDescription>
              {user.passwordSet
                ? t("The current password works until the user sets a new one. Earlier setup links no longer work.")
                : t("Earlier setup links no longer work.")}
            </DialogDescription>
          </DialogHeader>
          {create.data && <SetupLinkView username={user.username} link={create.data} />}
          <DialogFooter>
            <DialogClose asChild>
              <Button>{t("Done")}</Button>
            </DialogClose>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  )
}
