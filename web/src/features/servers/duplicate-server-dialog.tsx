import { useQuery } from "@tanstack/react-query"
import { useNavigate } from "@tanstack/react-router"
import { t } from "i18next"
import { type FormEvent, useState } from "react"
import { FormDialogContent, useBusy } from "@/components/form-dialog"
import { Button } from "@/components/ui/button"
import { Checkbox } from "@/components/ui/checkbox"
import { Dialog, DialogClose, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { Field, FieldDescription, FieldError, FieldGroup, FieldLabel } from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import { useAccess } from "@/features/access/use-access"
import { copyName, key, useNetworkOf } from "@/features/networks/servers"
import { nodeQuery } from "@/features/nodes/api"
import { OperationStatus } from "@/features/operations/operation-status"
import { useOperation } from "@/features/operations/use-operation"
import { type Server, serversQuery, useDuplicateServer } from "./api"
import { nextName, serverPorts, serverType, suggestPort, usedPorts } from "./server-types"

/** Copies a server with all its data into a new server on the same node; the copy of a game server of a network can join it. */
export function DuplicateServerDialog({
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
      <FormDialogContent className="sm:max-w-md">
        <DuplicateServerForm nodeId={nodeId} server={server} onClose={() => onOpenChange(false)} />
      </FormDialogContent>
    </Dialog>
  )
}

/** The name and port of the copy, chosen anew each time the dialog opens. */
function DuplicateServerForm({ nodeId, server, onClose }: { nodeId: string; server: Server; onClose: () => void }) {
  const { data: node } = useQuery(nodeQuery(nodeId))
  const { data: servers = [] } = useQuery(serversQuery(nodeId))
  const [name, setName] = useState<string>()
  const [port, setPort] = useState<number>()
  const [join, setJoin] = useState(false)
  const duplicate = useDuplicateServer(nodeId)
  const operation = useOperation()
  const navigate = useNavigate()
  const { can } = useAccess()
  const ref = { nodeId, serverId: server.id }
  const network = useNetworkOf()(ref)
  // A game server of a network, whose copy may join the network, as which.
  const backend = can("networks.manage") ? network?.backends.find((b) => key(b) === key(ref)) : undefined
  const joined = network && backend && copyName(backend.name, network.backends.map((b) => b.name))
  const saves = server.state !== "stopped" && !serverType(server.type).proxy
  const values = {
    name:
      name ??
      nextName(
        server.name,
        servers.map((s) => s.name),
      ),
    port:
      port ??
      suggestPort(
        usedPorts(servers),
        server.port + 1,
        node?.portMin ?? undefined,
        node?.portMax ?? undefined,
        serverPorts(server).length, // a Bedrock server's players take ports after its own
      ),
    network: join && !!joined,
  }
  useBusy(duplicate.isPending)

  const title = t("Create {{name}} as a copy", { name: values.name })

  function submit(event: FormEvent) {
    event.preventDefault()
    const open = (id: string) => navigate({ to: "/nodes/$nodeId/servers/$serverId", params: { nodeId, serverId: id } })
    operation.run((onStart) => duplicate.mutateAsync({ id: server.id, ...values, onStart }), {
      title,
      done: (copy) => ({
        message:
          values.network && network
            ? t("Created {{copy}}, a copy of {{name}}, in {{network}}", { copy: copy.name, name: server.name, network: network.name })
            : t("Created {{copy}}, a copy of {{name}}", { copy: copy.name, name: server.name }),
        action: { label: t("Open"), onClick: () => void open(copy.id) },
      }),
      then: (copy) => {
        onClose()
        void open(copy.id)
      },
    })
  }

  return operation.live ? (
    <OperationStatus
      op={operation.live}
      title={title}
      onBackground={() => {
        operation.background(title)
        onClose()
      }}
      onBack={() => {
        operation.reset()
        duplicate.reset()
      }}
    />
  ) : (
    <form onSubmit={submit} className="grid gap-6">
      <DialogHeader>
        <DialogTitle>{t("Duplicate {{name}}", { name: server.name })}</DialogTitle>
        <DialogDescription>
          {values.network
            ? t("The copy gets the worlds, plugins and settings of {{name}}. It stays stopped until you start it.", { name: server.name })
            : t("The copy gets the worlds, plugins and settings of {{name}}, but not its place in a network, so it authenticates its players itself. It stays stopped until you start it.", {
                name: server.name,
              })}
        </DialogDescription>
      </DialogHeader>
      <FieldGroup>
        <Field>
          <FieldLabel htmlFor="duplicate-name">{t("Name")}</FieldLabel>
          <Input id="duplicate-name" required maxLength={32} value={values.name} onChange={(e) => setName(e.target.value)} />
        </Field>
        <Field>
          <FieldLabel htmlFor="duplicate-port">{t("Port")}</FieldLabel>
          <Input
            id="duplicate-port"
            type="number"
            min={1024}
            max={65535}
            required
            className="font-mono"
            value={values.port}
            onChange={(e) => setPort(e.target.valueAsNumber || 0)}
          />
          <FieldDescription>
            {t("Every server on a node needs its own port.")}
            {server.extraPorts.length > 0 && ` ${t("The copy gets no extra ports, as {{name}} keeps them.", { name: server.name })}`}
          </FieldDescription>
        </Field>
        {network && backend && (
          <Field orientation="horizontal">
            <Checkbox id="duplicate-network" checked={join} onCheckedChange={(on) => setJoin(on === true)} />
            <div>
              <FieldLabel htmlFor="duplicate-network">{t("Add the copy to the network {{network}}", { network: network.name })}</FieldLabel>
              <FieldDescription>
                {t(
                  "It joins as {{copy}} right after {{original}}, also among the servers players join and those of host names if {{original}} is one of them. Then the network is applied.",
                  { copy: joined, original: backend.name },
                )}
              </FieldDescription>
            </div>
          </Field>
        )}
        {saves && (
          <FieldDescription>
            {t("The server saves its worlds first and pauses saving while they are copied. Players stay connected.")}
          </FieldDescription>
        )}
        {duplicate.error && <FieldError>{duplicate.error.message}</FieldError>}
      </FieldGroup>
      <DialogFooter>
        <DialogClose asChild>
          <Button variant="outline" disabled={duplicate.isPending}>
            {t("Cancel")}
          </Button>
        </DialogClose>
        <Button type="submit" disabled={duplicate.isPending}>
          {duplicate.isPending ? t("Copying…") : t("Duplicate server")}
        </Button>
      </DialogFooter>
    </form>
  )
}
