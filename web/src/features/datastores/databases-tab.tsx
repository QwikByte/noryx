import {
  ArchiveIcon,
  DatabaseIcon,
  DownloadSimpleIcon,
  EyeIcon,
  EyeSlashIcon,
  GearIcon,
  HardDrivesIcon,
  KeyIcon,
  MemoryIcon,
  PlayIcon,
  PlugsConnectedIcon,
  PlusIcon,
  StopIcon,
  TableIcon,
  TrashIcon,
  WarningCircleIcon,
} from "@phosphor-icons/react"
import { useQuery } from "@tanstack/react-query"
import { getRouteApi, Link } from "@tanstack/react-router"
import { t } from "i18next"
import { type FormEvent, useRef, useState } from "react"
import { Trans } from "react-i18next"
import { toast } from "sonner"
import { Callout, ErrorCallout } from "@/components/callout"
import { Chip } from "@/components/chip"
import { ConfirmDialog } from "@/components/confirm-dialog"
import { CopyField } from "@/components/copy-field"
import { EmptyState } from "@/components/empty-state"
import { IconTile } from "@/components/icon-tile"
import { Section } from "@/components/section"
import { Pill, StatusBadge } from "@/components/status"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Skeleton } from "@/components/ui/skeleton"
import { useAccess } from "@/features/access/use-access"
import { formatBytes, formatDateTime, formatMegabytes } from "@/lib/format"
import { type Database, type Datastore, downloadUrl, type Dump, dumpsQuery, networkDatastoresQuery, passwordQuery, useDatastore } from "./api"
import { ChangeDatastoreDialog, CreateDatastoreDialog, DeleteDatastoreDialog } from "./datastore-dialogs"
import { DatastoreLog } from "./datastore-log"
import { DatastoreUsage } from "./datastore-usage"
import { BackUpDialog, RestoreDialog, UploadDialog } from "./dump-dialogs"
import { engines, states } from "./labels"

const route = getRouteApi("/_app/networks/$networkId/databases")

const exampleDatabase = "luckperms"

/** The Databases tab of a network: its datastores, their databases and dumps. */
export function DatabasesTab() {
  const { networkId } = route.useParams()
  const { can } = useAccess()
  const { data, isPending, error } = useQuery(networkDatastoresQuery(networkId))
  if (isPending) return <Skeleton className="h-64 rounded-xl" />
  if (error) return <ErrorCallout error={error} />
  return (
    <Section
      title={t("Datastores")}
      description={t(
        "MariaDB and PostgreSQL servers that only this network's servers can reach. Enter a database's connection details in the configuration of the plugin that uses it, or in a file set.",
      )}
      className="mt-0"
      actions={can("datastores.manage") && data.length > 0 && <CreateDatastoreDialog networkId={networkId} />}
    >
      {data.length === 0 ? (
        <EmptyState
          icon={DatabaseIcon}
          tone="info"
          title={t("No datastores yet")}
          description={t("Add one for plugins that share their data across the network, e.g. LuckPerms or CoreProtect.")}
        >
          {can("datastores.manage") && <CreateDatastoreDialog networkId={networkId} />}
        </EmptyState>
      ) : (
        <div className="space-y-6">
          {data.map((ds) => (
            <DatastoreCard key={ds.id} datastore={ds} />
          ))}
        </div>
      )}
    </Section>
  )
}

function DatastoreCard({ datastore: ds }: { datastore: Datastore }) {
  const { can } = useAccess()
  const manage = can("datastores.manage")
  const { power, addDatabase, update } = useDatastore(ds.id)
  const [logOpen, setLogOpen] = useState(false)
  const log = useRef<HTMLElement>(null)
  const showLog = () => {
    setLogOpen(true)
    log.current?.focus({ preventScroll: true })
    log.current?.scrollIntoView({ behavior: "smooth", block: "start" })
  }
  const run = (action: "start" | "stop") =>
    power.mutate(action, {
      onSuccess: () => toast.success(action === "start" ? t("Started {{name}}", { name: ds.name }) : t("Stopped {{name}}", { name: ds.name })),
      onError: (e) => toast.error(e.message),
    })

  return (
    <article className="surface rounded-xl">
      <header className="flex flex-wrap items-center gap-3 border-b px-4 py-3">
        <IconTile icon={DatabaseIcon} tone="violet" size="sm" />
        <div className="min-w-0 flex-1 basis-56">
          <p className="flex flex-wrap items-center gap-2 font-semibold">
            <span className="font-mono">{ds.name}</span>
            <StatusBadge status={states[ds.state]} />
          </p>
          <p className="mt-1 flex flex-wrap gap-1.5">
            <Chip icon={DatabaseIcon}>
              {engines[ds.engine].label} {ds.version}
            </Chip>
            <Chip icon={HardDrivesIcon}>{ds.nodeName}</Chip>
            <Chip icon={MemoryIcon}>{formatMegabytes(ds.memoryMb)}</Chip>
            <Chip icon={ArchiveIcon}>{formatBytes(ds.size)}</Chip>
          </p>
        </div>
        {manage && (
          <div className="flex items-center gap-1.5">
            {ds.state === "stopped" ? (
              <Button size="sm" variant="outline" disabled={power.isPending} onClick={() => run("start")}>
                <PlayIcon />
                {t("Start")}
              </Button>
            ) : (
              <ConfirmDialog
                trigger={
                  <Button size="sm" variant="outline" disabled={power.isPending || ds.state === "unknown"}>
                    <StopIcon />
                    {t("Stop")}
                  </Button>
                }
                title={t("Stop {{name}}?", { name: ds.name })}
                description={t("The plugins that use its databases lose access to them until it starts again.")}
                action={t("Stop")}
                destructive
                onConfirm={() => run("stop")}
              />
            )}
            <ChangeDatastoreDialog
              datastore={ds}
              trigger={
                <Button size="icon-sm" variant="ghost" aria-label={t("Change {{name}}", { name: ds.name })} title={t("Change")}>
                  <GearIcon />
                </Button>
              }
            />
            <DeleteDatastoreDialog
              datastore={ds}
              trigger={
                <Button
                  size="icon-sm"
                  variant="ghost"
                  aria-label={t("Delete {{name}}", { name: ds.name })}
                  title={t("Delete")}
                  className="text-muted-foreground hover:bg-destructive/10 hover:text-destructive"
                >
                  <TrashIcon />
                </Button>
              }
            />
          </div>
        )}
      </header>
      <div className="space-y-4 p-4">
        {ds.problem && (
          <Callout tone="warning" icon={WarningCircleIcon} role="alert">
            {ds.problem}
          </Callout>
        )}
        {ds.state === "unhealthy" && (
          <Callout tone="destructive" icon={WarningCircleIcon} role="alert">
            {manage ? (
              <Trans
                i18nKey="Its health check fails. <link>The log of its container</link> on <node/> tells why."
                components={{
                  // oxlint-disable-next-line jsx-a11y/control-has-associated-label -- <Trans> puts the text in
                  link: <button type="button" className="font-medium underline underline-offset-4" onClick={showLog} />,
                  node: <span>{ds.nodeName}</span>,
                }}
              />
            ) : (
              t("Its health check fails. The log of its container on {{node}} tells why.", { node: ds.nodeName })
            )}
          </Callout>
        )}
        {ds.missing.length > 0 && (
          <Callout tone="warning" icon={WarningCircleIcon} title={t("Databases are missing")}>
            {t("The datastore lacks {{names}}, e.g. as its data was replaced.", { names: ds.missing.join(", ") })}
            {manage && (
              <div className="mt-2 flex flex-wrap gap-2">
                {ds.missing.map((name) => (
                  <Button key={name} size="xs" variant="outline" disabled={addDatabase.isPending} onClick={() =>
                      addDatabase.mutate(name, {
                        onSuccess: (db) => toast.success(t("Created the database {{name}}", { name: db.name })),
                        onError: (e) => toast.error(e.message),
                      })
                    }
                  >
                    {t("Create {{name}} again", { name })}
                  </Button>
                ))}
              </div>
            )}
          </Callout>
        )}
        {ds.previous && (
          <Callout tone="info" title={t("The data of version {{version}} is kept", { version: ds.previous })}>
            {t("Remove it once the new version works, to free its space.")}
            {manage && (
              <div className="mt-2">
                <ConfirmDialog
                  trigger={
                    <Button size="xs" variant="outline" disabled={update.isPending}>
                      {t("Remove the data of {{version}}", { version: ds.previous })}
                    </Button>
                  }
                  title={t("Remove the data of {{version}}?", { version: ds.previous })}
                  description={t("You can't go back to that version afterwards. The datastore restarts.")}
                  action={t("Remove")}
                  destructive
                  onConfirm={() => update.mutate({ removePrevious: true }, { onError: (e) => toast.error(e.message) })}
                />
              </div>
            )}
          </Callout>
        )}
        <DatastoreUsage datastore={ds} />
        <Databases datastore={ds} />
        <Dumps datastore={ds} />
        {manage && <DatastoreLog ref={log} datastore={ds} open={logOpen} onOpenChange={setLogOpen} />}
      </div>
    </article>
  )
}

function Databases({ datastore: ds }: { datastore: Datastore }) {
  const { can } = useAccess()
  const [name, setName] = useState("")
  const { addDatabase } = useDatastore(ds.id)

  function submit(event: FormEvent) {
    event.preventDefault()
    addDatabase.mutate(name.trim(), {
      onSuccess: (db) => {
        toast.success(t("Created the database {{name}}", { name: db.name }))
        setName("")
      },
    })
  }

  return (
    <section aria-label={t("Databases")} className="space-y-2">
      <h3 className="text-sm font-semibold">{t("Databases")}</h3>
      {ds.databases.length === 0 ? (
        <p className="text-sm text-muted-foreground">{t("No databases yet. Each plugin gets one, with a user of the same name.")}</p>
      ) : (
        <ul className="divide-y rounded-lg ring-1 ring-border">
          {ds.databases.map((db) => (
            <DatabaseRow key={db.name} datastore={ds} database={db} />
          ))}
        </ul>
      )}
      {can("datastores.manage") && (
        <form onSubmit={submit} className="flex flex-wrap items-start gap-2">
          <Input
            aria-label={t("Name of the new database")}
            required
            maxLength={32}
            pattern="[a-z][a-z0-9_]*"
            placeholder={exampleDatabase}
            className="w-56 font-mono"
            value={name}
            onChange={(e) => setName(e.target.value.toLowerCase())}
          />
          <Button type="submit" size="sm" variant="outline" disabled={addDatabase.isPending || ds.state !== "running"}>
            <PlusIcon />
            {t("Add database")}
          </Button>
          {addDatabase.error && <p className="w-full text-sm text-destructive">{addDatabase.error.message}</p>}
        </form>
      )}
    </section>
  )
}

function DatabaseRow({ datastore: ds, database: db }: { datastore: Datastore; database: Database }) {
  const manage = useAccess().can("datastores.manage")
  const [open, setOpen] = useState(false)
  const { dropDatabase, rotate } = useDatastore(ds.id)

  return (
    <li className="space-y-3 px-3 py-2.5">
      <div className="flex flex-wrap items-center gap-2">
        <span className="font-mono text-sm font-semibold">{db.name}</span>
        <div className="ml-auto flex items-center gap-1.5">
          <Button size="sm" variant="ghost" aria-expanded={open} onClick={() => setOpen(!open)}>
            <PlugsConnectedIcon />
            {open ? t("Hide connection") : t("Connection")}
          </Button>
          {manage && (
            <>
              <Button asChild size="sm" variant="outline">
                <Link
                  to="/networks/$networkId/databases/$datastoreId/$database"
                  params={{ networkId: ds.networkId, datastoreId: ds.id, database: db.name }}
                >
                  <TableIcon />
                  <span className="max-sm:sr-only">{t("Browse")}</span>
                </Link>
              </Button>
              <ConfirmDialog
                trigger={
                  <Button size="icon-sm" variant="ghost" aria-label={t("New password for {{name}}", { name: db.name })} title={t("New password")} disabled={rotate.isPending}>
                    <KeyIcon />
                  </Button>
                }
                title={t("Give {{name}} a new password?", { name: db.name })}
                description={t("The plugins that use the database lose access until you enter the new password in their configuration.")}
                action={t("New password")}
                onConfirm={() =>
                  rotate.mutate(db.name, {
                    onSuccess: () => {
                      toast.success(t("{{name}} has a new password", { name: db.name }), {
                        description: t("Enter it in the configuration of the plugins that use the database."),
                      })
                      setOpen(true)
                    },
                    onError: (e) => toast.error(e.message),
                  })
                }
              />
              <ConfirmDialog
                trigger={
                  <Button
                    size="icon-sm"
                    variant="ghost"
                    aria-label={t("Drop {{name}}", { name: db.name })}
                    title={t("Drop")}
                    disabled={dropDatabase.isPending}
                    className="text-muted-foreground hover:bg-destructive/10 hover:text-destructive"
                  >
                    <TrashIcon />
                  </Button>
                }
                title={t("Drop the database {{name}}?", { name: db.name })}
                description={t("Its user and all its data are deleted, but existing backups are kept. This can't be undone.")}
                action={t("Drop database")}
                destructive
                onConfirm={() =>
                  dropDatabase.mutate(db.name, {
                    onSuccess: () => toast.success(t("Dropped the database {{name}}", { name: db.name })),
                    onError: (e) => toast.error(e.message),
                  })
                }
              />
            </>
          )}
        </div>
      </div>
      {open && <Connection datastore={ds} database={db.name} />}
    </li>
  )
}

/** What the plugins of the network enter into their configuration to connect to a database. */
function Connection({ datastore: ds, database }: { datastore: Datastore; database: string }) {
  const manage = useAccess().can("datastores.manage")
  return (
    <div className="space-y-3">
      {ds.endpoints.map((e) => (
        <div key={e.host} className="space-y-1.5">
          <p className="text-xs font-medium text-muted-foreground">
            {e.remote ? t("Servers on other nodes") : t("Servers on {{node}}", { node: ds.nodeName })}
          </p>
          <div className="grid gap-2 sm:grid-cols-[1fr_10rem]">
            <CopyField prefix="host" label={t("Host")} value={e.host} />
            <CopyField prefix="port" label={t("Port")} value={String(e.port)} />
          </div>
        </div>
      ))}
      {!ds.endpoints.some((e) => e.remote) && (
        <p className="text-xs text-muted-foreground">
          {t("Servers on other nodes reach it once both nodes are in the private network of the nodes and the network was applied.")}
        </p>
      )}
      <div className="grid gap-2 sm:grid-cols-2">
        <CopyField prefix="database" label={t("Database")} value={database} />
        <CopyField prefix="user" label={t("User")} value={database} />
        {manage && <PasswordField datastoreId={ds.id} database={database} />}
      </div>
      <p className="text-xs text-muted-foreground">
        {t("In a file set, {{example}} and the like fill in where each server reaches the database, and its password, which the panel hides.", {
          example: `{{datastore:${ds.name}.${database}.host}}`,
        })}
      </p>
    </div>
  )
}

/** The password of a database's user, fetched only once it is to be shown. */
function PasswordField({ datastoreId, database }: { datastoreId: string; database: string }) {
  const [shown, setShown] = useState(false)
  const { data, error, isFetching } = useQuery({ ...passwordQuery(datastoreId, database), enabled: shown })
  if (shown && data) {
    return (
      <div className="flex items-center gap-1">
        <div className="min-w-0 flex-1">
          <CopyField prefix="password" label={t("Password")} value={data.password} />
        </div>
        <Button size="icon-sm" variant="ghost" aria-label={t("Hide the password")} title={t("Hide")} onClick={() => setShown(false)}>
          <EyeSlashIcon />
        </Button>
      </div>
    )
  }
  return (
    <div className="space-y-1">
      <div className="flex items-center gap-2 rounded-lg bg-console py-1 pr-1 pl-3 text-console-foreground">
        {/* i18next-instrument-ignore-next-line: the key of configuration files, like the prefixes of the other fields */}
        <span aria-hidden className="font-mono text-xs text-console-command">password</span>
        {/* i18next-instrument-ignore-next-line: the hidden password */}
        <span aria-hidden className="h-8 flex-1 font-mono text-xs leading-8 tracking-widest text-console-muted">••••••••••••</span>
        <button
          type="button"
          disabled={isFetching}
          onClick={() => setShown(true)}
          aria-label={t("Show the password")}
          className="grid size-7 shrink-0 place-items-center rounded-md text-console-muted transition-colors hover:bg-white/10 hover:text-console-foreground"
        >
          <EyeIcon className="size-4" />
        </button>
      </div>
      {error && <p className="text-xs text-destructive">{error.message}</p>}
    </div>
  )
}

function Dumps({ datastore: ds }: { datastore: Datastore }) {
  const { can } = useAccess()
  const { data: dumps, isPending, error } = useQuery(dumpsQuery(ds.id))

  return (
    <section aria-label={t("Backups")} className="space-y-2">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <h3 className="text-sm font-semibold">{t("Backups")}</h3>
        {can("datastores.manage") && (
          <div className="flex flex-wrap gap-1.5">
            <UploadDialog datastore={ds} />
            <BackUpDialog datastore={ds} />
          </div>
        )}
      </div>
      {isPending ? (
        <Skeleton className="h-12 rounded-lg" />
      ) : error ? (
        <ErrorCallout error={error} />
      ) : dumps.length === 0 ? (
        <p className="text-sm text-muted-foreground">{t("No backups yet. Backup jobs can back the datastore up on a schedule.")}</p>
      ) : (
        <ul className="divide-y rounded-lg ring-1 ring-border">
          {dumps.map((d) => (
            <DumpRow key={d.id} datastore={ds} dump={d} />
          ))}
        </ul>
      )}
    </section>
  )
}

function DumpRow({ datastore: ds, dump: d }: { datastore: Datastore; dump: Dump }) {
  const { can } = useAccess()
  const { removeDump } = useDatastore(ds.id)
  const created = formatDateTime(d.createdAt)
  return (
    <li className="flex flex-wrap items-center gap-3 px-3 py-2">
      <div className="min-w-0 flex-1">
        <p className="flex items-center gap-2 text-sm font-medium">
          <span className="truncate">{d.label || created}</span>
          {d.jobId && <Pill tone="info">{t("Scheduled")}</Pill>}
        </p>
        <p className="truncate text-xs text-muted-foreground">
          {d.label && `${created} · `}
          {formatBytes(d.size)} · {d.databases.join(", ")}
        </p>
      </div>
      {can("datastores.manage") && (
        <div className="flex items-center gap-1.5">
          <Button asChild size="sm" variant="outline">
            <a href={downloadUrl(ds.id, d.id)} download>
              <DownloadSimpleIcon />
              <span className="max-sm:sr-only">{t("Download")}</span>
            </a>
          </Button>
          <RestoreDialog datastore={ds} dump={d} />
          <ConfirmDialog
            trigger={
              <Button
                size="icon-sm"
                variant="ghost"
                aria-label={t("Delete the backup of {{time}}", { time: created })}
                title={t("Delete")}
                disabled={removeDump.isPending}
                className="text-muted-foreground hover:bg-destructive/10 hover:text-destructive"
              >
                <TrashIcon />
              </Button>
            }
            title={t("Delete the backup of {{time}}?", { time: created })}
            description={t("The backup is deleted from the node. This can't be undone.")}
            action={t("Delete backup")}
            destructive
            onConfirm={() =>
              removeDump.mutate(d.id, { onSuccess: () => toast.success(t("Deleted the backup")), onError: (e) => toast.error(e.message) })
            }
          />
        </div>
      )}
    </li>
  )
}
