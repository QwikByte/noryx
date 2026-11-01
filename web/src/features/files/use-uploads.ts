import { useQueryClient } from "@tanstack/react-query"
import { useBlocker } from "@tanstack/react-router"
import { t } from "i18next"
import { useRef, useState } from "react"
import { toast } from "sonner"
import { createFolders, join, type ServerFiles, upload } from "./api"

export interface Upload {
  id: number
  name: string
  progress: number
  state: "queued" | "uploading" | "done" | "failed"
  error?: string
}

/** A file to upload, by its path in the folder it goes into, e.g. inside a dropped folder. */
interface Dropped {
  file: File
  path: string
}

/** Files to upload, with the folders to create for them, also empty ones, by their paths. */
export interface Batch {
  files: Dropped[]
  folders: string[]
}

/** The most files uploaded at once, which a dropped folder could exceed by far. */
const maxBatch = 10_000

const tooMany = () => new Error(t("Upload up to {{count}} files at once.", { count: maxBatch }))

/** Reads dropped files and folders with everything in them. The entries must be taken from the drop event. */
export async function readDrop(entries: FileSystemEntry[]): Promise<Batch> {
  const batch: Batch = { files: [], folders: [] }
  const visit = async (entry: FileSystemEntry, path: string) => {
    if (entry.isFile) {
      if (batch.files.length === maxBatch) throw tooMany()
      const file = await new Promise<File>((resolve, reject) => (entry as FileSystemFileEntry).file(resolve, reject))
      batch.files.push({ file, path })
    } else if (entry.isDirectory) {
      batch.folders.push(path)
      const reader = (entry as FileSystemDirectoryEntry).createReader()
      const read = () => new Promise<FileSystemEntry[]>((resolve, reject) => reader.readEntries(resolve, reject))
      // Browsers return the entries of a folder in parts, until an empty one.
      for (let part = await read(); part.length > 0; part = await read()) {
        for (const child of part) await visit(child, `${path}/${child.name}`)
      }
    }
  }
  for (const entry of entries) await visit(entry, entry.name)
  return batch
}

/** Reads the files of a file input, with the folders that those of a chosen folder are in. */
export function readPicked(picked: File[]): Batch {
  if (picked.length > maxBatch) throw tooMany()
  const files = picked.map((file) => ({ file, path: file.webkitRelativePath || file.name }))
  return { files, folders: [...new Set(files.map((f) => f.path.split("/").slice(0, -1).join("/")).filter(Boolean))] }
}

interface Job {
  id: number
  server: ServerFiles
  file: File
  path: string
  overwrite: boolean
  controller: AbortController
}

/** Uploads files one after another and tracks their progress. */
export function useUploads(s: ServerFiles) {
  const queryClient = useQueryClient()
  const [uploads, setUploads] = useState<Upload[]>([])
  const queue = useRef<Job[]>([])
  const jobs = useRef(new Map<number, Job>())
  const nextId = useRef(0)
  const running = useRef(false)
  const busy = uploads.some((u) => u.state === "queued" || u.state === "uploading")

  const update = (id: number, patch: Partial<Upload>) => setUploads((list) => list.map((u) => (u.id === id ? { ...u, ...patch } : u)))
  const remove = (id: number) => setUploads((list) => list.filter((u) => u.id !== id))

  // Leaving the panel would cancel running uploads; other pages of it don't.
  useBlocker({ shouldBlockFn: () => false, enableBeforeUnload: () => busy })

  async function run() {
    if (running.current) return
    running.current = true
    for (let job = queue.current.shift(); job; job = queue.current.shift()) {
      const { id } = job
      update(id, { state: "uploading" })
      try {
        await upload(job.server, job.path, job.file, {
          overwrite: job.overwrite,
          signal: job.controller.signal,
          onProgress: (progress) => update(id, { progress }),
        })
        update(id, { state: "done", progress: 1 })
        setTimeout(() => remove(id), 4000)
        void queryClient.invalidateQueries({ queryKey: ["files", job.server.nodeId, job.server.serverId] })
      } catch (e) {
        if (job.controller.signal.aborted) remove(id)
        else update(id, { state: "failed", error: (e as Error).message })
      } finally {
        jobs.current.delete(id)
      }
    }
    running.current = false
  }

  /** Creates the folders of a batch in a folder, then queues its files for upload into them. */
  async function add({ files, folders }: Batch, dir: string, overwrite: boolean) {
    try {
      await createFolders(s, folders.map((f) => join(dir, f)))
    } catch (e) {
      toast.error((e as Error).message)
      return
    } finally {
      if (folders.length) void queryClient.invalidateQueries({ queryKey: ["files", s.nodeId, s.serverId] })
    }
    const added = files.map(({ file, path }) => ({
      id: nextId.current++,
      server: s,
      file,
      path: join(dir, path),
      overwrite,
      controller: new AbortController(),
    }))
    for (const job of added) jobs.current.set(job.id, job)
    queue.current.push(...added)
    setUploads((list) => [...list, ...added.map(({ id }, i) => ({ id, name: files[i].path, progress: 0, state: "queued" as const }))])
    void run()
  }

  /** Cancels a queued or running upload, or dismisses a finished one. */
  function cancel(id: number) {
    queue.current = queue.current.filter((job) => job.id !== id)
    jobs.current.get(id)?.controller.abort()
    remove(id)
  }

  return { uploads, add, cancel }
}
