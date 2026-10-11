import { CheckCircleIcon, CircleNotchIcon, ListChecksIcon, ProhibitIcon, XCircleIcon } from "@phosphor-icons/react"
import { useQuery } from "@tanstack/react-query"
import { Link } from "@tanstack/react-router"
import { t } from "i18next"
import { type ReactNode, useState } from "react"
import { Button } from "@/components/ui/button"
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle, SheetTrigger } from "@/components/ui/sheet"
import { allServersQuery, type Move, movesQuery } from "@/features/servers/api"
import { formatAgo } from "@/lib/format"
import { useNow } from "@/lib/use-now"
import { type Operation, operationsQuery, useLiveOperations } from "./api"
import { CancelButton } from "./cancel-button"
import { titleOf } from "./labels"
import { OperationLine } from "./operation-progress"

const movePhases = ["stopping", "copying", "backups", "finishing"]

/** A server moving to another node, as an operation. */
function fromMove(m: Move): Operation {
  const step = m.phase === "done" || m.phase === "failed" ? movePhases.length - 1 : movePhases.indexOf(m.phase)
  return {
    id: `move/${m.serverId}/${m.startedAt}`,
    kind: "server.move",
    subject: t("{{name}} to {{node}}", { name: m.serverName, node: m.toName }),
    nodeId: m.phase === "done" ? m.to : m.from,
    serverId: m.serverId,
    user: "",
    steps: movePhases,
    step,
    ...(m.phase === "backups" ? { done: m.backups, total: m.backupsTotal, unit: "backups" } : { done: m.bytes, total: 0, unit: "bytes" }),
    error: m.error,
    startedAt: m.startedAt,
    finishedAt: m.finishedAt,
  }
}

/**
 * The operations in progress and those that ended recently, as long as the settings say, behind a button that shows
 * how many run. The ones this browser follows show up at once, the others with the next update.
 */
export function Activity() {
  const [open, setOpen] = useState(false)
  const { data: listed = [] } = useQuery(operationsQuery)
  const { data: moves = [] } = useQuery(movesQuery)
  const { data: servers } = useQuery({ ...allServersQuery, enabled: open })
  const live = useLiveOperations()
  const byId = new Map([...listed, ...live].map((op) => [op.id, op]))
  const ops = [...byId.values(), ...moves.map(fromMove)].sort((a, b) => Date.parse(b.startedAt) - Date.parse(a.startedAt))
  const running = ops.filter((op) => !op.finishedAt)
  // Renders again now and then, so that the times stay current while the list is open.
  useNow(open, 30_000)
  const nameOf = (op: Operation) => servers?.find((s) => s.nodeId === op.nodeId && s.id === op.serverId)?.name
  const label =
    running.length > 0
      ? t("{{count}} operations in progress", { count: running.length, defaultValue_one: "{{count}} operation in progress" })
      : t("Operations")

  return (
    <Sheet open={open} onOpenChange={setOpen}>
      <SheetTrigger asChild>
        <Button variant="ghost" size="icon-sm" aria-label={label} title={label} className="relative shrink-0 text-muted-foreground">
          {running.length > 0 ? (
            <CircleNotchIcon className="animate-spin text-primary motion-reduce:animate-none" weight="bold" />
          ) : (
            <ListChecksIcon />
          )}
          {running.length > 0 && (
            <span
              aria-hidden
              className="absolute -top-0.5 -right-0.5 grid h-4 min-w-4 place-items-center rounded-full bg-primary px-1 text-[0.625rem] font-bold text-primary-foreground tabular-nums"
            >
              {running.length}
            </span>
          )}
        </Button>
      </SheetTrigger>
      <SheetContent side="right" className="w-full gap-0 overflow-y-auto sm:max-w-md">
        <SheetHeader className="border-b">
          <SheetTitle>{t("Operations")}</SheetTitle>
          <SheetDescription>{t("What's running now, and what ran recently.")}</SheetDescription>
        </SheetHeader>
        {ops.length === 0 ? (
          <p className="p-6 text-center text-sm text-muted-foreground">
            {t("Nothing is running. Long-running actions, such as creating a server, show up here with their progress.")}
          </p>
        ) : (
          <ul className="grid gap-2 p-4">
            {ops.map((op) => (
              <li
                key={op.id}
                className="relative flex items-start gap-3 rounded-xl p-3 ring-1 ring-border transition-colors has-[a:hover]:bg-muted/50"
              >
                <StateIcon op={op} />
                <div className="min-w-0 flex-1 space-y-1">
                  <p className="truncate text-sm font-medium">
                    <Target op={op} onNavigate={() => setOpen(false)}>
                      {titleOf(op, nameOf(op))}
                    </Target>
                  </p>
                  <p className="truncate text-xs text-muted-foreground">
                    {[op.user, formatAgo(op.startedAt)].filter(Boolean).join(" · ")}
                  </p>
                  {!op.finishedAt && (
                    <div className="pt-1">
                      <OperationLine op={op} />
                    </div>
                  )}
                  {op.cancelled && op.finishedAt ? (
                    <p className="text-xs text-muted-foreground">{t("Cancelled")}</p>
                  ) : (
                    op.error && <p className="line-clamp-2 text-xs text-destructive">{op.error}</p>
                  )}
                </div>
                {/* Above the link, which covers the whole item. */}
                <CancelButton op={op} size="sm" className="relative" />
              </li>
            ))}
          </ul>
        )}
      </SheetContent>
    </Sheet>
  )
}

function StateIcon({ op }: { op: Operation }) {
  if (!op.finishedAt)
    return (
      <CircleNotchIcon
        aria-label={t("In progress")}
        className="mt-0.5 size-5 shrink-0 animate-spin text-primary motion-reduce:animate-none"
        weight="bold"
      />
    )
  if (op.cancelled) return <ProhibitIcon aria-label={t("Cancelled")} className="mt-0.5 size-5 shrink-0 text-muted-foreground" weight="bold" />
  if (op.error) return <XCircleIcon aria-label={t("Failed")} className="mt-0.5 size-5 shrink-0 text-destructive" weight="fill" />
  return <CheckCircleIcon aria-label={t("Done")} className="mt-0.5 size-5 shrink-0 text-success" weight="fill" />
}

/** Leads to what an operation is about, its server or network, from anywhere in its item. */
function Target({ op, onNavigate, children }: { op: Operation; onNavigate: () => void; children: ReactNode }) {
  const className =
    "outline-none after:absolute after:inset-0 after:rounded-xl focus-visible:after:ring-2 focus-visible:after:ring-ring"
  if (op.nodeId && op.serverId) {
    return (
      <Link
        to="/nodes/$nodeId/servers/$serverId"
        params={{ nodeId: op.nodeId, serverId: op.serverId }}
        onClick={onNavigate}
        className={className}
      >
        {children}
      </Link>
    )
  }
  if (op.networkId && op.kind !== "network.delete") {
    return (
      <Link to="/networks/$networkId" params={{ networkId: op.networkId }} onClick={onNavigate} className={className}>
        {children}
      </Link>
    )
  }
  return children
}
