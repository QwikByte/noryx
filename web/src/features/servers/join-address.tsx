import { GlobeIcon, HashIcon } from "@phosphor-icons/react"
import { useQuery } from "@tanstack/react-query"
import { t } from "i18next"
import { Chip } from "@/components/chip"
import { CopyButton } from "@/components/copy-button"
import { useAccess } from "@/features/access/use-access"
import { networksQuery } from "@/features/networks/api"
import { hostOf, joinAddress, key } from "@/features/networks/servers"
import { nodeQuery } from "@/features/nodes/api"
import { type Server, serversQuery } from "./api"
import { isBedrock, serverPorts } from "./server-types"

/**
 * The address players join a server at, to copy: the host of its node's address and its port,
 * or its proxy's for a server of a network. Only the server's port without the permissions to
 * see networks and that node, as the address depends on both.
 */
export function JoinAddress({ nodeId, server }: { nodeId: string; server: Server }) {
  const { can } = useAccess()
  const ref = { nodeId, serverId: server.id }
  const { data: networks } = useQuery({ ...networksQuery, enabled: can("networks.view") })
  const network = networks?.find((n) => n.backends.some((b) => key(b) === key(ref)))
  const at = network?.proxy ?? ref
  const known = !!networks && can("nodes.view", at.nodeId)
  const { data: node } = useQuery({ ...nodeQuery(at.nodeId), enabled: known })
  const { data: servers } = useQuery({ ...serversQuery(at.nodeId), enabled: known })
  const host = known ? hostOf(node?.address) : undefined
  const target = servers?.find((s) => s.id === at.serverId)

  if (!host || !target)
    return (
      <Chip icon={HashIcon}>
        <span className="font-mono">{server.port}</span>
      </Chip>
    )
  const address = joinAddress(host, target.port)
  return (
    <Chip
      icon={GlobeIcon}
      title={
        network
          ? t("Players join through the proxy of {{network}}", { network: network.name })
          : isBedrock(server.type)
            ? t("Players join at this address and play over the UDP ports {{first}} to {{last}}", { first: server.port, last: serverPorts(server).at(-1) })
            : t("Players join at this address")
      }
      className="py-0 pr-0.5"
    >
      <span className="font-mono">{address}</span>
      <CopyButton value={address} label={t("Address")} />
    </Chip>
  )
}
