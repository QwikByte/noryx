import { useQuery } from "@tanstack/react-query"
import { t } from "i18next"
import { type FormEvent, useState } from "react"
import { toast } from "sonner"
import { Button } from "@/components/ui/button"
import { Checkbox } from "@/components/ui/checkbox"
import { Dialog, DialogClose, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { Field, FieldContent, FieldDescription, FieldError, FieldGroup, FieldLabel } from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { useAccess } from "@/features/access/use-access"
import { nodeQuery, nodesQuery } from "@/features/nodes/api"
import { ApiError } from "@/lib/api"
import { type Server, useMoveServer } from "./api"

/** Moves a server with its data, and its backups if chosen, to another node. */
export function MoveServerDialog({
  nodeId,
  server,
  open,
  onOpenChange,
}: {
  nodeId: string
  server: Server
  open: boolean
  onOpenChange: (open: boolean) => void
}) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-md">
        <MoveServerForm nodeId={nodeId} server={server} onClose={() => onOpenChange(false)} />
      </DialogContent>
    </Dialog>
  )
}

/** The node to move to, chosen anew each time the dialog opens. */
function MoveServerForm({ nodeId, server, onClose }: { nodeId: string; server: Server; onClose: () => void }) {
  const { can } = useAccess()
  const { data: nodes = [] } = useQuery(nodesQuery)
  const { data: node } = useQuery(nodeQuery(nodeId))
  const [form, setForm] = useState<{ node?: string; port?: number; storage?: string; backups: boolean; withoutDatabases?: boolean }>({
    backups: true,
  })
  const move = useMoveServer(nodeId, server.id)
  const targets = nodes.filter((n) => n.id !== nodeId && n.status === "online" && can("servers.create", n.id))
  const target = targets.find((n) => n.id === form.node) ?? targets[0]
  const values = {
    node: target?.id ?? "",
    port: form.port ?? server.port,
    storage: form.storage ?? target?.defaultStorage ?? "default",
    backups: form.backups,
    withoutDatabases: form.withoutDatabases,
  }
  // The new node doesn't reach the databases of the server's network, which the master asks to confirm.
  const unreachable = move.error instanceof ApiError && move.error.code === "datastores-unreachable"

  function submit(event: FormEvent) {
    event.preventDefault()
    move.mutate(values, {
      onSuccess: () => {
        toast.success(t("Moving {{name}} to {{node}}", { name: server.name, node: target?.name }), {
          description: t("It is offline while its files are copied."),
        })
        onClose()
      },
    })
  }

  return (
    <form onSubmit={submit} className="grid gap-6">
      <DialogHeader>
        <DialogTitle>{t("Move {{name}}", { name: server.name })}</DialogTitle>
        <DialogDescription>
          {t(
            "The server stops while its files are copied through the master, and starts on the new node if it is running now. Players then join it at the new node's address.",
          )}
        </DialogDescription>
      </DialogHeader>
      {targets.length === 0 ? (
        <p className="text-sm text-muted-foreground">{t("There is no other online node on which you may create servers.")}</p>
      ) : (
        <FieldGroup>
          <Field>
            <FieldLabel htmlFor="move-node">{t("Node")}</FieldLabel>
            <Select value={values.node} onValueChange={(id) => setForm({ ...form, node: id, storage: undefined })}>
              <SelectTrigger id="move-node" className="w-full">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {targets.map((n) => (
                  <SelectItem key={n.id} value={n.id}>
                    {n.name}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </Field>
          <div className="grid gap-5 sm:grid-cols-2">
            <Field>
              <FieldLabel htmlFor="move-port">{t("Port")}</FieldLabel>
              <Input
                id="move-port"
                type="number"
                min={1024}
                max={65535}
                required
                className="font-mono"
                value={values.port}
                onChange={(e) => setForm({ ...form, port: e.target.valueAsNumber || 0 })}
              />
            </Field>
            <Field>
              <FieldLabel htmlFor="move-storage">{t("Storage location")}</FieldLabel>
              <Select value={values.storage} onValueChange={(storage) => setForm({ ...form, storage })}>
                <SelectTrigger id="move-storage" className="w-full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {(target?.info?.storage ?? []).map((l) => (
                    <SelectItem key={l.name} value={l.name}>
                      {l.name}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </Field>
          </div>
          <Field orientation="horizontal">
            <Checkbox id="move-backups" checked={values.backups} onCheckedChange={(v) => setForm({ ...form, backups: v === true })} />
            <FieldContent>
              <FieldLabel htmlFor="move-backups">{t("Move the backups too")}</FieldLabel>
              <FieldDescription>
                {node
                  ? t("Otherwise they are deleted with the server on {{node}}.", { node: node.name })
                  : t("Otherwise they are deleted with the server on this node.")}
              </FieldDescription>
            </FieldContent>
          </Field>
          <FieldDescription>{t("If the server is part of a network, the network is updated and its proxy restarts.")}</FieldDescription>
          {move.error && <FieldError>{move.error.message}</FieldError>}
          {unreachable && (
            <Field orientation="horizontal">
              <Checkbox
                id="move-without-databases"
                checked={!!values.withoutDatabases}
                onCheckedChange={(v) => setForm({ ...form, withoutDatabases: v === true })}
              />
              <FieldContent>
                <FieldLabel htmlFor="move-without-databases">{t("Move it anyway")}</FieldLabel>
                <FieldDescription>{t("Its plugins can't reach the databases of the network from the new node.")}</FieldDescription>
              </FieldContent>
            </Field>
          )}
        </FieldGroup>
      )}
      <DialogFooter>
        <DialogClose asChild>
          <Button variant="outline">{t("Cancel")}</Button>
        </DialogClose>
        <Button type="submit" disabled={move.isPending || !target}>
          {move.isPending ? t("Checking…") : t("Move server")}
        </Button>
      </DialogFooter>
    </form>
  )
}
