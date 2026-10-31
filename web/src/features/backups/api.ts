import { queryOptions, useMutation, useQueryClient } from "@tanstack/react-query"
import { t } from "i18next"
import type { Listing, ServerFiles } from "@/features/files/api"
import { type Operation, operate } from "@/features/operations/api"
import { defaultSchedule, type TaskInput, taskApi } from "@/features/schedules/api"
import { api, send } from "@/lib/api"
import { locale } from "@/lib/i18n"

/** What of a server is backed up; the agent finds the matching files when it backs up. */
export interface Selection {
  /** The whole folder of the server; the other choices don't matter then. */
  everything: boolean
  /** Folders with a level.dat, e.g. world and world_nether. */
  worlds: boolean
  /** The plugins or mods folder; for mods also config/. */
  plugins: boolean
  /** Files in the server's folder except .jar files and logs, and the config folder. */
  config: boolean
  /** Further files or folders, relative to the server's folder. */
  paths: string[]
  /** Files and folders inside the others that are left out, e.g. the tiles of a map plugin; jobs from before have none. */
  exclude?: string[]
}

export interface Backup {
  id: string
  label: string
  createdAt: string
  size: number
  /** Storage location on the node. */
  location: string
  /** Files and folders in the backup; "." is everything. */
  paths: string[]
  /** Files and folders inside the paths that the backup left out. */
  exclude: string[]
  /** The job that made the backup; empty for backups made by hand. */
  jobId?: string
  /** Kept backups are never deleted by their job. */
  kept: boolean
  /** The backup came from elsewhere, e.g. an upload, so restoring it trusts nothing in its archive. Older masters don't tell. */
  untrusted?: boolean
}

export interface JobSettings {
  selection: Selection
  /** Datastores backed up with all their databases; jobs from before datastores have none. */
  datastores?: string[]
  /** Storage location on each node; empty means the default one. */
  location: string
  /** The newest backups of the job kept per server; 0, like the others, keeps all. */
  keep: number
  /** The newest backup of each of the last days, weeks and months with backups is kept too; jobs from before have none. */
  keepDays?: number
  keepWeeks?: number
  keepMonths?: number
  /** Where the job copies its backups of servers to, away from their nodes; none for nowhere. */
  copy?: CopyTo
}

/** S3-compatible storage, or another node in one of its storage locations (empty for the default one). */
export interface CopyTo {
  storage?: string
  node?: string
  location?: string
}

export const jobs = taskApi<JobSettings>("/backup-jobs")

export const defaultSelection: Selection = { everything: false, worlds: true, plugins: true, config: true, paths: [], exclude: [] }

export const emptyJob: TaskInput<JobSettings> = {
  name: "",
  enabled: true,
  schedule: defaultSchedule,
  targets: [],
  settings: { selection: defaultSelection, datastores: [], location: "", keep: 0, keepDays: 7, keepWeeks: 4, keepMonths: 0 },
}

/** Where the backup job of the master keeps the archives of its database and certificate authority, and how many. */
export interface MasterSettings {
  /** A storage, or a node in one of its storage locations. */
  to: CopyTo
  /** How many of the newest archives it keeps there, from 1 to 1000. */
  keep: number
}

/** The backup job of the master, of which there is one at most. Only administrators see it. */
export const masterJobs = taskApi<MasterSettings>("/master-backups")

/** The least characters of the passphrase that encrypts the archives of the master. */
export const minPassphrase = 12

export const emptyMasterJob = (): TaskInput<MasterSettings> => ({
  name: t("Backup of the master"),
  enabled: true,
  schedule: { ...defaultSchedule, times: ["03:30"] },
  targets: [],
  settings: { to: {}, keep: 7 },
})

/** Why the master refuses paths of a selection or a restore, if it does. */
export function pathsError(paths: string[]): string | undefined {
  if (paths.length > 20) return t("Enter at most 20 files or folders.")
  const invalid = paths.find((p) => p.length > 1024 || /[\0\\]/.test(p) || p.split("/").includes(".."))
  if (invalid !== undefined) {
    return t("The path {{path}} is invalid. Enter paths inside the server's folder, like plugins/LuckPerms.", { path: invalid })
  }
}

/** Whether a selection backs up nothing. */
export const nothingSelected = (s: Selection) => !s.everything && !s.worlds && !s.plugins && !s.config && s.paths.length === 0

const and = (parts: string[]) => new Intl.ListFormat(locale, { type: "conjunction" }).format(parts)

/** Adds what is left out to a description of what is backed up. */
const without = (text: string, exclude: string[] = []) =>
  exclude.length > 0 ? t("{{content}} without {{paths}}", { content: text, paths: and(exclude) }) : text

/** Describes a selection, e.g. "Worlds, plugins and configuration". */
export function describeSelection(s: Selection): string {
  if (s.everything) return without(t("Everything"), s.exclude)
  const parts = [s.worlds && t("worlds"), s.plugins && t("plugins or mods"), s.config && t("configuration"), ...s.paths].filter(
    (p): p is string => !!p,
  )
  const text = and(parts)
  return without(text.charAt(0).toUpperCase() + text.slice(1), s.exclude)
}

/** Describes what a backup contains, e.g. "world, plugins and server.properties". */
export const describeContent = (b: Pick<Backup, "paths" | "exclude">) =>
  without(b.paths.includes(".") ? t("Everything") : and(b.paths), b.exclude)

/** Describes which backups a job keeps, e.g. "keeps 7 daily and 4 weekly". */
export function describeRetention(s: JobSettings): string {
  const parts = [
    s.keep > 0 && t("{{count}} newest", { count: s.keep }),
    !!s.keepDays && t("{{count}} daily", { count: s.keepDays }),
    !!s.keepWeeks && t("{{count}} weekly", { count: s.keepWeeks }),
    !!s.keepMonths && t("{{count}} monthly", { count: s.keepMonths }),
  ].filter((p): p is string => !!p)
  return parts.length > 0 ? t("keeps {{what}}", { what: and(parts) }) : t("keeps all")
}

const base = (nodeId: string, serverId: string) => `/nodes/${nodeId}/servers/${serverId}/backups`

export const backupsQuery = (nodeId: string, serverId: string) =>
  queryOptions({
    queryKey: ["nodes", nodeId, "servers", serverId, "backups"],
    queryFn: () => api<Backup[]>(base(nodeId, serverId)),
    refetchInterval: 30_000, // e.g. backup jobs add some
  })

/** A folder of a backup, listed like one of the server. */
export const backupFilesQuery = (s: ServerFiles, id: string, path: string) =>
  queryOptions({
    queryKey: ["nodes", s.nodeId, "servers", s.serverId, "backups", id, "files", path],
    queryFn: () => api<Listing>(`${base(s.nodeId, s.serverId)}/${id}/files?path=${encodeURIComponent(path)}`),
    staleTime: Infinity, // a backup doesn't change
  })

export const downloadUrl = (nodeId: string, serverId: string, id: string) => `/api${base(nodeId, serverId)}/${id}/download`

/** Uploads a ZIP archive as a backup of a server, with progress reports. */
export function uploadBackup(
  nodeId: string,
  serverId: string,
  archive: File,
  { label, location, onProgress, signal }: { label: string; location: string; onProgress: (fraction: number) => void; signal: AbortSignal },
) {
  const query = new URLSearchParams({ label, location })
  return send<Backup>("POST", `${base(nodeId, serverId)}/upload?${query}`, archive, { onProgress, signal })
}

/** What a restore restores, and where. */
export interface Restore {
  id: string
  /** Files and folders of the backup; none restores all of it. */
  paths: string[]
  /** Backs up what the restore replaces first. */
  snapshotFirst: boolean
  /** Another server to restore the backup into, also on another node. */
  into?: ServerFiles
  onStart?: (op: Operation) => void
}

/** How a restore went. */
export interface Restored {
  /** The backup of what the restore replaced, if one was made. */
  snapshot?: Backup
  /** Tells that the network of the server couldn't be configured again. */
  warning?: string
}

/** Backs up a server, changes, restores and deletes its backups. */
export function useBackups(nodeId: string, serverId: string) {
  const queryClient = useQueryClient()
  const onSettled = () => queryClient.invalidateQueries({ queryKey: backupsQuery(nodeId, serverId).queryKey })
  return {
    create: useMutation({
      mutationFn: ({ onStart, ...input }: { label: string; selection: Selection; location: string; onStart?: (op: Operation) => void }) =>
        operate<Backup>(base(nodeId, serverId), { body: input }, onStart),
      onSettled,
    }),
    update: useMutation({
      mutationFn: ({ id, ...change }: { id: string; label?: string; kept?: boolean }) =>
        api<Backup>(`${base(nodeId, serverId)}/${id}`, { method: "PATCH", body: change }),
      onSettled,
    }),
    restore: useMutation({
      mutationFn: ({ id, into, onStart, ...what }: Restore) =>
        into
          ? operate<Restored>(`${base(nodeId, serverId)}/${id}/restore-into`, { body: { node: into.nodeId, server: into.serverId, ...what } }, onStart)
          : operate<Restored>(`${base(nodeId, serverId)}/${id}/restore`, { body: what }, onStart),
      // Also the backups of the server restored into, which got the backup of what was replaced.
      onSettled: (_, __, { into }) =>
        Promise.all([nodeId, into?.nodeId].map((node) => node && queryClient.invalidateQueries({ queryKey: ["nodes", node, "servers"] }))),
    }),
    remove: useMutation({ mutationFn: (id: string) => api(`${base(nodeId, serverId)}/${id}`, { method: "DELETE" }), onSettled }),
  }
}

/** S3-compatible storage that jobs copy backups to. Its secret key is never shown. */
export interface Storage extends StorageSettings {
  id: string
  /** How many copies it holds. */
  copies: number
}

export interface StorageSettings {
  name: string
  /** The host, with a port unless it is 443; the master only connects over HTTPS. */
  endpoint: string
  region: string
  bucket: string
  /** Folder of the copies in the bucket; empty for its top. */
  prefix: string
  accessKey: string
  /** Addresses the bucket in the path rather than in the host, e.g. for MinIO. */
  pathStyle: boolean
  /** Asks the storage to encrypt the copies with its own keys. */
  encrypt: boolean
}

export const storagesQuery = queryOptions({
  queryKey: ["backup-storages"],
  queryFn: () => api<Storage[]>("/backup-storages"),
})

/** Whether a query is about copies or storages, which deleting copies or storages changes. */
const aboutCopies = ({ queryKey }: { queryKey: readonly unknown[] }) => queryKey.includes("copies") || queryKey[0] === "backup-storages"

/** Adds, changes and deletes storages; an empty secret key keeps the one a storage has. */
export function useStorages() {
  const queryClient = useQueryClient()
  return {
    save: useMutation({
      mutationFn: ({ id, ...input }: StorageSettings & { id?: string; secretKey: string }) =>
        api<Storage>(id ? `/backup-storages/${id}` : "/backup-storages", { method: id ? "PUT" : "POST", body: input }),
      onSettled: () => queryClient.invalidateQueries({ queryKey: storagesQuery.queryKey }),
    }),
    // The master forgets the copies of a deleted storage.
    remove: useMutation({
      mutationFn: (id: string) => api(`/backup-storages/${id}`, { method: "DELETE" }),
      onSettled: () => queryClient.invalidateQueries({ predicate: aboutCopies }),
    }),
  }
}

/** A copy of a backup of a server away from its node, which the server can be restored from, also once it is gone. */
export interface Copy extends Pick<Backup, "label" | "createdAt" | "size" | "paths" | "exclude" | "kept"> {
  id: number
  /** The job that made it; none once the job is deleted. */
  jobId?: string
  serverId: string
  serverName: string
  /** The node that the server was on when it was copied. */
  nodeId: string
  nodeName: string
  proxy: boolean
  backupId: string
  copiedAt: string
  /** The storage that holds it, or the node, in its storage location. */
  storageId?: string
  copyNodeId?: string
  location?: string
  /** The name of the storage or node. */
  where: string
}

const copiesBase = (server?: ServerFiles) => (server ? `/nodes/${server.nodeId}/servers/${server.serverId}/copies` : "/backup-copies")

/** The copies of a server, or of all servers without one, e.g. of those that are gone. */
export const copiesQuery = (server?: ServerFiles) =>
  queryOptions({
    queryKey: server ? ["nodes", server.nodeId, "servers", server.serverId, "copies"] : ["backup-copies"],
    queryFn: () => api<Copy[]>(copiesBase(server)),
    refetchInterval: 60_000, // jobs add some
  })

/** Restores and deletes copies, through the routes of a server or, without one, those of all copies. */
export function useCopies(server?: ServerFiles) {
  const queryClient = useQueryClient()
  return {
    restore: useMutation({
      mutationFn: ({ id, into, snapshotFirst, onStart }: { id: number; into: ServerFiles; snapshotFirst: boolean; onStart?: (op: Operation) => void }) =>
        operate<Restored>(`${copiesBase(server)}/${id}/restore`, { body: { node: into.nodeId, server: into.serverId, snapshotFirst } }, onStart),
      // The server restored into got the backup of what was replaced.
      onSettled: (_, __, { into }) => queryClient.invalidateQueries({ queryKey: ["nodes", into.nodeId, "servers"] }),
    }),
    remove: useMutation({
      mutationFn: (id: number) => api(`${copiesBase(server)}/${id}`, { method: "DELETE" }),
      onSettled: () => queryClient.invalidateQueries({ predicate: aboutCopies }),
    }),
  }
}
