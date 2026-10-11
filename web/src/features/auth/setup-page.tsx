import { useQuery } from "@tanstack/react-query"
import { Link, useNavigate } from "@tanstack/react-router"
import { t } from "i18next"
import { type FormEvent, useEffect, useState } from "react"
import { Trans } from "react-i18next"
import { toast } from "sonner"
import { Button } from "@/components/ui/button"
import { Field, FieldDescription, FieldError, FieldGroup, FieldLabel } from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import { Skeleton } from "@/components/ui/skeleton"
import { publicSettingsQuery } from "@/features/settings/public"
import { formatHours } from "@/lib/format"
import { setupUserQuery, useSetup } from "./api"
import { AuthLayout } from "./auth-layout"

/** Where invited users, and users whose password was reset, set their password. */
export function SetupPage() {
  const [token] = useState(() => location.hash.slice(1))
  // The token leaves the address bar and the history once it was read.
  useEffect(() => history.replaceState(null, "", location.pathname), [])
  const { data: user, isPending, error } = useQuery({ ...setupUserQuery(token), enabled: token !== "" })
  const hours = useQuery(publicSettingsQuery).data?.setupLinkHours

  if (!token || error) {
    return (
      <AuthLayout
        title={t("This link doesn't work")}
        description={hours && t("Setup links work once and expire after {{time}}.", { time: formatHours(hours) })}
      >
        <p className="text-sm text-muted-foreground">
          {error?.message ?? t("The link is incomplete.")}{" "}
          <Trans
            i18nKey="Ask an administrator for a new setup link, or <link>sign in</link> if you have a password already."
            components={{
              link: <Link to="/login" search={{}} className="font-medium text-foreground underline-offset-4 hover:underline" />,
            }}
          />
        </p>
      </AuthLayout>
    )
  }
  return (
    <AuthLayout title={user ? t("Welcome, {{name}}", { name: user.username }) : t("Welcome")}>
      {isPending ? <Skeleton className="h-52 rounded-xl" /> : <PasswordForm token={token} username={user.username} />}
    </AuthLayout>
  )
}

function PasswordForm({ token, username }: { token: string; username: string }) {
  const [form, setForm] = useState({ password: "", repeat: "" })
  const setup = useSetup()
  const navigate = useNavigate()
  const mismatch = form.repeat !== "" && form.repeat !== form.password
  const length = useQuery(publicSettingsQuery).data?.minPasswordLength

  function submit(event: FormEvent) {
    event.preventDefault()
    if (mismatch) return
    setup.mutate(
      { token, password: form.password },
      {
        onSuccess: (result) => {
          if (!("mfaRequired" in result)) return navigate({ to: "/" })
          toast.success(t("Saved your password"), { description: t("Sign in with it and a code from your authenticator app.") })
          return navigate({ to: "/login", search: {} })
        },
      },
    )
  }

  return (
    <form onSubmit={submit}>
      <FieldGroup>
        {/* Lets password managers save the new password for the right account. */}
        <input type="text" name="username" autoComplete="username" value={username} readOnly hidden />
        <Field>
          <FieldLabel htmlFor="setup-password">{t("Password")}</FieldLabel>
          <Input
            id="setup-password"
            type="password"
            autoComplete="new-password"
            autoFocus
            required
            minLength={length}
            value={form.password}
            onChange={(e) => setForm({ ...form, password: e.target.value })}
          />
          {length && <FieldDescription>{t("At least {{length}} characters.", { length })}</FieldDescription>}
        </Field>
        <Field data-invalid={mismatch}>
          <FieldLabel htmlFor="setup-repeat">{t("Repeat password")}</FieldLabel>
          <Input
            id="setup-repeat"
            type="password"
            autoComplete="new-password"
            required
            aria-invalid={mismatch}
            value={form.repeat}
            onChange={(e) => setForm({ ...form, repeat: e.target.value })}
          />
          {mismatch && <FieldError>{t("The passwords don't match.")}</FieldError>}
        </Field>
        {setup.error && <FieldError>{setup.error.message}</FieldError>}
        <Button type="submit" size="lg" className="w-full" disabled={setup.isPending || mismatch}>
          {setup.isPending ? t("Saving…") : t("Set password and sign in")}
        </Button>
      </FieldGroup>
    </form>
  )
}
