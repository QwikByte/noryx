import { PlusIcon } from "@phosphor-icons/react"
import { useQuery } from "@tanstack/react-query"
import { t } from "i18next"
import { type FormEvent, type ReactElement, useState } from "react"
import { toast } from "sonner"
import { Button } from "@/components/ui/button"
import { Checkbox } from "@/components/ui/checkbox"
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
import { Field, FieldContent, FieldDescription, FieldError, FieldGroup, FieldLabel, FieldTitle } from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import { RadioGroup, RadioGroupItem } from "@/components/ui/radio-group"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { nodesQuery } from "@/features/nodes/api"
import { guard, useOperation } from "@/features/operations/use-operation"
import { type Datastore, type DatastoreInput, type Engine, parameterBounds, useCreateDatastore, useDatastore } from "./api"
import { engines } from "./labels"

const exampleName = "main"

/** Creates a datastore of a network on a node; the servers of the network on that node join it. */
export function CreateDatastoreDialog({ networkId }: { networkId: string }) {
  const [open, setOpen] = useState(false)
  const { data: nodes = [] } = useQuery(nodesQuery)
  const online = nodes.filter((n) => n.status === "online")
  const initial: DatastoreInput = { name: "", nodeId: "", engine: "mariadb", version: "", memoryMb: 1024, cpuMillis: 0, storage: "" }
  const [input, setInput] = useState(initial)
  const create = useCreateDatastore(networkId)
  const operation = useOperation()
  const node = online.find((n) => n.id === input.nodeId) ?? online[0]
  const storage = input.storage || node?.defaultStorage || "default"

  function close(next: boolean) {
    setOpen(next)
    if (!next) {
      create.reset()
      setInput(initial)
    }
  }

  function submit(event: FormEvent) {
    event.preventDefault()
    if (!node) return
    operation.run((onStart) => create.mutateAsync({ ...input, name: input.name.trim(), nodeId: node.id, storage, onStart }), {
      title: t("Creating the datastore {{name}}…", { name: input.name }),
      done: (ds) => ({ message: t("Created the datastore {{name}}", { name: ds.name }) }),
      then: () => close(false),
    })
  }

  return (
    <Dialog open={open} onOpenChange={close}>
      <DialogTrigger asChild>
        <Button size="sm">
          <PlusIcon />
          {t("Add datastore")}
        </Button>
      </DialogTrigger>
      <DialogContent className="sm:max-w-lg" {...guard(create.isPending)}>
        <form onSubmit={submit} className="grid gap-6">
          <DialogHeader>
            <DialogTitle>{t("Add a datastore")}</DialogTitle>
            <DialogDescription>
              {t(
                "A database server that only the network's servers can reach: by name on its own node, and over the nodes' private network from other nodes.",
              )}
            </DialogDescription>
          </DialogHeader>
          <FieldGroup>
            <RadioGroup
              value={input.engine}
              onValueChange={(engine) => setInput({ ...input, engine: engine as Engine })}
              aria-label={t("Engine")}
              className="grid gap-2 sm:grid-cols-2"
            >
              {(Object.keys(engines) as Engine[]).map((e) => (
                <FieldLabel key={e} htmlFor={`datastore-engine-${e}`}>
                  <Field orientation="horizontal">
                    <FieldContent>
                      <FieldTitle>{engines[e].label}</FieldTitle>
                      <FieldDescription>{t(engines[e].description)}</FieldDescription>
                    </FieldContent>
                    <RadioGroupItem id={`datastore-engine-${e}`} value={e} />
                  </Field>
                </FieldLabel>
              ))}
            </RadioGroup>
            <Field>
              <FieldLabel htmlFor="datastore-name">{t("Name")}</FieldLabel>
              <Input
                id="datastore-name"
                required
                maxLength={32}
                pattern="[a-z0-9][a-z0-9\-]*"
                placeholder={exampleName}
                className="font-mono"
                value={input.name}
                onChange={(e) => setInput({ ...input, name: e.target.value.toLowerCase() })}
              />
            </Field>
            <div className="grid gap-5 sm:grid-cols-2">
              <Field>
                <FieldLabel htmlFor="datastore-node">{t("Node")}</FieldLabel>
                <Select value={node?.id ?? ""} onValueChange={(nodeId) => setInput({ ...input, nodeId, storage: "" })}>
                  <SelectTrigger id="datastore-node" className="w-full">
                    <SelectValue placeholder={t("No node is online")} />
                  </SelectTrigger>
                  <SelectContent>
                    {online.map((n) => (
                      <SelectItem key={n.id} value={n.id}>
                        {n.name}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </Field>
              <Field>
                <FieldLabel htmlFor="datastore-storage">{t("Storage location")}</FieldLabel>
                <Select value={storage} onValueChange={(s) => setInput({ ...input, storage: s })}>
                  <SelectTrigger id="datastore-storage" className="w-full">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    {(node?.info?.storage ?? [{ name: storage }]).map((l) => (
                      <SelectItem key={l.name} value={l.name}>
                        {l.name}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </Field>
            </div>
            <Field>
              <FieldLabel htmlFor="datastore-memory">{t("Memory (MB)")}</FieldLabel>
              <Input
                id="datastore-memory"
                type="number"
                min={256}
                step={128}
                required
                className="sm:w-40"
                value={input.memoryMb}
                onChange={(e) => setInput({ ...input, memoryMb: e.target.valueAsNumber || 0 })}
              />
            </Field>
            <FieldDescription>{t("It gets the newest version the node's agent runs. Its memory counts against the node's memory limit.")}</FieldDescription>
            {create.error && <FieldError>{create.error.message}</FieldError>}
          </FieldGroup>
          <DialogFooter>
            <DialogClose asChild>
              <Button variant="outline">{t("Cancel")}</Button>
            </DialogClose>
            <Button type="submit" disabled={create.isPending || !node}>
              {create.isPending ? t("Creating…") : t("Add datastore")}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

/** Changes the limits and parameters of a datastore, pulls its image again or moves it to a newer major version. */
export function ChangeDatastoreDialog({ datastore: ds, trigger }: { datastore: Datastore; trigger: ReactElement }) {
  const [open, setOpen] = useState(false)
  const [memoryMb, setMemoryMb] = useState(ds.memoryMb)
  const [cores, setCores] = useState(ds.cpuMillis / 1000)
  const [maxConnections, setMaxConnections] = useState(ds.maxConnections)
  const [bufferMb, setBufferMb] = useState(ds.bufferMb)
  const bounds = parameterBounds(ds.engine, memoryMb)
  const [version, setVersion] = useState(ds.version)
  const [updateImage, setUpdateImage] = useState(false)
  const { update } = useDatastore(ds.id)
  const operation = useOperation()
  const newer = ds.versions.slice(ds.versions.indexOf(ds.version))
  const upgrade = version !== ds.version

  function close(next: boolean) {
    setOpen(next)
    if (!next) update.reset()
    if (next) {
      setMemoryMb(ds.memoryMb)
      setCores(ds.cpuMillis / 1000)
      setMaxConnections(ds.maxConnections)
      setBufferMb(ds.bufferMb)
      setVersion(ds.version)
      setUpdateImage(false)
    }
  }

  function submit(event: FormEvent) {
    event.preventDefault()
    const title = upgrade
      ? t("Moving {{name}} to version {{version}}…", { name: ds.name, version })
      : t("Changing the datastore {{name}}…", { name: ds.name })
    operation.run(
      (onStart) =>
        update.mutateAsync({
          memoryMb,
          cpuMillis: Math.round(cores * 1000),
          maxConnections,
          bufferMb,
          version: upgrade ? version : undefined,
          updateImage,
          onStart: (op) => {
            onStart(op)
            if (upgrade) {
              operation.background(title, op.id)
              close(false)
            }
          },
        }),
      {
        title,
        done: (changed) => ({ message: t("Changed the datastore {{name}}", { name: ds.name }), description: changed.warning, warning: !!changed.warning }),
        then: () => close(false),
      },
    )
  }

  return (
    <Dialog open={open} onOpenChange={close}>
      <DialogTrigger asChild>{trigger}</DialogTrigger>
      <DialogContent className="sm:max-w-md" {...guard(update.isPending)}>
        <form onSubmit={submit} className="grid gap-6">
          <DialogHeader>
            <DialogTitle>{t("Change {{name}}", { name: ds.name })}</DialogTitle>
            <DialogDescription>{t("The datastore restarts, which briefly interrupts the plugins that use it.")}</DialogDescription>
          </DialogHeader>
          <FieldGroup>
            <div className="grid gap-5 sm:grid-cols-2">
              <Field>
                <FieldLabel htmlFor="change-memory">{t("Memory (MB)")}</FieldLabel>
                <Input
                  id="change-memory"
                  type="number"
                  min={256}
                  step={128}
                  required
                  value={memoryMb}
                  onChange={(e) => setMemoryMb(e.target.valueAsNumber || 0)}
                />
              </Field>
              <Field>
                <FieldLabel htmlFor="change-cpu">{t("CPU limit (cores)")}</FieldLabel>
                <Input id="change-cpu" type="number" min={0} step={0.25} value={cores} onChange={(e) => setCores(e.target.valueAsNumber || 0)} />
                <FieldDescription>{t("0 means no limit.")}</FieldDescription>
              </Field>
              <Field>
                <FieldLabel htmlFor="change-connections">{t("Max. connections")}</FieldLabel>
                <Input
                  id="change-connections"
                  type="number"
                  min={bounds.connections.min}
                  max={bounds.connections.max}
                  placeholder={String(bounds.connections.default)}
                  value={maxConnections || ""}
                  onChange={(e) => setMaxConnections(e.target.valueAsNumber || 0)}
                />
                <FieldDescription>
                  {t("Plugins keep several open on each server. Up to {{max}} with this memory.", { max: bounds.connections.max })}
                </FieldDescription>
              </Field>
              <Field>
                <FieldLabel htmlFor="change-buffer">{t(engines[ds.engine].buffer)}</FieldLabel>
                <Input
                  id="change-buffer"
                  type="number"
                  min={bounds.bufferMb.min}
                  max={bounds.bufferMb.max}
                  placeholder={String(bounds.bufferMb.default)}
                  value={bufferMb || ""}
                  onChange={(e) => setBufferMb(e.target.valueAsNumber || 0)}
                />
                <FieldDescription>{t("Data kept in memory. Up to {{max}} MB with this memory.", { max: bounds.bufferMb.max })}</FieldDescription>
              </Field>
            </div>
            <FieldDescription>{t("Leave connections and buffers empty for the defaults; the buffers then follow the memory.")}</FieldDescription>
            <Field>
              <FieldLabel htmlFor="change-version">{t("Version")}</FieldLabel>
              <Select value={version} onValueChange={setVersion}>
                <SelectTrigger id="change-version" className="w-full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {newer.map((v) => (
                    <SelectItem key={v} value={v}>
                      {engines[ds.engine].label} {v}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
              {upgrade && (
                <FieldDescription>
                  {t(
                    "The databases are dumped and loaded into the new version, which the plugins can't reach meanwhile, so stop their servers first. The data of {{version}} stays until you remove it, so you can go back.",
                    { version: ds.version },
                  )}
                </FieldDescription>
              )}
            </Field>
            {!upgrade && (
              <Field orientation="horizontal">
                <Checkbox id="change-image" checked={updateImage} onCheckedChange={(v) => setUpdateImage(v === true)} />
                <FieldContent>
                  <FieldLabel htmlFor="change-image">{t("Update the image")}</FieldLabel>
                  <FieldDescription>{t("Downloads the newest release of version {{version}}.", { version: ds.version })}</FieldDescription>
                </FieldContent>
              </Field>
            )}
            {update.error && <FieldError>{update.error.message}</FieldError>}
          </FieldGroup>
          <DialogFooter>
            <DialogClose asChild>
              <Button variant="outline">{t("Cancel")}</Button>
            </DialogClose>
            <Button type="submit" disabled={update.isPending}>
              {update.isPending ? t("Saving…") : upgrade ? t("Upgrade") : t("Save")}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

/** Deletes a datastore with its data and dumps, once its name is typed. */
export function DeleteDatastoreDialog({ datastore: ds, trigger }: { datastore: Datastore; trigger: ReactElement }) {
  const [open, setOpen] = useState(false)
  const [typed, setTyped] = useState("")
  const { remove } = useDatastore(ds.id)

  function submit(event: FormEvent) {
    event.preventDefault()
    remove.mutate(undefined, {
      onSuccess: () => {
        toast.success(t("Deleted the datastore {{name}}", { name: ds.name }))
        setOpen(false)
      },
    })
  }

  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        setOpen(next)
        setTyped("")
        remove.reset()
      }}
    >
      <DialogTrigger asChild>{trigger}</DialogTrigger>
      <DialogContent className="sm:max-w-md" {...guard(remove.isPending)}>
        <form onSubmit={submit} className="grid gap-6">
          <DialogHeader>
            <DialogTitle>{t("Delete the datastore {{name}}?", { name: ds.name })}</DialogTitle>
            <DialogDescription>
              {t("Its {{count}} databases and all their data and backups are deleted from {{node}}. This can't be undone.", {
                count: ds.databases.length,
                node: ds.nodeName,
                defaultValue_one: "Its database and all its data and backups are deleted from {{node}}. This can't be undone.",
              })}
            </DialogDescription>
          </DialogHeader>
          <Field>
            <FieldLabel htmlFor="delete-datastore">{t("Type {{name}} to confirm", { name: ds.name })}</FieldLabel>
            <Input id="delete-datastore" autoComplete="off" className="font-mono" value={typed} onChange={(e) => setTyped(e.target.value)} />
            {remove.error && <FieldError>{remove.error.message}</FieldError>}
          </Field>
          <DialogFooter>
            <DialogClose asChild>
              <Button variant="outline">{t("Cancel")}</Button>
            </DialogClose>
            <Button type="submit" variant="destructive" disabled={typed !== ds.name || remove.isPending}>
              {t("Delete datastore")}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
