import { CubeIcon, LockKeyIcon, LockKeyOpenIcon, PencilSimpleIcon, PlusIcon, WarningIcon } from "@phosphor-icons/react"
import { useQuery } from "@tanstack/react-query"
import { Link } from "@tanstack/react-router"
import { t } from "i18next"
import { type ReactNode, useState } from "react"
import { Trans } from "react-i18next"
import { toast } from "sonner"
import { BackLink } from "@/components/back-link"
import { Callout, ErrorCallout } from "@/components/callout"
import { EmptyState } from "@/components/empty-state"
import { FormSection } from "@/components/form-section"
import { NumberInput } from "@/components/number-input"
import { PageHeader } from "@/components/page-header"
import { usePageName } from "@/components/page-title"
import { Section } from "@/components/section"
import { Pill } from "@/components/status"
import { Button } from "@/components/ui/button"
import { Field, FieldDescription, FieldError, FieldLabel } from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import { Skeleton } from "@/components/ui/skeleton"
import { useAccess } from "@/features/access/use-access"
import { nodesQuery } from "@/features/nodes/api"
import type { Task } from "@/features/schedules/api"
import { RunHistory } from "@/features/schedules/run-history"
import { TaskCard } from "@/features/schedules/task-card"
import { type SecretInput, TaskForm } from "@/features/schedules/task-form"
import { type CopyTo, emptyMasterJob, type MasterSettings, masterJobs, minPassphrase, storagesQuery } from "./api"
import { PlaceField } from "./storages"

const code = "font-mono text-xs"

/** The backup job of the master, if there is one, for administrators. */
function useMasterJob() {
  const { admin } = useAccess()
  const query = useQuery({ ...masterJobs.tasksQuery, enabled: admin })
  return { admin, ...query, job: query.data?.[0] }
}

/** The name of the storage or node of a place. */
function usePlaceName() {
  const { data: storages = [] } = useQuery(storagesQuery)
  const { data: nodes = [] } = useQuery(nodesQuery)
  return ({ storage, node }: CopyTo) => (storage ? storages.find((s) => s.id === storage) : nodes.find((n) => n.id === node))?.name ?? "…"
}

/** Warns that the archives of the master aren't encrypted. */
function Unencrypted({ className }: { className?: string }) {
  return (
    <Callout tone="warning" icon={WarningIcon} title={t("The archives aren't encrypted")} className={className}>
      {t(
        "They hold the private key of the certificate authority, with which anyone who gets one controls the agents of all nodes, and every secret of the master, such as the secret keys of storages. Set a passphrase unless you trust the storage or node with them.",
      )}
    </Callout>
  )
}

const MasterLink = ({ children }: { children: ReactNode }) => (
  <Link to="/backups/master" className="block truncate font-semibold hover:underline">
    {children}
  </Link>
)

/** The backup job of the master on the Backups tab of the Automation, which only administrators see. */
export function MasterBackupSection() {
  const { admin, job, isPending, error } = useMasterJob()
  const placeName = usePlaceName()
  if (!admin) return null
  return (
    <Section
      title={t("Backup of the master")}
      description={t("Its database and certificate authority: without them, every node has to be connected again, and the master loses track of the copies of backups.")}
    >
      {isPending ? (
        <Skeleton className="h-48 rounded-xl" />
      ) : error ? (
        <ErrorCallout error={error} />
      ) : job ? (
        <ul className="grid gap-4 md:grid-cols-2">
          <TaskCard
            manage
            servers={false}
            task={job}
            taskApi={masterJobs}
            icon={CubeIcon}
            tone="violet"
            title={<MasterLink>{job.name}</MasterLink>}
            summary={[
              t("keeps {{count}} in {{where}}", { count: job.settings.keep, where: placeName(job.settings.to) }),
              job.hasSecret ? t("encrypted") : t("not encrypted"),
            ].join(" · ")}
            edit={
              <Button asChild size="sm" variant="outline">
                <Link to="/backups/master">
                  <PencilSimpleIcon />
                  {t("Edit")}
                </Link>
              </Button>
            }
          >
            {!job.hasSecret && <Unencrypted className="py-3" />}
          </TaskCard>
        </ul>
      ) : (
        <EmptyState
          icon={CubeIcon}
          tone="violet"
          title={t("The master isn't backed up yet")}
          description={t("Keep its newest backups in S3-compatible storage or on a node, so that a new master can take over if this one is lost.")}
        >
          <Button asChild>
            <Link to="/backups/master">
              <PlusIcon />
              {t("Set up")}
            </Link>
          </Button>
        </EmptyState>
      )}
    </Section>
  )
}

/** The page of the backup job of the master, which creates it if there is none yet. */
export function MasterBackupPage() {
  const { admin, job, isPending, error } = useMasterJob()
  usePageName(t("Backup of the master"))
  const save = masterJobs.useSaveTask(job?.id)
  // Remounting after each save resets the form to what was stored, also when only the passphrase changed.
  const [saves, setSaves] = useState(0)
  return (
    <>
      <BackLink to="/backups">{t("Backups")}</BackLink>
      <PageHeader
        icon={CubeIcon}
        tone="violet"
        title={job?.name ?? t("Backup of the master")}
        description={t("The database of the master with every secret, and its certificate authority, at set times.")}
      />
      {!admin ? (
        <Callout>{t("Only administrators may see and change the backup of the master.")}</Callout>
      ) : isPending ? (
        <Skeleton className="h-96 rounded-xl" />
      ) : error ? (
        <ErrorCallout error={error} />
      ) : (
        <>
          <Callout className="mb-6">
            <Trans
              i18nKey="A new master takes over the nodes with an archive, after <restore/> on its machine. The archives are named like <name/>, in the folder <folder/> of the storage, or in <path/> on the node."
              components={{
                restore: <code className={code}>noryx-master restore &lt;file&gt;</code>,
                name: <code className={code}>noryx-master-20261031-033000-k3x7qa.tar.gz.age</code>,
                folder: <code className={code}>master</code>,
                path: <code className={code}>backups/master</code>,
              }}
            />
          </Callout>
          <MasterForm
            key={`${saves}/${JSON.stringify(job)}`}
            job={job}
            save={save}
            onSaved={(j) => {
              toast.success(job ? t("Saved {{name}}", { name: j.name }) : t("Created {{name}}", { name: j.name }))
              setSaves((n) => n + 1)
            }}
          />
          {job && <RunHistory taskApi={masterJobs} id={job.id} />}
        </>
      )}
    </>
  )
}

function MasterForm({
  job,
  save,
  onSaved,
}: {
  job?: Task<MasterSettings>
  save: ReturnType<typeof masterJobs.useSaveTask>
  onSaved: (job: Task<MasterSettings>) => void
}) {
  const [invalid, setInvalid] = useState(false)
  return (
    <TaskForm
      initial={job ? { name: job.name, enabled: job.enabled, schedule: job.schedule, targets: [], settings: job.settings } : emptyMasterJob()}
      kind="job"
      submitLabel={job ? t("Save job") : t("Create job")}
      pending={save.isPending}
      error={save.error}
      onSubmit={(input) => save.mutate(input, { onSuccess: onSaved })}
      servers={false}
      invalid={invalid}
    >
      {(settings, set, secret) => (
        <FormSection title={t("Backups")}>
          <PlaceField id="master-to" label={t("Keep in")} required value={settings.to} onChange={(to) => set({ to: to ?? {} })}>
            {t(
              "S3-compatible storage, which is set up below the backup jobs, or a node, which keeps the archives where only its agent can read them. Keep them away from the master's machine.",
            )}
          </PlaceField>
          <Field>
            <FieldLabel htmlFor="master-keep">{t("Backups to keep")}</FieldLabel>
            <NumberInput id="master-keep" min={1} max={1000} unit={t("newest")} value={settings.keep} onChange={(keep) => set({ keep })} />
            <FieldDescription>{t("The job deletes its older archives there, and nothing else.")}</FieldDescription>
          </Field>
          <PassphraseField has={!!job?.hasSecret} secret={secret} onInvalid={setInvalid} />
        </FormSection>
      )}
    </TaskForm>
  )
}

/**
 * The passphrase that encrypts the archives, which the master never shows: it can be set, changed or removed. A new
 * one has to be typed twice, as nobody can restore the archives without it.
 */
function PassphraseField({ has, secret, onInvalid }: { has: boolean; secret: SecretInput; onInvalid: (invalid: boolean) => void }) {
  const [editing, setEditing] = useState(false)
  const [entered, setEntered] = useState({ first: "", repeat: "" })
  const removed = secret.value === ""
  const set = has && !removed
  const short = entered.first !== "" && [...entered.first].length < minPassphrase
  const mismatch = entered.repeat !== "" && entered.first !== entered.repeat

  function enter(change: Partial<typeof entered>) {
    const next = { ...entered, ...change }
    const ok = [...next.first].length >= minPassphrase && next.first === next.repeat
    setEntered(next)
    secret.set(ok ? next.first : undefined)
    onInvalid(!ok)
  }

  function edit(on: boolean) {
    setEditing(on)
    setEntered({ first: "", repeat: "" })
    secret.set(undefined)
    onInvalid(on)
  }

  return (
    <Field>
      <FieldLabel htmlFor={editing ? "master-passphrase" : undefined}>{t("Passphrase")}</FieldLabel>
      {editing ? (
        <>
          <div className="grid gap-3 sm:grid-cols-2">
            <Input
              id="master-passphrase"
              type="password"
              autoComplete="new-password"
              placeholder={t("At least {{length}} characters.", { length: minPassphrase })}
              value={entered.first}
              onChange={(e) => enter({ first: e.target.value })}
            />
            <Input
              type="password"
              autoComplete="new-password"
              aria-label={t("Repeat the passphrase")}
              placeholder={t("Repeat it")}
              value={entered.repeat}
              onChange={(e) => enter({ repeat: e.target.value })}
            />
          </div>
          {(short || mismatch) && (
            <FieldError>
              {short ? t("At least {{length}} characters.", { length: minPassphrase }) : t("The passphrases don't match.")}
            </FieldError>
          )}
          <div>
            <Button type="button" size="sm" variant="ghost" onClick={() => edit(false)}>
              {t("Cancel")}
            </Button>
          </div>
        </>
      ) : (
        <div className="flex flex-wrap items-center gap-2">
          {set ? (
            <Pill tone="success">
              <LockKeyIcon className="size-3" />
              {t("Set")}
            </Pill>
          ) : (
            <Pill tone="warning">
              <LockKeyOpenIcon className="size-3" />
              {removed ? t("Removed when you save") : t("None")}
            </Pill>
          )}
          <Button type="button" size="sm" variant="outline" onClick={() => edit(true)}>
            {set ? t("Change") : t("Set passphrase")}
          </Button>
          {set && (
            <Button type="button" size="sm" variant="ghost" onClick={() => secret.set("")}>
              {t("Remove")}
            </Button>
          )}
          {removed && (
            <Button type="button" size="sm" variant="ghost" onClick={() => secret.set(undefined)}>
              {t("Keep it")}
            </Button>
          )}
        </div>
      )}
      <FieldDescription>
        <Trans
          i18nKey="Encrypts the archives with age, which also decrypts them with <command/>. The master keeps it in its database and never shows it. Keep it elsewhere too, e.g. in a password manager: without it, no archive can be restored. A new one applies to the next archives."
          components={{ command: <code className={code}>age -d</code> }}
        />
      </FieldDescription>
      {!editing && !set && <Unencrypted className="mt-1" />}
    </Field>
  )
}
