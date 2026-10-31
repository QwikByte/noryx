import { PlusIcon } from "@phosphor-icons/react"
import { useQuery } from "@tanstack/react-query"
import { t } from "i18next"
import { type FormEvent, useState } from "react"
import { Button } from "@/components/ui/button"
import {
  Dialog,
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from "@/components/ui/dialog"
import { allServersQuery } from "@/features/servers/api"
import { type Network, networksQuery, type ServerRef } from "./api"
import type { Draft } from "./draft"
import { ServerPicker } from "./server-picker"
import { availableServers, backendName, findServer, key, proxyTypes, refOf } from "./servers"

/** Adds servers to the draft of a network; they join when it is saved. */
export function AddBackendDialog({ network, draft, onAdd }: { network: Network; draft: Draft; onAdd: (draft: Draft) => void }) {
  const [open, setOpen] = useState(false)
  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogTrigger asChild>
        <Button>
          <PlusIcon />
          {t("Add servers")}
        </Button>
      </DialogTrigger>
      <DialogContent className="sm:max-w-xl">
        <AddBackendForm network={network} draft={draft} onAdd={onAdd} onClose={() => setOpen(false)} />
      </DialogContent>
    </Dialog>
  )
}

/** The servers to add, chosen anew each time the dialog opens. */
function AddBackendForm({ network, draft, onAdd, onClose }: { network: Network; draft: Draft; onAdd: (draft: Draft) => void; onClose: () => void }) {
  const [picked, setPicked] = useState<ServerRef[]>([])
  const { data: servers } = useQuery(allServersQuery)
  const { data: networks } = useQuery(networksQuery)
  const inDraft = new Set(draft.backends.map(key))
  const candidates = availableServers(servers, networks, (s) => !inDraft.has(key(refOf(s))) && !proxyTypes.includes(s.type), network)

  function submit(event: FormEvent) {
    event.preventDefault()
    const backends = [...draft.backends]
    for (const ref of picked) {
      const name = backendName(findServer(servers, ref)?.name ?? "", backends.map((b) => b.name))
      backends.push({ ...ref, name, restricted: false, motd: "" })
    }
    onAdd({ ...draft, backends, try: draft.try.length > 0 ? draft.try : [key(picked[0])] })
    onClose()
  }

  return (
    <form onSubmit={submit} className="grid gap-6">
      <DialogHeader>
        <DialogTitle>{t("Add servers to {{network}}", { network: draft.name })}</DialogTitle>
        <DialogDescription>
          {t("They join when you save the network: each restarts and then only accepts players who come through the proxy.")}
        </DialogDescription>
      </DialogHeader>
      <ServerPicker servers={candidates} forwarding={draft.forwarding} selected={picked} onChange={setPicked} />
      <DialogFooter>
        <DialogClose asChild>
          <Button variant="outline">{t("Cancel")}</Button>
        </DialogClose>
        <Button type="submit" disabled={picked.length === 0}>
          {t("Add {{count}} servers", { count: picked.length, defaultValue_one: "Add {{count}} server" })}
        </Button>
      </DialogFooter>
    </form>
  )
}
