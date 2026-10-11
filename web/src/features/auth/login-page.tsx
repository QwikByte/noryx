import { useQuery } from "@tanstack/react-query"
import { getRouteApi, useNavigate } from "@tanstack/react-router"
import { t } from "i18next"
import { type FormEvent, useState } from "react"
import { Callout } from "@/components/callout"
import { Button } from "@/components/ui/button"
import { Field, FieldDescription, FieldError, FieldGroup, FieldLabel } from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import { publicSettingsQuery } from "@/features/settings/public"
import { useLogin } from "./api"
import { AuthLayout } from "./auth-layout"

const route = getRouteApi("/login")

export function LoginPage() {
  const { redirect } = route.useSearch()
  const navigate = useNavigate()
  const login = useLogin()
  const [credentials, setCredentials] = useState({ username: "", password: "" })
  // Set once the password was right, if the account needs a code too.
  const [code, setCode] = useState<string>()

  function submit(event: FormEvent) {
    event.preventDefault()
    login.mutate(
      { ...credentials, code },
      // The panel opens where the user was going, or at "/" on their start page.
      { onSuccess: (result) => ("mfaRequired" in result ? setCode("") : navigate({ to: redirect ?? "/", state: { open: true } })) },
    )
  }

  function back() {
    setCode(undefined)
    login.reset()
  }

  const needsCode = code !== undefined
  return (
    <AuthLayout
      title={needsCode ? t("Two-factor authentication") : t("Welcome back")}
      description={needsCode && t("Enter the code from your authenticator app.")}
    >
      <form onSubmit={submit} noValidate>
        <FieldGroup>
          {needsCode ? (
            <CodeField value={code} onChange={setCode} />
          ) : (
            <>
              <Field>
                <FieldLabel htmlFor="username">{t("Username")}</FieldLabel>
                <Input
                  id="username"
                  autoComplete="username"
                  autoFocus
                  required
                  value={credentials.username}
                  onChange={(e) => setCredentials({ ...credentials, username: e.target.value })}
                />
              </Field>
              <Field>
                <FieldLabel htmlFor="password">{t("Password")}</FieldLabel>
                <Input
                  id="password"
                  type="password"
                  autoComplete="current-password"
                  required
                  value={credentials.password}
                  onChange={(e) => setCredentials({ ...credentials, password: e.target.value })}
                />
              </Field>
            </>
          )}
          {login.error && <FieldError>{login.error.message}</FieldError>}
          <Button type="submit" size="lg" className="w-full" disabled={login.isPending || code === ""}>
            {login.isPending ? t("Signing in…") : needsCode ? t("Verify") : t("Sign in")}
          </Button>
          {needsCode && (
            <Button type="button" variant="ghost" className="-mt-2 w-full" onClick={back}>
              {t("Sign in as someone else")}
            </Button>
          )}
        </FieldGroup>
      </form>
      <SignInNotice />
    </AuthLayout>
  )
}

/** The notice of the settings, e.g. whom to ask for access, as plain text with its line breaks. */
function SignInNotice() {
  const { data } = useQuery(publicSettingsQuery)
  if (!data?.notice) return null
  return (
    <Callout role="note" className="mt-8 break-words whitespace-pre-line">
      {data.notice}
    </Callout>
  )
}

/** A code of the authenticator app, or a recovery code for those who lost it. */
function CodeField({ value, onChange }: { value: string; onChange: (code: string) => void }) {
  const [recovery, setRecovery] = useState(false)
  return (
    <Field>
      <FieldLabel htmlFor="code">{recovery ? t("Recovery code") : t("Code")}</FieldLabel>
      <Input
        // Remounts, and so takes the focus, when switching between the kinds of code.
        key={String(recovery)}
        id="code"
        autoFocus
        autoComplete="one-time-code"
        inputMode={recovery ? "text" : "numeric"}
        maxLength={recovery ? 11 : 6}
        placeholder={recovery ? "XXXXX-XXXXX" : "000000"}
        className="h-12 text-center font-mono text-xl tracking-[0.3em]"
        value={value}
        onChange={(e) => onChange(recovery ? e.target.value.trim() : e.target.value.replace(/\D/g, ""))}
      />
      <FieldDescription>
        {recovery ? t("Each recovery code works once.") : t("Lost your device?")}{" "}
        <button
          type="button"
          className="font-medium text-foreground underline-offset-4 hover:underline"
          onClick={() => {
            setRecovery(!recovery)
            onChange("")
          }}
        >
          {recovery ? t("Use your app instead") : t("Use a recovery code")}
        </button>
      </FieldDescription>
    </Field>
  )
}
