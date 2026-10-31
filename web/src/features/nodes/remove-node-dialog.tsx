import { TrashIcon, WarningIcon } from "@phosphor-icons/react"
import { useQuery } from "@tanstack/react-query"
import { useNavigate } from "@tanstack/react-router"
import { t } from "i18next"
import { useState } from "react"
import { Callout } from "@/components/callout"
import { guard } from "@/components/form-dialog"
import { Button } from "@/components/ui/button"
import { Dialog, DialogClose, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { FieldError } from "@/components/ui/field"
import { useAccess } from "@/features/access/use-access"
import { type Network, networksQuery } from "@/features/networks/api"
import { OperationStatus } from "@/features/operations/operation-status"
import { useOperation } from "@/features/operations/use-operation"
import { ApiError } from "@/lib/api"
import { type Node, networksOnNode, useDeleteNode } from "./api"

/**
 * Removes a node. While servers of networks run on it, the master refuses and names the networks; the dialog then
 * shows what taking the servers out of them does, and removes the node once they are out.
 */
export function RemoveNodeDialog({ node, onClose }: { node: Node; onClose: () => void }) {
  const { can } = useAccess()
  const remove = useDeleteNode()
  const operation = useOperation()
  const navigate = useNavigate()
  // The refusal stays once it came, so that trying again takes the servers out.
  const [refusal, setRefusal] = useState<string>()
  const { data: networks } = useQuery({ ...networksQuery, enabled: !!refusal && can("networks.view") })
  const affected = networks?.filter((n) => n.proxy.nodeId === node.id || n.backends.some((b) => b.nodeId === node.id))
  const mayRelease = can("networks.manage")
  const title = t("Remove {{name}}", { name: node.name })

  function submit() {
    operation.run(
      (onStart) =>
        remove.mutateAsync({ id: node.id, release: refusal !== undefined, onStart }).catch((e: unknown) => {
          if (e instanceof ApiError && e.code === networksOnNode) setRefusal(e.message)
          throw e
        }),
      {
        title,
        done: () => ({ message: t("Removed {{name}}", { name: node.name }) }),
        then: () => {
          onClose()
          void navigate({ to: "/nodes" })
        },
      },
    )
  }

  // The refusal shows as what taking the servers out does.
  const error = remove.error instanceof ApiError && remove.error.code === networksOnNode ? undefined : remove.error

  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent className="sm:max-w-lg" {...guard(remove.isPending)}>
        {operation.live ? (
          <OperationStatus
            op={operation.live}
            title={title}
            onBackground={() => {
              operation.background(title)
              onClose()
            }}
            onBack={() => {
              operation.reset()
              remove.reset()
            }}
          />
        ) : (
          <div className="grid gap-6">
            <DialogHeader>
              <DialogTitle>{t("Remove {{name}}?", { name: node.name })}</DialogTitle>
              <DialogDescription>
                {t("The panel stops managing this node. Its servers and datastores keep running until you stop them on the node or uninstall the agent.")}
              </DialogDescription>
            </DialogHeader>
            {refusal !== undefined && (
              <Callout tone="warning" icon={WarningIcon} title={t("Servers of networks run on this node")}>
                <div className="space-y-2">
                  {affected?.length ? (
                    <ul className="list-disc space-y-1 pl-4">
                      {affected.map((n) => (
                        <li key={n.id}>{effect(n, node.id)}</li>
                      ))}
                    </ul>
                  ) : (
                    <p>{refusal}</p>
                  )}
                  <p>
                    {t(
                      "They leave their networks first, so that no server keeps trusting a proxy of this node and no proxy keeps sending players to its servers. The node itself isn't contacted, so this works while it is offline too.",
                    )}
                  </p>
                  {!mayRelease && <p>{t("This needs the permission to manage networks.")}</p>}
                </div>
              </Callout>
            )}
            {error && <FieldError>{error.message}</FieldError>}
            <DialogFooter>
              <DialogClose asChild>
                <Button variant="outline">{t("Cancel")}</Button>
              </DialogClose>
              <Button variant="destructive" disabled={remove.isPending || (refusal !== undefined && !mayRelease)} onClick={submit}>
                <TrashIcon />
                {refusal === undefined ? t("Remove node") : t("Take them out and remove")}
              </Button>
            </DialogFooter>
          </div>
        )}
      </DialogContent>
    </Dialog>
  )
}

/** What taking the servers of a node out of a network does to it. */
function effect(n: Network, nodeId: string): string {
  const elsewhere = n.backends.filter((b) => b.nodeId !== nodeId)
  if (n.proxy.nodeId === nodeId || elsewhere.length === 0) {
    return t("{{network}} is deleted, and its servers on other nodes accept players directly again.", { network: n.name })
  }
  const gone = n.backends.filter((b) => b.nodeId === nodeId).map((b) => b.name)
  return t("{{network}} goes on without {{servers}}.", { network: n.name, servers: gone.join(", ") })
}
