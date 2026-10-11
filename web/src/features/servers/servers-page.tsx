import { CubeIcon, WifiSlashIcon } from "@phosphor-icons/react"
import { useQuery } from "@tanstack/react-query"
import { getRouteApi, Link } from "@tanstack/react-router"
import { t } from "i18next"
import { Callout, ErrorCallout } from "@/components/callout"
import { EmptyState } from "@/components/empty-state"
import { PageHeader } from "@/components/page-header"
import { Button } from "@/components/ui/button"
import { Skeleton } from "@/components/ui/skeleton"
import { useAccess } from "@/features/access/use-access"
import { nodesQuery } from "@/features/nodes/api"
import { allServersQuery } from "./api"
import { CreateServerDialog } from "./create-server-dialog"
import { ServerBrowser } from "./server-browser"

const route = getRouteApi("/_app/servers")

/** The servers the user may see on all nodes, to search, filter, group and act on at once. */
export function ServersPage() {
  const { data: servers, isPending, error } = useQuery(allServersQuery)
  const { data: nodes } = useQuery(nodesQuery)
  const search = route.useSearch()
  const navigate = route.useNavigate()
  const { canSomewhere } = useAccess()
  const create = canSomewhere("servers.create") && <CreateServerDialog />
  const offline = nodes?.filter((n) => n.status === "offline").map((n) => n.name) ?? []

  return (
    <>
      <PageHeader icon={CubeIcon} title={t("Servers")} actions={servers && servers.length > 0 && create} />
      {offline.length > 0 && (
        <Callout tone="warning" icon={WifiSlashIcon} title={t("Some nodes are offline")} className="mb-6">
          {t("The servers on {{nodes}} aren't listed until they are back.", {
            nodes: offline.join(", "),
            count: offline.length,
            defaultValue_one: "The servers on {{nodes}} aren't listed until it is back.",
          })}
        </Callout>
      )}
      {isPending ? (
        <Skeleton className="h-44 rounded-xl" />
      ) : error ? (
        <ErrorCallout error={error} />
      ) : servers.length === 0 ? (
        // Servers on offline nodes may exist; the notice above tells about them. Without a node, servers
        // can't be created yet.
        nodes &&
        offline.length === 0 &&
        (nodes.length > 0 && create ? (
          <EmptyState icon={CubeIcon} title={t("No servers yet")}>
            {create}
          </EmptyState>
        ) : (
          <EmptyState icon={CubeIcon} title={t("No servers yet")} description={t("Open a node to create a game server or a proxy on it.")}>
            <Button asChild>
              <Link to="/nodes">{t("Go to the nodes")}</Link>
            </Button>
          </EmptyState>
        ))
      ) : (
        <ServerBrowser
          servers={servers}
          search={search}
          onSearch={(change) => navigate({ search: (s) => ({ ...s, ...change }), replace: true })}
          views
        />
      )}
    </>
  )
}
