import { CaretDownIcon, CheckCircleIcon, CheckIcon, DownloadSimpleIcon, WarningCircleIcon } from "@phosphor-icons/react"
import { useQuery } from "@tanstack/react-query"
import { t } from "i18next"
import { type ReactNode, useId, useState } from "react"
import { Callout } from "@/components/callout"
import { FormDialogContent, useBusy } from "@/components/form-dialog"
import { Pill } from "@/components/status"
import { Button } from "@/components/ui/button"
import { Checkbox } from "@/components/ui/checkbox"
import {
  Dialog,
  DialogClose,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from "@/components/ui/dialog"
import { FieldError } from "@/components/ui/field"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { useAccess } from "@/features/access/use-access"
import { networksQuery, type ServerRef } from "@/features/networks/api"
import { ServerPicker } from "@/features/networks/server-picker"
import { key, refOf } from "@/features/networks/servers"
import { allServersQuery, type NodeServer } from "@/features/servers/api"
import { OperationStatus } from "@/features/operations/operation-status"
import { mergeResults } from "@/features/operations/retry"
import { RetryButton } from "@/features/operations/retry-button"
import { useOperation } from "@/features/operations/use-operation"
import {
  type Channel,
  channelLabel,
  channels,
  fallback,
  type InstallResult,
  installedQuery,
  maxServers,
  newestLabel,
  type ProjectVersion,
  type SearchHit,
  supports,
  useInstallPlugins,
} from "./api"
import { ChannelPill } from "./channel-pill"
import { PluginIcon } from "./plugin-icon"
import { RestartOption, RestartPill } from "./restart-option"
import { useRestart } from "./use-restart"
import { VersionMenu } from "./version-menu"

/**
 * Installs a plugin or mod on any number of servers it runs on, also on the game servers or the proxy of a network at
 * once, and marks the servers that have it already.
 */
export function InstallDialog({ hit }: { hit: SearchHit }) {
  const [open, setOpen] = useState(false)
  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogTrigger asChild>
        <Button size="sm" variant="outline">
          <DownloadSimpleIcon />
          {t("Install")}
        </Button>
      </DialogTrigger>
      <FormDialogContent className="sm:max-w-xl">
        <InstallForm hit={hit} onClose={() => setOpen(false)} />
      </FormDialogContent>
    </Dialog>
  )
}

/** The servers to install a project on, chosen anew each time the dialog opens. */
function InstallForm({ hit, onClose }: { hit: SearchHit; onClose: () => void }) {
  const [selected, setSelected] = useState<string[]>([])
  const [pinned, setPinned] = useState<ProjectVersion>()
  const [channel, setChannel] = useState<Channel>("release")
  // The batch that runs, of how many, e.g. 1 of 2 for 150 servers.
  const [[part, parts], setPart] = useState([0, 0])
  const { can } = useAccess()
  const { data: servers = [] } = useQuery(allServersQuery)
  const { data: networks = [] } = useQuery({ ...networksQuery, enabled: can("networks.view") })
  const { data: everywhere } = useQuery(installedQuery)
  const install = useInstallPlugins()
  const operation = useOperation()
  const suitable = servers.filter((s) => supports(hit.loaders, s.type) && can("plugins.manage", s.nodeId, s.id))
  const picked = new Set(selected)
  const chosen = suitable.filter((s) => picked.has(key(refOf(s))))
  const restart = useRestart(chosen.filter((s) => s.state === "running").map(refOf))
  // A version can be chosen for servers that run the same software and Minecraft version.
  const same = chosen.length > 0 && chosen.every((s) => s.type === chosen[0].type && s.version === chosen[0].version)
  const choose = (keys: string[], on: boolean) => {
    setSelected((list) => (on ? [...new Set([...list, ...keys])] : list.filter((k) => !keys.includes(k))))
    setPinned(undefined)
  }
  // The servers that have the project, with its file.
  const has = new Map(everywhere?.projects.find((p) => p.project.id === hit.id)?.servers.map((s) => [key(s), s]))
  // The servers of networks that can run the project, to choose them at once.
  const groups = networks.flatMap((n) => {
    const of = (refs: ServerRef[]) => {
      const keys = new Set(refs.map(key))
      return suitable.filter((s) => keys.has(key(refOf(s))))
    }
    return [
      { id: `${n.id}/servers`, label: t("Game servers of {{network}}", { network: n.name }), servers: of(n.backends) },
      { id: `${n.id}/proxy`, label: t("Proxy of {{network}}", { network: n.name }), servers: of([n.proxy]) },
    ].filter((g) => g.servers.length > 0)
  })

  const title = t("Install {{name}}", { name: hit.title })

  const versions = pinned && { [hit.id]: pinned.id }
  const chosenChannels = { [hit.id]: channel }
  // The results of a retry take the place of those of the servers it tried again.
  const [earlier, setEarlier] = useState<InstallResult[]>()
  const results = install.data && mergeResults(earlier, install.data)
  useBusy(install.isPending)

  function start(servers = chosen.map(refOf)) {
    operation.run(
      (onStart) =>
        install.mutateAsync({
          projects: [hit.id],
          servers,
          versions,
          channels: chosenChannels,
          ...restart.request,
          onStart: (op, batch) => {
            setPart([batch + 1, Math.ceil(servers.length / maxServers)])
            onStart(op)
          },
        }),
      {
        title,
        done: (results) => {
          const ok = results.filter((r) => !r.error).length
          const preReleases = results.some((r) => r.installed.some((f) => fallback(f, versions, chosenChannels)))
          return {
            message:
              ok === results.length
                ? t("Installed {{name}} on {{count}} servers", {
                    name: hit.title,
                    count: ok,
                    defaultValue_one: "Installed {{name}} on {{count}} server",
                  })
                : t("Installed {{name}} on {{ok}} of {{count}} servers", { name: hit.title, ok, count: results.length }),
            description: preReleases ? t("Where no release suits a server, a pre-release was installed.") : undefined,
            warning: ok < results.length || preReleases,
          }
        },
      },
    )
  }

  return operation.live && !install.data ? (
    <OperationStatus
      op={operation.live}
      title={parts > 1 ? t("{{title}}, part {{part}} of {{parts}}", { title, part, parts }) : title}
      onBackground={() => {
        operation.background(title)
        onClose()
      }}
      onBack={() => {
        operation.reset()
        install.reset()
        setEarlier(undefined)
      }}
    />
  ) : (
    <>
      <DialogHeader className="flex-row items-center gap-3">
        <PluginIcon src={hit.icon} />
        <div className="min-w-0 space-y-1 text-left">
          <DialogTitle>{t("Install {{name}}", { name: hit.title })}</DialogTitle>
          <DialogDescription>{t("The newest version of the channel that suits each server is installed, with what it requires.")}</DialogDescription>
        </div>
      </DialogHeader>
      {results ? (
        <Results results={results} servers={servers} versions={versions} channels={chosenChannels} />
      ) : suitable.length === 0 ? (
        <p className="py-4 text-sm text-muted-foreground">{t("None of your servers can run {{name}}.", { name: hit.title })}</p>
      ) : (
        <div className="-mx-1 grid max-h-[28rem] gap-4 overflow-y-auto px-1">
          {groups.length > 0 && (
            <Group title={t("Networks")}>
              {groups.map((g) => {
                const keys = g.servers.map((s) => key(refOf(s)))
                const count = keys.filter((k) => picked.has(k)).length
                return (
                  <Row
                    key={g.id}
                    checked={count === 0 ? false : count === keys.length || "indeterminate"}
                    onChange={(on) => choose(keys, on)}
                    title={g.label}
                    detail={
                      g.servers.length === 1
                        ? g.servers[0].name
                        : t("{{count}} servers", { count: g.servers.length, defaultValue_one: "{{count}} server" })
                    }
                  />
                )
              })}
            </Group>
          )}
          <Group title={groups.length > 0 ? t("Servers") : undefined}>
            <ServerPicker
              servers={suitable}
              selected={chosen.map(refOf)}
              onChange={(refs) => {
                setSelected(refs.map(key))
                setPinned(undefined)
              }}
              badge={(s) => {
                const file = has.get(key(refOf(s)))
                return (
                  file && (
                    <Pill tone={file.disabled ? "neutral" : "success"}>
                      <CheckIcon weight="bold" />
                      <span className="max-w-24 truncate font-mono">{file.version}</span>
                      <span className="sr-only">{file.disabled ? t("Installed, but turned off") : t("Installed")}</span>
                    </Pill>
                  )
                )
              }}
            />
          </Group>
        </div>
      )}
      {!install.data && chosen.length > 0 && (
        <div className="grid gap-2 rounded-lg bg-muted/50 px-3 py-2 text-sm">
          <div className="flex items-center justify-between gap-3">
            <span className="font-medium">{t("Channel")}</span>
            <Select value={channel} onValueChange={(c) => setChannel(c as Channel)}>
              <SelectTrigger size="sm" aria-label={t("Channel")}>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {channels.map((c) => (
                  <SelectItem key={c} value={c}>
                    {channelLabel(c)}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
          {channel !== "release" && (
            <p className="text-xs text-muted-foreground">
              {t("The servers keep the channel, so updates of {{name}} take these versions too.", { name: hit.title })}
            </p>
          )}
          <div className="flex items-center justify-between gap-3">
            <span className="font-medium">{t("Version")}</span>
            {same ? (
              <VersionMenu
                project={hit.id}
                title={hit.title}
                type={chosen[0].type}
                version={chosen[0].version}
                current={pinned?.id}
                channel={channel}
                onNewest={() => setPinned(undefined)}
                onPick={setPinned}
              >
                <Button size="sm" variant="outline">
                  {pinned ? <span className="font-mono">{pinned.number}</span> : newestLabel(channel)}
                  <CaretDownIcon />
                </Button>
              </VersionMenu>
            ) : (
              <span className="text-right text-xs text-muted-foreground">
                {t("The newest suitable version of the channel, as the servers run different software or Minecraft versions.")}
              </span>
            )}
          </div>
        </div>
      )}
      {!install.data && <RestartOption restart={restart} />}
      {install.error && <FieldError>{install.error.message}</FieldError>}
      <DialogFooter>
        {results && (
          <RetryButton
            results={results}
            onRetry={(failed) => {
              setEarlier(results)
              start(failed)
            }}
          />
        )}
        <DialogClose asChild>
          <Button variant="outline" disabled={install.isPending}>
            {install.data ? t("Done") : t("Cancel")}
          </Button>
        </DialogClose>
        {!install.data && (
          <Button disabled={chosen.length === 0 || install.isPending} onClick={() => start()}>
            {install.isPending
              ? t("Installing…")
              : t("Install on {{count}} servers", { count: chosen.length, defaultValue_one: "Install on {{count}} server" })}
          </Button>
        )}
      </DialogFooter>
    </>
  )
}

/** Servers to tick, under a title. */
function Group({ title, children }: { title?: string; children: ReactNode }) {
  const id = useId()
  return (
    <div role="group" aria-labelledby={title && id} className="grid gap-1.5">
      {title && (
        <p id={id} className="text-xs font-medium text-muted-foreground">
          {title}
        </p>
      )}
      {children}
    </div>
  )
}

/** The servers of a network to tick; a network of which only some are ticked shows a mixed state. */
function Row({
  checked,
  onChange,
  title,
  detail,
}: {
  checked: boolean | "indeterminate"
  onChange: (on: boolean) => void
  title: string
  detail: string
}) {
  return (
    <label className="flex cursor-pointer items-center gap-3 rounded-lg px-3 py-2 ring-1 ring-border hover:bg-muted/50">
      <Checkbox checked={checked} onCheckedChange={(on) => onChange(on === true)} />
      <span className="min-w-0 flex-1">
        <span className="block truncate text-sm font-medium">{title}</span>
        <span className="block truncate text-xs text-muted-foreground">{detail}</span>
      </span>
    </label>
  )
}

/** What was installed on each server, with the pre-releases installed where no release suits it. Servers load new plugins when they restart. */
function Results({
  results,
  servers,
  versions,
  channels,
}: {
  results: InstallResult[]
  servers: NodeServer[]
  versions?: Record<string, string>
  channels: Record<string, Channel>
}) {
  const preReleases = results.some((r) => r.installed.some((f) => fallback(f, versions, channels)))
  return (
    <div className="grid gap-3">
      <ul className="grid gap-2">
        {results.map((r) => (
          <li key={key(r)} className="flex items-start gap-3 rounded-lg px-3 py-2 text-sm ring-1 ring-border">
            {r.error ? (
              <WarningCircleIcon className="mt-0.5 size-4 shrink-0 text-destructive" weight="fill" />
            ) : (
              <CheckCircleIcon className="mt-0.5 size-4 shrink-0 text-success" weight="fill" />
            )}
            <span className="min-w-0 flex-1">
              <span className="flex flex-wrap items-center gap-2 font-medium">
                {servers.find((s) => key(refOf(s)) === key(r))?.name ?? r.serverId}
                <RestartPill result={r} />
              </span>
              {r.error && <span className="block text-xs text-muted-foreground">{r.error}</span>}
              {r.installed.length > 0 && (
                <span className="flex flex-wrap items-center gap-x-2 gap-y-1 text-xs text-muted-foreground">
                  {r.installed.map((f) => (
                    <span key={f.fileName} className="inline-flex items-center gap-1.5">
                      {f.fileName}
                      <ChannelPill channel={f.channel} />
                    </span>
                  ))}
                </span>
              )}
            </span>
          </li>
        ))}
      </ul>
      {preReleases && (
        <Callout tone="warning">{t("No release suits some of the servers, so a pre-release was installed there. Test it before you use it in production.")}</Callout>
      )}
      {results.some((r) => r.restart) && <p className="text-xs text-muted-foreground">{t("Restart the servers to load what was installed.")}</p>}
    </div>
  )
}
