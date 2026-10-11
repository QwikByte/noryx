import { HardDrivesIcon } from "@phosphor-icons/react"
import { Link } from "@tanstack/react-router"
import { t } from "i18next"
import { IconTile } from "@/components/icon-tile"
import { Meter } from "@/components/meter"
import { SortableHead, Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table"
import { assignedMemoryMb, runningCount } from "@/features/servers/api"
import { formatCores, formatNumber } from "@/features/usage/format"
import { formatBytes, formatMegabytes } from "@/lib/format"
import type { Sorting } from "@/lib/sort"
import { memoryCapacityMb, type Node } from "./api"
import { cpuLoad, memoryLoad, type NodeFacts, type NodeSort } from "./browse"
import { NodeStatusBadge } from "./node-status"

const none = <span className="text-muted-foreground">–</span>

/** A share of a node's CPU or memory as a meter, with what it is. */
function Load({ value, label, detail }: { value: number; label: string; detail: string }) {
  return (
    <div className="ml-auto w-36 space-y-1">
      <p className="text-right tabular-nums">{detail}</p>
      <Meter value={value} label={label} />
    </div>
  )
}

/** A compact table of nodes, for many of them: their state, servers, what they use now and the memory assigned. */
export function NodeTable({ nodes, facts, sorting }: { nodes: Node[]; facts: NodeFacts; sorting: Sorting<NodeSort> }) {
  return (
    <div className="surface @container overflow-hidden rounded-xl">
      <Table>
        <TableHeader>
          <TableRow className="hover:bg-transparent">
            <SortableHead sorting={sorting} column="name" className="pl-4">
              {t("Node")}
            </SortableHead>
            <SortableHead sorting={sorting} column="state">{t("State")}</SortableHead>
            <SortableHead sorting={sorting} column="servers" className="text-right">{t("Servers")}</SortableHead>
            <SortableHead sorting={sorting} column="cpu" className="text-right @max-xl:hidden">{t("CPU")}</SortableHead>
            <SortableHead sorting={sorting} column="memory" className="text-right @max-xl:hidden">{t("Memory")}</SortableHead>
            <TableHead className="text-right @max-4xl:hidden">{t("Memory assigned")}</TableHead>
            <TableHead className="@max-3xl:hidden">{t("Agent")}</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {nodes.map((node) => {
            const usage = facts.usage(node)
            const servers = facts.servers(node)
            const capacityMb = memoryCapacityMb(node)
            return (
              <TableRow key={node.id}>
                <TableCell className="max-w-72 min-w-48 pl-4 whitespace-normal">
                  <div className="flex items-center gap-3">
                    <IconTile icon={HardDrivesIcon} tone="info" size="sm" />
                    <div className="min-w-0">
                      <Link to="/nodes/$nodeId" params={{ nodeId: node.id }} className="block truncate font-semibold hover:underline">
                        {node.name}
                      </Link>
                      {node.address && <p className="truncate font-mono text-xs text-muted-foreground">{node.address}</p>}
                    </div>
                  </div>
                </TableCell>
                <TableCell>
                  <NodeStatusBadge status={node.status} />
                </TableCell>
                <TableCell className="text-right tabular-nums">{servers ? `${runningCount(servers)} / ${servers.length}` : none}</TableCell>
                <TableCell className="text-xs @max-xl:hidden">
                  {usage ? (
                    <Load
                      value={cpuLoad(usage) ?? 0}
                      label={t("CPU used")}
                      detail={t("{{used}} of {{total}}", { used: formatNumber(usage.cpuMillis / 1000), total: formatCores(usage.cpuCount * 1000) })}
                    />
                  ) : (
                    <p className="text-right">{none}</p>
                  )}
                </TableCell>
                <TableCell className="text-xs @max-xl:hidden">
                  {usage ? (
                    <Load
                      value={memoryLoad(usage) ?? 0}
                      label={t("Memory used")}
                      detail={t("{{used}} of {{total}}", { used: formatBytes(usage.memoryUsedBytes), total: formatBytes(usage.memoryTotalBytes) })}
                    />
                  ) : (
                    <p className="text-right">{none}</p>
                  )}
                </TableCell>
                <TableCell className="text-right tabular-nums @max-4xl:hidden">
                  {servers && capacityMb !== undefined
                    ? t("{{used}} of {{total}}", { used: formatMegabytes(assignedMemoryMb(servers)), total: formatMegabytes(capacityMb) })
                    : none}
                </TableCell>
                <TableCell className="text-muted-foreground @max-3xl:hidden">{node.info?.agentVersion ?? "–"}</TableCell>
              </TableRow>
            )
          })}
        </TableBody>
      </Table>
    </div>
  )
}
