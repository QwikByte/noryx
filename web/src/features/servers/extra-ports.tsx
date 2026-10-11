import { ArrowSquareOutIcon, PlugIcon, PlusIcon, XIcon } from "@phosphor-icons/react"
import { useQuery } from "@tanstack/react-query"
import { t } from "i18next"
import { Chip } from "@/components/chip"
import { CopyButton } from "@/components/copy-button"
import { Button } from "@/components/ui/button"
import { FieldDescription, FieldLegend, FieldSet } from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { useAccess } from "@/features/access/use-access"
import { networksQuery } from "@/features/networks/api"
import { hostOf, joinAddress, key } from "@/features/networks/servers"
import { nodeQuery } from "@/features/nodes/api"
import type { ExtraPort, Server } from "./api"

/** As many extra ports as the agent publishes for a server. */
const maxExtraPorts = 5

const protocols: ExtraPort["protocol"][] = ["tcp", "udp"]

/**
 * The extra ports of a server, e.g. of a web map or a voice chat, which its node publishes at the same port in the container,
 * where the plugin listens. Given a server, it warns if the server is part of a network, as they are public all the same.
 */
export function ExtraPortsField({
  value,
  onChange,
  server,
}: {
  value: ExtraPort[]
  onChange: (ports: ExtraPort[]) => void
  server?: { nodeId: string; serverId: string }
}) {
  const { can } = useAccess()
  const { data: networks } = useQuery({ ...networksQuery, enabled: !!server && can("networks.view") })
  const network = server && networks?.find((n) => n.backends.some((b) => key(b) === key(server)))
  const change = (i: number, next: Partial<ExtraPort>) => onChange(value.map((p, j) => (j === i ? { ...p, ...next } : p)))
  return (
    <FieldSet>
      <FieldLegend variant="label">{t("Extra ports")}</FieldLegend>
      {value.map((p, i) => (
        // On phones, the port, its protocol and the button to remove it share a line, and the label takes one.
        <div key={i} className="grid grid-cols-[minmax(0,1fr)_5.5rem_auto] items-center gap-2 sm:grid-cols-[6rem_5.5rem_minmax(0,1fr)_auto]">
          <Button
            type="button"
            size="icon-sm"
            variant="ghost"
            aria-label={t("Remove extra port {{number}}", { number: i + 1 })}
            className="col-start-3 row-start-1 sm:col-start-4"
            onClick={() => onChange(value.filter((_, j) => j !== i))}
          >
            <XIcon />
          </Button>
          <Input
            type="number"
            min={1024}
            max={65535}
            required
            aria-label={t("Extra port {{number}}", { number: i + 1 })}
            className="font-mono"
            value={p.port || ""}
            onChange={(e) => change(i, { port: e.target.valueAsNumber || 0 })}
          />
          <Select value={p.protocol} onValueChange={(protocol) => change(i, { protocol: protocol as ExtraPort["protocol"] })}>
            <SelectTrigger aria-label={t("Protocol of extra port {{number}}", { number: i + 1 })} className="w-full">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {protocols.map((protocol) => (
                <SelectItem key={protocol} value={protocol}>
                  {protocol.toUpperCase()}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          <Input
            aria-label={t("Label of extra port {{number}}", { number: i + 1 })}
            placeholder={t("e.g. Map")}
            maxLength={32}
            className="col-span-3 sm:col-span-1"
            value={p.label}
            onChange={(e) => change(i, { label: e.target.value })}
          />
        </div>
      ))}
      {value.length < maxExtraPorts && (
        <Button type="button" variant="ghost" size="sm" className="self-start" onClick={() => onChange([...value, { port: 0, protocol: "tcp", label: "" }])}>
          <PlusIcon />
          {t("Add port")}
        </Button>
      )}
      <FieldDescription>
        {t("Ports of plugins, e.g. of a web map, a voice chat or Votifier. The node publishes each at the same port of the server, where its plugin must listen, and anyone on the internet reaches it, like the server's port.")}
        {network &&
          ` ${t("That includes this server of {{network}}, which players otherwise only reach through the proxy.", { network: network.name })}`}
      </FieldDescription>
    </FieldSet>
  )
}

/** The extra ports of a server at the address of its node, to copy, and TCP ports to open, e.g. a web map; only their ports without the permission to see the node. */
export function ExtraPortChips({ nodeId, server }: { nodeId: string; server: Server }) {
  const { can } = useAccess()
  const { data: node } = useQuery({ ...nodeQuery(nodeId), enabled: server.extraPorts.length > 0 && can("nodes.view", nodeId) })
  const host = hostOf(node?.address)
  return server.extraPorts.map(({ port, protocol, label }) => {
    const address = host ? joinAddress(host, port) : String(port)
    const name = label || t("Port {{port}}", { port })
    return (
      <Chip key={`${port}/${protocol}`} icon={PlugIcon} title={`${name} · ${protocol.toUpperCase()}`} className={host ? "py-0 pr-0.5" : undefined}>
        {label}
        <span className="font-mono text-muted-foreground">{address}</span>
        {host && <CopyButton value={address} label={name} />}
        {host && protocol === "tcp" && (
          <Button asChild size="icon-xs" variant="ghost" className="text-muted-foreground">
            <a href={`http://${address}`} target="_blank" rel="noreferrer" aria-label={t("Open {{name}}", { name })} title={t("Open {{name}}", { name })}>
              <ArrowSquareOutIcon />
            </a>
          </Button>
        )}
      </Chip>
    )
  })
}
