import {
  ArrowsInLineVerticalIcon,
  CheckIcon,
  DesktopIcon,
  DeviceMobileIcon,
  DeviceTabletIcon,
  KeyIcon,
  PaletteIcon,
  ShieldCheckIcon,
  ShieldIcon,
  SignOutIcon,
  SwatchesIcon,
  TranslateIcon,
  UserCircleIcon,
} from "@phosphor-icons/react"
import { useQuery } from "@tanstack/react-query"
import { useNavigate } from "@tanstack/react-router"
import { t } from "i18next"
import { RadioGroup as RadioGroupPrimitive } from "radix-ui"
import { useState } from "react"
import { toast } from "sonner"
import { ErrorCallout } from "@/components/callout"
import { ConfirmDialog } from "@/components/confirm-dialog"
import { FormSection } from "@/components/form-section"
import { PageHeader } from "@/components/page-header"
import { Segmented } from "@/components/segmented"
import { Button } from "@/components/ui/button"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { Skeleton } from "@/components/ui/skeleton"
import { useAccess } from "@/features/access/use-access"
import { AlertsSettings } from "@/features/notify/alerts-setting"
import { DesktopNotificationsSetting } from "@/features/notify/desktop-setting"
import { useSettings } from "@/features/preferences/api"
import { CodeSettings, ExportSettings, NavigationSettings, PowerSettings, TimesSettings } from "@/features/preferences/settings"
import { useNoLanguage } from "@/features/settings/public"
import { formatAgo, formatDateTime } from "@/lib/format"
import { languageName, languages, msg } from "@/lib/i18n"
import { type Accent, accents, type Density, type Theme, useLook } from "@/lib/theme"
import {
  meQuery,
  mfaQuery,
  type Session,
  sessionsQuery,
  useDisableMfa,
  useEnableMfa,
  useEndSessions,
  useNewRecoveryCodes,
  useSetLanguage,
  type User,
} from "./api"
import { AccountRow } from "./account-row"
import { ApiTokens } from "./api-tokens"
import { ConfirmPasswordDialog, MfaSetupDialog, RecoveryCodesDialog } from "./mfa-dialogs"
import { PasswordDialog } from "./password-dialog"

/** The signed-in user's own account: how they sign in, and how the panel looks for them. */
export function AccountPage() {
  const { data: user } = useQuery(meQuery)
  const { canSomewhere } = useAccess()
  if (!user) return null
  return (
    <>
      <PageHeader
        icon={UserCircleIcon}
        tone="violet"
        title={t("Your account")}
        description={t("Signed in as {{name}}.", { name: user.username })}
      />
      <div className="surface rounded-xl px-5 sm:px-8 [&>section:last-child]:border-b-0">
        <FormSection title={t("Password")}>
          <AccountRow icon={KeyIcon} tone="info" title={t("Password")} actions={<PasswordDialog username={user.username} />}>
            {t("At least 12 characters that you don't use anywhere else.")}
          </AccountRow>
        </FormSection>
        <FormSection title={t("Two-factor authentication")}>
          <MfaSettings username={user.username} />
        </FormSection>
        <FormSection title={t("Sessions")}>
          <Sessions />
        </FormSection>
        <FormSection title={t("API tokens")}>
          <ApiTokens user={user} />
        </FormSection>
        <FormSection title={t("Panel")}>
          <PanelSettings user={user} />
        </FormSection>
        <FormSection id="navigation" title={t("Navigation and layout")}>
          <NavigationSettings />
        </FormSection>
        <FormSection id="power" title={t("Servers")}>
          <PowerSettings />
        </FormSection>
        <FormSection title={t("Times and dates")}>
          <TimesSettings />
        </FormSection>
        <FormSection id="code" title={t("Console, terminal and editor")}>
          <CodeSettings />
        </FormSection>
        {canSomewhere("logs.view") && (
          <FormSection id="notifications" title={t("Notifications")}>
            <DesktopNotificationsSetting />
            <AlertsSettings />
          </FormSection>
        )}
        <FormSection title={t("Exports")}>
          <ExportSettings />
        </FormSection>
      </div>
    </>
  )
}

const themes = [
  { value: "light", label: msg("Light") },
  { value: "dark", label: msg("Dark") },
  { value: "system", label: msg("System") },
] satisfies { value: Theme; label: string }[]

/** How the panel looks for the user, in all their browsers. */
function PanelSettings({ user }: { user: User }) {
  const { change } = useSettings()
  const look = useLook()
  const setLanguage = useSetLanguage()
  const noLanguage = useNoLanguage()
  return (
    <>
      <AccountRow
        icon={PaletteIcon}
        tone="violet"
        title={t("Colour theme")}
        actions={
          <Segmented
            label={t("Colour theme")}
            value={look.theme}
            options={themes.map((o) => ({ value: o.value, label: t(o.label) }))}
            onChange={(value) => change({ theme: value })}
          />
        }
      >
        {t("System follows your operating system.")}
      </AccountRow>
      <AccountRow
        icon={SwatchesIcon}
        tone="violet"
        title={t("Accent colour")}
        actions={<AccentChoices value={look.accent} onChange={(value) => change({ accent: value })} />}
      >
        {t("The colour of buttons, links and highlights. The colours of states stay.")}
      </AccountRow>
      <AccountRow
        icon={ArrowsInLineVerticalIcon}
        tone="violet"
        title={t("Density")}
        actions={
          <Segmented<Density>
            label={t("Density")}
            value={look.density}
            options={[
              { value: "comfortable", label: t("Comfortable") },
              { value: "compact", label: t("Compact") },
            ]}
            onChange={(value) => change({ density: value })}
          />
        }
      >
        {t("Compact fits more on the screen, e.g. long lists of servers. Touch screens keep the room to tap.")}
      </AccountRow>
      <AccountRow
        icon={TranslateIcon}
        tone="info"
        title={t("Language")}
        actions={
          <Select
            value={user.language || "browser"}
            onValueChange={(language) =>
              setLanguage.mutate(language === "browser" ? "" : language, { onError: (error) => toast.error(error.message) })
            }
          >
            <SelectTrigger aria-label={t("Language")} className="w-auto min-w-44">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="browser">{noLanguage}</SelectItem>
              {languages.map((code) => (
                <SelectItem key={code} value={code} lang={code}>
                  {languageName(code)}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        }
      >
        {t("Dates, times and numbers follow it too.")}
      </AccountRow>
    </>
  )
}

const accentNames: Record<Accent, string> = {
  emerald: msg("Emerald"),
  blue: msg("Blue"),
  violet: msg("Violet"),
  graphite: msg("Graphite"),
}

/** The accents as swatches in their own colours; the arrow keys move through them. */
function AccentChoices({ value, onChange }: { value: Accent; onChange: (accent: Accent) => void }) {
  return (
    <RadioGroupPrimitive.Root
      value={value}
      onValueChange={(accent) => onChange(accent as Accent)}
      orientation="horizontal"
      aria-label={t("Accent colour")}
      className="flex gap-2"
    >
      {accents.map((accent) => (
        <RadioGroupPrimitive.Item
          key={accent}
          value={accent}
          data-accent={accent}
          aria-label={t(accentNames[accent])}
          title={t(accentNames[accent])}
          className="grid size-8 place-items-center rounded-full bg-primary text-primary-foreground shadow-sm ring-offset-2 ring-offset-card transition-transform outline-none hover:scale-110 focus-visible:ring-2 focus-visible:ring-ring pointer-coarse:size-10 motion-reduce:hover:scale-100"
        >
          <RadioGroupPrimitive.Indicator className="grid place-items-center">
            <CheckIcon weight="bold" className="size-4" />
          </RadioGroupPrimitive.Indicator>
        </RadioGroupPrimitive.Item>
      ))}
    </RadioGroupPrimitive.Root>
  )
}

function MfaSettings({ username }: { username: string }) {
  const { data, isPending, error } = useQuery(mfaQuery)
  // The mutations live here, as the dialogs that use them go away when the status changes.
  const enable = useEnableMfa()
  const disable = useDisableMfa()
  const renew = useNewRecoveryCodes()
  const [codes, setCodes] = useState<string[]>()
  const navigate = useNavigate()

  if (isPending) return <Skeleton className="h-20 rounded-xl" />
  if (error) return <ErrorCallout error={error} />
  return (
    <>
      {data.enabled ? (
        <AccountRow
          icon={ShieldCheckIcon}
          tone="success"
          title={t("Authenticator app")}
          status={{ tone: "success", label: msg("On") }}
          actions={
            <>
              <ConfirmPasswordDialog
                trigger={<Button variant="outline">{t("New recovery codes")}</Button>}
                title={t("Create new recovery codes")}
                description={t("The new codes replace those you have, which stop working.")}
                action={t("Create codes")}
                change={renew}
                onSuccess={({ recoveryCodes }) => setCodes(recoveryCodes)}
              />
              <ConfirmPasswordDialog
                trigger={<Button variant="destructive">{t("Turn off")}</Button>}
                title={t("Turn off two-factor authentication")}
                description={
                  data.required
                    ? t(
                        "Your authenticator app and recovery codes stop working. As your account requires two-factor authentication, you then set it up again right away, e.g. with a new phone.",
                      )
                    : t("Then your password alone signs you in. Your authenticator app and recovery codes stop working.")
                }
                action={t("Turn off")}
                destructive
                change={disable}
                onSuccess={() =>
                  data.required
                    ? navigate({ to: "/two-factor", search: { redirect: "/account" } })
                    : toast.success(t("Turned off two-factor authentication"))
                }
              />
            </>
          }
        >
          {t("Signing in asks for a code from the app.")} {data.required && `${t("Your account requires it.")} `}
          <span className={data.recoveryCodes <= 3 ? "font-medium text-warning" : undefined}>
            {t("{{count}} recovery codes left.", { count: data.recoveryCodes, defaultValue_one: "{{count}} recovery code left." })}
          </span>
        </AccountRow>
      ) : (
        <AccountRow
          icon={ShieldIcon}
          tone="neutral"
          title={t("Authenticator app")}
          status={{ tone: "neutral", label: msg("Off") }}
          actions={
            <MfaSetupDialog
              username={username}
              enable={enable}
              onEnabled={(codes) => {
                setCodes(codes)
                toast.success(t("Turned on two-factor authentication"), {
                  description: t("You were signed out everywhere else, and your API tokens were revoked."),
                })
              }}
            />
          }
        >
          {t("Your password alone signs you in.")}
        </AccountRow>
      )}
      <RecoveryCodesDialog codes={codes} onClose={() => setCodes(undefined)} />
    </>
  )
}

/** Where the user is signed in, each with a way to sign out there, e.g. in a lost or shared browser. */
function Sessions() {
  const { data, isPending, error } = useQuery(sessionsQuery)
  const end = useEndSessions()
  if (isPending) return <Skeleton className="h-20 rounded-xl" />
  if (error) return <ErrorCallout error={error} />

  const signOut = (id?: string) =>
    end.mutate(id, {
      onSuccess: () => toast.success(id ? t("Signed out there") : t("Signed out everywhere else")),
      onError: (e) => toast.error(e.message),
    })
  return (
    <>
      {data.map((session) => (
        <AccountRow
          key={session.id}
          icon={deviceIcon(session.os)}
          tone={session.current ? "success" : "neutral"}
          title={describeBrowser(session)}
          status={session.current ? { tone: "success", label: msg("This browser") } : undefined}
          actions={
            !session.current && (
              <Button variant="outline" disabled={end.isPending && end.variables === session.id} onClick={() => signOut(session.id)}>
                {t("Sign out")}
              </Button>
            )
          }
        >
          {session.ip && (
            <>
              <span className="font-mono">{session.ip}</span> ·{" "}
            </>
          )}
          {[
            session.current
              ? t("Active now")
              : session.lastUsedAt && t("Last active {{time}}", { time: formatAgo(session.lastUsedAt) }),
            session.createdAt ? t("Signed in {{time}}", { time: formatDateTime(session.createdAt) }) : t("Sign-in time not recorded"),
          ]
            .filter(Boolean)
            .join(" · ")}
        </AccountRow>
      ))}
      {data.length > 1 && (
        <ConfirmDialog
          trigger={
            <Button variant="outline" className="justify-self-start">
              <SignOutIcon /> {t("Sign out everywhere else")}
            </Button>
          }
          title={t("Sign out everywhere else?")}
          description={t("Your other sessions end, e.g. in a lost or shared browser. This browser stays signed in.")}
          action={t("Sign out everywhere else")}
          destructive
          onConfirm={() => signOut()}
        />
      )}
    </>
  )
}

/** The browser and operating system of a session, as far as the master recognised them. */
function describeBrowser({ browser, os }: Session) {
  if (browser && os) return t("{{browser}} on {{os}}", { browser, os })
  if (os) return t("Browser on {{os}}", { os })
  return browser || t("Unknown browser")
}

function deviceIcon(os?: string) {
  if (os === "iOS" || os === "Android") return DeviceMobileIcon
  if (os === "iPadOS") return DeviceTabletIcon
  return DesktopIcon
}
