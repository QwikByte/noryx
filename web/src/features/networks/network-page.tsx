import {
  ArrowsSplitIcon,
  CubeIcon,
  DatabaseIcon,
  DeviceMobileIcon,
  GraphIcon,
  HardDrivesIcon,
  HashIcon,
  ShieldCheckIcon,
  SlidersHorizontalIcon,
  UsersThreeIcon,
  WarningCircleIcon,
  WrenchIcon,
} from "@phosphor-icons/react"
import { useQuery } from "@tanstack/react-query"
import { getRouteApi, Link, Outlet } from "@tanstack/react-router"
import { t } from "i18next"
import { BackLink } from "@/components/back-link"
import { Callout, ErrorCallout } from "@/components/callout"
import { Chip } from "@/components/chip"
import { PageHeader } from "@/components/page-header"
import { usePageName } from "@/components/page-title"
import { StatCard, StatStrip } from "@/components/stat-card"
import { Pill } from "@/components/status"
import { TabLink } from "@/components/tab-link"
import { Tabs } from "@/components/tabs"
import { Skeleton } from "@/components/ui/skeleton"
import { useAccess } from "@/features/access/use-access"
import { allServersQuery } from "@/features/servers/api"
import { serverType } from "@/features/servers/server-types"
import { usePrivateRoute } from "@/features/overlay/api"
import { EndOfLifeNotice } from "@/features/servers/software"
import { maintenanceQuery, networkQuery } from "./api"
import { MaintenanceSection } from "./maintenance"
import { NetworkActions } from "./network-actions"
import { NetworkEditor } from "./network-editor"
import { ProxySettingsEditor } from "./proxy-settings"
import { ServerLabel } from "./server-label"
import { findServer, routeOf } from "./servers"
import { ChangeProxyButton } from "./swap-proxy-dialog"
import { playersOnline, useNetworkUsage } from "./usage"

const route = getRouteApi("/_app/networks/$networkId")

/** Header, key figures and tabs of a network; the tabs are child routes. */
export function NetworkPage() {
  const { can, canSomewhere } = useAccess()
  const { networkId } = route.useParams()
  const { data: network, isPending, error } = useQuery(networkQuery(networkId))
  usePageName(network?.name)
  const { data: servers } = useQuery(allServersQuery)
  const usage = useNetworkUsage(network ? [network] : [])
  const { data: maintenance } = useQuery(maintenanceQuery(networkId))
  const isPrivate = usePrivateRoute()

  if (isPending)
    return (
      <>
        <BackLink to="/networks">{t("Networks")}</BackLink>
        <Skeleton className="h-96 rounded-xl" />
      </>
    )
  if (error)
    return (
      <>
        <BackLink to="/networks">{t("Networks")}</BackLink>
        <ErrorCallout error={error} />
      </>
    )
  const proxy = findServer(servers, network.proxy)
  const backends = network.backends.map((b) => findServer(servers, b))
  const running = backends.filter((s) => s && s.state !== "stopped").length
  const players = playersOnline(network, usage)
  // Backends that publish their port publicly, although the proxy can reach them over the private network.
  const ready = network.backends.filter((b) => {
    const server = findServer(servers, b)
    return routeOf(network, b, isPrivate) === "private" && server && !server.overlay
  })

  return (
    <>
      <BackLink to="/networks">{t("Networks")}</BackLink>
      <PageHeader
        icon={GraphIcon}
        tone="violet"
        title={network.name}
        badge={
          maintenance?.enabled && (
            <Pill tone="warning">
              <WrenchIcon weight="bold" />
              {t("In maintenance")}
            </Pill>
          )
        }
        description={
          <span className="mt-1 flex flex-wrap gap-2 text-foreground">
            <Chip icon={ArrowsSplitIcon}>{serverType(network.proxyType).label}</Chip>
            <Chip icon={ShieldCheckIcon}>{network.forwarding === "modern" ? t("Modern forwarding") : t("Legacy forwarding")}</Chip>
            {proxy && <Chip icon={HardDrivesIcon}>{proxy.nodeName}</Chip>}
            {proxy && (
              <Chip icon={HashIcon}>
                <span className="font-mono">{proxy.port}</span>
              </Chip>
            )}
            {network.bedrockPort > 0 && (
              <Chip icon={DeviceMobileIcon}>
                {t("Bedrock")} <span className="font-mono">{network.bedrockPort}</span>
              </Chip>
            )}
          </span>
        }
        actions={<NetworkActions network={network} />}
      />
      <EndOfLifeNotice type={network.proxyType} className="mb-6">
        {can("networks.manage") && (
          <ChangeProxyButton network={network} />
        )}
      </EndOfLifeNotice>
      {ready.length > 0 && (
        <Callout icon={ShieldCheckIcon} title={t("Ready for the private network")} className="mb-6">
          {t("Apply the network again to reach {{names}} over the private network of the nodes instead of public ports. They restart once.", {
            names: ready.map((b) => b.name).join(", "),
          })}
        </Callout>
      )}
      {network.applyError && (
        <Callout tone="warning" icon={WarningCircleIcon} role="alert" title={t("The proxy may send players to the wrong address")} className="mb-6">
          {network.applyError}
        </Callout>
      )}
      <StatStrip className="mb-7 sm:grid-cols-3">
        <StatCard
          to={canSomewhere("servers.view") ? "/players" : undefined}
          search={{ network: network.id, tab: "online" }}
          icon={UsersThreeIcon}
          tone="info"
          label={t("Players online")}
          value={players ?? "–"}
        />
        <StatCard icon={CubeIcon} tone="success" label={t("Servers running")} value={`${running} / ${network.backends.length}`} />
        <StatCard icon={ArrowsSplitIcon} tone="violet" label={t("Proxy")} value={<ProxyLink network={network} />} />
      </StatStrip>
      <Tabs label={t("Network")}>
        <TabLink to="/networks/$networkId" params={{ networkId }} activeOptions={{ exact: true }}>
          <GraphIcon className="size-4" />
          {t("Overview")}
        </TabLink>
        {can("properties.edit", network.proxy.nodeId, network.proxy.serverId) && (
          <TabLink to="/networks/$networkId/proxy" params={{ networkId }}>
            <SlidersHorizontalIcon className="size-4" />
            {t("Proxy configuration")}
          </TabLink>
        )}
        {can("datastores.view") && (
          <TabLink to="/networks/$networkId/databases" params={{ networkId }}>
            <DatabaseIcon className="size-4" />
            {t("Databases")}
          </TabLink>
        )}
      </Tabs>
      <Outlet />
    </>
  )
}

function ProxyLink({ network }: { network: { proxy: { nodeId: string; serverId: string } } }) {
  const { data: servers } = useQuery(allServersQuery)
  return (
    <Link
      to="/nodes/$nodeId/servers/$serverId"
      params={{ nodeId: network.proxy.nodeId, serverId: network.proxy.serverId }}
      className="text-base hover:underline"
    >
      <ServerLabel server={findServer(servers, network.proxy)} />
    </Link>
  )
}

/** The Overview tab: where players go, the servers and the forwarding. */
export function NetworkOverview() {
  const { networkId } = route.useParams()
  const { data: network } = useQuery(networkQuery(networkId))
  if (!network) return null
  return (
    <>
      <MaintenanceSection network={network} />
      <NetworkEditor network={network} />
    </>
  )
}

/** The Proxy configuration tab: the settings of the proxy in its own file. */
export function NetworkProxy() {
  const { networkId } = route.useParams()
  const { data: network } = useQuery(networkQuery(networkId))
  const { data: servers } = useQuery(allServersQuery)
  if (!network) return null
  const proxy = findServer(servers, network.proxy)
  if (proxy === null) return <Skeleton className="h-96 rounded-xl" />
  return <ProxySettingsEditor proxy={network.proxy} type={network.proxyType} running={!!proxy && proxy.state !== "stopped"} />
}
