import {
  BracketsCurlyIcon,
  DatabaseIcon,
  DotsThreeIcon,
  DownloadSimpleIcon,
  FileIcon,
  KeyIcon,
  PencilSimpleIcon,
  PlusIcon,
  SlidersHorizontalIcon,
  TrashIcon,
  UploadSimpleIcon,
} from "@phosphor-icons/react"
import { useQuery, useQueryClient } from "@tanstack/react-query"
import { t } from "i18next"
import { type FormEvent, type RefObject, useRef, useState } from "react"
import { toast } from "sonner"
import { Callout } from "@/components/callout"
import { EmptyState } from "@/components/empty-state"
import { Pill } from "@/components/status"
import { Button } from "@/components/ui/button"
import { Dialog, DialogClose, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import {
  DropdownMenu,
  DropdownMenuCheckboxItem,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuSub,
  DropdownMenuSubContent,
  DropdownMenuSubTrigger,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { Field, FieldDescription, FieldError, FieldLabel } from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import { useAccess } from "@/features/access/use-access"
import { datastoresQuery } from "@/features/datastores/api"
import type { ServerFiles } from "@/features/files/api"
import { CodeEditor, type EditorHandle } from "@/features/files/code-editor"
import { PathPicker, type Picked, PickDialog, ServerSelect } from "@/features/files/path-picker"
import { formatBytes } from "@/lib/format"
import { msg } from "@/lib/i18n"
import type { ErrorsAt, FieldErrors } from "@/lib/use-draft"
import { type FileSet, type SetFile, type SetInput, usedSecrets, usedVariables } from "./api"
import { download, holdsCode, imageType, maxFileBytes, setFileOf, sizeOf } from "./binary"
import { FileTree } from "./file-tree"
import { importFiles, maxFiles } from "./import"
import { variables } from "./labels"
import { placeholders } from "./placeholders"
import { SecretDialog } from "./secrets"
import { SidePanel } from "./side-panel"
import { VariableDialog } from "./variables"

// An example in an empty field, which needs no translation.
const examplePath = "plugins/LuckPerms/config.yml"

const folderOf = (path = "") => path.split("/").slice(0, -1).join("/")

/** The files of a set in folders, the editor of the chosen one, and what the set is for. */
export function Workspace({
  set,
  draft,
  editable,
  selected,
  onSelect,
  onChange,
  errorAt,
}: {
  set: FileSet
  draft: SetInput
  editable: boolean
  selected?: string
  onSelect: (path: string) => void
  onChange: (change: Partial<SetInput>) => void
  /** What the master refused of the set, at its fields. */
  errorAt?: ErrorsAt
}) {
  const [dialog, setDialog] = useState<"new" | "rename" | "import" | "secret" | "variable">()
  const editor = useRef<EditorHandle>(null)
  const queryClient = useQueryClient()
  const files = draft.files
  const file = files.find((f) => f.path === selected) ?? [...files].sort((a, b) => a.path.localeCompare(b.path))[0]
  const binary = file?.data !== undefined
  const setFiles = (next: SetFile[]) => onChange({ files: next })
  const update = (path: string, change: Partial<SetFile>) => setFiles(files.map((f) => (f.path === path ? { ...f, ...change } : f)))
  const add = (added: SetFile[]) => {
    setFiles([...files.filter((f) => !added.some((a) => a.path === f.path)), ...added])
    if (added[0]) onSelect(added[0].path)
  }

  async function importFrom(server: ServerFiles, picked: Picked[]) {
    if (files.length >= maxFiles) throw new Error(t("A file set has up to {{count}} files.", { count: maxFiles }))
    const { files: read, skipped } = await importFiles(queryClient, server, picked, maxFiles - files.length)
    if (read.length === 0) throw new Error(t("None of them is a file of up to 1 MiB without code."))
    add(read)
    toast.success(t("Imported {{count}} files", { count: read.length, defaultValue_one: "Imported {{count}} file" }), {
      description:
        skipped > 0
          ? t("Skipped {{count}} that are larger than 1 MiB, hold code or don't fit into the set.", {
              count: skipped,
              defaultValue_one: "Skipped {{count}} that is larger than 1 MiB, holds code or doesn't fit into the set.",
            })
          : undefined,
    })
  }

  return (
    <div className="grid gap-4 lg:grid-cols-[16rem_minmax(0,1fr)] 2xl:grid-cols-[16rem_minmax(0,1fr)_20rem]">
      <FileTree
        files={files}
        selected={file?.path}
        editable={editable}
        onSelect={onSelect}
        onNew={() => setDialog("new")}
        onImport={() => setDialog("import")}
      />
      {file ? (
        <div className="min-w-0 space-y-3">
          <div className="flex flex-wrap items-center gap-2">
            <h2 className="min-w-0 flex-1 truncate font-mono text-sm font-medium" title={file.path}>
              {file.path}
            </h2>
            {file.onlyIfMissing && <Pill tone="info">{t("Only if missing")}</Pill>}
            {editable && (
              <>
                {!binary && (
                  <InsertMenu
                    variables={[...new Set([...draft.variables.map((v) => v.name), ...usedVariables(files)])].sort()}
                    secrets={[...new Set([...set.secrets.map((s) => s.name), ...usedSecrets(files)])].sort()}
                    onInsert={(text) => editor.current?.insert(text)}
                    onNewVariable={() => setDialog("variable")}
                    onNewSecret={() => setDialog("secret")}
                  />
                )}
                <DropdownMenu>
                  <DropdownMenuTrigger asChild>
                    <Button size="icon-sm" variant="outline" aria-label={t("More actions for {{path}}", { path: file.path })}>
                      <DotsThreeIcon weight="bold" />
                    </Button>
                  </DropdownMenuTrigger>
                  <DropdownMenuContent align="end" className="w-64">
                    <DropdownMenuCheckboxItem checked={file.onlyIfMissing} onCheckedChange={(onlyIfMissing) => update(file.path, { onlyIfMissing })}>
                      {t("Only if missing")}
                    </DropdownMenuCheckboxItem>
                    <DropdownMenuItem onSelect={() => setDialog("rename")}>
                      <PencilSimpleIcon />
                      {t("Rename or move")}
                    </DropdownMenuItem>
                    <DropdownMenuSeparator />
                    <DropdownMenuItem variant="destructive" onSelect={() => setFiles(files.filter((f) => f.path !== file.path))}>
                      <TrashIcon />
                      {t("Remove from the set")}
                    </DropdownMenuItem>
                  </DropdownMenuContent>
                </DropdownMenu>
              </>
            )}
          </div>
          {file.onlyIfMissing && (
            <Callout>{t("Servers that have the file keep their own, e.g. as a plugin rewrites it. Changes here only reach servers without it.")}</Callout>
          )}
          {binary ? (
            <BinaryView
              key={file.path}
              file={file}
              editable={editable}
              onReplace={(replaced) => setFiles(files.map((f) => (f.path === file.path ? { ...replaced, onlyIfMissing: f.onlyIfMissing } : f)))}
            />
          ) : (
            <FileEditor key={file.path} editor={editor} file={file} readOnly={!editable} onChange={(content) => update(file.path, { content })} />
          )}
        </div>
      ) : (
        <EmptyState
          icon={FileIcon}
          tone="info"
          title={t("Add the files of the set")}
          description={t("Create or upload them here, or import them from a server and replace what differs with variables and secrets.")}
        />
      )}
      <div className="lg:col-span-2 2xl:col-span-1">
        <SidePanel set={set} draft={draft} editable={editable} onChange={onChange} errorAt={errorAt} />
      </div>

      {dialog === "new" && (
        <PathDialog
          title={t("New file")}
          initial={folderOf(file?.path) && `${folderOf(file?.path)}/`}
          files={files}
          upload
          onClose={() => setDialog(undefined)}
          onSubmit={(path, bytes) => add([bytes ? setFileOf(path, bytes) : { path, content: "", onlyIfMissing: false }])}
        />
      )}
      {dialog === "rename" && file && (
        <PathDialog
          title={t("Rename or move {{path}}", { path: file.path })}
          initial={file.path}
          files={files.filter((f) => f.path !== file.path)}
          onClose={() => setDialog(undefined)}
          onSubmit={(path) => {
            setFiles(files.map((f) => (f.path === file.path ? { ...f, path } : f)))
            onSelect(path)
          }}
        />
      )}
      {dialog === "import" && (
        <PickDialog
          title={t("Import from a server")}
          description={t(
            "Choose files or whole folders. The set gets them at the same paths as in the file manager; replace any passwords in them with secrets before applying the set. Files already in the set are replaced.",
          )}
          folder={folderOf(file?.path)}
          action={t("Import")}
          onClose={() => setDialog(undefined)}
          onPick={importFrom}
        />
      )}
      {dialog === "secret" && <SecretDialog setId={set.id} onClose={() => setDialog(undefined)} onSaved={(name) => editor.current?.insert(`{{secret:${name}}}`)} />}
      {dialog === "variable" && (
        <VariableDialog
          taken={draft.variables.map((v) => v.name)}
          onClose={() => setDialog(undefined)}
          onSave={(variable) => {
            onChange({ variables: [...draft.variables, variable] })
            editor.current?.insert(`{{var:${variable.name}}}`)
          }}
        />
      )}
    </div>
  )
}

// The fields of a database that files can use, with what they are.
const databaseFields = [
  ["host", msg("Host, as the server reaches it")],
  ["port", msg("Port, as the server reaches it")],
  ["database", msg("Database")],
  ["user", msg("User")],
  ["password", msg("Password, which only the agents fill in")],
] as const

/** Inserts the variables that the master fills in, a field of a database, or a secret, where the cursor is. */
function InsertMenu({
  variables: own,
  secrets,
  onInsert,
  onNewVariable,
  onNewSecret,
}: {
  variables: string[]
  secrets: string[]
  onInsert: (text: string) => void
  onNewVariable: () => void
  onNewSecret: () => void
}) {
  const { can } = useAccess()
  const { data: datastores } = useQuery({ ...datastoresQuery, enabled: can("datastores.view") })
  // Files name a database by its datastore, which servers find in their own network.
  const databases = [...new Set(datastores?.flatMap((ds) => ds.databases.map((db) => `${ds.name}.${db.name}`)))].sort()
  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button size="sm" variant="outline">
          <BracketsCurlyIcon />
          {t("Insert")}
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="w-80">
        <DropdownMenuLabel>{t("Variables of each server")}</DropdownMenuLabel>
        {variables.map(([placeholder, description]) => (
          <DropdownMenuItem key={placeholder} onSelect={() => onInsert(placeholder)} className="flex-col items-start gap-0">
            <span className="font-mono text-xs">{placeholder}</span>
            <span className="text-xs text-muted-foreground">{t(description)}</span>
          </DropdownMenuItem>
        ))}
        <DropdownMenuSeparator />
        <DropdownMenuLabel>{t("Variables of the set, with a value for each server")}</DropdownMenuLabel>
        {own.map((name) => (
          <DropdownMenuItem key={name} onSelect={() => onInsert(`{{var:${name}}}`)}>
            <SlidersHorizontalIcon />
            <span className="font-mono text-xs">{name}</span>
          </DropdownMenuItem>
        ))}
        <DropdownMenuItem onSelect={onNewVariable}>
          <PlusIcon />
          {t("New variable…")}
        </DropdownMenuItem>
        {databases.length > 0 && (
          <>
            <DropdownMenuSeparator />
            <DropdownMenuLabel>{t("Databases of each server's network")}</DropdownMenuLabel>
            {databases.map((database) => (
              <DropdownMenuSub key={database}>
                <DropdownMenuSubTrigger>
                  <DatabaseIcon />
                  <span className="font-mono text-xs">{database}</span>
                </DropdownMenuSubTrigger>
                <DropdownMenuSubContent className="w-72">
                  {databaseFields.map(([field, description]) => (
                    <DropdownMenuItem
                      key={field}
                      disabled={field === "password" && !can("datastores.manage")}
                      onSelect={() => onInsert(`{{datastore:${database}.${field}}}`)}
                      className="flex-col items-start gap-0"
                    >
                      <span className="font-mono text-xs">{field}</span>
                      <span className="text-xs text-muted-foreground">{t(description)}</span>
                    </DropdownMenuItem>
                  ))}
                </DropdownMenuSubContent>
              </DropdownMenuSub>
            ))}
          </>
        )}
        <DropdownMenuSeparator />
        <DropdownMenuLabel>{t("Secrets, which only the agents fill in")}</DropdownMenuLabel>
        {secrets.map((name) => (
          <DropdownMenuItem key={name} onSelect={() => onInsert(`{{secret:${name}}}`)}>
            <KeyIcon />
            <span className="font-mono text-xs">{name}</span>
          </DropdownMenuItem>
        ))}
        <DropdownMenuItem onSelect={onNewSecret}>
          <PlusIcon />
          {t("New secret…")}
        </DropdownMenuItem>
      </DropdownMenuContent>
    </DropdownMenu>
  )
}

/** The editor of a file, which starts with its content and reports each change. */
function FileEditor({
  editor,
  file,
  readOnly,
  onChange,
}: {
  editor: RefObject<EditorHandle | null>
  file: SetFile
  readOnly: boolean
  onChange: (content: string) => void
}) {
  const [initial] = useState(file.content)
  return (
    <CodeEditor
      ref={editor}
      value={initial}
      filename={file.path}
      readOnly={readOnly}
      extensions={placeholders}
      onChange={() => onChange(editor.current?.value() ?? "")}
    />
  )
}

/** Reads a file of the computer that a set can have, or throws why it can't. */
async function readUpload(picked: File) {
  const bytes = new Uint8Array(await picked.arrayBuffer())
  if (bytes.length > maxFileBytes) throw new Error(t("The file is larger than 1 MiB."))
  if (holdsCode(picked.name, bytes)) throw new Error(t("A file set can't add code. Install plugins on the Plugins page."))
  return bytes
}

/** A binary file of a set, which is never shown as text: images are previewed, others only named. */
function BinaryView({ file, editable, onReplace }: { file: SetFile; editable: boolean; onReplace: (file: SetFile) => void }) {
  const input = useRef<HTMLInputElement>(null)
  const type = imageType(file.path)
  async function replace(picked?: File) {
    if (!picked) return
    try {
      onReplace(setFileOf(file.path, await readUpload(picked)))
    } catch (e) {
      toast.error((e as Error).message)
    }
  }
  return (
    <div className="surface grid justify-items-center gap-4 rounded-xl px-4 py-8 text-center">
      {type ? (
        <img
          src={`data:${type};base64,${file.data}`}
          alt={t("Preview of {{path}}", { path: file.path })}
          className="max-h-64 min-h-16 max-w-full rounded-lg object-contain ring-1 ring-border [image-rendering:pixelated]"
        />
      ) : (
        <FileIcon className="size-12 text-muted-foreground" />
      )}
      <div className="space-y-1">
        <p className="text-sm font-medium">{t("Binary file, {{size}}", { size: formatBytes(sizeOf(file)) })}</p>
        <p className="text-sm text-muted-foreground">{t("Servers get it as it is: nothing is filled in.")}</p>
      </div>
      <div className="flex flex-wrap justify-center gap-2">
        <Button variant="outline" size="sm" onClick={() => download(file)}>
          <DownloadSimpleIcon />
          {t("Download")}
        </Button>
        {editable && (
          <>
            <Button variant="outline" size="sm" onClick={() => input.current?.click()}>
              <UploadSimpleIcon />
              {t("Replace…")}
            </Button>
            <input ref={input} type="file" className="hidden" onChange={(e) => void replace(e.target.files?.[0])} />
          </>
        )}
      </div>
    </div>
  )
}

/** Checks a path of a file of a set like the master, as far as the panel can, and returns it cleaned. */
function cleanPath(input: string, files: SetFile[]): [string, string?] {
  const path = input.trim().replace(/^\/+/, "").replace(/\/+/g, "/")
  if (!path || path.endsWith("/")) return [path, t("Enter the path of a file, e.g. plugins/LuckPerms/config.yml.")]
  if (path.split("/").some((s) => s === ".." || s === ".") || path.includes("\\")) return [path, t("Enter a path inside the server folder.")]
  if (files.some((f) => f.path === path)) return [path, t("The set has this file already.")]
  return [path]
}

/** Asks for the path of a file, whose folder can be chosen on a server, and with upload for a file of the computer. */
function PathDialog({
  title,
  initial,
  files,
  upload,
  onClose,
  onSubmit,
}: {
  title: string
  initial: string
  files: SetFile[]
  upload?: boolean
  onClose: () => void
  onSubmit: (path: string, bytes?: Uint8Array) => void
}) {
  const [path, setPath] = useState(initial)
  const [problem, setProblem] = useState<{ at: "path" | "upload"; message: string }>()
  const errors = (at: "path" | "upload"): FieldErrors | undefined => (problem?.at === at ? [problem] : undefined)
  const [server, setServer] = useState<ServerFiles>()
  const [browsing, setBrowsing] = useState(false)
  const [uploaded, setUploaded] = useState<{ name: string; bytes: Uint8Array }>()
  const name = path.split("/").pop() ?? ""

  function submit(event: FormEvent) {
    event.preventDefault()
    const [clean, message] = cleanPath(path, files)
    if (message) return setProblem({ at: "path", message })
    onSubmit(clean, uploaded?.bytes)
    onClose()
  }

  async function choose(picked?: File) {
    if (!picked) return
    try {
      setUploaded({ name: picked.name, bytes: await readUpload(picked) })
      setProblem(undefined)
      if (!name) setPath(`${path}${picked.name}`)
    } catch (e) {
      setUploaded(undefined)
      setProblem({ at: "upload", message: (e as Error).message })
    }
  }

  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent className="sm:max-w-xl">
        <form onSubmit={submit} className="grid gap-5">
          <DialogHeader>
            <DialogTitle>{title}</DialogTitle>
            <DialogDescription>{t("Servers get the file at this path in their folder.")}</DialogDescription>
          </DialogHeader>
          <Field data-invalid={!!errors("path")}>
            <FieldLabel htmlFor="fileset-path">{t("Path in the server folder")}</FieldLabel>
            <Input
              id="fileset-path"
              className="font-mono"
              autoFocus
              required
              aria-invalid={!!errors("path")}
              placeholder={examplePath}
              value={path}
              onChange={(e) => setPath(e.target.value)}
            />
            <FieldError errors={errors("path")} />
          </Field>
          {upload && (
            <Field data-invalid={!!errors("upload")}>
              <FieldLabel htmlFor="fileset-upload">{t("Content")}</FieldLabel>
              <Input id="fileset-upload" type="file" aria-invalid={!!errors("upload")} onChange={(e) => void choose(e.target.files?.[0])} />
              <FieldDescription>
                {uploaded
                  ? t("{{name}}, {{size}}", { name: uploaded.name, size: formatBytes(uploaded.bytes.length) })
                  : t("Empty, or a file of your computer of up to 1 MiB, e.g. an image, which servers get as it is.")}
              </FieldDescription>
              <FieldError errors={errors("upload")} />
            </Field>
          )}
          {browsing ? (
            <div className="grid gap-3">
              <Field>
                <FieldLabel htmlFor="path-server">{t("Choose the folder on a server")}</FieldLabel>
                <ServerSelect id="path-server" value={server} onChange={setServer} />
              </Field>
              {server && <PathPicker server={server} folder={folderOf(path)} onFolder={(folder) => setPath(folder ? `${folder}/${name}` : name)} />}
            </div>
          ) : (
            <Button type="button" variant="ghost" size="sm" className="justify-self-start" onClick={() => setBrowsing(true)}>
              {t("Choose the folder on a server…")}
            </Button>
          )}
          <DialogFooter>
            <DialogClose asChild>
              <Button variant="outline">{t("Cancel")}</Button>
            </DialogClose>
            <Button type="submit">{t("Save")}</Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
