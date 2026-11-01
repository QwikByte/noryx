import { infiniteQueryOptions, keepPreviousData, queryOptions, useMutation, useQueryClient } from "@tanstack/react-query"
import { t } from "i18next"
import type { ServerRef } from "@/features/networks/api"
import { serverType } from "@/features/servers/server-types"
import { isCancelled, type Operation, operate } from "@/features/operations/api"
import { api, responseError } from "@/lib/api"

/** A plugin or mod on Modrinth, or a plugin on Hangar. */
export interface Project {
  id: string
  slug: string
  title: string
  /** Served by the panel, which fetches it from Modrinth or Hangar. */
  icon?: string
}

export interface SearchHit extends Project {
  description: string
  author: string
  downloads: number
  follows: number
  updated: string
  /** Whether players need the project too. */
  clientSide: "required" | "optional" | "unsupported" | "unknown"
  /** Loaders of manageable servers the project supports, e.g. "paper" or "fabric". */
  loaders: string[]
  /** The main categories of the project, e.g. "economy". */
  categories: string[]
}

export type Sort = "relevance" | "downloads" | "follows" | "newest" | "updated"

/** What servers load from Modrinth. */
export type Kind = "plugins" | "mods"

/** Where plugins come from: Modrinth has plugins and mods, Hangar plugins of Paper, Velocity and BungeeCord (Waterfall there). */
export type Source = "modrinth" | "hangar"

/** The page of a project on Modrinth or Hangar, whose project IDs start with "hangar-". */
export const projectUrl = (project: Project) =>
  project.id.startsWith("hangar-") ? `https://hangar.papermc.io/${project.slug}` : `https://modrinth.com/project/${project.slug}`

/** Whether Hangar has plugins for a server type: Paper and its forks except Folia, and the proxies. */
export const onHangar = (type: string) =>
  serverType(type).addons?.loaders.some((l) => ["paper", "velocity", "waterfall"].includes(l)) ?? false

/** A search on Modrinth or Hangar; without a kind, type or version, projects for any of them are found. */
export interface Search {
  source?: Source
  query: string
  /** Modpacks are searched to create servers from them. */
  kind?: Kind | "modpacks"
  type?: string
  version?: string
  /** Categories the projects must all be in. */
  categories: string[]
  sort: Sort
  /** Only projects that players don't have to install. */
  serverOnly: boolean
}

/**
 * Where a version was published: as a release, or as a pre-release in beta or alpha. A server takes a project in a
 * channel too: releases only, betas too, or alphas too.
 */
export type Channel = "release" | "beta" | "alpha"

/** The channels from the most stable; each takes the versions of those before it too. */
export const channels: Channel[] = ["release", "beta", "alpha"]

/** Whether a server that takes a project in a channel takes a version of the channel of. */
const takes = (channel: Channel = "release", of: Channel = "release") => channels.indexOf(of) <= channels.indexOf(channel)

/** What a server takes of a project in a channel. */
export const channelLabel = (channel: Channel) =>
  channel === "beta" ? t("Betas too") : channel === "alpha" ? t("Alphas too") : t("Releases")

/** The newest version that suits a server in a channel. */
export const newestLabel = (channel: Channel = "release") =>
  channel === "beta" ? t("Newest suitable beta or release") : channel === "alpha" ? t("Newest suitable version") : t("Newest suitable release")

/** A version of a project. */
export interface ProjectVersion {
  id: string
  number: string
  channel: Channel
  published: string
  /** The versions of Minecraft it supports. */
  gameVersions?: string[]
}

/** A version of a project with what changed in it: Markdown of its author, shown without HTML. */
export interface Change extends ProjectVersion {
  changelog: string
}

/** A plugin file on a server; files from Modrinth and Hangar are recognised by their hash. */
export interface InstalledPlugin {
  fileName: string
  size: number
  project?: Project
  version?: string
  versionId?: string
  /** Set for a version that isn't a release. */
  channel?: Exclude<Channel, "release">
  /** A newer version that suits the server in the channel it takes the project in, its ID and its channel. */
  update?: string
  updateId?: string
  updateChannel?: Exclude<Channel, "release">
  /** The server keeps the project at its version: updating all plugins leaves it out. */
  pinned?: boolean
  /** The channel the server takes the project in, if not only releases. */
  tracks?: Exclude<Channel, "release">
  /** Turned off: the server keeps the file but doesn't load it. */
  disabled?: boolean
  /** The folder of the plugin's settings, e.g. plugins/LuckPerms. */
  settings?: string
  /** The other turned-on projects of the server that require it. */
  requiredBy?: string[]
}

export interface PluginListing {
  folder: "plugins" | "mods"
  plugins: InstalledPlugin[]
  /** Why the plugins couldn't be looked up on Modrinth or Hangar. */
  catalogueError?: string
}

/** A file installed on a server. */
export interface InstalledFile {
  projectId: string
  fileName: string
  version: string
  /** Set for a version that isn't a release. */
  channel?: Exclude<Channel, "release">
}

/** What installing, updating or removing plugins did on a server. */
export interface InstallResult extends ServerRef {
  /** The files installed, also those present already; an update only tells the files it wrote. */
  installed: InstalledFile[]
  removed?: string[]
  /** The projects with a newer release that the server keeps at their version. */
  pinned?: string[]
  error?: string
  /** The server runs without what changed, or was restarted to load it. */
  restart?: boolean
  restarted?: boolean
}

/** Restarts the running servers whose plugins changed afterwards, the game servers of a network batch at a time. */
export interface Restart {
  restart?: boolean
  batch?: number
}

/** A file of a project on a server, as the server's listing describes it. */
export type InstalledOn = ServerRef & Omit<InstalledPlugin, "project">

/** What the servers the user may see have installed, by project of Modrinth or Hangar. */
export interface Everywhere {
  projects: { project: Project; servers: InstalledOn[] }[]
  /** Nodes, or single servers, whose plugins couldn't be listed. */
  unreachable: { nodeId: string; nodeName: string; serverId?: string; error: string }[]
  catalogueError?: string
}

/** What the servers have installed; it asks all nodes, so it isn't asked often. */
export const installedQuery = queryOptions({
  queryKey: ["plugins", "installed"],
  queryFn: () => api<Everywhere>("/plugins/installed"),
  staleTime: 30_000,
})

/**
 * Whether a pre-release was installed because no version of the chosen channel suits the server, rather than chosen
 * among the versions.
 */
export const fallback = (file: InstalledFile, versions?: Record<string, string>, channels?: Record<string, Channel>) =>
  !takes(channels?.[file.projectId], file.channel) && !versions?.[file.projectId]

/** Whether a project runs on a server type. */
export function supports(loaders: string[], type: string) {
  return serverType(type).addons?.loaders.some((l) => loaders.includes(l)) ?? false
}

/** Searches Modrinth for plugins and mods, or Hangar for plugins. */
export const searchQuery = (search: Search) =>
  infiniteQueryOptions({
    queryKey: ["plugins", "search", search],
    queryFn: ({ pageParam }) => {
      const { source = "modrinth", query, kind = "", type = "", version = "", categories, sort, serverOnly } = search
      const params = new URLSearchParams({ source, query, kind, type, version, sort, offset: String(pageParam) })
      for (const category of categories) params.append("category", category)
      if (serverOnly) params.set("serverOnly", "true")
      return api<{ hits: SearchHit[]; total: number }>(`/plugins/search?${params}`)
    },
    initialPageParam: 0,
    getNextPageParam: (last, pages) => {
      const loaded = pages.reduce((n, p) => n + p.hits.length, 0)
      return loaded < last.total && last.hits.length > 0 ? loaded : undefined
    },
    placeholderData: keepPreviousData,
    staleTime: 60_000,
  })

/** The releases of Minecraft, the newest first. */
export const gameVersionsQuery = queryOptions({
  queryKey: ["plugins", "game-versions"],
  queryFn: () => api<string[]>("/plugins/game-versions"),
  staleTime: 3_600_000,
})

/** The versions of a project that run on a server type and Minecraft version, the newest first. */
export const versionsQuery = (project: string, type: string, version: string) =>
  queryOptions({
    queryKey: ["plugins", "versions", project, type, version],
    queryFn: () => api<ProjectVersion[]>(`/plugins/projects/${project}/versions?${new URLSearchParams({ type, version })}`),
    staleTime: 60_000,
  })

/**
 * What changed in the versions of a project that run on a server type and Minecraft version, the newest first: those
 * after the version from up to the version to, or the newest ones.
 */
export const changesQuery = (project: string, type: string, version: string, from = "", to = "") =>
  queryOptions({
    queryKey: ["plugins", "changes", project, type, version, from, to],
    queryFn: () => api<Change[]>(`/plugins/projects/${project}/changes?${new URLSearchParams({ type, version, from, to })}`),
    staleTime: 300_000,
  })

const base = ({ nodeId, serverId }: ServerRef) => `/nodes/${nodeId}/servers/${serverId}/plugins`

export const pluginsQuery = (ref: ServerRef) =>
  queryOptions({
    queryKey: ["plugins", ref.nodeId, ref.serverId],
    queryFn: () => api<PluginListing>(base(ref)),
    refetchInterval: 30_000, // e.g. other users or file sets change them
  })

/** The most servers the master installs on in one request. */
export const maxServers = 100

/**
 * Installs or updates projects, with the projects they require, on servers: the newest version of the channel chosen
 * for a project that suits each server, a release unless chosen, or the version chosen for it. The servers take the
 * projects in these channels from then on. On many, it takes a while, and more than maxServers are installed on in
 * batches, one after the other, each an operation of its own.
 */
export function useInstallPlugins() {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: async ({
      projects,
      servers,
      versions,
      channels,
      onStart,
      ...restart
    }: Restart & {
      projects: string[]
      servers: ServerRef[]
      versions?: Record<string, string>
      channels?: Record<string, Channel>
      /** Learns of the operation of each batch, counted from 0, that the master runs as one. */
      onStart?: (op: Operation, batch: number) => void
    }) => {
      const results: InstallResult[] = []
      for (let i = 0; i < servers.length; i += maxServers) {
        const batch = servers.slice(i, i + maxServers)
        try {
          const res = await operate<{ results: InstallResult[] }>(
            "/plugins/install",
            { body: { projects, servers: batch, versions, channels, ...restart } },
            (op) => onStart?.(op, i / maxServers),
          )
          results.push(...res.results)
        } catch (e) {
          // Once a batch went through, a failed one is told with the results, as an error on each of its servers;
          // a cancelled one also ends the batches after it.
          if (i === 0) throw e
          const failed = isCancelled(e) ? servers.slice(i) : batch
          results.push(...failed.map((s) => ({ ...s, installed: [], error: (e as Error).message })))
          if (isCancelled(e)) break
        }
      }
      return results
    },
    onSettled: () => queryClient.invalidateQueries({ queryKey: ["plugins"] }),
  })
}

/**
 * Updates all plugins or mods of a server to the newest version that suits it in the channel it takes each in, except
 * those it keeps at their version and turned-off ones.
 */
export function useUpdatePlugins(ref: ServerRef) {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: async () => (await operate<{ results: InstallResult[] }>("/plugins/update", { body: { servers: [ref] } })).results[0],
    onSettled: () => queryClient.invalidateQueries({ queryKey: ["plugins"] }),
  })
}

/**
 * Updates a project to the newest version that suits each server in the channel it takes the project in, except where
 * it is kept at its version or turned off, or removes it, also where it is turned off, on many servers at once.
 */
export function usePluginsEverywhere(action: "update" | "remove") {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: async ({ onStart, ...body }: Restart & { project: string; servers: ServerRef[]; onStart?: (op: Operation) => void }) =>
      (await operate<{ results: InstallResult[] }>(`/plugins/${action}`, { body }, onStart)).results,
    onSettled: () => queryClient.invalidateQueries({ queryKey: ["plugins"] }),
  })
}

export type PluginChange =
  | { action: "remove"; fileName: string; disabled?: boolean }
  | { action: "upload"; file: File }
  | { action: "enable" | "disable"; fileName: string }
  /** Keeps a project at its version, or lets updates of all plugins update it again. */
  | { action: "pin" | "unpin"; project: string }
  /** Sets the channel the server takes a project in, which its updates follow. */
  | { action: "track"; project: string; channel: Channel }

export function useChangePlugins(ref: ServerRef) {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: async (change: PluginChange) => {
      switch (change.action) {
        case "remove":
          return api(`${base(ref)}/${encodeURIComponent(change.fileName)}${change.disabled ? "?disabled=true" : ""}`, { method: "DELETE" })
        case "enable":
        case "disable":
          return api(`${base(ref)}/${encodeURIComponent(change.fileName)}/${change.action}`, { method: "POST" })
        case "pin":
        case "unpin":
          return api(`${base(ref)}/pins/${change.project}`, { method: change.action === "pin" ? "PUT" : "DELETE" })
        case "track":
          return api(`${base(ref)}/channels/${change.project}`, { method: "PUT", body: { channel: change.channel } })
      }
      const res = await fetch(`/api${base(ref)}/${encodeURIComponent(change.file.name)}`, { method: "PUT", body: change.file })
      if (!res.ok) throw await responseError(res, t("The upload failed with status {{status}}.", { status: res.status }))
    },
    onSettled: () => queryClient.invalidateQueries({ queryKey: ["plugins"] }),
  })
}
