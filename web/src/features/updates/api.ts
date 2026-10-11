import { queryOptions, useMutation, useQueryClient } from "@tanstack/react-query"
import { api } from "@/lib/api"

/** A published release of Noryx. */
export interface Release {
  version: string
  /** Release notes, as written on GitHub; empty when GitHub's API limited the master's requests. */
  notes: string
  url: string
  publishedAt?: string
}

/** An update that was asked for and hasn't finished yet, or failed. */
export interface Progress {
  since: string
  error?: string
}

/** An online agent that is older than the master. */
export interface OutdatedAgent {
  nodeId: string
  name: string
  version: string
  update?: Progress
}

/** What administrators see about updates. */
export interface UpdateStatus {
  /** The master's version. */
  version: string
  /** The latest release, if it is newer than the master. */
  latest?: Release
  checkedAt?: string
  checkError?: string
  /** Whether the master can update itself; otherwise it is updated on its host. */
  updatable: boolean
  /** Whether the master offers pre-releases too, as --prereleases on its host says. */
  prereleases: boolean
  /** Whether the master updates the agents once it runs the new release. */
  agentsFollow: boolean
  master?: Progress
  agents: OutdatedAgent[]
}

/** The agents the master updates once it runs a release installed from the panel: all that are older, or none. */
export type UpdateAgents = "all" | "none"

/** Updates the master on its host, for masters that weren't installed from a package. */
export const updateCommand = "curl -fsSL https://github.com/QwikByte/noryx/releases/latest/download/install.sh | sudo bash -s -- update"

export const updateQuery = queryOptions({
  queryKey: ["update"],
  queryFn: () => api<UpdateStatus>("/update"),
})

/** Whether an update of the master or of an agent is running. */
export const updating = (s?: UpdateStatus) =>
  !!s && ((!!s.master && !s.master.error) || s.agents.some((a) => a.update && !a.update.error))

/** Whether an agent waits for an update: it never started, or it failed. */
export const waitsForUpdate = (a: OutdatedAgent) => !a.update || !!a.update.error

/** Checks for a new release, or updates the master or the agents. */
export function useUpdateAction(action: "check" | "master" | "agents") {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: () => api<UpdateStatus>(`/update/${action}`, { method: "POST" }),
    onSuccess: (status) => queryClient.setQueryData(updateQuery.queryKey, status),
  })
}

/** Updates the agent of one node; a failure also shows in the status. */
export function useUpdateAgent() {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: (nodeId: string) => api<UpdateStatus>(`/update/agents/${nodeId}`, { method: "POST" }),
    onSuccess: (status) => queryClient.setQueryData(updateQuery.queryKey, status),
    onError: () => queryClient.invalidateQueries({ queryKey: updateQuery.queryKey }),
  })
}
