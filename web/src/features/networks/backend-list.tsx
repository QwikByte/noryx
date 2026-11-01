import { CaretDownIcon, CubeIcon, MagnifyingGlassIcon, ShieldWarningIcon, TrashIcon } from "@phosphor-icons/react"
import { useQuery } from "@tanstack/react-query"
import { Link } from "@tanstack/react-router"
import { t } from "i18next"
import { useState } from "react"
import { CopyField } from "@/components/copy-field"
import { IconTile } from "@/components/icon-tile"
import { Section } from "@/components/section"
import { Pill } from "@/components/status"
import { Button } from "@/components/ui/button"
import { Field, FieldDescription, FieldError, FieldLabel } from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import { InputGroup, InputGroupAddon, InputGroupInput } from "@/components/ui/input-group"
import { Switch } from "@/components/ui/switch"
import { nodeQuery } from "@/features/nodes/api"
import type { NodeServer } from "@/features/servers/api"
import { displayVersion, serverLook, serverType } from "@/features/servers/server-types"
import { TagList } from "@/features/servers/tags"
import type { ServerUsage } from "@/features/usage/api"
import { cn } from "@/lib/utils"
import { AddBackendDialog } from "./add-backend-dialog"
import type { Backend, Network } from "./api"
import { BackendActions } from "./backend-actions"
import { type Draft, withoutBackend } from "./draft"
import { nameError } from "./problems"
import { ServerLabel } from "./server-label"
import { findServer, firewallCommand, forwardingMod, isBungee, key, routeOf } from "./servers"

/**
 * The game servers behind the proxy, one row each with the name players use, searchable for
 * networks with many servers. BungeeCord's settings and firewall rules fold out of the rows,
 * and running servers restart safely or send their players elsewhere from their menu.
 */
export function BackendList({
  network,
  draft,
  onChange,
  servers,
  usage,
  proxyHost,
  isPrivate,
  editable,
}: {
  network: Network
  draft: Draft
  onChange: (draft: Draft) => void
  servers?: NodeServer[]
  usage: (b: Backend) => ServerUsage | undefined
  /** The address of the proxy's node, which servers on other nodes must let in. */
  proxyHost?: string
  /** Whether a proxy on node a reaches the servers of node b over the private network. */
  isPrivate: (a: string, b: string) => boolean
  editable: boolean
}) {
  const [search, setSearch] = useState("")
  const [open, setOpen] = useState(new Set<string>())
  const bungee = isBungee(network.proxyType)
  const set = (i: number, change: Partial<Backend>) =>
    onChange({ ...draft, backends: draft.backends.map((b, j) => (i === j ? { ...b, ...change } : b)) })
  const toggle = (k: string) => setOpen((o) => new Set(o.has(k) ? [...o].filter((x) => x !== k) : [...o, k]))
  const words = search.toLowerCase().split(/\s+/).filter(Boolean)
  const rows = draft.backends
    .map((backend, i) => ({ backend, i, server: findServer(servers, backend) }))
    // Rows with a mistake stay, so that it can't hide behind the search.
    .filter(({ backend, i, server }) => {
      const text = [backend.name, server?.name, server?.nodeName, ...(server?.tags ?? []).map((tag) => `#${tag}`)].join(" ").toLowerCase()
      return words.every((w) => text.includes(w)) || !!nameError(draft.backends, i)
    })

  return (
    <Section
      title={t("Servers")}
      description={t("Players switch between them with /server and their name.")}
      actions={editable && <AddBackendDialog network={network} draft={draft} onAdd={onChange} />}
    >
      {draft.backends.length > 5 && (
        <div className="mb-3 flex flex-wrap items-center gap-3">
          <InputGroup className="w-full sm:max-w-xs">
            <InputGroupAddon>
              <MagnifyingGlassIcon />
            </InputGroupAddon>
            <InputGroupInput
              type="search"
              placeholder={t("Search servers")}
              aria-label={t("Search the servers of the network")}
              value={search}
              onChange={(e) => setSearch(e.target.value)}
            />
          </InputGroup>
          {words.length > 0 && (
            <p className="text-xs text-muted-foreground">
              {t("{{shown}} of {{count}} servers", { shown: rows.length, count: draft.backends.length })}
            </p>
          )}
        </div>
      )}
      <ul className="surface divide-y overflow-hidden rounded-xl">
        {rows.length === 0 && <li className="p-6 text-center text-sm text-muted-foreground">{t("No server matches your search.")}</li>}
        {rows.map(({ backend, i, server }) => {
          const k = key(backend)
          const position = draft.try.indexOf(k)
          const hosts = draft.forcedHosts.filter((h) => h.servers.includes(k)).map((h) => h.host)
          const route = routeOf(network, backend, isPrivate)
          const players = usage(backend)?.players
          const mod = server && forwardingMod(server.type)
          const error = nameError(draft.backends, i)
          const firewall = route === "public" && draft.forwarding === "legacy" && server && proxyHost
          const details = bungee || firewall
          // Servers that are only added in the draft can't be acted on yet.
          const saved = network.backends.find((b) => key(b) === k)
          return (
            <li key={k} className="grid gap-3 px-4 py-3 md:grid-cols-[minmax(0,1fr)_15rem_auto] md:items-start">
              <div className="flex min-w-0 items-center gap-3">
                <IconTile {...(server ? serverLook(server.type) : { icon: CubeIcon })} size="sm" />
                <div className="min-w-0">
                  <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
                    <Link
                      to="/nodes/$nodeId/servers/$serverId"
                      params={{ nodeId: backend.nodeId, serverId: backend.serverId }}
                      className="min-w-0 hover:underline"
                    >
                      <ServerLabel server={server} />
                    </Link>
                    {position === 0 && <Pill tone="info">{t("Players join here")}</Pill>}
                    {position > 0 && <Pill tone="neutral">{t("Fallback {{position}}", { position })}</Pill>}
                    {hosts.map((host) => (
                      <Pill key={host} tone="violet">
                        <span className="font-mono">{host}</span>
                      </Pill>
                    ))}
                    {firewall && (
                      <Pill tone="warning">
                        <ShieldWarningIcon className="size-3.5" />
                        {t("Firewall rule")}
                      </Pill>
                    )}
                    {route === "private" && server && !server.overlay && <Pill tone="info">{t("Moves to the private network when applied")}</Pill>}
                    {server && <TagList tags={server.tags} />}
                  </div>
                  <p className="mt-0.5 truncate text-xs text-muted-foreground">
                    {[
                      server && `${serverType(server.type).label} ${displayVersion(server.version)}`,
                      server &&
                        { local: t("internal"), private: t("private network, port {{port}}", { port: server.port }), public: t("port {{port}}", { port: server.port }) }[
                          route
                        ],
                      players && t("{{count}} players", { count: players.online, defaultValue_one: "{{count}} player" }),
                      mod,
                    ]
                      .filter(Boolean)
                      .join(" · ")}
                  </p>
                </div>
              </div>
              <Field data-invalid={!!error}>
                <FieldLabel htmlFor={`backend-${k}`} className="sr-only">
                  {t("Name in the network")}
                </FieldLabel>
                <div className="flex h-8 items-center rounded-md ring-1 ring-input focus-within:ring-2 focus-within:ring-ring">
                  <span className="pl-2.5 font-mono text-xs text-muted-foreground">/server</span>
                  <Input
                    id={`backend-${k}`}
                    value={backend.name}
                    disabled={!editable}
                    aria-invalid={!!error}
                    className="h-8 border-0 font-mono text-sm shadow-none ring-0 focus-visible:ring-0"
                    onChange={(e) => set(i, { name: e.target.value.toLowerCase() })}
                  />
                </div>
                {error && <FieldError>{error}</FieldError>}
              </Field>
              <div className="flex items-center justify-end gap-1">
                {saved && <BackendActions network={network} backend={saved} servers={servers} />}
                {details && (
                  <Button
                    size="icon-sm"
                    variant="ghost"
                    className="text-muted-foreground"
                    aria-expanded={open.has(k)}
                    aria-label={t("Settings of {{name}}", { name: backend.name })}
                    title={t("More settings")}
                    onClick={() => toggle(k)}
                  >
                    <CaretDownIcon className={cn("transition-transform", open.has(k) && "rotate-180")} />
                  </Button>
                )}
                {editable && (
                  <Button
                    size="icon-sm"
                    variant="ghost"
                    className="text-muted-foreground hover:bg-destructive/10 hover:text-destructive"
                    aria-label={t("Remove {{name}} from the network", { name: server?.name ?? backend.name })}
                    title={draft.backends.length === 1 ? t("A network needs at least one server") : t("Remove from the network")}
                    disabled={draft.backends.length === 1}
                    onClick={() => onChange(withoutBackend(draft, k))}
                  >
                    <TrashIcon />
                  </Button>
                )}
              </div>
              {details && open.has(k) && (
                <div className="grid gap-4 rounded-lg bg-muted/40 p-4 md:col-span-3">
                  {bungee && (
                    <>
                      <Field>
                        <FieldLabel htmlFor={`motd-${k}`}>{t("MOTD for its host names")}</FieldLabel>
                        <Input
                          id={`motd-${k}`}
                          value={backend.motd}
                          maxLength={256}
                          placeholder={t("The proxy's MOTD")}
                          disabled={!editable}
                          className="font-mono"
                          onChange={(e) => set(i, { motd: e.target.value })}
                        />
                      </Field>
                      <Field orientation="horizontal">
                        <Switch
                          id={`restricted-${k}`}
                          checked={backend.restricted}
                          disabled={!editable}
                          onCheckedChange={(restricted) => set(i, { restricted })}
                        />
                        <div>
                          <FieldLabel htmlFor={`restricted-${k}`}>{t("Restricted")}</FieldLabel>
                          <FieldDescription>
                            {t("Only players with the permission bungeecord.server.{{name}} may join.", { name: backend.name })}
                          </FieldDescription>
                        </div>
                      </Field>
                    </>
                  )}
                  {firewall && (
                    <div className="space-y-2 rounded-lg bg-warning/10 p-3 text-xs ring-1 ring-warning/20 ring-inset">
                      <p className="flex items-center gap-2 font-medium text-warning">
                        <ShieldWarningIcon className="size-4" />
                        {t("Let only the proxy reach port {{port}} on {{node}}:", { port: server.port, node: server.nodeName })}
                      </p>
                      <FirewallCommand server={server} proxyHost={proxyHost} />
                    </div>
                  )}
                </div>
              )}
            </li>
          )
        })}
      </ul>
    </Section>
  )
}

/** The firewall command for the runtime of the server's node, Docker's until it is known. */
function FirewallCommand({ server, proxyHost }: { server: NodeServer; proxyHost: string }) {
  const runtime = useQuery(nodeQuery(server.nodeId)).data?.info?.runtimeName
  return <CopyField value={firewallCommand(server.port, proxyHost, runtime)} label={t("firewall command")} prefix="#" />
}
