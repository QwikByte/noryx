import { CodeIcon, PlusIcon } from "@phosphor-icons/react"
import { useQuery } from "@tanstack/react-query"
import { t } from "i18next"
import { type FormEvent, useState } from "react"
import { Trans } from "react-i18next"
import { toast } from "sonner"
import { ErrorCallout } from "@/components/callout"
import { ConfirmDialog } from "@/components/confirm-dialog"
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
import { Field, FieldContent, FieldDescription, FieldError, FieldGroup, FieldLabel, FieldTitle } from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import { RadioGroup, RadioGroupItem } from "@/components/ui/radio-group"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { Skeleton } from "@/components/ui/skeleton"
import { accessQuery, catalogQuery } from "@/features/access/api"
import type { Permission } from "@/features/access/permissions"
import { PermissionsField } from "@/features/access/permissions-field"
import { accessOf } from "@/features/access/use-access"
import { formatAgo, formatDate, formatHours } from "@/lib/format"
import { msg } from "@/lib/i18n"
import { useNow } from "@/lib/use-now"
import { AccountRow } from "./account-row"
import { type ApiToken, limitsQuery, tokensQuery, type User, useCreateToken, useRevokeToken } from "./api"

/** How long new tokens are valid, in days; 0 for no expiry. */
const expiries = [
  { days: "30", label: msg("30 days") },
  { days: "90", label: msg("90 days") },
  { days: "365", label: msg("1 year") },
  { days: "0", label: msg("No expiry") },
]

/** The expiries within the longest lifetime the settings allow, which is one of them, or all of them without a limit. */
function allowedExpiries(maxDays: number) {
  const allowed = expiries
    .filter((e) => !maxDays || (e.days !== "0" && Number(e.days) <= maxDays))
    .map((e) => ({ days: e.days, label: t(e.label) }))
  if (maxDays && !allowed.some((e) => Number(e.days) === maxDays)) allowed.push({ days: String(maxDays), label: formatHours(maxDays * 24) })
  return allowed
}

const linkClass = "font-medium text-primary underline-offset-4 hover:underline"

/** The user's API tokens for scripts, with where they were used last, and ways to create and revoke them. */
export function ApiTokens({ user }: { user: User }) {
  const { data, isPending, error } = useQuery(tokensQuery)
  const { data: catalog } = useQuery(catalogQuery)
  const now = useNow(true, 60_000)
  if (isPending) return <Skeleton className="h-20 rounded-xl" />
  if (error) return <ErrorCallout error={error} />
  const labels = new Map(catalog?.flatMap((area) => area.permissions).map((p) => [p.id, p.label]))
  return (
    <>
      <p className="text-sm text-muted-foreground">
        <Trans
          i18nKey="Scripts use the API with a token instead of your password, in the header <code/>. The <link>description of the API</link> lists every route with the permissions it needs."
          components={{
            code: <code className="font-mono text-xs break-all">Authorization: Bearer noryx_…</code>,
            // oxlint-disable-next-line jsx-a11y/anchor-has-content, jsx-a11y/control-has-associated-label -- <Trans> puts the text in
            link: <a href="/api/openapi.json" target="_blank" rel="noreferrer" className={linkClass} />,
          }}
        />
      </p>
      {data.map((token) => (
        <TokenRow key={token.id} token={token} labels={labels} now={now} />
      ))}
      <CreateTokenDialog user={user} />
    </>
  )
}

/** A token with what it may do, when it was used last and when it expires. */
function TokenRow({ token, labels, now }: { token: ApiToken; labels: Map<Permission, string>; now: number }) {
  const revoke = useRevokeToken()
  const expired = !!token.expiresAt && Date.parse(token.expiresAt) <= now
  const used = token.lastUsedAt
    ? t("Last used {{time}} from {{ip}}", { time: formatAgo(token.lastUsedAt, now), ip: token.ip })
    : t("Never used")
  const date = token.expiresAt && formatDate(token.expiresAt)
  const expiry = !date ? t("Doesn't expire") : expired ? t("Expired {{date}}", { date }) : t("Expires {{date}}", { date })
  return (
    <AccountRow
      icon={CodeIcon}
      tone={expired ? "neutral" : "info"}
      title={token.name}
      status={expired ? { tone: "warning", label: msg("Expired") } : undefined}
      actions={
        <ConfirmDialog
          trigger={
            <Button variant="outline" disabled={revoke.isPending}>
              {t("Revoke")}
            </Button>
          }
          title={t("Revoke {{name}}?", { name: token.name })}
          description={t("Scripts that use this token can't use the API anymore.")}
          action={t("Revoke")}
          destructive
          onConfirm={() =>
            revoke.mutate(token.id, {
              onSuccess: () => toast.success(t("Revoked {{name}}", { name: token.name })),
              onError: (e) => toast.error(e.message),
            })
          }
        />
      }
    >
      <p>{token.permissions ? token.permissions.map((p) => labels.get(p) ?? p).join(", ") : t("All your permissions")}</p>
      <p>{[t("Created {{date}}", { date: formatDate(token.createdAt) }), used, expiry].join(" · ")}</p>
    </AccountRow>
  )
}

const emptyForm = { name: "", days: "90", all: true, permissions: [] as Permission[], password: "", code: "" }

/** Creates a token with the password, and a code with two-factor authentication, and then shows it once. */
function CreateTokenDialog({ user }: { user: User }) {
  const [open, setOpen] = useState(false)
  const [secret, setSecret] = useState<string>()
  return (
    <>
      <Dialog open={open} onOpenChange={setOpen}>
        <DialogTrigger asChild>
          <Button variant="outline" className="self-start">
            <PlusIcon /> {t("Create token")}
          </Button>
        </DialogTrigger>
        <DialogContent className="sm:max-w-2xl">
          <CreateTokenForm
            user={user}
            onCreated={(secret) => {
              setOpen(false)
              setSecret(secret)
            }}
          />
        </DialogContent>
      </Dialog>
      <NewTokenDialog secret={secret} onClose={() => setSecret(undefined)} />
    </>
  )
}

/** The token to create, entered anew each time the dialog opens. */
function CreateTokenForm({ user, onCreated }: { user: User; onCreated: (secret: string) => void }) {
  const [form, setForm] = useState(emptyForm)
  const create = useCreateToken()
  const { data: catalog } = useQuery(catalogQuery)
  const { data: grants } = useQuery(accessQuery)
  const set = (change: Partial<typeof emptyForm>) => setForm({ ...form, ...change })
  // An expiry the settings don't allow, e.g. the default of 90 days, gives way to the longest they do.
  const allowed = allowedExpiries(useQuery(limitsQuery).data?.maxTokenDays ?? 0)
  const days = allowed.some((e) => e.days === form.days) ? form.days : allowed[allowed.length - 1].days
  // Only the permissions the user has somewhere are offered; a token never gets more anyway.
  const access = accessOf(grants)
  const own = catalog
    ?.map((area) => ({ ...area, permissions: area.permissions.filter((p) => access.canSomewhere(p.id)) }))
    .filter((area) => area.permissions.length > 0)

  function submit(event: FormEvent) {
    event.preventDefault()
    create.mutate(
      {
        name: form.name,
        expiresAt: Number(days) ? new Date(Date.now() + Number(days) * 86_400_000).toISOString() : undefined,
        permissions: form.all ? undefined : form.permissions,
        password: form.password,
        code: user.mfa ? form.code : undefined,
      },
      {
        onSuccess: (token) => onCreated(token.secret),
      },
    )
  }

  return (
    <form onSubmit={submit} className="grid gap-6">
      <DialogHeader>
        <DialogTitle>{t("Create an API token")}</DialogTitle>
        <DialogDescription>
          {t("Scripts act as you with it, with your permissions or fewer. When yours change, so do the token's.")}
        </DialogDescription>
      </DialogHeader>
      <FieldGroup>
        <div className="grid gap-4 sm:grid-cols-[1fr_auto]">
          <Field>
            <FieldLabel htmlFor="token-name">{t("Name")}</FieldLabel>
            <Input
              id="token-name"
              required
              maxLength={64}
              placeholder={t("e.g. Backup script")}
              value={form.name}
              onChange={(e) => set({ name: e.target.value })}
            />
          </Field>
          <Field>
            <FieldLabel htmlFor="token-expiry">{t("Expires after")}</FieldLabel>
            <Select value={days} onValueChange={(days) => set({ days })}>
              <SelectTrigger id="token-expiry" className="w-full sm:w-40">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {allowed.map((e) => (
                  <SelectItem key={e.days} value={e.days}>
                    {e.label}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </Field>
        </div>
        <RadioGroup
          value={form.all ? "all" : "some"}
          onValueChange={(v) => set({ all: v === "all" })}
          aria-label={t("Permissions")}
          className="gap-2 sm:grid-cols-2"
        >
          <PermissionsChoice value="all" title={t("All your permissions")} description={t("Also those you get later.")} />
          <PermissionsChoice value="some" title={t("Only some")} description={t("Those you choose, where you have them.")} />
        </RadioGroup>
        {!form.all &&
          (own ? (
            <PermissionsField
              catalog={own}
              value={form.permissions}
              onChange={(permissions) => set({ permissions })}
              scoped={false}
            />
          ) : (
            <Skeleton className="h-64 rounded-xl" />
          ))}
        <div className="grid gap-4 sm:grid-cols-2">
          <input type="text" name="username" autoComplete="username" value={user.username} readOnly hidden />
          <Field>
            <FieldLabel htmlFor="token-password">{t("Password")}</FieldLabel>
            <Input
              id="token-password"
              type="password"
              autoComplete="current-password"
              required
              value={form.password}
              onChange={(e) => set({ password: e.target.value })}
            />
          </Field>
          {user.mfa && (
            <Field>
              <FieldLabel htmlFor="token-code">{t("Code")}</FieldLabel>
              <Input
                id="token-code"
                autoComplete="one-time-code"
                inputMode="numeric"
                maxLength={6}
                placeholder="000000"
                className="font-mono tracking-[0.3em]"
                value={form.code}
                onChange={(e) => set({ code: e.target.value.replace(/\D/g, "") })}
              />
            </Field>
          )}
        </div>
        {create.error && <FieldError>{create.error.message}</FieldError>}
      </FieldGroup>
      <DialogFooter>
        <DialogClose asChild>
          <Button variant="outline">{t("Cancel")}</Button>
        </DialogClose>
        <Button
          type="submit"
          disabled={
            create.isPending ||
            !form.name.trim() ||
            !form.password ||
            (!form.all && form.permissions.length === 0) ||
            (user.mfa && form.code.length !== 6)
          }
        >
          {create.isPending ? t("Creating…") : t("Create token")}
        </Button>
      </DialogFooter>
    </form>
  )
}

function PermissionsChoice({ value, title, description }: { value: string; title: string; description: string }) {
  return (
    <FieldLabel htmlFor={`token-${value}`}>
      <Field orientation="horizontal">
        <FieldContent>
          <FieldTitle>{title}</FieldTitle>
          <FieldDescription>{description}</FieldDescription>
        </FieldContent>
        <RadioGroupItem id={`token-${value}`} value={value} />
      </Field>
    </FieldLabel>
  )
}

/** A new token, which is shown only once. It stays open until the user confirms. */
function NewTokenDialog({ secret = "", onClose }: { secret?: string; onClose: () => void }) {
  return (
    <Dialog open={secret !== ""} onOpenChange={(open) => !open && onClose()}>
      <DialogContent className="sm:max-w-lg" showCloseButton={false} onInteractOutside={(e) => e.preventDefault()}>
        <DialogHeader>
          <DialogTitle>{t("Copy your API token")}</DialogTitle>
          <DialogDescription>
            {t("It won't be shown again, so keep it somewhere safe, such as in your password manager or the secrets of your script.")}
          </DialogDescription>
        </DialogHeader>
        <CopyField label={t("API token")} value={secret} />
        <div className="grid gap-2">
          <p className="text-sm text-muted-foreground">{t("For example, this lists your servers:")}</p>
          <CopyField label={t("Command")} prefix="$" value={`curl -H "Authorization: Bearer ${secret}" ${location.origin}/api/servers`} />
        </div>
        <DialogFooter>
          <DialogClose asChild>
            <Button>{t("I copied it")}</Button>
          </DialogClose>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
