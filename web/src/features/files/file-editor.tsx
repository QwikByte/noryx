import { ArrowCounterClockwiseIcon, CaretLeftIcon, DownloadSimpleIcon, FloppyDiskIcon, SlidersHorizontalIcon } from "@phosphor-icons/react"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { useBlocker } from "@tanstack/react-router"
import { t } from "i18next"
import { lazy, Suspense, useEffect, useEffectEvent, useRef, useState } from "react"
import { Trans } from "react-i18next"
import { toast } from "sonner"
import { ConfirmDialog } from "@/components/confirm-dialog"
import { guard } from "@/components/form-dialog"
import { Pill } from "@/components/status"
import { Button } from "@/components/ui/button"
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { Skeleton } from "@/components/ui/skeleton"
import { CodeViewMenu } from "@/features/preferences/code-view"
import { useAccess } from "@/features/access/use-access"
import { ApiError } from "@/lib/api"
import { contentUrl, FileChangedError, readText, saveText, type ServerFiles, type TextFile } from "./api"
import type { EditorHandle, Problem } from "./code-editor"

// CodeMirror is only loaded when a file is opened.
const CodeEditor = lazy(() => import("./code-editor").then((m) => ({ default: m.CodeEditor })))
const DiffView = lazy(() => import("@/features/filesets/diff-view").then((m) => ({ default: m.DiffView })))

/** A change of the file on the server since it was opened, which saving would replace. */
interface Conflict {
  /** The file on the server; empty if it is gone. */
  server: TextFile
  mine: string
}

/**
 * Edits a text file of a server. Leaving with unsaved changes asks first, and saving asks
 * before it replaces a change made on the server since the file was opened.
 */
export function FileEditor({ files, path, onClose }: { files: ServerFiles; path: string; onClose: () => void }) {
  const writable = useAccess().can("files.write", files.nodeId, files.serverId)
  const name = path.split("/").pop() ?? path
  const queryClient = useQueryClient()
  const editor = useRef<EditorHandle>(null)
  const [dirty, setDirty] = useState(false)
  const [conflict, setConflict] = useState<Conflict>()
  const key = ["file", files.nodeId, files.serverId, path]
  const {
    data: file,
    error,
    isPending,
  } = useQuery({
    queryKey: key,
    queryFn: () => readText(files, path),
    staleTime: Infinity,
    gcTime: 0,
    retry: false,
  })
  // The version saved last, which the next save expects; until then, the one opened.
  const saved = useRef<string>(undefined)
  const save = useMutation({
    /** Saves the editor's text over the given version of the file, or over any without one. */
    mutationFn: ({ over }: { over?: string }) => saveText(files, path, editor.current?.value() ?? "", over),
    onSuccess: (version) => {
      saved.current = version
      setConflict(undefined)
      setDirty(false)
      toast.success(t("Saved {{name}}", { name }))
      void queryClient.invalidateQueries({ queryKey: ["files", files.nodeId, files.serverId] })
    },
    onError: async (e) => {
      if (!(e instanceof FileChangedError)) return void toast.error(e.message)
      const mine = editor.current?.value() ?? ""
      try {
        setConflict({ server: await readText(files, path), mine })
      } catch (e) {
        if (e instanceof ApiError && e.status === 404) setConflict({ server: { text: "" }, mine })
        else toast.error((e as Error).message)
      }
    },
  })
  const blocker = useBlocker({ shouldBlockFn: () => dirty, enableBeforeUnload: () => dirty, withResolver: true })
  const [problem, setProblem] = useState<Problem>()
  const opened = file?.version

  /** Saves the changes, after a warning about a syntax error, which servers trip over. */
  async function trySave() {
    if (!dirty || save.isPending) return
    const found = await editor.current?.problem()
    if (found) setProblem(found)
    else save.mutate({ over: saved.current ?? opened })
  }

  // Ctrl+S saves wherever the focus is, e.g. after a dialog closed.
  const onKey = useEffectEvent((e: KeyboardEvent) => {
    if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === "s") {
      e.preventDefault()
      void trySave()
    }
  })
  useEffect(() => {
    const listener = (e: KeyboardEvent) => onKey(e)
    window.addEventListener("keydown", listener)
    return () => window.removeEventListener("keydown", listener)
  }, [])

  /** Discards the changes and edits the file as it is on the server. */
  const takeServers = (server: TextFile) => {
    saved.current = undefined
    queryClient.setQueryData(key, server)
    setConflict(undefined)
    setDirty(false)
  }

  return (
    <section aria-labelledby="editor-heading">
      <div className="mb-4 flex flex-wrap items-center justify-between gap-3">
        <div className="flex min-w-0 items-center gap-2">
          <Button variant="ghost" size="sm" onClick={onClose}>
            <CaretLeftIcon />
            {t("Back")}
          </Button>
          <h2 id="editor-heading" className="truncate font-mono text-sm">
            {path}
          </h2>
          {dirty && <Pill tone="warning">{t("Unsaved")}</Pill>}
        </div>
        <div className="flex gap-2">
          <CodeViewMenu
            wrap="editorWrap"
            trigger={
              <Button variant="outline" size="icon" aria-label={t("View")} title={t("View")}>
                <SlidersHorizontalIcon />
              </Button>
            }
          />
          <Button variant="outline" asChild>
            <a href={contentUrl(files, path)} download>
              <DownloadSimpleIcon />
              {t("Download")}
            </a>
          </Button>
          {writable && (
            <Button disabled={!dirty || save.isPending} onClick={trySave}>
              <FloppyDiskIcon />
              {save.isPending ? t("Saving…") : t("Save")}
            </Button>
          )}
        </div>
      </div>
      {isPending ? (
        <Skeleton className="h-[65vh] min-h-80 rounded-xl" />
      ) : error ? (
        <p role="alert" className="rounded-xl border border-dashed px-4 py-14 text-center text-sm text-muted-foreground">
          {error.message}
        </p>
      ) : (
        <Suspense fallback={<Skeleton className="h-[65vh] min-h-80 rounded-xl" />}>
          <CodeEditor ref={editor} value={file.text} filename={name} readOnly={!writable} onChange={() => setDirty(true)} />
        </Suspense>
      )}
      <p className="mt-3 text-xs text-muted-foreground" hidden={!writable}>
        <Trans
          i18nKey="<key>Ctrl+S</key> saves. Servers read most settings when they start, so restart the server to apply your changes."
          components={{ key: <kbd className="rounded-md border bg-muted px-1.5 py-0.5 font-mono text-[0.6875rem]" /> }}
        />
      </p>
      <Dialog open={!!conflict} onOpenChange={(open) => !open && setConflict(undefined)}>
        {conflict && (
          <DialogContent className="sm:max-w-4xl" {...guard(save.isPending)}>
            <DialogHeader>
              <DialogTitle>{t("{{name}} changed on the server", { name })}</DialogTitle>
              <DialogDescription>
                {conflict.server.version
                  ? t("It changed since you opened it, e.g. by a plugin, a file set or another user. Saving replaces that change with yours:")
                  : t("It was deleted since you opened it. Saving creates it again with your text.")}
              </DialogDescription>
            </DialogHeader>
            <Suspense fallback={<Skeleton className="h-48 rounded-lg" />}>
              <DiffView
                before={conflict.server.text}
                after={conflict.mine}
                filename={name}
                label={t("Your changes to the file on the server")}
              />
            </Suspense>
            <DialogFooter>
              {conflict.server.version && (
                <Button variant="outline" disabled={save.isPending} onClick={() => takeServers(conflict.server)}>
                  <ArrowCounterClockwiseIcon />
                  {t("Load the server's version")}
                </Button>
              )}
              <Button variant="destructive" disabled={save.isPending} onClick={() => save.mutate({ over: conflict.server.version })}>
                <FloppyDiskIcon />
                {conflict.server.version ? t("Overwrite") : t("Save")}
              </Button>
            </DialogFooter>
          </DialogContent>
        )}
      </Dialog>
      {problem && (
        <ConfirmDialog
          open
          onOpenChange={() => {
            setProblem(undefined)
            editor.current?.reveal(problem.at)
          }}
          title={t("{{name}} has a syntax error", { name })}
          description={
            <>
              <span className="mb-2 block font-mono text-xs break-words text-foreground">
                {t("Line {{line}}: {{message}}", { line: problem.line, message: problem.message })}
              </span>
              {t("Servers and plugins may fail to read the file, or replace it with their defaults.")}
            </>
          }
          action={t("Save anyway")}
          destructive
          onConfirm={() => save.mutate({ over: saved.current ?? opened })}
        />
      )}
      <ConfirmDialog
        open={blocker.status === "blocked"}
        onOpenChange={(open) => !open && blocker.reset?.()}
        title={t("Discard your changes?")}
        description={t("The changes to {{name}} haven't been saved.", { name })}
        action={t("Discard changes")}
        destructive
        onConfirm={() => blocker.proceed?.()}
      />
    </section>
  )
}
