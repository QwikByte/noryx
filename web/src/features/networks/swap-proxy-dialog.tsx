import { ArrowsLeftRightIcon } from "@phosphor-icons/react"
import { useQuery } from "@tanstack/react-query"
import { t } from "i18next"
import { type FormEvent, useState } from "react"
import { Callout } from "@/components/callout"
import { guard } from "@/components/form-dialog"
import { Button } from "@/components/ui/button"
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { Field, FieldError, FieldLabel } from "@/components/ui/field"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { nodesQuery } from "@/features/nodes/api"
import { OperationStatus } from "@/features/operations/operation-status"
import { usePrivateRoute } from "@/features/overlay/api"
import { useOperation } from "@/features/operations/use-operation"
import { allServersQuery, type NodeServer } from "@/features/servers/api"
import { serverType } from "@/features/servers/server-types"
import { type Forwarding, maintenanceQuery, type Network, networksQuery, type ServerRef, useSwapProxy } from "./api"
import { FirewallConfirmation, ForwardingChoice } from "./forwarding"
import { availableServers, findServer, hostOf, isBungee, joinAddress, key, proxyTypes, refOf, routeOf } from "./servers"

/** Gives a network another proxy, e.g. Velocity instead of BungeeCord, and tells first what that does. */
export function SwapProxyDialog({ network, onClose }: { network: Network; onClose: () => void }) {
  const [proxy, setProxy] = useState<ServerRef>()
  const [forwarding, setForwarding] = useState<Forwarding>("modern")
  const [firewalled, setFirewalled] = useState(false)
  const { data: servers } = useQuery(allServersQuery)
  const { data: networks } = useQuery(networksQuery)
  const swap = useSwapProxy(network.id)
  const operation = useOperation()
  const chosen = proxy && findServer(servers, proxy)
  const proxies = availableServers(servers, networks, (s) => proxyTypes.includes(s.type))
  const isPrivate = usePrivateRoute()
  const exposed = forwarding === "legacy" && !!proxy && network.backends.some((b) => routeOf({ ...network, proxy }, b, isPrivate) === "public")
  const title = t("Change the proxy of {{name}}", { name: network.name })

  function submit(event: FormEvent) {
    event.preventDefault()
    if (!proxy) return
    operation.run((onStart) => swap.mutateAsync({ proxy, forwarding, firewalled, onStart }), {
      title,
      done: () => ({ message: t("{{name}} has another proxy", { name: network.name }) }),
      then: onClose,
    })
  }

  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent className="sm:max-w-2xl" {...guard(swap.isPending)}>
        {operation.live ? (
          <OperationStatus
            op={operation.live}
            title={title}
            onBackground={() => {
              operation.background(title)
              onClose()
            }}
            onBack={() => {
              operation.reset()
              swap.reset()
            }}
          />
        ) : (
          <form onSubmit={submit} className="grid gap-6">
            <DialogHeader>
              <DialogTitle>{t("Change proxy")}</DialogTitle>
              <DialogDescription>
                {t("The network keeps its servers, where players go and its Bedrock port. Players connect to the new proxy instead.")}
              </DialogDescription>
            </DialogHeader>
            <Field>
              <FieldLabel htmlFor="swap-proxy">{t("New proxy")}</FieldLabel>
              <Select
                value={proxy ? key(proxy) : ""}
                onValueChange={(v) => {
                  const server = proxies.find((s) => key(refOf(s)) === v)
                  if (!server) return
                  setProxy(refOf(server))
                  setForwarding(isBungee(server.type) ? "legacy" : "modern")
                }}
                disabled={proxies.length === 0}
              >
                <SelectTrigger id="swap-proxy" className="w-full">
                  <SelectValue
                    placeholder={proxies.length === 0 ? t("No free proxy; create a Velocity or BungeeCord server first") : t("Choose a proxy")}
                  />
                </SelectTrigger>
                <SelectContent>
                  {proxies.map((s) => (
                    <SelectItem key={key(refOf(s))} value={key(refOf(s))}>
                      {s.name}
                      <span className="text-muted-foreground">
                        {serverType(s.type).label} · {t("{{node}} · port {{port}}", { node: s.nodeName, port: s.port })}
                      </span>
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </Field>
            {chosen && (
              <>
                <ForwardingChoice value={forwarding} bungee={isBungee(chosen.type)} onChange={setForwarding} />
                {exposed && <FirewallConfirmation checked={firewalled} onChange={setFirewalled} proxyNode={chosen.nodeName} />}
                <SwapEffects network={network} old={findServer(servers, network.proxy)} proxy={chosen} forwarding={forwarding} isPrivate={isPrivate} />
              </>
            )}
            {swap.error && <FieldError>{swap.error.message}</FieldError>}
            <DialogFooter>
              <Button type="submit" disabled={!chosen || (exposed && !firewalled) || swap.isPending}>
                <ArrowsLeftRightIcon />
                {t("Change proxy")}
              </Button>
            </DialogFooter>
          </form>
        )}
      </DialogContent>
    </Dialog>
  )
}

/** Tells what changing the proxy does: what the new one takes over, which servers restart and where players join. */
function SwapEffects({
  network,
  old,
  proxy,
  forwarding,
  isPrivate,
}: {
  network: Network
  old?: NodeServer | null
  proxy: NodeServer
  forwarding: Forwarding
  isPrivate: (a: string, b: string) => boolean
}) {
  const { data: maintenance } = useQuery(maintenanceQuery(network.id))
  const { data: nodes } = useQuery(nodesQuery)
  const host = hostOf(nodes?.find((n) => n.id === proxy.nodeId)?.address)
  // Game servers restart for another forwarding, and when the proxy reaches them another way, for which they publish their port.
  const swapped = { ...network, proxy: refOf(proxy) }
  const restart = network.backends.filter(
    (b) => forwarding !== network.forwarding || routeOf(network, b, isPrivate) !== routeOf(swapped, b, isPrivate),
  )
  const oldName = old?.name ?? t("The old proxy")
  const sameConfig = isBungee(network.proxyType) === isBungee(proxy.type)
  const effects = [
    sameConfig
      ? t("{{new}} takes over the settings in {{file}} of {{old}}.", {
          new: proxy.name,
          old: oldName,
          file: isBungee(proxy.type) ? "config.yml" : "velocity.toml",
        })
      : t("{{new}} keeps its own settings, as {{software}} has another configuration.", {
          new: proxy.name,
          software: serverType(network.proxyType).label,
        }),
    maintenance?.installed &&
      (maintenance.enabled
        ? t("The Maintenance plugin moves along with who may join, and maintenance stays on.")
        : t("The Maintenance plugin moves along with who may join.")),
    network.bedrockPort > 0 && t("Geyser and Floodgate move along, and Bedrock players join at port {{port}}.", { port: network.bedrockPort }),
    restart.length > 0 && t("Restarts: {{names}}.", { names: restart.map((b) => b.name).join(", ") }),
    t("{{old}} leaves the network and stops, which disconnects its players. Its other plugins stay with it.", { old: oldName }),
    proxy.state !== "stopped"
      ? t("{{new}} restarts to load what it takes over.", { new: proxy.name })
      : old && old.state !== "stopped" && t("{{new}} starts once the servers are configured.", { new: proxy.name }),
    host && t("Players join at {{address}} from now on.", { address: joinAddress(host, proxy.port) }),
    old &&
      old.nodeId === proxy.nodeId &&
      old.port !== proxy.port &&
      t("To keep port {{port}}, delete {{old}} afterwards and give {{new}} that port in its settings.", {
        port: old.port,
        old: old.name,
        new: proxy.name,
      }),
  ].filter(Boolean)

  return (
    <Callout title={t("What happens")}>
      <ul className="list-disc space-y-1 pl-4">
        {effects.map((e) => (
          <li key={String(e)}>{e}</li>
        ))}
      </ul>
    </Callout>
  )
}
