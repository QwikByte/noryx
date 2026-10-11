import { CaretRightIcon, GraphIcon } from "@phosphor-icons/react"
import { Link } from "@tanstack/react-router"
import { t } from "i18next"
import { motion } from "motion/react"
import type { ReactNode } from "react"
import { IconTile } from "@/components/icon-tile"
import { Meter } from "@/components/meter"
import { Checkbox } from "@/components/ui/checkbox"
import { SortableHead, Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table"
import { PinButton } from "@/features/preferences/pin-button"
import type { ServerUsage } from "@/features/usage/api"
import { formatCores, formatNumber } from "@/features/usage/format"
import { formatBytes, formatMegabytes } from "@/lib/format"
import { rise } from "@/lib/motion"
import type { Sorting } from "@/lib/sort"
import { cn } from "@/lib/utils"
import { type NodeServer, serverKey } from "./api"
import { type Column, columns as columnLabels, type Facts, type Group, type Sort, sorts } from "./browse"
import { ServerActions } from "./server-actions"
import { ServerStateBadge } from "./server-state"
import { displayVersion, memoryTitle, serverLook, serverType } from "./server-types"
import { TagList } from "./tags"

/** How the servers of a list are shown and selected. */
export interface ViewProps {
  groups: Group[]
  facts: Facts
  sorting: Sorting<Sort>
  /** Whether servers of several nodes are listed, which then show their node. */
  showNode: boolean
  /** The columns the table shows besides the server and its state. */
  columns: Column[]
  selected: (s: NodeServer) => boolean
  onSelect: (servers: NodeServer[], selected: boolean) => void
  collapsed: (group: Group) => boolean
  onCollapse: (group: Group) => void
}

const live = (facts: Facts, s: NodeServer) => {
  const usage = facts.usage(s)
  return usage?.running ? usage : undefined
}

function ServerLink({ server, className }: { server: NodeServer; className?: string }) {
  return (
    <Link to="/nodes/$nodeId/servers/$serverId" params={{ nodeId: server.nodeId, serverId: server.id }} className={className}>
      {server.name}
    </Link>
  )
}

/** Selects all servers of a group, or some of them. */
function SelectAll({
  servers,
  selected,
  onSelect,
  label,
}: Pick<ViewProps, "selected" | "onSelect"> & { servers: NodeServer[]; label: string }) {
  const count = servers.filter(selected).length
  return (
    <Checkbox
      aria-label={label}
      checked={count > 0 && count === servers.length ? true : count > 0 ? "indeterminate" : false}
      onCheckedChange={(on) => onSelect(servers, on === true)}
    />
  )
}

/** The heading of a group, which folds it away. */
function GroupHeading({
  group,
  facts,
  collapsed,
  onCollapse,
  children,
}: {
  group: Group
  facts: Facts
  collapsed: boolean
  onCollapse: () => void
  children: ReactNode
}) {
  const running = group.servers.filter((s) => s.state !== "stopped").length
  const players = group.servers.reduce((sum, s) => sum + (live(facts, s)?.players?.online ?? 0), 0)
  return (
    <div className="flex items-center gap-3">
      {children}
      <button
        type="button"
        aria-expanded={!collapsed}
        onClick={onCollapse}
        className="flex min-w-0 flex-1 items-center gap-2 rounded-md text-left outline-none focus-visible:ring-2 focus-visible:ring-ring"
      >
        <CaretRightIcon
          className={cn("size-3.5 shrink-0 text-muted-foreground transition-transform", !collapsed && "rotate-90")}
          weight="bold"
        />
        <span className="truncate text-sm font-semibold">{group.label}</span>
        <span className="text-xs whitespace-nowrap text-muted-foreground">
          {t("{{running}} of {{count}} running", { running, count: group.servers.length })}
          {players > 0 && ` · ${t("{{count}} players", { count: players, defaultValue_one: "{{count}} player" })}`}
        </span>
      </button>
    </div>
  )
}

/** Cards of servers, each opening its server, in groups that fold away. */
export function ServerGrid({ groups, facts, showNode, selected, onSelect, collapsed, onCollapse }: ViewProps) {
  return (
    <div className="space-y-8">
      {groups.map((group) => (
        <section key={group.key} aria-label={group.label || undefined}>
          {!group.label && (
            <label className="mb-3 inline-flex items-center gap-2 text-sm text-muted-foreground">
              <SelectAll servers={group.servers} selected={selected} onSelect={onSelect} label={t("Select all servers")} />
              {t("Select all")}
            </label>
          )}
          {group.label && (
            <div className="mb-3">
              <GroupHeading group={group} facts={facts} collapsed={collapsed(group)} onCollapse={() => onCollapse(group)}>
                <SelectAll
                  servers={group.servers}
                  selected={selected}
                  onSelect={onSelect}
                  label={t("Select the servers of {{group}}", { group: group.label })}
                />
              </GroupHeading>
            </div>
          )}
          {!collapsed(group) && (
            <ul className="grid grid-cols-1 gap-4 sm:grid-cols-2 xl:grid-cols-3">
              {group.servers.map((server, i) => (
                <ServerCard
                  key={serverKey(server)}
                  index={i}
                  server={server}
                  facts={facts}
                  showNode={showNode}
                  selected={selected(server)}
                  onSelect={(on) => onSelect([server], on)}
                />
              ))}
            </ul>
          )}
        </section>
      ))}
    </div>
  )
}

/** A fact of a server card, e.g. its port, with its label above it. */
function Fact({ label, children, title }: { label: string; children: ReactNode; title?: string }) {
  return (
    <div className="min-w-0" title={title}>
      <dt className="eyebrow text-muted-foreground">{label}</dt>
      <dd className="mt-0.5 truncate text-sm tabular-nums">{children}</dd>
    </div>
  )
}

/**
 * The whole card opens the server; its buttons sit above the link. Its facts are labelled, and running servers
 * show what they use.
 */
function ServerCard({
  index,
  server,
  facts,
  showNode,
  selected,
  onSelect,
}: {
  index: number
  server: NodeServer
  facts: Facts
  showNode: boolean
  selected: boolean
  onSelect: (selected: boolean) => void
}) {
  const usage = live(facts, server)
  const network = facts.network(server)
  const look = serverLook(server.type)
  return (
    <motion.li
      {...rise(index)}
      className={cn(
        "group surface lift relative flex flex-col rounded-xl hover:ring-primary/40",
        selected && "ring-2 ring-primary/60 hover:ring-primary/60",
      )}
    >
      <div className="flex items-start gap-3 p-4 pb-3">
        <IconTile icon={look.icon} tone={look.tone} />
        <div className="min-w-0 flex-1">
          <ServerLink
            server={server}
            className="line-clamp-2 font-semibold break-words outline-none after:absolute after:inset-0 after:rounded-xl focus-visible:after:ring-2 focus-visible:after:ring-ring"
          />
          <p className="truncate text-xs text-muted-foreground">
            {serverType(server.type).label} {displayVersion(server.version)}
          </p>
        </div>
        {/* Shows on hover, and always once pinned or without a pointer that hovers. */}
        <PinButton
          nodeId={server.nodeId}
          server={server}
          size="icon-xs"
          className="relative z-10 -my-0.5 opacity-0 transition-opacity group-focus-within:opacity-100 group-hover:opacity-100 aria-pressed:opacity-100 pointer-coarse:opacity-100"
        />
        <Checkbox
          checked={selected}
          aria-label={t("Select {{name}}", { name: server.name })}
          onCheckedChange={(on) => onSelect(on === true)}
          className="relative z-10 mt-0.5"
        />
      </div>
      <div className="flex min-w-0 flex-wrap items-center gap-2 px-4">
        <ServerStateBadge server={server} nodeId={server.nodeId} />
        {network && (
          <span className="inline-flex min-w-0 items-center gap-1.5 text-xs text-muted-foreground">
            <GraphIcon className="size-3.5 shrink-0" />
            <span className="truncate">{network.name}</span>
          </span>
        )}
      </div>
      <dl className="grid grid-cols-3 gap-x-4 gap-y-3 px-4 pt-4 pb-3">
        {showNode && <Fact label={t("Node")}>{server.nodeName}</Fact>}
        <Fact label={t("Port")}>
          <span className="font-mono text-[0.8125rem]">{server.port}</span>
        </Fact>
        {/* While it runs, its memory of the container's limit, which includes what Java needs besides the heap. */}
        <Fact
          label={t("Memory")}
          title={usage?.memoryLimitBytes ? `${formatBytes(usage.memoryBytes)} / ${formatBytes(usage.memoryLimitBytes)}` : memoryTitle(server)}
        >
          {usage?.memoryLimitBytes ? (
            <>
              {formatBytes(usage.memoryBytes)}
              <Meter value={usage.memoryBytes / usage.memoryLimitBytes} label={t("Memory used")} className="mt-1 h-1" />
            </>
          ) : (
            formatMegabytes(server.memoryMb)
          )}
        </Fact>
        {usage && <Fact label={t("CPU")}>{formatCores(usage.cpuMillis)}</Fact>}
        {usage?.players && (
          <Fact label={t("Players")}>
            {usage.players.online} <span className="text-muted-foreground">/ {usage.players.max}</span>
          </Fact>
        )}
      </dl>
      <TagList tags={server.tags} className="px-4 pb-3" />
      <div className="relative z-10 mt-auto border-t px-4 py-3 empty:hidden">
        <ServerActions nodeId={server.nodeId} server={server} />
      </div>
    </motion.li>
  )
}

const none = <span className="text-muted-foreground">–</span>

/**
 * How each column shows a server, and the classes of its cells and header, e.g. where narrow tables leave it out:
 * the type, version, port and tags below 4xl, where they show with the name instead.
 */
const cells: Record<Column, { className: string; show: (s: NodeServer, usage: ServerUsage | undefined, facts: Facts) => ReactNode }> = {
  node: { className: "text-muted-foreground @max-3xl:hidden", show: (s) => s.nodeName },
  network: { className: "text-muted-foreground @max-5xl:hidden", show: (s, _, facts) => facts.network(s)?.name ?? "–" },
  type: { className: "text-muted-foreground @max-4xl:hidden", show: (s) => serverType(s.type).label },
  version: { className: "text-muted-foreground @max-4xl:hidden", show: (s) => displayVersion(s.version) },
  port: { className: "text-muted-foreground @max-4xl:hidden", show: (s) => <span className="font-mono">{s.port}</span> },
  tags: { className: "@max-4xl:hidden", show: (s) => <TagList tags={s.tags} /> },
  players: {
    className: "text-right tabular-nums",
    show: (_, usage) => (usage?.players ? `${usage.players.online} / ${usage.players.max}` : none),
  },
  tps: { className: "text-right tabular-nums @max-2xl:hidden", show: (_, usage) => (usage?.tps === undefined ? none : formatNumber(usage.tps)) },
  cpu: { className: "text-right tabular-nums @max-xl:hidden", show: (_, usage) => (usage ? formatCores(usage.cpuMillis) : none) },
  memory: {
    className: "text-right tabular-nums @max-xl:hidden",
    show: (s, usage) => (usage ? formatBytes(usage.memoryBytes) : <span className="text-muted-foreground">{formatMegabytes(s.memoryMb)}</span>),
  },
}

const sortable = (column: Column): column is Column & Sort => column in sorts

/**
 * A compact table of servers, for many of them, in groups that fold away. It shows the chosen columns, the node
 * only if servers of several nodes are listed and the network only if some server has one; the type, version, port
 * and tags of servers show with their name where they have no column.
 */
export function ServerTable({ groups, facts, sorting, showNode, columns, selected, onSelect, collapsed, onCollapse }: ViewProps) {
  const all = groups.flatMap((g) => g.servers)
  const showNetwork = all.some((s) => facts.network(s))
  const shown = columns.filter((c) => (c !== "node" || showNode) && (c !== "network" || showNetwork))
  // Hides what shows with the name once these all have columns, as long as the table is wide enough for them.
  const inColumns = (...these: Column[]) => (these.every((c) => shown.includes(c)) ? "@4xl:hidden" : undefined)
  return (
    <div className="surface @container overflow-hidden rounded-xl">
      <Table>
        <TableHeader>
          <TableRow className="hover:bg-transparent">
            <TableHead className="w-10 pl-4">
              <SelectAll servers={[...new Set(all)]} selected={selected} onSelect={onSelect} label={t("Select all servers")} />
            </TableHead>
            <SortableHead sorting={sorting} column="name">{t("Server")}</SortableHead>
            <SortableHead sorting={sorting} column="state">{t("State")}</SortableHead>
            {shown.map((c) =>
              sortable(c) ? (
                <SortableHead key={c} sorting={sorting} column={c} className={cells[c].className}>
                  {t(columnLabels[c])}
                </SortableHead>
              ) : (
                <TableHead key={c} className={cells[c].className}>
                  {t(columnLabels[c])}
                </TableHead>
              ),
            )}
            <TableHead className="w-0">
              <span className="sr-only">{t("Actions")}</span>
            </TableHead>
          </TableRow>
        </TableHeader>
        {groups.map((group) => (
          <TableBody key={group.key}>
            {group.label && (
              <TableRow className="bg-muted/40 hover:bg-muted/40">
                <TableCell colSpan={4 + shown.length} className="py-2 pl-4">
                  <GroupHeading group={group} facts={facts} collapsed={collapsed(group)} onCollapse={() => onCollapse(group)}>
                    <SelectAll
                      servers={group.servers}
                      selected={selected}
                      onSelect={onSelect}
                      label={t("Select the servers of {{group}}", { group: group.label })}
                    />
                  </GroupHeading>
                </TableCell>
              </TableRow>
            )}
            {!collapsed(group) &&
              group.servers.map((server) => {
                const usage = live(facts, server)
                const look = serverLook(server.type)
                return (
                  <TableRow key={serverKey(server)} data-state={selected(server) ? "selected" : undefined}>
                    <TableCell className="pl-4">
                      <Checkbox
                        checked={selected(server)}
                        aria-label={t("Select {{name}}", { name: server.name })}
                        onCheckedChange={(on) => onSelect([server], on === true)}
                      />
                    </TableCell>
                    <TableCell className="max-w-72 min-w-48 whitespace-normal">
                      <div className="flex items-center gap-3">
                        <IconTile icon={look.icon} tone={look.tone} size="sm" />
                        <div className="min-w-0">
                          <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
                            <ServerLink server={server} className="truncate font-semibold hover:underline" />
                            <TagList tags={server.tags} className={inColumns("tags")} />
                          </div>
                          <p className={cn("truncate text-xs text-muted-foreground", inColumns("type", "version", "port"))}>
                            <span className={inColumns("type")}>{serverType(server.type).label} </span>
                            <span className={inColumns("version")}>{displayVersion(server.version)}</span>
                            <span className={inColumns("port")}>
                              <span className={inColumns("type", "version")}> · </span>
                              <span className="font-mono">{server.port}</span>
                            </span>
                          </p>
                        </div>
                      </div>
                    </TableCell>
                    <TableCell>
                      <ServerStateBadge server={server} nodeId={server.nodeId} />
                    </TableCell>
                    {shown.map((c) => (
                      <TableCell key={c} className={cells[c].className}>
                        {cells[c].show(server, usage, facts)}
                      </TableCell>
                    ))}
                    <TableCell className="pr-3">
                      <ServerActions nodeId={server.nodeId} server={server} compact pin />
                    </TableCell>
                  </TableRow>
                )
              })}
          </TableBody>
        ))}
      </Table>
    </div>
  )
}
