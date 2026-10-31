import { DotsThreeIcon, FileTextIcon, PlusIcon, TimerIcon, WrenchIcon, XIcon } from "@phosphor-icons/react"
import { useQuery } from "@tanstack/react-query"
import { Link } from "@tanstack/react-router"
import { t } from "i18next"
import { type FormEvent, useState } from "react"
import { toast } from "sonner"
import { ErrorCallout } from "@/components/callout"
import { Section } from "@/components/section"
import { Pill } from "@/components/status"
import { Button } from "@/components/ui/button"
import {
  Dialog,
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuSeparator, DropdownMenuTrigger } from "@/components/ui/dropdown-menu"
import { Field, FieldDescription, FieldError, FieldGroup, FieldLabel } from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { Skeleton } from "@/components/ui/skeleton"
import { useAccess } from "@/features/access/use-access"
import { useOperation } from "@/features/operations/use-operation"
import { allServersQuery } from "@/features/servers/api"
import { formatAgo, formatDuration } from "@/lib/format"
import {
  type Maintenance,
  type MaintenanceChange,
  maintenanceQuery,
  type Network,
  useAbortMaintenanceTimer,
  useMaintenancePlayer,
  useSetMaintenance,
} from "./api"
import { findServer, isBungee } from "./servers"

/** The choices of when maintenance starts or ends and how long it lasts, in minutes; 0 is now, or until it is ended. */
const delays = [0, 5, 15, 60]
const durations = [0, 30, 60, 120, 1440]
/** The whole network, among the servers a change can be about. */
const whole = "network"

const minutes = (m: number) => formatDuration(m * 60_000)

/**
 * Maintenance of a network with the Maintenance plugin on its proxy: on or off for the network or single servers, now or
 * with the plugin's timers, and who may join meanwhile.
 */
export function MaintenanceSection({ network }: { network: Network }) {
  const { can } = useAccess()
  const manage = can("networks.manage")
  const { data: m, isPending, error } = useQuery(maintenanceQuery(network.id))
  // The list of servers is read more often, so it tells first when the proxy starts or stops.
  const proxy = findServer(useQuery(allServersQuery).data, network.proxy)
  const proxyRunning = proxy ? proxy.state === "running" : !!m?.proxyRunning
  const set = useSetMaintenance(network.id)
  const operation = useOperation()
  const [dialog, setDialog] = useState<"start" | "abort">()
  const folder = isBungee(network.proxyType) ? "plugins/Maintenance" : "plugins/maintenance"
  const { nodeId, serverId } = network.proxy
  const timers = manage && proxyRunning && !!m?.installed

  function change(c: MaintenanceChange) {
    const name = c.server || network.name
    const [delay, duration] = [c.delay ?? 0, c.duration ?? 0]
    let title = c.enabled ? t("Turn on maintenance of {{name}}", { name }) : t("Turn off maintenance of {{name}}", { name })
    let message = c.enabled ? t("Maintenance of {{name}} is on", { name }) : t("Maintenance of {{name}} is off", { name })
    if (c.enabled && delay > 0) {
      title = t("Plan maintenance of {{name}}", { name })
      message = duration
        ? t("Maintenance of {{name}} starts in {{time}} and lasts {{duration}}", { name, time: minutes(delay), duration: minutes(duration) })
        : t("Maintenance of {{name}} starts in {{time}}", { name, time: minutes(delay) })
    } else if (c.enabled && duration > 0) {
      message = t("Maintenance of {{name}} is on for {{duration}}", { name, duration: minutes(duration) })
    } else if (delay > 0) {
      title = t("Plan the end of maintenance of {{name}}", { name })
      message = t("Maintenance of {{name}} ends in {{time}}", { name, time: minutes(delay) })
    }
    operation.run((onStart) => set.mutateAsync({ ...c, onStart }), { title, notify: true, done: () => ({ message }) })
  }

  return (
    <Section
      title={t("Maintenance")}
      description={t("Only the team may join, and the server list shows that the network is in maintenance.")}
      className="mt-0 mb-10"
      actions={
        manage &&
        m && (
          <div className="flex gap-2">
            <Button
              variant={m.enabled ? "default" : "outline"}
              disabled={set.isPending || !proxyRunning}
              title={proxyRunning ? undefined : t("Start the proxy first.")}
              onClick={() => (m.enabled ? change({ enabled: false }) : setDialog("start"))}
            >
              <WrenchIcon />
              {m.enabled ? t("End maintenance") : t("Start maintenance…")}
            </Button>
            {timers && (
              <DropdownMenu>
                <DropdownMenuTrigger asChild>
                  <Button variant="outline" size="icon" aria-label={t("More maintenance actions")} title={t("More actions")}>
                    <DotsThreeIcon weight="bold" />
                  </Button>
                </DropdownMenuTrigger>
                <DropdownMenuContent align="end" className="w-56">
                  {m.enabled && (
                    <>
                      {delays.slice(1).map((delay) => (
                        <DropdownMenuItem key={delay} disabled={set.isPending} onSelect={() => change({ enabled: false, delay })}>
                          <TimerIcon />
                          {t("End in {{time}}", { time: minutes(delay) })}
                        </DropdownMenuItem>
                      ))}
                      <DropdownMenuSeparator />
                    </>
                  )}
                  <DropdownMenuItem onSelect={() => setDialog("abort")}>
                    <XIcon />
                    {t("Abort a timer…")}
                  </DropdownMenuItem>
                </DropdownMenuContent>
              </DropdownMenu>
            )}
          </div>
        )
      }
    >
      {isPending ? (
        <Skeleton className="h-24 rounded-xl" />
      ) : error ? (
        <ErrorCallout error={error} />
      ) : (
        <div className="surface grid gap-4 rounded-xl p-5">
          <div className="flex flex-wrap items-center gap-3 text-sm">
            <Pill tone={m.enabled ? "warning" : "neutral"}>{m.enabled ? t("In maintenance") : t("Open to all players")}</Pill>
            {m.enabled && m.endsAt && <span className="text-muted-foreground">{t("Ends {{time}}", { time: formatAgo(m.endsAt) })}</span>}
            {!proxyRunning && <span className="text-muted-foreground">{t("The proxy isn't running.")}</span>}
            {!m.installed && proxyRunning && (
              <span className="text-muted-foreground">{t("The first time, the panel installs the Maintenance plugin on the proxy.")}</span>
            )}
          </div>
          {m.servers.length > 0 && (
            <div className="grid gap-2">
              <p className="text-sm font-medium">{t("Servers in maintenance")}</p>
              <div className="flex flex-wrap gap-2">
                {m.servers.map((name) => (
                  <span key={name} className="inline-flex items-center gap-1 rounded-md bg-warning/10 py-1 pr-1 pl-2 font-mono text-xs font-medium">
                    {name}
                    {timers && (
                      <Button
                        variant="ghost"
                        size="icon-xs"
                        aria-label={t("End maintenance of {{name}}", { name })}
                        title={t("End maintenance of {{name}}", { name })}
                        disabled={set.isPending}
                        onClick={() => change({ enabled: false, server: name })}
                      >
                        <XIcon />
                      </Button>
                    )}
                  </span>
                ))}
              </div>
            </div>
          )}
          {m.installed && (
            <>
              <Team network={network} players={m.players} editable={manage && proxyRunning} />
              {can("files.read", nodeId, serverId) && (
                <p className="text-sm text-muted-foreground">
                  <Link
                    to="/nodes/$nodeId/servers/$serverId/files"
                    params={{ nodeId, serverId }}
                    search={{ path: folder, edit: `${folder}/config.yml` }}
                    className="inline-flex items-center gap-1.5 font-medium text-foreground hover:underline"
                  >
                    <FileTextIcon />
                    {t("Change the texts")}
                  </Link>{" "}
                  {t("The message in the server list and the one for players who may not join are in the plugin's config.yml.")}
                </p>
              )}
              <p className="text-sm text-muted-foreground">
                {t(
                  "Timers count down in the chat. The plugin keeps them to itself, so the panel shows what they change once they end, and a new timer replaces the one that runs.",
                )}
              </p>
            </>
          )}
        </div>
      )}
      {dialog === "start" && m && <StartDialog network={network} m={m} onClose={() => setDialog(undefined)} onStart={change} />}
      {dialog === "abort" && <AbortDialog network={network} onClose={() => setDialog(undefined)} />}
    </Section>
  )
}

/** Starts maintenance of the network or one of its servers, now or later, until it is ended or for a while. */
function StartDialog({
  network,
  m,
  onClose,
  onStart,
}: {
  network: Network
  m: Maintenance
  onClose: () => void
  onStart: (c: MaintenanceChange) => void
}) {
  const [server, setServer] = useState(whole)
  const [delay, setDelay] = useState(0)
  const [duration, setDuration] = useState(0)
  const one = server !== whole

  function submit(event: FormEvent) {
    event.preventDefault()
    onStart({ enabled: true, server: one ? server : undefined, delay, duration })
    onClose()
  }

  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent className="sm:max-w-lg">
        <form onSubmit={submit} className="grid gap-6">
          <DialogHeader>
            <DialogTitle>{t("Start maintenance of {{name}}", { name: network.name })}</DialogTitle>
            <DialogDescription>
              {m.installed
                ? t("Players who aren't in the team leave and can't join until maintenance ends.")
                : t(
                    "The panel installs the Maintenance plugin from Modrinth on the proxy and restarts the proxy to load it, which disconnects all players once. Then only the team may join.",
                  )}
            </DialogDescription>
          </DialogHeader>
          <FieldGroup>
            <Field>
              <FieldLabel htmlFor="maintenance-server">{t("Servers")}</FieldLabel>
              <Select value={server} onValueChange={setServer}>
                <SelectTrigger id="maintenance-server" className="w-full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value={whole}>{t("The whole network")}</SelectItem>
                  {network.backends.map((b) => (
                    <SelectItem key={b.name} value={b.name} disabled={m.servers.includes(b.name)}>
                      <span className="font-mono">{b.name}</span>
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
              {one && (
                <FieldDescription>
                  {t("The proxy sends the players on {{name}} to the plugin's fallback server, or off the network, and lets only the team join it.", {
                    name: server,
                  })}
                </FieldDescription>
              )}
            </Field>
            <div className="grid gap-4 sm:grid-cols-2">
              <Field>
                <FieldLabel htmlFor="maintenance-delay">{t("Starts")}</FieldLabel>
                <Select value={String(delay)} onValueChange={(v) => setDelay(Number(v))}>
                  <SelectTrigger id="maintenance-delay" className="w-full">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    {delays.map((d) => (
                      <SelectItem key={d} value={String(d)}>
                        {d ? t("In {{time}}", { time: minutes(d) }) : t("Now")}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </Field>
              <Field>
                <FieldLabel htmlFor="maintenance-duration">{t("Lasts")}</FieldLabel>
                <Select value={String(duration)} onValueChange={(v) => setDuration(Number(v))}>
                  <SelectTrigger id="maintenance-duration" className="w-full">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    {durations.map((d) => (
                      <SelectItem key={d} value={String(d)}>
                        {d ? minutes(d) : t("Until it is ended")}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </Field>
            </div>
            {(delay > 0 || duration > 0) && (
              <FieldDescription>{t("The plugin tells the players in the chat when maintenance starts and ends.")}</FieldDescription>
            )}
          </FieldGroup>
          <DialogFooter>
            <DialogClose asChild>
              <Button variant="outline">{t("Cancel")}</Button>
            </DialogClose>
            <Button type="submit">
              <WrenchIcon />
              {delay ? t("Plan maintenance") : t("Start maintenance")}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

/** Aborts the timer of the plugin for the network or one of its servers. */
function AbortDialog({ network, onClose }: { network: Network; onClose: () => void }) {
  const abort = useAbortMaintenanceTimer(network.id)
  const [server, setServer] = useState(whole)

  function submit(event: FormEvent) {
    event.preventDefault()
    const name = server === whole ? network.name : server
    abort.mutate(server === whole ? "" : server, {
      onSuccess: () => {
        toast.success(t("Aborted the timer of {{name}}, if one ran", { name }))
        onClose()
      },
    })
  }

  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent className="sm:max-w-md">
        <form onSubmit={submit} className="grid gap-6">
          <DialogHeader>
            <DialogTitle>{t("Abort a timer")}</DialogTitle>
            <DialogDescription>
              {t("The timer that starts or ends maintenance stops, and maintenance stays as it is. The plugin runs one timer for the network and one for each server.")}
            </DialogDescription>
          </DialogHeader>
          <Field>
            <FieldLabel htmlFor="maintenance-abort">{t("Timer of")}</FieldLabel>
            <Select value={server} onValueChange={setServer}>
              <SelectTrigger id="maintenance-abort" className="w-full">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value={whole}>{t("The whole network")}</SelectItem>
                {network.backends.map((b) => (
                  <SelectItem key={b.name} value={b.name}>
                    <span className="font-mono">{b.name}</span>
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
            {abort.error && <FieldError>{abort.error.message}</FieldError>}
          </Field>
          <DialogFooter>
            <DialogClose asChild>
              <Button variant="outline">{t("Cancel")}</Button>
            </DialogClose>
            <Button type="submit" disabled={abort.isPending}>
              <XIcon />
              {t("Abort timer")}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

/** The players who may join during maintenance. */
function Team({ network, players, editable }: { network: Network; players: { name: string }[]; editable: boolean }) {
  const change = useMaintenancePlayer(network.id)
  const [name, setName] = useState("")

  function add(event: FormEvent) {
    event.preventDefault()
    change.mutate({ name: name.trim(), add: true }, { onSuccess: () => setName("") })
  }

  return (
    <div className="grid gap-2">
      <p className="text-sm font-medium">{t("Team")}</p>
      <div className="flex flex-wrap gap-2">
        {players.length === 0 && (
          <span className="text-sm text-muted-foreground">
            {t("Nobody yet: only players with the permission maintenance.bypass may join.")}
          </span>
        )}
        {players.map((p) => (
          <span key={p.name} className="inline-flex items-center gap-1 rounded-md bg-muted py-1 pr-1 pl-2 font-mono text-xs font-medium">
            {p.name}
            {editable && (
              <Button
                variant="ghost"
                size="icon-xs"
                aria-label={t("Remove {{name}}", { name: p.name })}
                disabled={change.isPending}
                onClick={() => change.mutate({ name: p.name, add: false })}
              >
                <XIcon />
              </Button>
            )}
          </span>
        ))}
      </div>
      {editable && (
        <form onSubmit={add} className="flex max-w-sm gap-2">
          <Input
            aria-label={t("Player")}
            placeholder={t("Name of a player")}
            maxLength={17}
            pattern="\.?[A-Za-z0-9_]{1,16}"
            className="font-mono"
            value={name}
            onChange={(e) => setName(e.target.value)}
          />
          <Button type="submit" variant="outline" disabled={!name.trim() || change.isPending}>
            <PlusIcon />
            {t("Add")}
          </Button>
        </form>
      )}
      {change.error && <FieldError>{change.error.message}</FieldError>}
    </div>
  )
}
