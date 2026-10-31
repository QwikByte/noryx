import { CopyIcon, DownloadSimpleIcon } from "@phosphor-icons/react"
import type { UseMutationResult } from "@tanstack/react-query"
import { t } from "i18next"
import { type FormEvent, type ReactElement, type ReactNode, useState } from "react"
import { toast } from "sonner"
import { ErrorCallout } from "@/components/callout"
import { CopyField } from "@/components/copy-field"
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
import { Skeleton } from "@/components/ui/skeleton"
import { useSetUpMfa } from "./api"
import { QrCode } from "./qr-code"

type Codes = { recoveryCodes: string[] }

interface Setup {
  username: string
  enable: UseMutationResult<Codes, Error, { password: string; code: string }>
  onEnabled: (codes: string[]) => void
}

/** Sets up an authenticator app with a QR code; a code of the app and the password turn it on. */
export function MfaSetupDialog({ trigger = <Button>{t("Set up")}</Button>, ...props }: Setup & { trigger?: ReactElement }) {
  const [open, setOpen] = useState(false)
  const setup = useSetUpMfa()

  // Every setup gets a new secret.
  function onOpenChange(next: boolean) {
    setOpen(next)
    props.enable.reset()
    if (next) setup.mutate()
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogTrigger asChild>{trigger}</DialogTrigger>
      <DialogContent className="sm:max-w-lg">
        <MfaSetupForm {...props} setup={setup} onClose={() => onOpenChange(false)} />
      </DialogContent>
    </Dialog>
  )
}

/** The code and password, entered anew each time the dialog opens, under the QR code of the setup. */
function MfaSetupForm({
  username,
  enable,
  onEnabled,
  setup,
  onClose,
}: Setup & { setup: ReturnType<typeof useSetUpMfa>; onClose: () => void }) {
  const [form, setForm] = useState({ code: "", password: "" })

  function submit(event: FormEvent) {
    event.preventDefault()
    enable.mutate(form, {
      onSuccess: ({ recoveryCodes }) => {
        onClose()
        onEnabled(recoveryCodes)
      },
    })
  }

  return (
    <form onSubmit={submit} className="grid gap-6">
      <DialogHeader>
        <DialogTitle>{t("Set up two-factor authentication")}</DialogTitle>
        <DialogDescription>
          {t(
            "Use an authenticator app on your phone, such as Google Authenticator, Microsoft Authenticator, Aegis or your password manager.",
          )}
        </DialogDescription>
      </DialogHeader>
      <ol className="grid gap-6">
        <Step n={1} title={t("Scan this QR code with the app")}>
          {setup.error ? (
            <ErrorCallout error={setup.error} retry={false} />
          ) : setup.data ? (
            <div className="flex flex-col items-center gap-4 sm:flex-row sm:items-start">
              <QrCode value={setup.data.uri} label={t("QR code for your authenticator app")} className="size-40 shrink-0" />
              <div className="w-full min-w-0 space-y-2 text-muted-foreground">
                <p>{t("Can't scan it? Enter this key in the app instead.")}</p>
                <CopyField label={t("Key")} value={setup.data.secret} />
              </div>
            </div>
          ) : (
            <Skeleton className="h-40 rounded-xl" />
          )}
        </Step>
        <Step n={2} title={t("Confirm with a code from the app and your password")}>
          <FieldGroup className="sm:grid sm:grid-cols-2 sm:gap-4">
            <input type="text" name="username" autoComplete="username" value={username} readOnly hidden />
            <Field>
              <FieldLabel htmlFor="mfa-code">{t("Code")}</FieldLabel>
              <Input
                id="mfa-code"
                autoComplete="one-time-code"
                inputMode="numeric"
                maxLength={6}
                placeholder="000000"
                className="font-mono tracking-[0.3em]"
                value={form.code}
                onChange={(e) => setForm({ ...form, code: e.target.value.replace(/\D/g, "") })}
              />
            </Field>
            <Field>
              <FieldLabel htmlFor="mfa-password">{t("Password")}</FieldLabel>
              <Input
                id="mfa-password"
                type="password"
                autoComplete="current-password"
                value={form.password}
                onChange={(e) => setForm({ ...form, password: e.target.value })}
              />
            </Field>
          </FieldGroup>
          <p className="text-muted-foreground">{t("Your other sessions end, and your API tokens stop working.")}</p>
        </Step>
      </ol>
      {enable.error && <FieldError>{enable.error.message}</FieldError>}
      <DialogFooter>
        <DialogClose asChild>
          <Button variant="outline">{t("Cancel")}</Button>
        </DialogClose>
        <Button type="submit" disabled={!setup.data || enable.isPending || form.code.length !== 6 || !form.password}>
          {enable.isPending ? t("Turning on…") : t("Turn on")}
        </Button>
      </DialogFooter>
    </form>
  )
}

function Step({ n, title, children }: { n: number; title: string; children: ReactNode }) {
  return (
    <li className="grid gap-3">
      <p className="flex items-center gap-2.5 font-medium">
        <span aria-hidden className="grid size-6 place-items-center rounded-full bg-primary/10 text-xs font-semibold text-primary">
          {n}
        </span>
        {title}
      </p>
      {children}
    </li>
  )
}

interface Confirmation<R> {
  title: string
  description: string
  action: string
  destructive?: boolean
  change: UseMutationResult<R, Error, string>
  onSuccess: (result: R) => void
}

/** Asks for the password before a change of two-factor authentication. */
export function ConfirmPasswordDialog<R>({ trigger, ...confirmation }: Confirmation<R> & { trigger: ReactElement }) {
  const [open, setOpen] = useState(false)

  function onOpenChange(next: boolean) {
    setOpen(next)
    confirmation.change.reset()
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogTrigger asChild>{trigger}</DialogTrigger>
      <DialogContent className="sm:max-w-md">
        <ConfirmPasswordForm {...confirmation} onClose={() => onOpenChange(false)} />
      </DialogContent>
    </Dialog>
  )
}

/** The password, entered anew each time the dialog opens. */
function ConfirmPasswordForm<R>({
  title,
  description,
  action,
  destructive = false,
  change,
  onSuccess,
  onClose,
}: Confirmation<R> & { onClose: () => void }) {
  const [password, setPassword] = useState("")

  function submit(event: FormEvent) {
    event.preventDefault()
    change.mutate(password, {
      onSuccess: (result) => {
        onClose()
        onSuccess(result)
      },
    })
  }

  return (
    <form onSubmit={submit} className="grid gap-6">
      <DialogHeader>
        <DialogTitle>{title}</DialogTitle>
        <DialogDescription>{description}</DialogDescription>
      </DialogHeader>
      <FieldGroup>
        <Field>
          <FieldLabel htmlFor="confirm-password">{t("Password")}</FieldLabel>
          <Input
            id="confirm-password"
            type="password"
            autoComplete="current-password"
            autoFocus
            value={password}
            onChange={(e) => setPassword(e.target.value)}
          />
        </Field>
        {change.error && <FieldError>{change.error.message}</FieldError>}
      </FieldGroup>
      <DialogFooter>
        <DialogClose asChild>
          <Button variant="outline">{t("Cancel")}</Button>
        </DialogClose>
        <Button type="submit" variant={destructive ? "destructive" : "default"} disabled={change.isPending || !password}>
          {action}
        </Button>
      </DialogFooter>
    </form>
  )
}

/** New recovery codes, which are shown only once. It stays open until the user confirms. */
export function RecoveryCodesDialog({ codes, onClose }: { codes?: string[]; onClose: () => void }) {
  const text = codes?.join("\n") ?? ""

  async function copy() {
    await navigator.clipboard.writeText(text)
    toast.success(t("Copied the recovery codes"))
  }

  function download() {
    const url = URL.createObjectURL(new Blob([`${text}\n`], { type: "text/plain" }))
    Object.assign(document.createElement("a"), { href: url, download: "noryx-recovery-codes.txt" }).click()
    setTimeout(() => URL.revokeObjectURL(url))
  }

  return (
    <Dialog open={codes !== undefined} onOpenChange={(open) => !open && onClose()}>
      <DialogContent className="sm:max-w-md" showCloseButton={false} onInteractOutside={(e) => e.preventDefault()}>
        <DialogHeader>
          <DialogTitle>{t("Save your recovery codes")}</DialogTitle>
          <DialogDescription>
            {t(
              "If you lose your phone, each code signs you in once in place of an app code. They won't be shown again, so keep them somewhere safe, such as in your password manager.",
            )}
          </DialogDescription>
        </DialogHeader>
        <ol className="grid grid-cols-2 gap-x-6 gap-y-2 rounded-xl bg-console px-5 py-4 text-center font-mono text-sm text-console-foreground">
          {codes?.map((code) => (
            <li key={code}>{code}</li>
          ))}
        </ol>
        <div className="flex gap-2">
          <Button variant="outline" size="sm" onClick={copy}>
            <CopyIcon /> {t("Copy")}
          </Button>
          <Button variant="outline" size="sm" onClick={download}>
            <DownloadSimpleIcon /> {t("Download")}
          </Button>
        </div>
        <DialogFooter>
          <DialogClose asChild>
            <Button>{t("I saved them")}</Button>
          </DialogClose>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
