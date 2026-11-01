import {
  ArrowsClockwiseIcon,
  LinkIcon,
  PencilSimpleIcon,
  ShieldCheckIcon,
  ShieldWarningIcon,
  SignOutIcon,
  WarningIcon,
} from "@phosphor-icons/react"
import { useQuery } from "@tanstack/react-query"
import { t } from "i18next"
import { type FormEvent, type ReactNode, useState } from "react"
import { Trans } from "react-i18next"
import { toast } from "sonner"
import { Callout, ErrorCallout } from "@/components/callout"
import { ConfirmDialog } from "@/components/confirm-dialog"
import { Section } from "@/components/section"
import { type Status, StatusBadge } from "@/components/status"
import { Button } from "@/components/ui/button"
import { Dialog, DialogClose, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { Field, FieldDescription, FieldError, FieldLabel } from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import { Skeleton } from "@/components/ui/skeleton"
import { useAccess } from "@/features/access/use-access"
import type { Node } from "@/features/nodes/api"
import { useOperation } from "@/features/operations/use-operation"
import { formatDate } from "@/lib/format"
import { msg } from "@/lib/i18n"
import { cn } from "@/lib/utils"
import {
  type FirewallState,
  type NodeOverlay,
  nodeOverlayQuery,
  overlayQuery,
  useJoinOverlay,
  useLeaveOverlay,
  useRotateOverlayKey,
  useSetOverlayEndpoint,
} from "./api"
import { PeerTable, PublishedPorts } from "./overlay-ports"

const firewallStatus: Record<FirewallState, Status> = {
  in_place: { tone: "success", label: msg("In place") },
  missing: { tone: "destructive", label: msg("Missing") },
  incomplete: { tone: "warning", label: msg("Incomplete") },
}

/** The class of commands in texts. */
const code = "rounded bg-muted px-1.5 py-0.5 font-mono text-xs"

/** A node's part in the private network of the nodes: whether it may and can join, its address and its peers. */
export function OverlaySection({ node }: { node: Node }) {
  const manage = useAccess().can("overlay.manage")
  const { data, error } = useQuery(nodeOverlayQuery(node.id))
  return (
    <Section
      title={t("Private network")}
      description={t("Proxies reach the servers of other members over WireGuard, at ports that only they reach.")}
      actions={data?.member && manage && <MemberActions node={node} overlay={data} />}
    >
      {error ? (
        <ErrorCallout error={error} />
      ) : !data ? (
        <Skeleton className="h-32 rounded-xl" />
      ) : !data.allowed ? (
        <Callout icon={ShieldCheckIcon} title={t("Not allowed on this node")}>
          <Trans
            i18nKey="Its administrator allows it with <command/> on the node."
            components={{ command: <code className={code}>noryx-agent overlay allow</code> }}
          />
        </Callout>
      ) : data.unsupported ? (
        <Callout tone="warning" icon={WarningIcon} title={t("{{name}} can't join", { name: node.name })}>
          {data.unsupported}
        </Callout>
      ) : data.member ? (
        <Membership nodeId={node.id} overlay={data} manage={manage} />
      ) : (
        <JoinForm node={node} manage={manage} />
      )}
    </Section>
  )
}

/** Lets a node join, at the address where the other nodes reach it. */
function JoinForm({ node, manage }: { node: Node; manage: boolean }) {
  const [endpoint, setEndpoint] = useState("")
  const join = useJoinOverlay(node.id)
  const { data: overlay } = useQuery(overlayQuery)
  const host = node.address?.slice(0, node.address.lastIndexOf(":"))

  function submit(event: FormEvent) {
    event.preventDefault()
    join.mutate(endpoint.trim(), { onSuccess: (m) => toast.success(t("{{name}} joined as {{address}}", { name: node.name, address: m.address })) })
  }

  return (
    <form onSubmit={submit} className="surface grid gap-4 rounded-xl p-5">
      <p className="text-sm text-muted-foreground">
        {t("Not part of it yet. Joining restarts nothing: networks move to it when they are applied again.")}
      </p>
      {manage && (
        <div className="flex flex-wrap items-end gap-3">
          <Field className="min-w-60 flex-1">
            <FieldLabel htmlFor="overlay-endpoint">{t("Endpoint")}</FieldLabel>
            <Input
              id="overlay-endpoint"
              className="font-mono"
              value={endpoint}
              placeholder={host && overlay ? `${host}:${overlay.port}` : "203.0.113.10:51820"}
              onChange={(e) => setEndpoint(e.target.value)}
            />
            <FieldDescription>{t("Where the other nodes reach it; empty for the host of its agent's address.")}</FieldDescription>
          </Field>
          <Button type="submit" disabled={join.isPending} className="mb-6">
            <LinkIcon />
            {join.isPending ? t("Joining…") : t("Join")}
          </Button>
        </div>
      )}
      {join.error && <FieldError>{join.error.message}</FieldError>}
    </form>
  )
}

/** The address, key and firewall of a member, how it reaches the others, and the ports it publishes for them. */
function Membership({ nodeId, overlay, manage }: { nodeId: string; overlay: NodeOverlay; manage: boolean }) {
  const member = overlay.member!
  const firewall = overlay.firewall && firewallStatus[overlay.firewall]
  const facts: [string, ReactNode][] = [
    [t("Address"), member.address],
    [t("Endpoint"), overlay.endpoint ?? "–"],
    [t("Key"), overlay.fingerprint ? `${overlay.fingerprint}…` : "–"],
    [t("Joined"), formatDate(member.joinedAt)],
  ]
  if (firewall) facts.push([t("Firewall"), <StatusBadge key="firewall" status={firewall} className="font-sans" />])
  return (
    <div className="space-y-4">
      {member.problem && (
        <Callout tone="warning" icon={WarningIcon} role="alert" title={t("Not configured")}>
          {member.problem}
        </Callout>
      )}
      {overlay.firewall && overlay.firewall !== "in_place" && (
        <Callout
          tone={overlay.firewall === "missing" ? "destructive" : "warning"}
          icon={ShieldWarningIcon}
          role="alert"
          title={overlay.firewall === "missing" ? t("Its firewall rules are missing") : t("Its firewall rules are incomplete")}
        >
          {overlay.firewallProblem && <p className="font-mono text-xs">{overlay.firewallProblem}</p>}
          <p>
            <Trans
              i18nKey="Until they are back, members may reach the node itself and ports it publishes for others. The master writes them again within 5 minutes, or right away with <command/> on the node."
              components={{ command: <code className={code}>noryx-agent overlay up</code> }}
            />
          </p>
        </Callout>
      )}
      <dl className={cn("surface grid grid-cols-2 gap-x-6 gap-y-4 rounded-xl px-5 py-4", firewall ? "md:grid-cols-5" : "md:grid-cols-4")}>
        {facts.map(([term, value]) => (
          <div key={term} className="min-w-0">
            <dt className="text-xs text-muted-foreground">{term}</dt>
            <dd className="mt-0.5 truncate font-mono text-sm" title={typeof value === "string" ? value : undefined}>
              {value}
            </dd>
          </div>
        ))}
      </dl>
      {overlay.peers.length === 0 ? (
        <p className="text-sm text-muted-foreground">{t("No other node is part of it yet.")}</p>
      ) : (
        <PeerTable nodeId={nodeId} peers={overlay.peers} manage={manage} />
      )}
      <PublishedPorts nodeId={nodeId} published={overlay.published} />
    </div>
  )
}

/** Changes where the others reach a member, rotates its key, or removes it. */
function MemberActions({ node, overlay }: { node: Node; overlay: NodeOverlay }) {
  const [editing, setEditing] = useState(false)
  const rotate = useRotateOverlayKey(node.id)
  const leave = useLeaveOverlay(node.id)
  const operation = useOperation()
  return (
    <div className="flex flex-wrap gap-2">
      <Button variant="outline" onClick={() => setEditing(true)}>
        <PencilSimpleIcon />
        {t("Endpoint")}
      </Button>
      <ConfirmDialog
        trigger={
          <Button variant="outline" disabled={rotate.isPending}>
            <ArrowsClockwiseIcon />
            {t("Rotate key")}
          </Button>
        }
        title={t("Rotate the key of {{name}}?", { name: node.name })}
        description={t(
          "The node creates a new key, the others get its public key, and the networks with servers on it and on other nodes are applied again. Until then, which takes a few seconds, they don't reach it.",
        )}
        action={t("Rotate key")}
        onConfirm={() =>
          operation.run((onStart) => rotate.mutateAsync({ onStart }), {
            title: t("Rotating the key of {{name}}…", { name: node.name }),
            notify: true,
            done: () => ({ message: t("Rotated the key of {{name}}", { name: node.name }) }),
          })
        }
      />
      <ConfirmDialog
        trigger={
          <Button variant="outline" className="text-destructive hover:bg-destructive/10 hover:text-destructive" disabled={leave.isPending}>
            <SignOutIcon />
            {t("Remove")}
          </Button>
        }
        title={t("Remove {{name}} from the private network?", { name: node.name })}
        description={t("Networks then reach its servers, and it reaches other servers, over public ports again: these servers restart once.")}
        action={t("Remove")}
        destructive
        onConfirm={() =>
          operation.run((onStart) => leave.mutateAsync({ onStart }), {
            title: t("Removing {{name}} from the private network…", { name: node.name }),
            notify: true,
            done: () => ({ message: t("Removed {{name}} from the private network", { name: node.name }) }),
          })
        }
      />
      {editing && <EndpointDialog node={node} current={overlay.member?.endpoint ?? ""} onClose={() => setEditing(false)} />}
    </div>
  )
}

function EndpointDialog({ node, current, onClose }: { node: Node; current: string; onClose: () => void }) {
  const [endpoint, setEndpoint] = useState(current)
  const save = useSetOverlayEndpoint(node.id)

  function submit(event: FormEvent) {
    event.preventDefault()
    save.mutate(endpoint.trim(), { onSuccess: onClose })
  }

  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent className="sm:max-w-lg">
        <form onSubmit={submit} className="grid gap-6">
          <DialogHeader>
            <DialogTitle>{t("Endpoint of {{name}}", { name: node.name })}</DialogTitle>
            <DialogDescription>{t("Where the other nodes reach it, as host:port; empty for the host of its agent's address.")}</DialogDescription>
          </DialogHeader>
          <Field>
            <FieldLabel htmlFor="overlay-endpoint-edit">{t("Endpoint")}</FieldLabel>
            <Input id="overlay-endpoint-edit" autoFocus className="font-mono" value={endpoint} onChange={(e) => setEndpoint(e.target.value)} />
            {save.error && <FieldError>{save.error.message}</FieldError>}
          </Field>
          <DialogFooter>
            <DialogClose asChild>
              <Button variant="outline">{t("Cancel")}</Button>
            </DialogClose>
            <Button type="submit" disabled={save.isPending}>
              {t("Save")}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
