import {
  ArrowCircleUpIcon,
  CaretDownIcon,
  CheckIcon,
  DotsThreeIcon,
  DownloadSimpleIcon,
  FolderOpenIcon,
  MagnifyingGlassIcon,
  NotepadIcon,
  PlusIcon,
  PowerIcon,
  PushPinIcon,
  PushPinSlashIcon,
  PuzzlePieceIcon,
  TrashIcon,
  UploadSimpleIcon,
} from "@phosphor-icons/react"
import { useQuery } from "@tanstack/react-query"
import { getRouteApi, Link } from "@tanstack/react-router"
import { t } from "i18next"
import { useRef, useState } from "react"
import { toast } from "sonner"
import { Callout, ErrorCallout } from "@/components/callout"
import { ConfirmDialog } from "@/components/confirm-dialog"
import { EmptyState } from "@/components/empty-state"
import { usePageName } from "@/components/page-title"
import { Section } from "@/components/section"
import { Segmented } from "@/components/segmented"
import { Pill } from "@/components/status"
import { Button } from "@/components/ui/button"
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle, DialogTrigger } from "@/components/ui/dialog"
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuSeparator, DropdownMenuTrigger } from "@/components/ui/dropdown-menu"
import { InputGroup, InputGroupAddon, InputGroupInput } from "@/components/ui/input-group"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { Skeleton } from "@/components/ui/skeleton"
import { useAccess } from "@/features/access/use-access"
import type { ServerRef } from "@/features/networks/api"
import { useChoice } from "@/features/preferences/api"
import { type Server, useServer } from "@/features/servers/api"
import { serverType } from "@/features/servers/server-types"
import { formatBytes } from "@/lib/format"
import { locale } from "@/lib/i18n"
import { cn } from "@/lib/utils"
import {
  fallback,
  type InstalledPlugin,
  type InstallResult,
  onHangar,
  type Project,
  type ProjectVersion,
  pluginsQuery,
  projectUrl,
  type SearchHit,
  useChangePlugins,
  useInstallPlugins,
  useUpdatePlugins,
} from "./api"
import { ChangesDialog } from "./changes-dialog"
import { ChannelPill } from "./channel-pill"
import { PluginIcon } from "./plugin-icon"
import { PluginSearch } from "./plugin-search"
import { VersionMenu } from "./version-menu"

const route = getRouteApi("/_app/nodes/$nodeId/servers/$serverId/plugins")

type Kind = "plugins" | "mods"
type Show = "all" | "updates" | "foreign"

/** The texts that name what a server of a type loads, plugins or mods, and where they come from. */
function texts(kind: Kind, serverTypeValue: string) {
  const type = serverType(serverTypeValue).label
  const elsewhere = onHangar(serverTypeValue) ? t("Not from Modrinth or Hangar") : t("Not from Modrinth")
  return kind === "plugins"
    ? {
        none: t("No plugins yet"),
        add: t("Add plugins"),
        addTitle: onHangar(serverTypeValue) ? t("Add plugins from Modrinth or Hangar") : t("Add plugins from Modrinth"),
        addDescription: t("Only plugins for {{type}} are shown. What they require is installed too.", { type }),
        elsewhere,
        updated: (count: number) =>
          t("Updated {{count}} plugins. Restart the server to load them.", {
            count,
            defaultValue_one: "Updated {{count}} plugin. Restart the server to load it.",
          }),
      }
    : {
        none: t("No mods yet"),
        add: t("Add mods"),
        addTitle: t("Add mods from Modrinth"),
        addDescription: t("Only mods for {{type}} are shown. What they require is installed too.", { type }),
        elsewhere,
        updated: (count: number) =>
          t("Updated {{count}} mods. Restart the server to load them.", {
            count,
            defaultValue_one: "Updated {{count}} mod. Restart the server to load it.",
          }),
      }
}

type Words = ReturnType<typeof texts>

const name = (p: InstalledPlugin) => p.project?.title ?? p.fileName

/** The Plugins tab of a server, Mods for modded servers. */
export function ServerPluginsPage() {
  const { nodeId, serverId } = route.useParams()
  const manage = useAccess().can("plugins.manage", nodeId, serverId)
  const { server } = useServer(nodeId, serverId)
  usePageName(server && (serverType(server.type).addons?.kind === "mods" ? t("Mods") : t("Plugins")))
  const ref = { nodeId, serverId }
  const { data, isPending, error } = useQuery(pluginsQuery(ref))
  const [input, setInput] = useState("")
  const [show, setShow] = useState<Show>("all")
  const [sort, setSort] = useChoice("serverPluginSort", "name")

  if (!server || isPending) return <Skeleton className="h-64 rounded-xl" />
  if (error) return <ErrorCallout error={error} />
  const kind = serverType(server.type).addons?.kind ?? "plugins"
  const words = texts(kind, server.type)
  const count = data.plugins.length
  const installed = data.plugins.flatMap((p) => (p.project ? [p.project.id] : []))
  const updates = data.plugins.filter((p) => p.update && p.project)
  // Update all leaves out the projects the server keeps at their version, and turned-off ones.
  const updatable = new Set(updates.filter((p) => !p.pinned && !p.disabled).map((p) => p.project!.id)).size
  const foreign = data.plugins.filter((p) => !p.project).length
  const needle = input.trim().toLowerCase()
  const shown = data.plugins
    .filter((p) => show === "all" || (show === "updates" ? p.update : !p.project))
    .filter((p) => [name(p), p.fileName].some((text) => text.toLowerCase().includes(needle)))
    .sort(sort === "size" ? (a, b) => b.size - a.size : (a, b) => name(a).localeCompare(name(b), locale))

  return (
    <Section
      title={
        kind === "plugins"
          ? t("{{count}} plugins", { count, defaultValue_one: "{{count}} plugin" })
          : t("{{count}} mods", { count, defaultValue_one: "{{count}} mod" })
      }
      className="mt-0"
      actions={
        manage && (
          <div className="flex flex-wrap gap-2">
            {updatable > 0 && <UpdateAllButton count={updatable} serverRef={ref} words={words} />}
            <UploadButton serverRef={ref} />
            <AddDialog server={server} serverRef={ref} words={words} installed={installed} />
          </div>
        )
      }
    >
      {data.catalogueError && (
        <Callout tone="warning" className="mb-4">
          {t("The files couldn't be identified: {{error}}", { error: data.catalogueError })}
        </Callout>
      )}
      {count === 0 ? (
        <EmptyState icon={PuzzlePieceIcon} tone="warning" title={words.none}>
          {manage && <AddDialog server={server} serverRef={ref} words={words} installed={installed} />}
        </EmptyState>
      ) : (
        <div className="space-y-3">
          <div className="flex flex-wrap items-center gap-2">
            <InputGroup className="min-w-48 flex-1">
              <InputGroupAddon>
                <MagnifyingGlassIcon />
              </InputGroupAddon>
              <InputGroupInput
                type="search"
                placeholder={t("Search by name or file")}
                aria-label={t("Search the installed files")}
                value={input}
                onChange={(e) => setInput(e.target.value)}
              />
            </InputGroup>
            <Segmented
              label={t("Show")}
              value={show}
              onChange={setShow}
              options={[
                { value: "all", label: t("All ({{number}})", { number: count }) },
                { value: "updates", label: t("Updates ({{number}})", { number: updates.length }) },
                { value: "foreign", label: `${words.elsewhere} (${foreign})` },
              ]}
            />
            <Select value={sort} onValueChange={(v) => setSort(v as typeof sort)}>
              <SelectTrigger aria-label={t("Sort")} className="max-sm:flex-1">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="name">{t("By name")}</SelectItem>
                <SelectItem value="size">{t("By size")}</SelectItem>
              </SelectContent>
            </Select>
          </div>
          {shown.length === 0 ? (
            <p className="surface rounded-xl py-8 text-center text-sm text-muted-foreground">{t("Nothing matches the filters.")}</p>
          ) : (
            <ul className="surface divide-y rounded-xl">
              {shown.map((plugin) => (
                <PluginRow
                  key={`${plugin.disabled ? "off/" : ""}${plugin.fileName}`}
                  plugin={plugin}
                  server={server}
                  serverRef={ref}
                  manage={manage}
                  elsewhere={words.elsewhere}
                />
              ))}
            </ul>
          )}
        </div>
      )}
    </Section>
  )
}

/** Installs a project on a server, the newest suitable release or a chosen version, and tells how it went. */
function useInstallOn(serverRef: ServerRef) {
  const install = useInstallPlugins()
  const run = (project: Project, version?: ProjectVersion) => {
    const versions = version && { [project.id]: version.id }
    toast.promise(
      install.mutateAsync({ projects: [project.id], servers: [serverRef], versions }).then((results) => failIfAny(results, versions)),
      {
        loading: t("Installing {{name}}…", { name: version ? `${project.title} ${version.number}` : project.title }),
        success: (files) => t("Installed {{files}}. Restart the server to load it.", { files }),
        error: (e: Error) => e.message,
      },
    )
  }
  return { run, pending: install.isPending }
}

function PluginRow({
  plugin,
  server,
  serverRef,
  manage,
  elsewhere,
}: {
  plugin: InstalledPlugin
  server: Server
  serverRef: ServerRef
  manage: boolean
  /** What files from elsewhere are called. */
  elsewhere: string
}) {
  const install = useInstallOn(serverRef)
  const { project, update } = plugin

  return (
    <li className={cn("flex items-center gap-3 px-4 py-3", plugin.disabled && "bg-muted/30")}>
      <PluginIcon src={project?.icon} className={cn(plugin.disabled && "opacity-50 grayscale")} />
      <div className="min-w-0 flex-1">
        <p className="flex items-center gap-2 truncate text-sm font-semibold">
          {project ? (
            <a
              href={projectUrl(project)}
              target="_blank"
              rel="noreferrer"
              className={cn("truncate hover:underline", plugin.disabled && "text-muted-foreground")}
            >
              {project.title}
            </a>
          ) : (
            <span className={cn("truncate", plugin.disabled && "text-muted-foreground")}>{plugin.fileName}</span>
          )}
          {plugin.version && manage && project ? (
            <VersionMenu
              project={project.id}
              title={project.title}
              type={server.type}
              version={server.version}
              current={plugin.versionId}
              onPick={(v) => install.run(project, v)}
            >
              <Button
                variant="ghost"
                size="xs"
                disabled={install.pending}
                aria-label={t("Change the version of {{name}}", { name: project.title })}
                className="min-w-0 font-mono font-normal text-muted-foreground"
              >
                <span className="truncate">{plugin.version}</span>
                <CaretDownIcon />
              </Button>
            </VersionMenu>
          ) : (
            plugin.version && <span className="truncate font-mono text-xs font-normal text-muted-foreground">{plugin.version}</span>
          )}
          <ChannelPill channel={plugin.channel} />
          {plugin.pinned && (
            <span title={t("Kept at this version")} className="shrink-0 text-muted-foreground">
              <PushPinIcon className="size-3.5" weight="fill" aria-hidden />
              <span className="sr-only">{t("Kept at this version")}</span>
            </span>
          )}
          {plugin.disabled && <Pill tone="neutral">{t("Off")}</Pill>}
        </p>
        <p className="truncate text-xs text-muted-foreground">
          {project ? plugin.fileName : elsewhere} · {formatBytes(plugin.size)}
        </p>
      </div>
      {update && project && <UpdateButton plugin={plugin} project={project} server={server} manage={manage} pending={install.pending} onUpdate={() => install.run(project)} />}
      <PluginMenu plugin={plugin} serverRef={serverRef} manage={manage} />
    </li>
  )
}

/** Updates a project to its newest suitable release, and tells what changed since the installed version. */
function UpdateButton({
  plugin,
  project,
  server,
  manage,
  pending,
  onUpdate,
}: {
  plugin: InstalledPlugin
  project: Project
  server: Server
  manage: boolean
  pending: boolean
  onUpdate: () => void
}) {
  const [changes, setChanges] = useState(false)
  const label = t("Update to {{version}}", { version: plugin.update })
  return (
    <div className="flex shrink-0 items-center">
      {manage ? (
        <Button size="sm" variant="outline" className="rounded-r-none" disabled={pending} onClick={onUpdate}>
          <ArrowCircleUpIcon />
          <span className="max-sm:sr-only">{label}</span>
        </Button>
      ) : (
        <Pill tone="info" className="max-sm:hidden">
          {label}
        </Pill>
      )}
      <Button
        size="icon-sm"
        variant={manage ? "outline" : "ghost"}
        className={cn(manage && "-ml-px rounded-l-none")}
        aria-label={t("What changed in {{name}} up to {{version}}", { name: project.title, version: plugin.update })}
        title={t("What changed")}
        onClick={() => setChanges(true)}
      >
        <NotepadIcon />
      </Button>
      <ChangesDialog
        open={changes}
        onOpenChange={setChanges}
        title={t("What changed in {{name}} up to {{version}}", { name: project.title, version: plugin.update })}
        project={project.id}
        type={server.type}
        version={server.version}
        from={plugin.versionId}
        to={plugin.updateId}
        current={plugin.versionId}
        footer={
          manage && (
            <Button
              disabled={pending}
              onClick={() => {
                setChanges(false)
                onUpdate()
              }}
            >
              <ArrowCircleUpIcon />
              {label}
            </Button>
          )
        }
      />
    </div>
  )
}

/**
 * The other actions on a plugin: keeping its version, turning it off or on, its settings in the file manager and
 * removing it. Turning off or removing a plugin that others require asks first.
 */
function PluginMenu({ plugin, serverRef, manage }: { plugin: InstalledPlugin; serverRef: ServerRef; manage: boolean }) {
  const { can } = useAccess()
  const change = useChangePlugins(serverRef)
  const [confirm, setConfirm] = useState<"off" | "remove">()
  const { project, requiredBy = [] } = plugin
  const title = name(plugin)
  const settings = plugin.settings && can("files.read", serverRef.nodeId, serverRef.serverId) ? plugin.settings : undefined
  if (!manage && !settings) return null

  const done = (message: string) => ({ onSuccess: () => toast.success(message), onError: (e: Error) => toast.error(e.message) })
  const turn = (on: boolean) =>
    change.mutate(
      { action: on ? "enable" : "disable", fileName: plugin.fileName },
      done(
        on
          ? t("Turned on {{name}}. Restart the server to load it.", { name: title })
          : t("Turned off {{name}}. Restart the server so that it no longer runs.", { name: title }),
      ),
    )
  const needed = requiredBy.length > 0 && (
    <span className="mt-2 block font-medium text-warning">{t("{{names}} need it and may stop working without it.", { names: requiredBy.join(", ") })}</span>
  )

  return (
    <>
      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <Button
            size="icon-sm"
            variant="ghost"
            className="shrink-0 text-muted-foreground"
            disabled={change.isPending}
            aria-label={t("More actions for {{name}}", { name: title })}
            title={t("More actions")}
          >
            <DotsThreeIcon weight="bold" />
          </Button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="end" className="w-60">
          {settings && (
            <DropdownMenuItem asChild>
              <Link to="/nodes/$nodeId/servers/$serverId/files" params={serverRef} search={{ path: settings }}>
                <FolderOpenIcon />
                {t("Open its settings")}
              </Link>
            </DropdownMenuItem>
          )}
          {manage && project && (
            <DropdownMenuItem
              onSelect={() =>
                change.mutate(
                  { action: plugin.pinned ? "unpin" : "pin", project: project.id },
                  done(
                    plugin.pinned
                      ? t("Update all updates {{name}} again.", { name: title })
                      : t("{{name}} stays at {{version}}. Update all leaves it out.", { name: title, version: plugin.version }),
                  ),
                )
              }
            >
              {plugin.pinned ? <PushPinSlashIcon /> : <PushPinIcon />}
              {plugin.pinned ? t("Update it with the others again") : t("Keep this version")}
            </DropdownMenuItem>
          )}
          {manage && (
            <DropdownMenuItem onSelect={() => (plugin.disabled ? turn(true) : requiredBy.length > 0 ? setConfirm("off") : turn(false))}>
              <PowerIcon />
              {plugin.disabled ? t("Turn on") : t("Turn off…")}
            </DropdownMenuItem>
          )}
          {manage && (
            <>
              <DropdownMenuSeparator />
              <DropdownMenuItem variant="destructive" onSelect={() => setConfirm("remove")}>
                <TrashIcon />
                {t("Remove…")}
              </DropdownMenuItem>
            </>
          )}
        </DropdownMenuContent>
      </DropdownMenu>
      {confirm === "off" && (
        <ConfirmDialog
          open
          onOpenChange={(open) => !open && setConfirm(undefined)}
          title={t("Turn off {{name}}?", { name: title })}
          description={
            <>
              {t("The server keeps the file, but no longer loads it once it restarts.")}
              {needed}
            </>
          }
          action={t("Turn off")}
          onConfirm={() => turn(false)}
        />
      )}
      {confirm === "remove" && (
        <ConfirmDialog
          open
          onOpenChange={(open) => !open && setConfirm(undefined)}
          title={t("Remove {{name}}?", { name: title })}
          description={
            <>
              {t("This deletes {{file}}. Its configuration in the server's folder is kept.", { file: plugin.fileName })}
              {needed}
            </>
          }
          action={t("Remove")}
          destructive
          onConfirm={() =>
            change.mutate({ action: "remove", fileName: plugin.fileName, disabled: plugin.disabled }, done(t("Removed {{name}}", { name: plugin.fileName })))
          }
        />
      )}
    </>
  )
}

/** Updates all plugins or mods with a newer release, except those kept at their version and turned-off ones. */
function UpdateAllButton({ count, serverRef, words }: { count: number; serverRef: ServerRef; words: Words }) {
  const update = useUpdatePlugins(serverRef)
  function updateAll() {
    const id = toast.loading(t("Updating all…"))
    update.mutate(undefined, {
      onSuccess: (result) => {
        const updated = result.installed.length
        const kept = result.pinned?.length ? t("Kept at their version: {{names}}", { names: result.pinned.join(", ") }) : undefined
        if (result.error) toast.warning(updated > 0 ? words.updated(updated) : t("Nothing was updated."), { id, description: result.error })
        else toast.success(updated > 0 ? words.updated(updated) : t("Nothing was updated."), { id, description: kept })
      },
      onError: (e) => toast.error(e.message, { id }),
    })
  }
  return (
    <Button variant="outline" disabled={update.isPending} onClick={updateAll}>
      <ArrowCircleUpIcon />
      {t("Update all ({{number}})", { number: count })}
    </Button>
  )
}

function UploadButton({ serverRef }: { serverRef: ServerRef }) {
  const input = useRef<HTMLInputElement>(null)
  const change = useChangePlugins(serverRef)
  return (
    <>
      <input
        ref={input}
        type="file"
        accept=".jar"
        multiple
        hidden
        onChange={(e) => {
          for (const file of e.target.files ?? []) {
            toast.promise(change.mutateAsync({ action: "upload", file }), {
              loading: t("Uploading {{name}}…", { name: file.name }),
              success: t("Uploaded {{name}}", { name: file.name }),
              error: (err: Error) => err.message,
            })
          }
          e.target.value = ""
        }}
      />
      <Button variant="outline" disabled={change.isPending} onClick={() => input.current?.click()}>
        <UploadSimpleIcon />
        {t("Upload .jar")}
      </Button>
    </>
  )
}

function AddDialog({ server, serverRef, words, installed }: { server: Server; serverRef: ServerRef; words: Words; installed: string[] }) {
  return (
    <Dialog>
      <DialogTrigger asChild>
        <Button>
          <PlusIcon />
          {words.add}
        </Button>
      </DialogTrigger>
      <DialogContent className="sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle>{words.addTitle}</DialogTitle>
          <DialogDescription>{words.addDescription}</DialogDescription>
        </DialogHeader>
        <PluginSearch
          type={server.type}
          version={server.version}
          autoFocus
          action={(hit) => <InstallButton hit={hit} server={server} serverRef={serverRef} installed={installed.includes(hit.id)} />}
        />
      </DialogContent>
    </Dialog>
  )
}

/** Installs the newest suitable release, or a version chosen from the menu next to it. */
function InstallButton({ hit, server, serverRef, installed }: { hit: SearchHit; server: Server; serverRef: ServerRef; installed: boolean }) {
  const install = useInstallOn(serverRef)
  if (installed)
    return (
      <Pill tone="success">
        <CheckIcon weight="bold" />
        {t("Installed")}
      </Pill>
    )
  return (
    <div className="flex">
      <Button size="sm" variant="outline" className="rounded-r-none" disabled={install.pending} onClick={() => install.run(hit)}>
        <DownloadSimpleIcon />
        {install.pending ? t("Installing…") : t("Install")}
      </Button>
      <VersionMenu project={hit.id} title={hit.title} type={server.type} version={server.version} onPick={(v) => install.run(hit, v)}>
        <Button
          size="icon-sm"
          variant="outline"
          className="-ml-px rounded-l-none"
          disabled={install.pending}
          aria-label={t("Install another version of {{name}}", { name: hit.title })}
        >
          <CaretDownIcon />
        </Button>
      </VersionMenu>
    </div>
  )
}

/** Throws the error of a single-server installation, or returns the installed files and warns of pre-releases nobody chose. */
function failIfAny([result]: InstallResult[], versions?: Record<string, string>): string {
  if (result.error) throw new Error(result.error)
  for (const file of result.installed.filter((f) => fallback(f, versions))) {
    toast.warning(t("No release suits the server, so the pre-release {{version}} was installed: {{file}}", { version: file.version, file: file.fileName }))
  }
  return result.installed.map((i) => i.fileName).join(", ")
}
