import { UserPlusIcon } from "@phosphor-icons/react"
import { useQuery } from "@tanstack/react-query"
import { t } from "i18next"
import { type FormEvent, useState } from "react"
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
import { Field, FieldDescription, FieldError, FieldGroup, FieldLabel, FieldLegend, FieldSet } from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import { type Group, inviteGroupsQuery, useInviteUser } from "./api"
import { GroupPicker } from "./group-picker"
import { SetupLinkView } from "./setup-link-view"

// The groups stay those that the settings preselect until the user chooses others.
const empty = { username: "", groups: undefined as string[] | undefined }

/** Adds a user, who sets a password with the setup link shown afterwards. */
export function InviteDialog({ groups }: { groups: Group[] }) {
  const [open, setOpen] = useState(false)
  const [form, setForm] = useState(empty)
  const invite = useInviteUser()
  const preselected = useQuery(inviteGroupsQuery).data?.groups
  const chosen = form.groups ?? preselected ?? []

  function onOpenChange(next: boolean) {
    setOpen(next)
    if (!next) {
      invite.reset()
      setForm(empty)
    }
  }

  function submit(event: FormEvent) {
    event.preventDefault()
    invite.mutate({ ...form, groups: chosen })
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogTrigger asChild>
        <Button>
          <UserPlusIcon />
          {t("Invite user")}
        </Button>
      </DialogTrigger>
      <DialogContent className="sm:max-w-lg">
        {invite.data ? (
          <>
            <DialogHeader>
              <DialogTitle>{t("Invited {{name}}", { name: invite.data.user.username })}</DialogTitle>
              <DialogDescription>{t("The user can sign in after setting a password.")}</DialogDescription>
            </DialogHeader>
            <SetupLinkView username={invite.data.user.username} link={invite.data.setupLink} />
            <DialogFooter>
              <DialogClose asChild>
                <Button>{t("Done")}</Button>
              </DialogClose>
            </DialogFooter>
          </>
        ) : (
          <form onSubmit={submit} className="grid gap-6">
            <DialogHeader>
              <DialogTitle>{t("Invite user")}</DialogTitle>
              <DialogDescription>{t("You get a link the user can use to set their password. You never see the password.")}</DialogDescription>
            </DialogHeader>
            <FieldGroup>
              <Field>
                <FieldLabel htmlFor="invite-username">{t("Username")}</FieldLabel>
                <Input
                  id="invite-username"
                  required
                  autoComplete="off"
                  pattern="[A-Za-z0-9_.\-]{3,32}"
                  value={form.username}
                  onChange={(e) => setForm({ ...form, username: e.target.value })}
                />
                <FieldDescription>{t("3 to 32 letters, digits, '_', '.' or '-'.")}</FieldDescription>
              </Field>
              <FieldSet>
                <FieldLegend variant="label">{t("Groups")}</FieldLegend>
                <FieldDescription>
                  {t("The user gets the permissions of these groups. You can only choose groups within your own permissions.")}
                </FieldDescription>
                <GroupPicker groups={groups} value={chosen} onChange={(groups) => setForm({ ...form, groups })} />
              </FieldSet>
              {invite.error && <FieldError>{invite.error.message}</FieldError>}
            </FieldGroup>
            <DialogFooter>
              <DialogClose asChild>
                <Button variant="outline">{t("Cancel")}</Button>
              </DialogClose>
              <Button type="submit" disabled={invite.isPending}>
                {invite.isPending ? t("Inviting…") : t("Invite")}
              </Button>
            </DialogFooter>
          </form>
        )}
      </DialogContent>
    </Dialog>
  )
}
