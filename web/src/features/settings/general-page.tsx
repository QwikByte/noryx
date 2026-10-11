import {
  ArrowClockwiseIcon,
  CertificateIcon,
  ClockIcon,
  CubeIcon,
  LockIcon,
  LockOpenIcon,
  ShieldCheckIcon,
  ShieldIcon,
  UsersThreeIcon,
} from "@phosphor-icons/react"
import { useQuery } from "@tanstack/react-query"
import { useBlocker } from "@tanstack/react-router"
import { t } from "i18next"
import { type FormEvent, useState } from "react"
import { Trans } from "react-i18next"
import { toast } from "sonner"
import { Callout, ErrorCallout } from "@/components/callout"
import { ConfirmDialog } from "@/components/confirm-dialog"
import { FormSection } from "@/components/form-section"
import { StatCard, StatStrip } from "@/components/stat-card"
import { Button } from "@/components/ui/button"
import { Field, FieldContent, FieldDescription, FieldLabel, FieldLegend, FieldSet, FieldTitle } from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import { RadioGroup, RadioGroupItem } from "@/components/ui/radio-group"
import { Skeleton } from "@/components/ui/skeleton"
import { Switch } from "@/components/ui/switch"
import { groupsQuery } from "@/features/access/api"
import { GroupPicker } from "@/features/access/group-picker"
import { useAccess } from "@/features/access/use-access"
import { mfaQuery } from "@/features/auth/api"
import { limitsForm, limitsOf } from "@/features/nodes/limits"
import { LimitsFields } from "@/features/nodes/limits-fields"
import { NewServerFields } from "@/features/servers/new-server-fields"
import { WarningFields } from "@/features/servers/warning-fields"
import { UpdateCheck } from "@/features/updates/update-check"
import { ThresholdFields, ThresholdsHelp } from "@/features/usage/thresholds"
import { formatDate, formatDateTime, formatDuration } from "@/lib/format"
import { msg } from "@/lib/i18n"
import {
  currentPanel,
  type Master,
  type MasterSettings,
  nextPanel,
  type PanelHTTPS,
  panelURL,
  type SettingsView,
  samePanel,
  settingsQuery,
  useUpdateSettings,
} from "./api"
import { InviteGroupsField, UserDefaultsFields } from "./defaults-fields"
import { NameFields } from "./name-fields"
import { RestartButton } from "./restart-button"

/** The General tab: the running master and its settings. */
export function GeneralSettingsPage() {
  const { data, isPending, error } = useQuery(settingsQuery)
  if (isPending) return <Skeleton className="h-96 rounded-xl" />
  if (error) return <ErrorCallout error={error} />
  return (
    <>
      {/* Remounting after a restart measures the uptime from now. */}
      <MasterFacts key={data.master.startedAt} master={data.master} settings={data.settings} />
      {/* Remounting on save resets the form to what the master stored. */}
      <SettingsForm key={JSON.stringify(data.settings)} view={data} />
    </>
  )
}

/** The certificates the panel can serve. */
const certificates: Record<PanelHTTPS, { label: string; description: string; icon: typeof LockIcon }> = {
  "": {
    label: msg("None"),
    description: msg(
      "Plain HTTP, for a reverse proxy that serves HTTPS or an SSH tunnel. Browsers only sign in over HTTPS or at localhost.",
    ),
    icon: LockOpenIcon,
  },
  "self-signed": {
    label: msg("Self-signed"),
    description: msg(
      "For IP addresses and domains. Browsers warn until you accept the certificate; compare its fingerprint with the one above.",
    ),
    icon: LockIcon,
  },
  letsencrypt: {
    label: msg("Let's Encrypt"),
    description: msg(
      "A trusted certificate for a domain that points to this machine, renewed automatically. Let's Encrypt checks the domain at port 443 or 80, which have to be reachable from the internet.",
    ),
    icon: ShieldCheckIcon,
  },
}

/** Whether the signed-in user can restart the master from the panel. */
function useCanRestart(master: Master) {
  return useAccess().admin && master.restartable
}

function MasterFacts({ master, settings }: { master: Master; settings: MasterSettings }) {
  const [openedAt] = useState(Date.now)
  const canRestart = useCanRestart(master)
  const details: [string, string][] = [
    [t("Enrollment endpoint"), master.enrollListenAddr],
    [t("Join tokens connect to"), settings.enrollAddr || master.enrollAddr],
    [t("CA fingerprint (SHA-256)"), master.caFingerprint],
  ]
  return (
    <>
      <StatStrip className="grid-cols-2 lg:grid-cols-4">
        <StatCard icon={CubeIcon} tone="info" label={t("Master")} value={master.version}>
          {/* i18next-instrument-ignore-next-line: the name of the program */}
          noryx-master
        </StatCard>
        <StatCard icon={ClockIcon} tone="success" label={t("Running for")} value={formatDuration(openedAt - Date.parse(master.startedAt))}>
          {t("since {{time}}", { time: formatDateTime(master.startedAt) })}
        </StatCard>
        <StatCard icon={LockIcon} tone="violet" label={t("Panel")} value={master.panelHttps ? "HTTPS" : "HTTP"}>
          <span className="font-mono">{master.panelAddr}</span>
        </StatCard>
        <StatCard
          icon={ShieldCheckIcon}
          tone="warning"
          label={t("Certificate valid until")}
          value={formatDate(master.certificateExpiresAt)}
        >
          {t("Renewed automatically")}
        </StatCard>
      </StatStrip>
      <div className="mt-4 surface flex flex-wrap items-center gap-x-6 gap-y-4 rounded-xl px-5 py-4">
        <dl className="grid min-w-0 flex-1 gap-x-6 gap-y-4 md:grid-cols-[auto_auto_1fr]">
          {details.map(([term, value]) => (
            <div key={term} className="min-w-0">
              <dt className="text-xs text-muted-foreground">{term}</dt>
              <dd className="mt-0.5 font-mono text-sm font-medium break-all">{value}</dd>
            </div>
          ))}
        </dl>
        {canRestart && <RestartButton master={master} next={nextPanel(settings, master)} />}
      </div>
      {master.panelCertificate && <PanelCertificateFacts master={master} />}
    </>
  )
}

/** The certificate the panel serves, and why it isn't one of Let's Encrypt yet. */
function PanelCertificateFacts({ master }: { master: Master }) {
  const cert = master.panelCertificate!
  const details: [string, string][] = [
    [t("Panel certificate"), cert.selfSigned ? t("Self-signed") : t("Let's Encrypt")],
    [t("Valid until"), formatDate(cert.expiresAt)],
    [t("Fingerprint (SHA-256)"), cert.fingerprint],
  ]
  return (
    <>
      <dl className="mt-4 surface grid gap-x-6 gap-y-4 rounded-xl px-5 py-4 md:grid-cols-[auto_auto_1fr]">
        {details.map(([term, value]) => (
          <div key={term} className="min-w-0">
            <dt className="text-xs text-muted-foreground">{term}</dt>
            <dd className="mt-0.5 font-mono text-sm font-medium break-all">{value}</dd>
          </div>
        ))}
        <div className="min-w-0 md:col-span-3">
          <dt className="text-xs text-muted-foreground">{t("Valid for")}</dt>
          <dd className="mt-0.5 font-mono text-sm break-all">{cert.names.join(", ")}</dd>
        </div>
      </dl>
      {master.panelHttps === "letsencrypt" && cert.selfSigned && (
        <Callout
          tone={cert.error ? "warning" : "info"}
          icon={CertificateIcon}
          role="status"
          className="mt-4"
          title={cert.error ? t("Let's Encrypt issued no certificate") : t("Waiting for Let's Encrypt")}
        >
          {cert.error ? (
            <Trans
              i18nKey="Meanwhile, the panel serves its self-signed certificate and asks again every 30 minutes. Check that <domain/> points to this machine and that port 443 or 80 is reachable from the internet: <error/>"
              components={{
                domain: <span className="font-mono">{master.panelDomain}</span>,
                error: <span className="font-mono">{cert.error}</span>,
              }}
            />
          ) : (
            t("Meanwhile, the panel serves its self-signed certificate.")
          )}
        </Callout>
      )}
    </>
  )
}

function formOf(s: MasterSettings) {
  const { panelAddr, panelHttps, panelDomain, enrollAddr, sessionHours, joinTokenMinutes, logDays, logSizeMb, checkUpdates, requireMfa } = s
  return {
    panelAddr,
    panelHttps,
    panelDomain,
    enrollAddr,
    sessionHours,
    joinTokenMinutes,
    logDays,
    logSizeMb,
    checkUpdates,
    requireMfa,
    thresholds: s.thresholds,
    nodeDefaults: limitsForm(s.nodeDefaults),
    panelName: s.panelName,
    signInNotice: s.signInNotice,
    userDefaults: s.userDefaults,
    inviteGroups: s.inviteGroups,
    newServers: s.newServers,
    warnings: s.warnings,
  }
}

function SettingsForm({ view: { settings, master } }: { view: SettingsView }) {
  const access = useAccess()
  const editable = access.can("settings.edit")
  const initial = formOf(settings)
  const [form, setForm] = useState(initial)
  const update = useUpdateSettings()
  const dirty = JSON.stringify(form) !== JSON.stringify(initial)
  const blocker = useBlocker({ shouldBlockFn: () => dirty && !update.isPending, enableBeforeUnload: () => dirty, withResolver: true })
  const set = (change: Partial<typeof form>) => setForm({ ...form, ...change })

  function submit(event: FormEvent) {
    event.preventDefault()
    toast.promise(update.mutateAsync({ ...form, nodeDefaults: limitsOf(form.nodeDefaults) }), {
      loading: t("Saving…"),
      success: t("Saved the settings"),
      error: (e: Error) => e.message,
    })
  }

  return (
    <form onSubmit={submit} className="mt-10 surface rounded-xl px-5 sm:px-8">
      {/* Without the permission to change them, the settings are only shown. */}
      <fieldset disabled={!editable} className="contents">
        <FormSection title={t("Panel")}>
          <Field>
            <FieldLabel htmlFor="settings-panel-addr">{t("Listen address")}</FieldLabel>
            <Input
              id="settings-panel-addr"
              className="font-mono"
              placeholder={master.panelDefaultAddr}
              maxLength={47}
              disabled={!access.admin}
              value={form.panelAddr}
              onChange={(e) => set({ panelAddr: e.target.value })}
            />
            <FieldDescription>
              <Trans
                i18nKey="IP address and port, e.g. <example/> for all interfaces, or <https/> for HTTPS without a port in the browser. Leave it empty to use <default/> from the command line, which the panel also falls back to if it can't listen at this address. Only administrators can change it and HTTPS, as they can open the panel to other networks."
                components={{
                  example: <span className="font-mono">0.0.0.0:8080</span>,
                  https: <span className="font-mono">0.0.0.0:443</span>,
                  default: <span className="font-mono">{master.panelDefaultAddr}</span>,
                }}
              />
            </FieldDescription>
          </Field>
          <PanelHTTPSFields form={form} master={master} disabled={!access.admin} onChange={set} />
          <PanelRestartNotice settings={settings} master={master} />
        </FormSection>

        <FormSection title={t("Name and notice")}>
          <NameFields value={form} onChange={set} />
        </FormSection>

        <FormSection title={t("Enrollment")}>
          <Field>
            <FieldLabel htmlFor="settings-enroll-addr">{t("Enrollment address")}</FieldLabel>
            <Input
              id="settings-enroll-addr"
              className="font-mono"
              placeholder={master.enrollAddr}
              maxLength={261}
              value={form.enrollAddr}
              onChange={(e) => set({ enrollAddr: e.target.value })}
            />
            <FieldDescription>
              <Trans
                i18nKey="Host and port that join tokens tell agents to connect to, e.g. after the master moved to another domain. Leave it empty to use <default/> from the command line."
                components={{ default: <span className="font-mono">{master.enrollAddr}</span> }}
              />
            </FieldDescription>
          </Field>
          <Field>
            <FieldLabel htmlFor="settings-join-token">{t("Join tokens are valid for")}</FieldLabel>
            <NumberInput
              id="settings-join-token"
              min={5}
              max={1440}
              unit={t("minutes")}
              value={form.joinTokenMinutes}
              onChange={(joinTokenMinutes) => set({ joinTokenMinutes })}
            />
            <FieldDescription>{t("From 5 minutes to a day. Each token works only once.")}</FieldDescription>
          </Field>
        </FormSection>

        <FormSection title={t("Sign-in")}>
          <Field>
            <FieldLabel htmlFor="settings-session">{t("Sessions last")}</FieldLabel>
            <NumberInput
              id="settings-session"
              min={1}
              max={168}
              unit={t("hours")}
              value={form.sessionHours}
              onChange={(sessionHours) => set({ sessionHours })}
            />
            <FieldDescription>{t("Up to a week. Applies from the next sign-in; shorter sessions are safer.")}</FieldDescription>
          </Field>
          <MfaRequirementFields value={form.requireMfa} onChange={(requireMfa) => set({ requireMfa })} />
        </FormSection>

        <FormSection title={t("Defaults for users")}>
          <UserDefaultsFields value={form.userDefaults} onChange={(userDefaults) => set({ userDefaults })} />
          <InviteGroupsField value={form.inviteGroups} onChange={(inviteGroups) => set({ inviteGroups })} />
        </FormSection>

        <FormSection title={t("Log")}>
          <Field>
            <FieldLabel htmlFor="settings-log-days">{t("Keep entries for")}</FieldLabel>
            <NumberInput
              id="settings-log-days"
              min={1}
              max={365}
              unit={t("days")}
              value={form.logDays}
              onChange={(logDays) => set({ logDays })}
            />
            <FieldDescription>{t("Up to a year. Export entries to keep them longer.")}</FieldDescription>
          </Field>
          <Field>
            <FieldLabel htmlFor="settings-log-size">{t("Maximum size")}</FieldLabel>
            <NumberInput
              id="settings-log-size"
              min={100}
              max={102400}
              unit="MiB"
              value={form.logSizeMb}
              onChange={(logSizeMb) => set({ logSizeMb })}
            />
            <FieldDescription>
              {t(
                "From 100 MiB to 100 GiB, so that the log can't fill the disk. Beyond this size or a million entries, the oldest entries are deleted before their time, and a warning is logged.",
              )}
            </FieldDescription>
          </Field>
        </FormSection>

        <FormSection title={t("Usage warnings")}>
          <ThresholdsHelp />
          <p className="text-sm text-muted-foreground">{t("Servers and nodes can have their own thresholds in their settings.")}</p>
          <FieldSet>
            <FieldLegend variant="label">{t("Servers")}</FieldLegend>
            <ThresholdFields
              id="settings-servers"
              kind="servers"
              value={form.thresholds.servers}
              onChange={(servers) => set({ thresholds: { ...form.thresholds, servers } })}
            />
          </FieldSet>
          <FieldSet>
            <FieldLegend variant="label">{t("Nodes")}</FieldLegend>
            <ThresholdFields
              id="settings-nodes"
              kind="nodes"
              value={form.thresholds.nodes}
              onChange={(nodes) => set({ thresholds: { ...form.thresholds, nodes } })}
            />
          </FieldSet>
        </FormSection>

        <FormSection title={t("Updates")}>
          <Field orientation="horizontal">
            <Switch id="settings-check-updates" checked={form.checkUpdates} onCheckedChange={(checkUpdates) => set({ checkUpdates })} />
            <FieldContent>
              <FieldLabel htmlFor="settings-check-updates">{t("Check for updates")}</FieldLabel>
              <FieldDescription>
                {t(
                  "The master asks GitHub for the latest release every 6 hours. Administrators then see a notice and install it with a click.",
                )}
              </FieldDescription>
            </FieldContent>
          </Field>
          {access.admin && <UpdateCheck />}
        </FormSection>

        <FormSection title={t("New nodes")}>
          <LimitsFields
            id="settings-node-defaults"
            form={form.nodeDefaults}
            onChange={(change) => set({ nodeDefaults: { ...form.nodeDefaults, ...change } })}
          />
        </FormSection>

        <FormSection title={t("New servers")}>
          <NewServerFields value={form.newServers} onChange={(newServers) => set({ newServers })} />
        </FormSection>

        <FormSection title={t("Warnings to players")}>
          <WarningFields value={form.warnings} onChange={(warnings) => set({ warnings })} />
        </FormSection>
      </fieldset>
      <div
        className="-mx-5 flex flex-wrap-reverse items-center justify-end gap-x-6 gap-y-3 rounded-b-2xl bg-muted/50 px-5 py-4 sm:-mx-8 sm:px-8"
        hidden={!editable}
      >
        <p className="text-sm text-muted-foreground">{t("Changes apply right away, except those to the panel, which apply once the master restarts.")}</p>
        <Button type="submit" disabled={!dirty || update.isPending}>
          {update.isPending ? t("Saving…") : t("Save settings")}
        </Button>
      </div>
      <ConfirmDialog
        open={blocker.status === "blocked"}
        onOpenChange={(open) => !open && blocker.reset?.()}
        title={t("Discard your changes?")}
        description={t("Your changes to the settings haven't been saved.")}
        action={t("Discard changes")}
        destructive
        onConfirm={() => blocker.proceed?.()}
      />
    </form>
  )
}

/** Chooses the certificate of the panel and its domain. */
function PanelHTTPSFields({
  form,
  master,
  disabled,
  onChange,
}: {
  form: { panelHttps: PanelHTTPS; panelDomain: string }
  master: Master
  disabled: boolean
  onChange: (change: { panelHttps?: PanelHTTPS; panelDomain?: string }) => void
}) {
  if (master.panelHttps === "files")
    return (
      <Callout icon={LockIcon} role="note" title={t("HTTPS")}>
        <Trans
          i18nKey="The panel serves the certificate of <flag/> on the command line, so it doesn't use these settings."
          components={{ flag: <span className="font-mono">--tls-cert</span> }}
        />
      </Callout>
    )
  return (
    <>
      <Field>
        <FieldLabel id="settings-panel-https">{t("HTTPS")}</FieldLabel>
        <RadioGroup
          value={form.panelHttps}
          onValueChange={(panelHttps) => onChange({ panelHttps: panelHttps as PanelHTTPS })}
          aria-labelledby="settings-panel-https"
          disabled={disabled}
          className="gap-3"
        >
          {Object.entries(certificates).map(([value, { label, description, icon: Icon }]) => (
            <FieldLabel key={value} htmlFor={`settings-panel-https-${value || "none"}`}>
              <Field orientation="horizontal" className="items-start">
                <Icon className="mt-0.5 size-5 shrink-0 text-primary" />
                <FieldContent>
                  <FieldTitle>{t(label)}</FieldTitle>
                  <FieldDescription>{t(description)}</FieldDescription>
                </FieldContent>
                <RadioGroupItem id={`settings-panel-https-${value || "none"}`} value={value} />
              </Field>
            </FieldLabel>
          ))}
        </RadioGroup>
      </Field>
      {form.panelHttps && (
        <Field>
          <FieldLabel htmlFor="settings-panel-domain">{t("Domain")}</FieldLabel>
          <Input
            id="settings-panel-domain"
            className="font-mono"
            // i18next-instrument-ignore-next-line: an example of what to enter
            placeholder="panel.example.com"
            maxLength={253}
            required={form.panelHttps === "letsencrypt"}
            disabled={disabled}
            value={form.panelDomain}
            onChange={(e) => onChange({ panelDomain: e.target.value })}
          />
          <FieldDescription>
            {form.panelHttps === "letsencrypt" ? (
              <Trans
                i18nKey="The domain name that points to this machine. With Let's Encrypt, you accept its <terms>Subscriber Agreement</terms>. Other names and IP addresses get the self-signed certificate."
                components={{
                  terms: (
                    <a
                      href="https://letsencrypt.org/repository/"
                      target="_blank"
                      rel="noreferrer"
                      className="underline underline-offset-4"
                    />
                  ),
                }}
              />
            ) : (
              t("Optional. The certificate includes it besides the IP addresses and names of this machine.")
            )}
          </FieldDescription>
        </Field>
      )}
    </>
  )
}

type MfaRequirement = MasterSettings["requireMfa"]

/** Who has to use two-factor authentication. */
const mfaChoices = {
  none: {
    label: msg("Optional"),
    description: msg("Each user decides on their account page."),
    icon: ShieldIcon,
  },
  all: {
    label: msg("Required for everyone"),
    description: msg("All users have to use it, also those invited later."),
    icon: ShieldCheckIcon,
  },
  groups: {
    label: msg("Required for groups"),
    description: msg("The members of the groups chosen here have to use it, e.g. the administrators."),
    icon: UsersThreeIcon,
  },
} satisfies Record<string, { label: string; description: string; icon: typeof LockIcon }>

/** Chooses who has to use two-factor authentication: nobody, all users, or the members of groups. */
function MfaRequirementFields({ value, onChange }: { value: MfaRequirement; onChange: (value: MfaRequirement) => void }) {
  // Choosing groups shows them before any is chosen, which would otherwise mean nobody.
  const [choice, setChoice] = useState<keyof typeof mfaChoices>(value.all ? "all" : value.groups.length ? "groups" : "none")
  const canSeeGroups = useAccess().can("users.view")
  const groups = useQuery({ ...groupsQuery, enabled: canSeeGroups && choice === "groups" })
  const own = useQuery(mfaQuery)

  function choose(next: keyof typeof mfaChoices) {
    setChoice(next)
    onChange({ all: next === "all", groups: next === "groups" ? value.groups : [] })
  }

  return (
    <Field>
      <FieldLabel id="settings-require-mfa">{t("Two-factor authentication")}</FieldLabel>
      <RadioGroup
        value={choice}
        onValueChange={(next) => choose(next as keyof typeof mfaChoices)}
        aria-labelledby="settings-require-mfa"
        className="gap-3"
      >
        {Object.entries(mfaChoices).map(([key, { label, description, icon: Icon }]) => (
          <FieldLabel key={key} htmlFor={`settings-require-mfa-${key}`}>
            <Field orientation="horizontal" className="items-start">
              <Icon className="mt-0.5 size-5 shrink-0 text-primary" />
              <FieldContent>
                <FieldTitle>{t(label)}</FieldTitle>
                <FieldDescription>{t(description)}</FieldDescription>
              </FieldContent>
              <RadioGroupItem id={`settings-require-mfa-${key}`} value={key} />
            </Field>
          </FieldLabel>
        ))}
      </RadioGroup>
      {choice === "groups" &&
        (!canSeeGroups ? (
          <FieldDescription>
            {t("{{count}} groups chosen. Choosing others needs the permission to see users and groups.", {
              count: value.groups.length,
              defaultValue_one: "{{count}} group chosen. Choosing others needs the permission to see users and groups.",
            })}
          </FieldDescription>
        ) : groups.error ? (
          <ErrorCallout error={groups.error} />
        ) : groups.data ? (
          <GroupPicker groups={groups.data} value={value.groups} onChange={(chosen) => onChange({ all: false, groups: chosen })} />
        ) : (
          <Skeleton className="h-24 rounded-xl" />
        ))}
      <FieldDescription>
        {t(
          "Users it applies to who haven't set it up are asked to as soon as they use the panel, and can do nothing else until they did. Setting it up always works, so nobody is locked out.",
        )}
      </FieldDescription>
      {choice !== "none" && own.data && !own.data.enabled && (
        <Callout tone="warning" icon={ShieldIcon} role="status" title={t("You haven't set it up yourself")}>
          {t("If the requirement applies to you, the panel asks you to set up two-factor authentication as soon as you save.")}
        </Callout>
      )}
    </Field>
  )
}

/** Tells where the panel is until the master starts again, if its settings changed. */
function PanelRestartNotice({ settings, master }: { settings: MasterSettings; master: Master }) {
  const canRestart = useCanRestart(master)
  const current = currentPanel(master)
  const next = nextPanel(settings, master)
  if (samePanel(next, current)) return null
  return (
    <Callout tone={master.panelAddrError ? "warning" : "info"} icon={ArrowClockwiseIcon} role="status" title={t("Applies after a restart")}>
      <p>
        <Trans
          i18nKey="The panel stays at <current/> until the master starts again, e.g. with <command/> or the next update, then it is at <next/>."
          components={{
            current: <span className="font-mono">{panelURL(current)}</span>,
            command: <span className="font-mono">systemctl restart noryx-master</span>,
            next: <span className="font-mono">{panelURL(next)}</span>,
          }}
        />{" "}
        {!next.https && t("Point a reverse proxy in front of it to the new address as well.")}
      </p>
      {master.panelAddrError && (
        <p className="mt-1">
          <Trans
            i18nKey="When it started last, it couldn't listen at the address from the settings: <error/>"
            components={{ error: <span className="font-mono">{master.panelAddrError}</span> }}
          />
        </p>
      )}
      {canRestart && (
        <div className="mt-3">
          <RestartButton master={master} next={next} label={t("Restart now")} />
        </div>
      )}
    </Callout>
  )
}

function NumberInput({
  id,
  min,
  max,
  unit,
  value,
  onChange,
}: {
  id: string
  min: number
  max: number
  unit: string
  value: number
  onChange: (value: number) => void
}) {
  return (
    <div className="flex items-center gap-2">
      <Input
        id={id}
        type="number"
        required
        min={min}
        max={max}
        className="font-mono sm:w-32"
        value={value}
        onChange={(e) => onChange(e.target.valueAsNumber || 0)}
      />
      <span className="text-sm text-muted-foreground">{unit}</span>
    </div>
  )
}
