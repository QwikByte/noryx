import { PlusIcon } from "@phosphor-icons/react"
import { useQuery } from "@tanstack/react-query"
import { useNavigate } from "@tanstack/react-router"
import { t } from "i18next"
import { type FormEvent, type ReactElement, useCallback, useState } from "react"
import { Trans } from "react-i18next"
import { toast } from "sonner"
import { Fold } from "@/components/fold"
import { Segmented } from "@/components/segmented"
import { UploadProgress } from "@/components/upload-progress"
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
import { Field, FieldDescription, FieldError, FieldGroup, FieldLabel, FieldLegend, FieldSet } from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import { useAccess } from "@/features/access/use-access"
import { meQuery } from "@/features/auth/api"
import type { ModpackChoice } from "@/features/modpacks/api"
import { ModpackPicker } from "@/features/modpacks/modpack-picker"
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectLabel,
  SelectSeparator,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { nodeQuery, nodesQuery } from "@/features/nodes/api"
import { OperationStatus } from "@/features/operations/operation-status"
import { guard, useOperation } from "@/features/operations/use-operation"
import { keptVersions, type Template, templatesQuery } from "@/features/templates/api"
import { formatBytes, formatSeconds, formatTimeZone } from "@/lib/format"
import { useUpload } from "@/lib/use-upload"
import {
  freeMemoryMb,
  type ImportedSettings,
  type NewServer,
  type NewServerSettings,
  newServersQuery,
  serversQuery,
  useCreateServer,
  useImportServer,
} from "./api"
import { lastNode, rememberNode } from "./last-node"
import { defaults, javaLabel, modpack, serverType, suggestPort, usedPorts } from "./server-types"
import { JavaField, MemoryField, StopTimeoutField, TimeZoneField, VersionField } from "./settings-fields"
import { EndOfLifeNotice, SoftwareOptions } from "./software"
import { TagList } from "./tags"
import { type World, worldChanges, worldOf } from "./world"
import { WorldFields } from "./world-fields"

// Node, port and storage stay unset until chosen, so that the suggestions apply.
type Form = Omit<NewServer, "port" | "storage"> & {
  port?: number
  storage?: string
  nodeId?: string
  templateId?: string
  modpack?: ModpackChoice
  world: World
  java: string
  stopTimeout: number
  timeZone: string
  /** A new server starts empty, or with the data of an archive of a server from elsewhere. */
  source: "empty" | "archive"
  archive?: File
}

const none = "none"

/** A new server as its template has it, or else as the settings of new servers, once they are known. */
function blank(template?: Template, settings?: NewServerSettings): Form {
  const type = template?.type ?? settings?.type ?? "paper"
  const { java = "", stopTimeout = 60, timeZone = "" } = template ?? settings ?? {}
  return {
    name: "",
    acceptEula: false,
    templateId: template?.id,
    world: worldOf(template?.properties),
    type,
    version: template && template.version !== "LATEST" ? template.version : "",
    memoryMb: template?.memoryMb ?? defaults(type, settings).memoryMb,
    java,
    stopTimeout,
    timeZone,
    source: "empty",
  }
}

/** What a template adds to a new server, e.g. "Java 21 · 3 properties · LuckPerms". */
function templateSummary(template: Template) {
  const count = Object.keys(template.properties).length
  return [
    template.java && t("Java {{version}}", { version: template.java }),
    template.aikarFlags && t("Aikar's flags"),
    count && t("{{count}} properties", { count, defaultValue_one: "{{count}} property" }),
    ...template.plugins.map((p) => (p.versionNumber ? `${p.title} ${p.versionNumber}` : p.title)),
  ]
    .filter(Boolean)
    .join(" · ")
}

/**
 * Creates a server, optionally from a template. The node is chosen in the dialog unless
 * given; a given template can't be changed.
 */
export function CreateServerDialog({
  nodeId: fixedNode,
  template: fixedTemplate,
  trigger,
  open: shown,
  onOpenChange: onShownChange,
}: {
  nodeId?: string
  template?: Template
  trigger?: ReactElement
  /** Opens the dialog without its button, e.g. from the palette; onOpenChange tells when it closes. */
  open?: boolean
  onOpenChange?: (open: boolean) => void
}) {
  const [ownOpen, setOpen] = useState(false)
  const open = shown ?? ownOpen
  const [changed, setForm] = useState<Form>()
  const create = useCreateServer()
  const importServer = useImportServer()
  const upload = useUpload()
  const [importError, setImportError] = useState<string>()
  const operation = useOperation()
  const navigate = useNavigate()
  const { data: settings } = useQuery({ ...newServersQuery, enabled: open })
  const { data: templates = [] } = useQuery({ ...templatesQuery, enabled: open && !fixedTemplate })
  // Until something is chosen, the form follows the settings of new servers and their template as they load.
  const form = changed ?? blank(fixedTemplate ?? templates.find((candidate) => candidate.id === settings?.template), settings)
  const { can } = useAccess()
  const { data: me } = useQuery(meQuery)
  const { data: allNodes = [], isPending: nodesPending } = useQuery({ ...nodesQuery, enabled: open && !fixedNode })
  const nodes = allNodes.filter((n) => can("servers.create", n.id))
  // The node the user created a server on last, while it is online, or else the first online one.
  const online = nodes.filter((n) => n.status === "online")
  const nodeId = fixedNode ?? form.nodeId ?? (online.find((n) => me && n.id === lastNode(me.id)) ?? online[0])?.id
  const template = fixedTemplate ?? templates.find((candidate) => candidate.id === form.templateId)
  const { data: node } = useQuery({ ...nodeQuery(nodeId ?? ""), enabled: open && !!nodeId })
  const { data: servers } = useQuery({ ...serversQuery(nodeId ?? ""), enabled: open && !!nodeId })
  const locations = node?.info?.storage ?? []
  const proxy = serverType(form.type).proxy
  const fromModpack = form.type === modpack
  const fromArchive = form.source === "archive"
  // The picker only shows once a modpack was chosen as the software.
  const chooseModpack = useCallback((choice?: ModpackChoice) => setForm((f) => f && { ...f, modpack: choice }), [])
  const port =
    form.port ??
    suggestPort(usedPorts(servers), defaults(form.type).port, node?.portMin ?? undefined, node?.portMax ?? undefined)
  const storage = form.storage ?? locations.find((l) => l.name === node?.defaultStorage)?.name ?? "default"
  // Tags of a new server need the permission to change the settings of the node's servers.
  const tags = template?.tags ?? []
  const tagsAllowed = !!nodeId && can("servers.settings", nodeId)

  const title = t("Create {{name}}", { name: form.name })

  function onOpenChange(next: boolean) {
    if (upload.uploading) return
    setImportError(undefined)
    setOpen(next)
    onShownChange?.(next)
    if (!next) {
      create.reset()
      operation.reset()
      setForm(undefined)
    }
  }

  // Switching between game server and proxy updates the memory unless it was changed.
  function changeType(type: string) {
    const before = defaults(form.type, settings).memoryMb
    setForm({ ...form, type, memoryMb: form.memoryMb === before ? defaults(type, settings).memoryMb : form.memoryMb })
  }

  // An archive brings its own data, so neither a template nor a modpack applies.
  function chooseSource(source: Form["source"]) {
    setForm({ ...form, source, archive: undefined, templateId: undefined, modpack: undefined, type: form.type === modpack ? (settings?.type ?? "paper") : form.type })
  }

  function chooseTemplate(id: string) {
    const chosen = templates.find((candidate) => candidate.id === id)
    setForm({ ...blank(chosen, settings), name: form.name, acceptEula: form.acceptEula, nodeId: form.nodeId, port: form.port, storage: form.storage })
  }

  function submit(event: FormEvent) {
    event.preventDefault()
    if (!nodeId) return
    if (me) rememberNode(me.id, nodeId)
    const { name, type, memoryMb, acceptEula, stopTimeout, timeZone } = form
    const [version, java] = proxy ? ["", ""] : [form.version.trim(), form.java]
    const server: NewServer = { name, type, memoryMb, acceptEula, port, storage, version, java, stopTimeout, timeZone }
    if (fromArchive) {
      if (form.archive) void createFromArchive(nodeId, server, form.archive)
      return
    }
    if (fromModpack) Object.assign(server, { type: "", version: "", modpack: form.modpack })
    if (template) {
      const { restartPolicy, aikarFlags, jvmOptions, cpuLimit, properties } = template
      Object.assign(server, { restartPolicy, aikarFlags, jvmOptions, cpuLimit, properties, versions: keptVersions(template.plugins) })
      if (tagsAllowed) server.tags = tags
    }
    if (!proxy) server.properties = { ...server.properties, ...worldChanges(form.world, template?.properties) }
    const open = (id: string) => navigate({ to: "/nodes/$nodeId/servers/$serverId", params: { nodeId, serverId: id } })
    operation.run((onStart) => create.mutateAsync({ nodeId, server, plugins: template?.plugins.map((p) => p.id), onStart }), {
      title,
      done: (created) => ({
        message: created.pluginError
          ? t("Created {{name}}, but not all of its plugins could be installed: {{error}}", { name: created.name, error: created.pluginError })
          : t("Created {{name}}", { name: created.name }),
        description: created.warning,
        warning: !!created.pluginError || !!created.warning,
        action: { label: t("Open"), onClick: () => void open(created.id) },
      }),
      then: (created) => {
        onOpenChange(false)
        if (!fixedNode) void open(created.id)
      },
    })
  }

  async function createFromArchive(nodeId: string, server: ImportedSettings, archive: File) {
    setImportError(undefined)
    try {
      const created = await upload.run((onProgress, signal) => importServer(nodeId, server, archive, { onProgress, signal }))
      if (!created) return
      const leftOut = created.leftOut.length > 0 && t("It didn't take {{files}} from the archive.", { files: created.leftOut.join(", ") })
      ;(created.warning ? toast.warning : toast.success)(t("Created {{name}} from {{archive}}", { name: created.name, archive: archive.name }), {
        description: [leftOut, created.warning].filter(Boolean).join(" ") || undefined,
      })
      setOpen(false)
      onShownChange?.(false)
      setForm(undefined)
      void navigate({ to: "/nodes/$nodeId/servers/$serverId", params: { nodeId, serverId: created.id } })
    } catch (e) {
      setImportError((e as Error).message)
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      {shown === undefined && (
        <DialogTrigger asChild>
          {trigger ?? (
            <Button>
              <PlusIcon />
              {t("Create server")}
            </Button>
          )}
        </DialogTrigger>
      )}
      <DialogContent className="sm:max-w-lg" {...guard(create.isPending || upload.uploading)}>
        {operation.live ? (
          <OperationStatus
            op={operation.live}
            title={title}
            onBackground={() => {
              operation.background(title)
              onOpenChange(false)
            }}
            onBack={() => {
              operation.reset()
              create.reset()
            }}
          />
        ) : (
          <form onSubmit={submit} className="grid gap-6">
            <DialogHeader>
              <DialogTitle>
                {fixedTemplate ? t("Create server from {{template}}", { template: fixedTemplate.name }) : t("Create server")}
              </DialogTitle>
              <DialogDescription>
                {t("The first server on a node downloads the server image, which can take a few minutes.")}
              </DialogDescription>
            </DialogHeader>
            <FieldGroup>
              {!fixedTemplate && (
                <FieldSet>
                  <FieldLegend variant="label">{t("Start with")}</FieldLegend>
                  <Segmented
                    label={t("Start with")}
                    className="w-fit"
                    value={form.source}
                    onChange={chooseSource}
                    options={[
                      { value: "empty", label: t("A new server") },
                      { value: "archive", label: t("An archive of a server") },
                    ]}
                  />
                </FieldSet>
              )}
              {!fixedTemplate && !fromArchive && templates.length > 0 && (
                <Field>
                  <FieldLabel htmlFor="server-template">{t("Template")}</FieldLabel>
                  <Select value={form.templateId ?? none} onValueChange={(id) => id && chooseTemplate(id)}>
                    <SelectTrigger id="server-template" className="w-full">
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem value={none}>{t("No template")}</SelectItem>
                      {templates.map((option) => (
                        <SelectItem key={option.id} value={option.id}>
                          {option.name}
                          <span className="text-muted-foreground">{serverType(option.type).label}</span>
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                </Field>
              )}
              {template && templateSummary(template) && (
                <FieldDescription>{t("From the template: {{summary}}", { summary: templateSummary(template) })}</FieldDescription>
              )}
              {tags.length > 0 && (
                <Field>
                  <FieldLabel>{t("Tags")}</FieldLabel>
                  <TagList tags={tags} className={tagsAllowed ? undefined : "opacity-50"} />
                  <FieldDescription>
                    {tagsAllowed
                      ? t("File sets of the tags still have to be applied to the server.")
                      : t("The server gets no tags, as you may not change the settings of servers on this node.")}
                  </FieldDescription>
                </Field>
              )}
              {!fixedNode && (
                <Field>
                  <FieldLabel htmlFor="server-node">{t("Node")}</FieldLabel>
                  {/* Radix reports "" while the options of a new value load; that is no choice. */}
                  <Select
                    value={nodeId ?? ""}
                    onValueChange={(id) => id && setForm({ ...form, nodeId: id, port: undefined, storage: undefined })}
                  >
                    <SelectTrigger id="server-node" className="w-full">
                      <SelectValue placeholder={nodesPending ? t("Loading nodes…") : t("No node is online")} />
                    </SelectTrigger>
                    <SelectContent>
                      {nodes.map((n) => (
                        <SelectItem key={n.id} value={n.id} disabled={n.status !== "online"}>
                          {n.name}
                          <span className="text-muted-foreground">
                            {n.status === "online"
                              ? (n.address ?? t("Online"))
                              : n.status === "pending"
                                ? t("Waiting for agent")
                                : t("Offline")}
                          </span>
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                </Field>
              )}
              <Field>
                <FieldLabel htmlFor="server-name">{t("Name")}</FieldLabel>
                <Input
                  id="server-name"
                  placeholder={t("Lobby")}
                  required
                  maxLength={32}
                  value={form.name}
                  onChange={(e) => setForm({ ...form, name: e.target.value })}
                />
              </Field>
              <div className="grid gap-4 sm:grid-cols-2">
                <Field>
                  <FieldLabel htmlFor="server-type">{t("Software")}</FieldLabel>
                  <Select value={form.type} onValueChange={changeType} disabled={!!template}>
                    <SelectTrigger id="server-type" className="w-full">
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      <SoftwareOptions chosen={form.type} />
                      {nodeId && can("plugins.manage", nodeId) && !fromArchive && (
                        <SelectGroup>
                          <SelectSeparator />
                          <SelectLabel>{t("Modpacks")}</SelectLabel>
                          <SelectItem value={modpack}>{t("Modrinth modpack")}</SelectItem>
                        </SelectGroup>
                      )}
                    </SelectContent>
                  </Select>
                </Field>
                {!proxy && !fromModpack && (
                  <VersionField id="server-version" value={form.version} onChange={(version) => setForm({ ...form, version })} />
                )}
              </div>
              <EndOfLifeNotice type={form.type} />
              {fromArchive && (
                <Field>
                  <FieldLabel htmlFor="server-archive">{t("Archive")}</FieldLabel>
                  <Input
                    id="server-archive"
                    type="file"
                    accept=".zip,.tar.gz,.tgz,application/zip,application/gzip"
                    required
                    disabled={upload.uploading}
                    onChange={(e) => setForm({ ...form, archive: e.target.files?.[0] })}
                  />
                  <FieldDescription>
                    {t(
                      "A ZIP or .tar.gz archive of the contents of a server's folder, e.g. from a host, with server.properties at its top; up to 16 GB. The server gets its worlds, plugins and settings, but not its secrets, such as the console password, nor its trust in a proxy.",
                    )}
                  </FieldDescription>
                </Field>
              )}
              {fromModpack && <ModpackPicker onChange={chooseModpack} />}
              <div className="grid gap-4 sm:grid-cols-2">
                <MemoryField
                  id="server-memory"
                  value={form.memoryMb}
                  onChange={(memoryMb) => setForm({ ...form, memoryMb })}
                  freeMb={freeMemoryMb(node, servers)}
                />
                <Field>
                  <FieldLabel htmlFor="server-port">{t("Port")}</FieldLabel>
                  <Input
                    id="server-port"
                    type="number"
                    min={1024}
                    max={65535}
                    required
                    className="font-mono"
                    value={port}
                    onChange={(e) => setForm({ ...form, port: e.target.valueAsNumber || 0 })}
                  />
                </Field>
              </div>
              {locations.length > 1 && (
                <Field>
                  <FieldLabel htmlFor="server-storage">{t("Storage")}</FieldLabel>
                  <Select value={storage} onValueChange={(storage) => setForm({ ...form, storage })}>
                    <SelectTrigger id="server-storage" className="w-full">
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      {locations.map((l) => (
                        <SelectItem key={l.name} value={l.name}>
                          {l.name}
                          <span className="text-muted-foreground">{t("{{size}} free", { size: formatBytes(l.freeBytes) })}</span>
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                </Field>
              )}
              {!proxy && !fromArchive && <WorldFields value={form.world} onChange={(world) => setForm({ ...form, world })} />}
              <Fold
                title={proxy ? t("Stop timeout and time zone") : t("Java, stop timeout and time zone")}
                summary={[!proxy && javaLabel(form.java), formatSeconds(form.stopTimeout), formatTimeZone(form.timeZone)].filter(Boolean).join(" · ")}
              >
                {!proxy && <JavaField id="server-java" value={form.java} onChange={(java) => setForm({ ...form, java })} />}
                <StopTimeoutField
                  id="server-stop-timeout"
                  value={form.stopTimeout}
                  onChange={(stopTimeout) => setForm({ ...form, stopTimeout })}
                />
                <TimeZoneField id="server-time-zone" value={form.timeZone} onChange={(timeZone) => setForm({ ...form, timeZone })} />
              </Fold>
              {!proxy && (
                <Field orientation="horizontal">
                  <Checkbox
                    id="server-eula"
                    checked={form.acceptEula}
                    onCheckedChange={(v) => setForm({ ...form, acceptEula: v === true })}
                  />
                  <FieldLabel htmlFor="server-eula" className="font-normal">
                    <span>
                      <Trans
                        i18nKey="I accept the <link>Minecraft EULA</link>"
                        components={{
                          link: (
                            <a
                              href="https://aka.ms/MinecraftEULA"
                              target="_blank"
                              rel="noreferrer"
                              className="underline underline-offset-4"
                            />
                          ),
                        }}
                      />
                    </span>
                  </FieldLabel>
                </Field>
              )}
              {proxy && <FieldDescription>{t("Proxies always run the latest release of their software.")}</FieldDescription>}
              {create.error && <FieldError>{create.error.message}</FieldError>}
              {upload.progress !== undefined && <UploadProgress progress={upload.progress} done={t("Creating the server and unpacking the archive…")} />}
              {importError && <FieldError>{importError}</FieldError>}
            </FieldGroup>
            <DialogFooter>
              {upload.uploading ? (
                <Button type="button" variant="outline" onClick={upload.cancel}>
                  {t("Cancel upload")}
                </Button>
              ) : (
                <DialogClose asChild>
                  <Button variant="outline" disabled={create.isPending}>
                    {t("Cancel")}
                  </Button>
                </DialogClose>
              )}
              <Button
                type="submit"
                disabled={create.isPending || upload.uploading || !nodeId || (fromModpack && !form.modpack) || (fromArchive && !form.archive)}
              >
                {upload.uploading
                  ? t("Uploading…")
                  : create.isPending
                    ? template?.plugins.length
                      ? t("Creating and installing…")
                      : t("Creating…")
                    : t("Create server")}
              </Button>
            </DialogFooter>
          </form>
        )}
      </DialogContent>
    </Dialog>
  )
}
