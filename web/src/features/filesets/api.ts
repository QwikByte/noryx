import { queryOptions, useMutation, useQueryClient } from "@tanstack/react-query"
import type { ServerRef } from "@/features/networks/api"
import { operate } from "@/features/operations/api"
import type { Followed } from "@/features/servers/api"
import { api } from "@/lib/api"

/** A file of a set, at a path relative to the folder of a server: text, or binary data in base64. */
export interface SetFile {
  path: string
  content: string
  /** The content of a binary file, e.g. an image, in base64; nothing is filled in. */
  data?: string
  /** Only written to servers that don't have the file, for files that plugins rewrite. */
  onlyIfMissing: boolean
}

export type ValueKind = "server" | "network" | "tag" | "all"

/** The value of a variable for a server, the servers of a network, those with a tag, or all servers. */
export interface Value {
  kind: ValueKind
  /** The ID of the server or network, or the tag. */
  scope?: string
  value: string
}

/** A variable of a set, which files use as {{var:<name>}}; a server gets the first of its values that is for it. */
export interface Variable {
  name: string
  values: Value[]
}

/** Servers a set is for: those with a tag, or the game servers or the proxy of a network. */
export type Target = { kind: "tag"; value: string; role?: "" } | { kind: "network"; value: string; role: "servers" | "proxy" }

/** A secret of a set; its value never leaves the master but to the agents. */
export interface Secret {
  name: string
  updatedAt: string
}

export interface Version {
  version: number
  user: string
  createdAt: string
  files?: SetFile[]
}

export interface FileSet {
  id: string
  name: string
  description: string
  version: number
  files: SetFile[]
  targets: Target[]
  variables: Variable[]
  secrets: Secret[]
  /** The kept versions, newest first, without their files. */
  versions: Version[]
  createdAt: string
}

export interface Summary {
  id: string
  name: string
  description: string
  version: number
  paths: string[]
  targets: Target[]
  updatedAt: string
}

export interface SetInput {
  name: string
  description: string
  files: SetFile[]
  targets: Target[]
  variables: Variable[]
  /** The version a change is based on; the master refuses it if someone saved a newer one. */
  version: number
}

export type State = "current" | "outdated" | "changed" | "missing" | "left" | "unreachable"

/** The state of a set on a server. */
export interface ServerStatus extends ServerRef {
  nodeName: string
  name?: string
  type?: string
  running: boolean
  state: State
  /** The version the server has. */
  version?: number
  /** Files that changed on the server since they were written. */
  changed?: string[]
  /** Why the set can't be applied to the server, e.g. a secret without value. */
  problem?: string
}

export type Action = "unchanged" | "created" | "changed" | "removed" | "kept"

interface Change {
  path: string
  action: Action
  secret?: boolean
}

/** What applying does with a file, with keys of the contents before and after. */
export interface PreviewFile extends Change {
  before?: string
  after?: string
  changedOnServer?: boolean
  /** What the server has can't be shown, e.g. as it is too large. */
  unknown?: boolean
  /** The new file is binary, which the preview doesn't show. */
  binary?: boolean
}

export interface PreviewServer extends ServerStatus {
  firstSecrets?: boolean
  files: PreviewFile[]
  error?: string
}

export interface Preview {
  version: number
  servers: PreviewServer[]
  /** Texts of the files by their SHA-256, shared by servers with the same files. */
  contents: Record<string, string>
}

export interface Result extends ServerStatus {
  changes: Change[]
  error?: string
  /** It runs with files it didn't load yet. */
  restart?: boolean
  restarted?: boolean
}

export const fileSetsQuery = queryOptions({
  queryKey: ["filesets"],
  queryFn: () => api<Summary[]>("/filesets"),
})

/** The states of all sets on the servers the user may see, which asks every node. */
export const statusesQuery = queryOptions({
  queryKey: ["filesets", "status"],
  queryFn: () => api<Record<string, ServerStatus[]>>("/filesets/status"),
  staleTime: 30_000,
  refetchInterval: 30_000, // the files on the servers change on their own too
})

export const fileSetQuery = (id: string) =>
  queryOptions({
    queryKey: ["filesets", id],
    queryFn: () => api<FileSet>(`/filesets/${id}`),
  })

export const statusQuery = (id: string) =>
  queryOptions({
    queryKey: ["filesets", id, "status"],
    queryFn: () => api<ServerStatus[]>(`/filesets/${id}/status`),
    staleTime: 30_000,
    refetchInterval: 30_000,
  })

export const versionQuery = (id: string, version: number) =>
  queryOptions({
    queryKey: ["filesets", id, "versions", version],
    queryFn: () => api<Version>(`/filesets/${id}/versions/${version}`),
    staleTime: Infinity, // versions never change
  })

/** Creates a set, or saves a change of it if an ID is given. */
export function useSaveFileSet(id?: string) {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: (input: SetInput) => (id ? api<FileSet>(`/filesets/${id}`, { method: "PUT", body: input }) : api<FileSet>("/filesets", { body: input })),
    onSuccess: (set) => {
      queryClient.setQueryData(fileSetQuery(set.id).queryKey, set)
      // The list and the states, which a change of the files or targets changes.
      return queryClient.invalidateQueries({ queryKey: ["filesets"], predicate: (q) => q.queryKey.length !== 2 || q.queryKey[1] !== set.id })
    },
  })
}

export function useDeleteFileSet() {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: (id: string) => api(`/filesets/${id}`, { method: "DELETE" }),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: fileSetsQuery.queryKey }),
  })
}

/** Sets the value of a secret, a new random one with generate, or deletes it without either. */
export function useSecret(id: string) {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: ({ name, value, generate, remove }: { name: string; value?: string; generate?: boolean; remove?: boolean }) =>
      remove
        ? api<unknown>(`/filesets/${id}/secrets/${encodeURIComponent(name)}`, { method: "DELETE" })
        : api<Secret>(`/filesets/${id}/secrets/${encodeURIComponent(name)}`, { method: "PUT", body: { value, generate } }),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ["filesets", id] }),
  })
}

export function usePreview(id: string) {
  return useMutation({ mutationFn: (version: number) => api<Preview>(`/filesets/${id}/preview`, { body: { version } }) })
}

export function useApply(id: string) {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: ({ onStart, ...body }: { version: number; restart: boolean; batch: number; servers?: ServerRef[] } & Followed) =>
      operate<{ results: Result[] }>(`/filesets/${id}/apply`, { body }, onStart),
    onSettled: () => queryClient.invalidateQueries({ queryKey: ["filesets"] }),
  })
}

/** Matches the placeholders of the files of sets, as the master does. */
export const placeholderPattern = /\{\{((?:server|network)\.[^{}\s]*|(?:secret|var|datastore):[^{}\n]*)\}\}/g

/** The placeholders a text uses, without their braces, in order and each once. */
export function placeholders(content: string) {
  const found = new Set<string>()
  for (const m of content.matchAll(placeholderPattern)) found.add(m[1])
  return [...found]
}

/** Whether a placeholder stands for a value only the agents fill in: a secret, or the password of a database. */
export const hidden = (placeholder: string) => placeholder.startsWith("secret:") || (placeholder.startsWith("datastore:") && placeholder.endsWith(".password"))

/** The names of the placeholders of a kind, e.g. secret, that the files of a set use. */
function used(files: SetFile[], kind: "secret" | "var") {
  const names = files.flatMap((f) => placeholders(f.content)).filter((p) => p.startsWith(`${kind}:`))
  return [...new Set(names.map((p) => p.slice(kind.length + 1)))].sort()
}

/** The names of the secrets the files of a set use. */
export const usedSecrets = (files: SetFile[]) => used(files, "secret")

/** The names of the variables the files of a set use. */
export const usedVariables = (files: SetFile[]) => used(files, "var")
