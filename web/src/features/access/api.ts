import { queryOptions, useMutation, useQueryClient } from "@tanstack/react-query"
import { t } from "i18next"
import type { Target } from "@/features/servers/api"
import { api } from "@/lib/api"
import type { Permission } from "./permissions"

/** Where a permission applies: everywhere, or on whole nodes and single servers. */
export interface Scope {
  all: boolean
  nodes: string[]
  servers: Target[]
}

/** The permissions of the signed-in user. */
export interface Grants {
  admin: boolean
  permissions: Partial<Record<Permission, Scope>>
}

export const accessQuery = queryOptions({
  queryKey: ["access", "me"],
  queryFn: () => api<Grants>("/access/me"),
  staleTime: 30_000,
})

export interface PermissionInfo {
  id: Permission
  label: string
  description: string
  /** Scoped permissions only apply to the nodes and servers of a group's scope. */
  scoped: boolean
  /** Granted along with the permission. */
  requires?: Permission[]
}

export interface Area {
  name: string
  permissions: PermissionInfo[]
}

export const catalogQuery = queryOptions({
  queryKey: ["access", "catalog"],
  queryFn: () => api<Area[]>("/access/permissions"),
  staleTime: Infinity,
})

export interface Group {
  id: string
  name: string
  description: string
  /** The Administrators group, which has every permission. */
  builtin: boolean
  permissions: Permission[]
  /** Whether the scoped permissions apply to all servers instead of only to the targets. */
  allServers: boolean
  targets: Target[]
  members: number[]
  createdAt: string
}

export type GroupInput = Pick<Group, "name" | "description" | "permissions" | "allServers" | "targets">

/** Where the node and server permissions of a group apply, e.g. "2 nodes · 1 server". */
export function describeScope(group: Pick<Group, "allServers" | "targets">) {
  if (group.allServers) return t("All servers")
  const nodes = group.targets.filter((target) => !target.serverId).length
  const servers = group.targets.length - nodes
  const parts = [
    nodes && t("{{count}} nodes", { count: nodes, defaultValue_one: "{{count}} node" }),
    servers && t("{{count}} servers", { count: servers, defaultValue_one: "{{count}} server" }),
  ]
  return parts.filter(Boolean).join(" · ") || t("No servers")
}

export const groupsQuery = queryOptions({ queryKey: ["groups"], queryFn: () => api<Group[]>("/groups") })

export const groupQuery = (id: string) => queryOptions({ queryKey: ["groups", id], queryFn: () => api<Group>(`/groups/${id}`) })

export interface User {
  id: number
  username: string
  disabled: boolean
  /** False until an invited user sets a password with the setup link. */
  passwordSet: boolean
  /** Whether signing in needs a code of an authenticator app too. */
  mfa: boolean
  createdAt: string
  /** IDs of the user's groups. */
  groups: string[]
}

export interface SetupLink {
  token: string
  expiresAt: string
}

export const usersQuery = queryOptions({ queryKey: ["users"], queryFn: () => api<User[]>("/users") })

/** The groups of the settings that inviting a user preselects, of those the signed-in user may give. */
export const inviteGroupsQuery = queryOptions({ queryKey: ["users", "invite"], queryFn: () => api<{ groups: string[] }>("/users/invite") })

/** The address of a setup link. The token is in the fragment, which browsers don't send to servers. */
export const setupUrl = (link: SetupLink) => `${location.origin}/setup#${link.token}`

/** Groups and users change each other's lists and may change the signed-in user's permissions. */
function useInvalidate() {
  const queryClient = useQueryClient()
  return () => Promise.all(["groups", "users", "access"].map((key) => queryClient.invalidateQueries({ queryKey: [key] })))
}

/** Creates a group, or changes it if an ID is given. */
export function useSaveGroup(id?: string) {
  const invalidate = useInvalidate()
  return useMutation({
    mutationFn: (input: GroupInput) =>
      id ? api<Group>(`/groups/${id}`, { method: "PUT", body: input }) : api<Group>("/groups", { body: input }),
    onSuccess: invalidate,
  })
}

export function useDeleteGroup() {
  const invalidate = useInvalidate()
  return useMutation({ mutationFn: (id: string) => api(`/groups/${id}`, { method: "DELETE" }), onSuccess: invalidate })
}

export function useInviteUser() {
  const invalidate = useInvalidate()
  return useMutation({
    mutationFn: (input: { username: string; groups: string[] }) =>
      api<{ user: { id: number; username: string }; setupLink: SetupLink }>("/users", { body: input }),
    onSuccess: invalidate,
  })
}

export function useUpdateUser(id: number) {
  const invalidate = useInvalidate()
  return useMutation({
    mutationFn: (input: { disabled: boolean; groups: string[] }) => api(`/users/${id}`, { method: "PUT", body: input }),
    onSuccess: invalidate,
  })
}

export function useDeleteUser() {
  const invalidate = useInvalidate()
  return useMutation({ mutationFn: (id: number) => api(`/users/${id}`, { method: "DELETE" }), onSuccess: invalidate })
}

export function useNewSetupLink(id: number) {
  const invalidate = useInvalidate()
  return useMutation({ mutationFn: () => api<SetupLink>(`/users/${id}/setup-link`, { method: "POST" }), onSuccess: invalidate })
}

/** Turns off two-factor authentication for a user who lost the app and the recovery codes. */
export function useResetMfa(id: number) {
  const invalidate = useInvalidate()
  return useMutation({ mutationFn: () => api(`/users/${id}/mfa`, { method: "DELETE" }), onSuccess: invalidate })
}
