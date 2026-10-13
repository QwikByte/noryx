import { ArrowUpIcon, UploadSimpleIcon, WarningIcon } from "@phosphor-icons/react"
import { useQuery, useQueryClient } from "@tanstack/react-query"
import { Link, useNavigate } from "@tanstack/react-router"
import { t } from "i18next"
import { type AnchorHTMLAttributes, type DragEvent, Fragment, useMemo, useRef, useState } from "react"
import { toast } from "sonner"
import { Callout, ErrorCallout } from "@/components/callout"
import { ConfirmDialog } from "@/components/confirm-dialog"
import { IconTile } from "@/components/icon-tile"
import { Breadcrumb, BreadcrumbItem, BreadcrumbLink, BreadcrumbList, BreadcrumbPage, BreadcrumbSeparator } from "@/components/ui/breadcrumb"
import { Checkbox } from "@/components/ui/checkbox"
import { Skeleton } from "@/components/ui/skeleton"
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table"
import { useAccess } from "@/features/access/use-access"
import type { Server } from "@/features/servers/api"
import { formatBytes, formatDateTime } from "@/lib/format"
import { contentUrl, type FileEntry, filesQuery, join, maxEditableBytes, type ServerFiles, upload, useChangeFiles } from "./api"
import { browse, storedSort, storeSort } from "./browse"
import { FileActions } from "./file-actions"
import { FileTypeIcon } from "./file-icon"
import { FileMenus, FilterBar } from "./file-toolbar"
import { isLog } from "./log-reader"
import { NameDialog } from "./name-dialog"
import { SearchDialog } from "./search-dialog"
import { SelectionBar } from "./selection-bar"
import { UploadList } from "./upload-list"
import { type Batch, readDrop, readPicked, useUploads } from "./use-uploads"

/** The file or folder at the top of a path, e.g. "world" of "world/region/r.0.0.mca". */
const top = (path: string) => path.split("/")[0]

/**
 * Lists a folder of a server, filtered and sorted; files and folders can be dropped onto it to
 * upload them. Chosen entries can be downloaded, moved and deleted at once; the selection only
 * counts the entries that are listed.
 */
export function FileBrowser({ files, path, server }: { files: ServerFiles; path: string; server: Server }) {
  const writable = useAccess().can("files.write", files.nodeId, files.serverId)
  const { data, isPending, error } = useQuery(filesQuery(files, path))
  const queryClient = useQueryClient()
  const navigate = useNavigate()
  const change = useChangeFiles(files)
  const { uploads, add, cancel } = useUploads(files)
  const [dragging, setDragging] = useState(false)
  const [creating, setCreating] = useState<"file" | "folder">()
  const [replace, setReplace] = useState<Batch>({ files: [], folders: [] })
  const [sort, setSort] = useState(storedSort)
  const [filter, setFilter] = useState("")
  const [selection, setSelection] = useState(new Set<string>())
  const [listed, setListed] = useState(path)
  const picker = useRef<HTMLInputElement>(null)
  const dragDepth = useRef(0)
  const folder = path.split("/").pop() || server.name
  const entries = useMemo(() => browse(data?.files ?? [], sort, filter), [data, sort, filter])
  const chosen = entries.filter((e) => selection.has(e.name))
  const replacing = [...new Set([...replace.files.map((f) => top(f.path)), ...replace.folders.map(top)])]

  // Another folder starts without a filter and a selection.
  if (listed !== path) {
    setListed(path)
    setFilter("")
    setSelection(new Set())
  }

  function select(names: string[], on: boolean) {
    setSelection((current) => {
      const next = new Set(current)
      for (const name of names) {
        if (on) next.add(name)
        else next.delete(name)
      }
      return next
    })
  }

  // Files and folders that exist already are only replaced after confirmation.
  function start(batch: Batch) {
    const existing = new Set(data?.files.map((f) => f.name))
    const part = (exists: boolean): Batch => ({
      files: batch.files.filter((f) => existing.has(top(f.path)) === exists),
      folders: batch.folders.filter((f) => existing.has(top(f)) === exists),
    })
    void add(part(false), path, false)
    setReplace(part(true))
  }

  function drop(event: DragEvent) {
    event.preventDefault()
    dragDepth.current = 0
    setDragging(false)
    if (!writable) return
    // The entries can only be taken during the event; reading them takes longer.
    const entries = [...event.dataTransfer.items].flatMap((item) => (item.kind === "file" ? (item.webkitGetAsEntry() ?? []) : []))
    readDrop(entries).then(start, (e: Error) => toast.error(e.message))
  }

  async function createFile(name: string) {
    const created = join(path, name)
    await upload(files, created, "")
    void queryClient.invalidateQueries({ queryKey: ["files", files.nodeId, files.serverId, path] })
    void navigate({
      to: "/nodes/$nodeId/servers/$serverId/files",
      params: { nodeId: files.nodeId, serverId: files.serverId },
      search: { path: path || undefined, edit: created },
    })
  }

  function dragBy(step: number) {
    return (event: DragEvent) => {
      if (!writable || !event.dataTransfer.types.includes("Files")) return
      event.preventDefault()
      dragDepth.current += step
      setDragging(dragDepth.current > 0)
    }
  }

  return (
    // oxlint-disable-next-line jsx-a11y/no-noninteractive-element-interactions -- dropping files does what Upload does with the keyboard
    <section
      aria-labelledby="files-heading"
      className="relative"
      onDragEnter={dragBy(1)}
      onDragLeave={dragBy(-1)}
      onDragOver={(e) => e.preventDefault()}
      onDrop={drop}
    >
      <h2 id="files-heading" className="sr-only">
        {t("Files")}
      </h2>
      {server.state !== "stopped" && (
        <Callout tone="warning" icon={WarningIcon} className="mb-4">
          {t("The server is running. Stop it before you replace worlds or plugins, as it may overwrite or still use them.")}
        </Callout>
      )}
      <UploadList uploads={uploads} onCancel={cancel} />
      <div className="surface overflow-hidden rounded-xl">
        <div className="flex flex-wrap items-center justify-between gap-3 border-b bg-muted/30 px-4 py-3">
          <PathBreadcrumb files={files} path={path} root={server.name} />
          <div className="flex gap-2">
            <SearchDialog files={files} dir={path} folder={folder} />
            {writable && (
              <FileMenus
                onCreate={setCreating}
                onUpload={(whole) => {
                  if (!picker.current) return
                  picker.current.webkitdirectory = whole
                  picker.current.click()
                }}
              />
            )}
          </div>
          <input
            ref={picker}
            type="file"
            multiple
            hidden
            onChange={(e) => {
              try {
                start(readPicked([...(e.target.files ?? [])]))
              } catch (err) {
                toast.error((err as Error).message)
              }
              e.target.value = ""
            }}
          />
        </div>
        {data && data.files.length > 0 && (
          <FilterBar
            filter={filter}
            onFilter={setFilter}
            sort={sort}
            onSort={(s) => {
              setSort(s)
              storeSort(s)
            }}
          />
        )}
        {isPending ? (
          <Skeleton className="m-4 h-64" />
        ) : error ? (
          <ErrorCallout error={error} className="m-4" />
        ) : (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead className="w-10">
                  <Checkbox
                    aria-label={t("Select all")}
                    disabled={entries.length === 0}
                    checked={chosen.length === 0 ? false : chosen.length === entries.length ? true : "indeterminate"}
                    onCheckedChange={(on) => select(entries.map((e) => e.name), on === true)}
                  />
                </TableHead>
                <TableHead>{t("Name")}</TableHead>
                <TableHead className="hidden w-28 text-right sm:table-cell">{t("Size")}</TableHead>
                <TableHead className="hidden w-48 md:table-cell">{t("Modified")}</TableHead>
                <TableHead className="w-12">
                  <span className="sr-only">{t("Actions")}</span>
                </TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {path && (
                <TableRow>
                  <TableCell colSpan={5}>
                    <FolderLink
                      files={files}
                      path={path.split("/").slice(0, -1).join("/")}
                      className="inline-flex items-center gap-2.5 text-muted-foreground hover:text-foreground"
                    >
                      <ArrowUpIcon className="size-4" />
                      {t("Parent folder")}
                    </FolderLink>
                  </TableCell>
                </TableRow>
              )}
              {entries.length === 0 && (
                <TableRow>
                  <TableCell colSpan={5} className="py-14 text-center text-muted-foreground">
                    {data.files.length === 0
                      ? t("This folder is empty. Drop files here to upload them.")
                      : t("No file or folder here matches “{{filter}}”.", { filter })}
                  </TableCell>
                </TableRow>
              )}
              {entries.map((entry) => (
                <TableRow key={entry.name} data-state={selection.has(entry.name) ? "selected" : undefined}>
                  <TableCell>
                    <Checkbox
                      aria-label={t("Select {{name}}", { name: entry.name })}
                      checked={selection.has(entry.name)}
                      onCheckedChange={(on) => select([entry.name], on === true)}
                    />
                  </TableCell>
                  <TableCell className="max-w-0 w-full">
                    <EntryLink files={files} dir={path} entry={entry} />
                  </TableCell>
                  <TableCell className="hidden text-right text-muted-foreground tabular-nums sm:table-cell">
                    {entry.directory ? "–" : formatBytes(entry.size)}
                  </TableCell>
                  <TableCell className="hidden text-muted-foreground md:table-cell">{formatDateTime(entry.modified)}</TableCell>
                  <TableCell>
                    <FileActions files={files} dir={path} entry={entry} />
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )}
      </div>
      {data?.truncated && (
        <p className="mt-3 text-sm text-muted-foreground">{t("This folder has more entries than can be listed here.")}</p>
      )}
      {chosen.length > 0 && (
        <SelectionBar
          files={files}
          dir={path}
          names={chosen.map((e) => e.name)}
          all={chosen.length === data?.files.length && !data.truncated}
          onClear={() => setSelection(new Set())}
        />
      )}
      {dragging && (
        <div className="pointer-events-none absolute inset-0 z-20 flex flex-col items-center justify-center gap-3 rounded-xl border-2 border-dashed border-primary bg-background/85 backdrop-blur-sm">
          <IconTile icon={UploadSimpleIcon} size="lg" />
          <p className="heading text-lg">{t("Drop to upload to {{folder}}", { folder })}</p>
        </div>
      )}
      <NameDialog
        open={creating === "folder"}
        onOpenChange={(open) => !open && setCreating(undefined)}
        title={t("New folder")}
        label={t("Name")}
        action={t("Create folder")}
        onSubmit={(name) => change.mutateAsync({ action: "mkdir", path: join(path, name) })}
      />
      <NameDialog
        open={creating === "file"}
        onOpenChange={(open) => !open && setCreating(undefined)}
        title={t("New file")}
        label={t("Name")}
        action={t("Create file")}
        onSubmit={createFile}
      />
      <ConfirmDialog
        open={replacing.length > 0}
        onOpenChange={(open) => !open && setReplace({ files: [], folders: [] })}
        title={
          replacing.length === 1
            ? t("Replace {{name}}?", { name: replacing[0] })
            : replace.folders.length > 0
              ? t("Replace {{count}} files and folders?", { count: replacing.length })
              : t("Replace {{count}} files?", { count: replacing.length })
        }
        description={
          <>
            {t("{{names}} already exist in {{folder}}.", {
              count: replacing.length,
              names: replacing.join(", "),
              folder,
              defaultValue_one: "{{names}} already exists in {{folder}}.",
            })}
            {replace.folders.length > 0 && ` ${t("Files in folders replace those with the same names, and the others stay.")}`}
          </>
        }
        action={t("Replace")}
        destructive
        onConfirm={() => void add(replace, path, true)}
      />
    </section>
  )
}

/** Opens a folder; other props, e.g. from a Slot, go to the link element. */
function FolderLink({
  files,
  path,
  ...props
}: { files: ServerFiles; path: string } & Omit<AnchorHTMLAttributes<HTMLAnchorElement>, "href">) {
  return (
    <Link
      {...props}
      to="/nodes/$nodeId/servers/$serverId/files"
      params={{ nodeId: files.nodeId, serverId: files.serverId }}
      search={{ path: path || undefined }}
    />
  )
}

/** Opens a folder, a text file in the editor, a log in the viewer, or downloads a large file. */
function EntryLink({ files, dir, entry }: { files: ServerFiles; dir: string; entry: FileEntry }) {
  const path = join(dir, entry.name)
  const content = (
    <>
      <FileTypeIcon entry={entry} />
      <span className="truncate">{entry.name}</span>
      {entry.fileSet && (
        <span
          className="shrink-0 rounded-md bg-info/10 px-1.5 py-0.5 text-[11px] font-medium text-info"
          title={t("Comes from the file set {{name}}. Applying the set again replaces changes made here.", { name: entry.fileSet })}
        >
          {entry.fileSet}
        </span>
      )}
    </>
  )
  const className = "flex min-w-0 items-center gap-3 font-medium hover:text-primary"
  if (entry.directory)
    return (
      <FolderLink files={files} path={path} className={className}>
        {content}
      </FolderLink>
    )
  if (entry.size > maxEditableBytes && !isLog(entry.name))
    return (
      <a href={contentUrl(files, path)} download className={className} title={t("Too large for the editor, downloads the file")}>
        {content}
      </a>
    )
  return (
    <Link
      to="/nodes/$nodeId/servers/$serverId/files"
      params={{ nodeId: files.nodeId, serverId: files.serverId }}
      search={{ path: dir || undefined, edit: path }}
      className={className}
    >
      {content}
    </Link>
  )
}

function PathBreadcrumb({ files, path, root }: { files: ServerFiles; path: string; root: string }) {
  const parts = path ? path.split("/") : []
  const crumbs = [{ name: root, path: "" }, ...parts.map((name, i) => ({ name, path: parts.slice(0, i + 1).join("/") }))]
  return (
    <Breadcrumb className="min-w-0">
      <BreadcrumbList>
        {crumbs.map((crumb, i) => (
          <Fragment key={crumb.path}>
            {i > 0 && <BreadcrumbSeparator />}
            <BreadcrumbItem>
              {i === crumbs.length - 1 ? (
                <BreadcrumbPage className="font-medium">{crumb.name}</BreadcrumbPage>
              ) : (
                <BreadcrumbLink asChild>
                  <FolderLink files={files} path={crumb.path}>
                    {crumb.name}
                  </FolderLink>
                </BreadcrumbLink>
              )}
            </BreadcrumbItem>
          </Fragment>
        ))}
      </BreadcrumbList>
    </Breadcrumb>
  )
}
