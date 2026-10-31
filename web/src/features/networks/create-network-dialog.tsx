import { ArrowLeftIcon, ArrowRightIcon, PlusIcon } from "@phosphor-icons/react"
import { useQuery } from "@tanstack/react-query"
import { useNavigate } from "@tanstack/react-router"
import { t } from "i18next"
import { type FormEvent, type ReactElement, useState } from "react"
import { FormDialogContent, useBusy } from "@/components/form-dialog"
import { Button } from "@/components/ui/button"
import { Dialog, DialogDescription, DialogFooter, DialogHeader, DialogTitle, DialogTrigger } from "@/components/ui/dialog"
import { Field, FieldDescription, FieldError, FieldGroup, FieldLabel } from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { allServersQuery } from "@/features/servers/api"
import { serverType } from "@/features/servers/server-types"
import { cn } from "@/lib/utils"
import { OperationStatus } from "@/features/operations/operation-status"
import { usePrivateRoute } from "@/features/overlay/api"
import { useOperation } from "@/features/operations/use-operation"
import { type Forwarding, networksQuery, type ServerRef, useCreateNetwork } from "./api"
import { FirewallConfirmation, ForwardingChoice } from "./forwarding"
import { ServerPicker } from "./server-picker"
import { availableServers, canJoin, findServer, isBungee, key, proxyTypes, refOf } from "./servers"

const steps = ["proxy", "forwarding", "servers"] as const
type Step = (typeof steps)[number]

/**
 * Creates a network in three steps: its proxy, how the proxy forwards players, and its servers.
 * trigger replaces the button that opens the dialog; with open, it opens without one, e.g. from
 * the palette, and onOpenChange tells when it closes.
 */
export function CreateNetworkDialog({
  trigger,
  open: shown,
  onOpenChange: onShownChange,
}: {
  trigger?: ReactElement
  open?: boolean
  onOpenChange?: (open: boolean) => void
}) {
  const [ownOpen, setOpen] = useState(false)
  const open = shown ?? ownOpen

  function onOpenChange(next: boolean) {
    setOpen(next)
    onShownChange?.(next)
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      {shown === undefined && (
        <DialogTrigger asChild>
          {trigger ?? (
            <Button>
              <PlusIcon />
              {t("Create network")}
            </Button>
          )}
        </DialogTrigger>
      )}
      <FormDialogContent className="sm:max-w-2xl">
        <CreateNetworkForm onClose={() => onOpenChange(false)} />
      </FormDialogContent>
    </Dialog>
  )
}

/** The steps of a new network, created anew each time the dialog opens. */
function CreateNetworkForm({ onClose }: { onClose: () => void }) {
  const [step, setStep] = useState<Step>("proxy")
  const [name, setName] = useState("")
  const [proxy, setProxy] = useState<ServerRef>()
  const [forwarding, setForwarding] = useState<Forwarding>("modern")
  const [picked, setPicked] = useState<ServerRef[]>([])
  const [firewalled, setFirewalled] = useState(false)
  const { data: servers } = useQuery(allServersQuery)
  const { data: networks } = useQuery(networksQuery)
  const create = useCreateNetwork()
  const operation = useOperation()
  const navigate = useNavigate()
  const proxyServer = proxy && findServer(servers, proxy)
  const bungee = !!proxyServer && isBungee(proxyServer.type)
  const proxies = availableServers(servers, networks, (s) => proxyTypes.includes(s.type))
  const candidates = availableServers(servers, networks, (s) => !proxyTypes.includes(s.type))
  const selected = picked.filter((r) => canJoin(findServer(servers, r)?.type ?? "", forwarding))
  const isPrivate = usePrivateRoute()
  const exposed = forwarding === "legacy" && !!proxy && selected.some((r) => r.nodeId !== proxy.nodeId && !isPrivate(proxy.nodeId, r.nodeId))
  useBusy(create.isPending)

  function chooseProxy(ref: ServerRef) {
    setProxy(ref)
    const type = findServer(servers, ref)?.type ?? ""
    setForwarding(isBungee(type) ? "legacy" : "modern")
  }

  const title = t("Create the network {{name}}", { name: name.trim() })

  function submit(event: FormEvent) {
    event.preventDefault()
    if (step !== "servers") return setStep(steps[steps.indexOf(step) + 1])
    if (!proxy) return
    const open = (id: string) => navigate({ to: "/networks/$networkId", params: { networkId: id } })
    operation.run((onStart) => create.mutateAsync({ name: name.trim(), proxy, forwarding, firewalled, servers: selected, onStart }), {
      title,
      done: (network) => ({
        message: t("Created {{name}}", { name: network.name }),
        action: { label: t("Open"), onClick: () => void open(network.id) },
      }),
      then: (network) => {
        onClose()
        void open(network.id)
      },
    })
  }

  const ready = { proxy: !!name.trim() && !!proxy, forwarding: true, servers: selected.length > 0 && (!exposed || firewalled) }[step]

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
        create.reset()
      }}
    />
  ) : (
    <form onSubmit={submit} className="grid gap-6">
      <DialogHeader>
        <DialogTitle>{t("Create network")}</DialogTitle>
        <DialogDescription>{t("The panel sets up the proxy and the servers the way their projects document it.")}</DialogDescription>
      </DialogHeader>
      <ol className="grid grid-cols-3 gap-2" aria-label={t("Steps")}>
        {[t("Proxy"), t("Forwarding"), t("Servers")].map((label, i) => (
          <li
            key={label}
            aria-current={steps[i] === step ? "step" : undefined}
            className={cn(
              "flex items-center gap-2 rounded-lg px-3 py-2 text-xs font-medium",
              steps.indexOf(step) >= i ? "bg-primary/10 text-foreground" : "bg-muted text-muted-foreground",
            )}
          >
            <span
              className={cn(
                "grid size-5 place-items-center rounded-full text-[0.6875rem] font-bold",
                steps.indexOf(step) >= i ? "bg-primary text-primary-foreground" : "bg-card",
              )}
            >
              {i + 1}
            </span>
            {label}
          </li>
        ))}
      </ol>
      {step === "proxy" && (
        <FieldGroup>
          <Field>
            <FieldLabel htmlFor="network-name">{t("Name")}</FieldLabel>
            <Input
              id="network-name"
              placeholder={t("Main network")}
              required
              maxLength={64}
              value={name}
              onChange={(e) => setName(e.target.value)}
            />
          </Field>
          <Field>
            <FieldLabel htmlFor="network-proxy">{t("Proxy")}</FieldLabel>
            <Select
              value={proxy ? key(proxy) : ""}
              onValueChange={(v) => {
                const server = proxies.find((s) => key(refOf(s)) === v)
                if (server) chooseProxy(refOf(server))
              }}
              disabled={proxies.length === 0}
            >
              <SelectTrigger id="network-proxy" className="w-full">
                <SelectValue
                  placeholder={
                    proxies.length === 0
                      ? t("No free proxy; create a Velocity or BungeeCord server first")
                      : t("Choose a proxy")
                  }
                />
              </SelectTrigger>
              <SelectContent>
                {proxies.map((s) => (
                  <SelectItem key={key(refOf(s))} value={key(refOf(s))}>
                    {s.name}
                    <span className="text-muted-foreground">
                      {serverType(s.type).label} · {t("{{node}} · port {{port}}", { node: s.nodeName, port: s.port })}
                    </span>
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
            <FieldDescription>
              {t("Players connect to the proxy, which sends them on to the servers of the network.")}
            </FieldDescription>
          </Field>
        </FieldGroup>
      )}
      {step === "forwarding" && (
        <div className="space-y-3">
          <ForwardingChoice value={forwarding} bungee={bungee} onChange={setForwarding} />
          <p className="text-xs text-muted-foreground">
            {bungee
              ? t(
                  "BungeeCord and Waterfall only support legacy forwarding. Servers on the proxy's node are safe, as only the proxy can reach them.",
                )
              : t("You can change this later; the servers then restart.")}
          </p>
        </div>
      )}
      {step === "servers" && (
        <div className="space-y-4">
          <p className="text-sm text-muted-foreground">
            {t(
              "Choose the servers in the order players should try them: they join the first. Fabric, Forge and NeoForge get their forwarding mod from Modrinth.",
            )}
          </p>
          <ServerPicker servers={candidates} forwarding={forwarding} selected={selected} onChange={setPicked} />
          {exposed && <FirewallConfirmation checked={firewalled} onChange={setFirewalled} proxyNode={proxyServer?.nodeName} />}
          {create.error && <FieldError>{create.error.message}</FieldError>}
        </div>
      )}
      <DialogFooter>
        {step !== "proxy" && (
          <Button type="button" variant="ghost" disabled={create.isPending} onClick={() => setStep(steps[steps.indexOf(step) - 1])}>
            <ArrowLeftIcon />
            {t("Back")}
          </Button>
        )}
        <Button type="submit" disabled={!ready || create.isPending}>
          {step === "servers" ? (create.isPending ? t("Creating…") : t("Create network")) : t("Next")}
          {step !== "servers" && <ArrowRightIcon />}
        </Button>
      </DialogFooter>
    </form>
  )
}
