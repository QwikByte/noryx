import { queryOptions, useMutation, useQueryClient } from "@tanstack/react-query"
import { t } from "i18next"
import type { JobSettings } from "@/features/backups/api"
import { type Followed, serversQuery } from "@/features/servers/api"
import { operate } from "@/features/operations/api"
import { type Project, pluginsQuery } from "@/features/plugins/api"
import { api } from "@/lib/api"

/** A version of a Modrinth modpack, which decides the Minecraft version and the mod loader of its servers. */
export interface ModpackVersion {
  id: string
  number: string
  channel: "release" | "beta" | "alpha"
  published: string
  gameVersions: string[]
  loaders: string[]
}

/** The version of a modpack a new server gets. */
export interface ModpackChoice {
  project: string
  version: string
}

/** The versions of a modpack for the mod loaders of servers, the newest first. */
export const modpackVersionsQuery = (project: string) =>
  queryOptions({
    queryKey: ["modpacks", project, "versions"],
    queryFn: () => api<ModpackVersion[]>(`/modpacks/${project}/versions`),
    staleTime: 60_000,
  })

/** What a version of a modpack runs, e.g. "1.0.2 · Minecraft 1.21.1 · NeoForge". */
export function describeVersion(v: ModpackVersion) {
  const loaders = v.loaders.map((l) => l.charAt(0).toUpperCase() + l.slice(1)).join(", ")
  return [v.number, v.gameVersions.length > 0 && t("Minecraft {{version}}", { version: v.gameVersions[0] }), loaders, v.channel !== "release" && v.channel]
    .filter(Boolean)
    .join(" · ")
}

/** The modpack a server was created from and the version it has. */
export interface ServerModpack {
  /** Only has its ID while Modrinth can't be reached. */
  project: Project
  /** The ID of the version on Modrinth. */
  version: string
  number: string
}

/** The modpack of a server; null for servers that weren't created from one. */
export const serverModpackQuery = (nodeId: string, serverId: string) =>
  queryOptions({
    queryKey: [...serversQuery(nodeId).queryKey, serverId, "modpack"],
    queryFn: () => api<ServerModpack | null>(`/nodes/${nodeId}/servers/${serverId}/modpack`),
  })

/** What moving a server to another version of its modpack did. */
export interface ModpackChange {
  number: string
  /** The label of the backup made before; empty if the update made none. */
  backup: string
  written: string[]
  removed: string[]
  /** Files the new version changes or no longer has, which stay as they are, as they changed on the server. */
  kept: string[]
  /** Tells that the server didn't start again. */
  warning?: string
}

/**
 * What an update backs up first, and where, as Back up first of schedules; none backs up the worlds, mods and configuration
 * into the default storage location. With noBackup, it backs up nothing.
 */
export interface ModpackBackup {
  backup?: Pick<JobSettings, "selection" | "location">
  noBackup?: true
}

/** Moves a server to another version of its modpack, newer or older, after backing it up unless asked not to. */
export function useUpdateModpack(nodeId: string, serverId: string) {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: ({ version, backup, noBackup, onStart }: { version: string } & ModpackBackup & Followed) =>
      operate<ModpackChange>(`/nodes/${nodeId}/servers/${serverId}/modpack`, { body: { version, backup, noBackup } }, onStart),
    onSettled: () =>
      Promise.all([
        queryClient.invalidateQueries({ queryKey: serversQuery(nodeId).queryKey }),
        queryClient.invalidateQueries({ queryKey: pluginsQuery({ nodeId, serverId }).queryKey }),
      ]),
  })
}
