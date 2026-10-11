import type { Access } from "@/features/access/use-access"
import { msg } from "@/lib/i18n"

/** The tabs a server can open on, in their order on its page, with the permission each needs. */
export const firstTabs = [
  { value: "console", label: msg("Console"), permission: "console.view" },
  { value: "usage", label: msg("Usage"), permission: "servers.view" },
  { value: "files", label: msg("Files"), permission: "files.read" },
  { value: "settings", label: msg("Settings"), permission: "servers.settings" },
] as const

export type ServerTab = (typeof firstTabs)[number]["value"]

/** The tab a server opens on: the one the user chose if they may see it, otherwise the first one they may. */
export function firstTab(access: Access, { nodeId, serverId }: { nodeId: string; serverId: string }, chosen: ServerTab = "console") {
  const visible = firstTabs.filter((tab) => access.can(tab.permission, nodeId, serverId))
  return (visible.find((tab) => tab.value === chosen) ?? visible[0])?.value ?? "console"
}
