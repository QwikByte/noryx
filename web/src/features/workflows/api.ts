import { queryOptions, useMutation, useQueryClient } from "@tanstack/react-query"
import type { Schedule, TaskTarget } from "@/features/schedules/api"
import { api } from "@/lib/api"
import { timeZone } from "@/lib/i18n"

export type TriggerKind = "schedule" | "interval" | "event" | "server" | "metric" | "webhook"

/** Starts a workflow; its data are {{trigger.…}} in templates. */
export interface Trigger {
  kind: TriggerKind
  schedule?: Schedule
  /** Minutes between the runs of an interval. */
  every?: number
  /** Servers whose entries, players or measures it watches; none for all. */
  targets?: TaskTarget[]
  /** Entries of the log of at least the level, of the categories (all if none), with the text in their message. */
  level?: "info" | "warn" | "error"
  categories?: string[]
  contains?: string
  on?: "started" | "stopped" | "joined" | "left"
  /** Names of the players whose joining or leaving fires it; none for all. */
  players?: string[]
  measure?: Measure
  below?: boolean
  value?: number
  minutes?: number
}

export type Measure = "players" | "cpu" | "memory" | "tps" | "disk"

/** A value a run gets, {{inputs.name}}. */
export interface Param {
  name: string
  type: "text" | "number" | "boolean" | "data"
  default: string
  required: boolean
  description: string
}

/** Compares two templates, or groups rules. */
export interface Rule {
  left?: string
  op?: Op
  right?: string
  group?: Condition
}

export type Op = "eq" | "ne" | "gt" | "ge" | "lt" | "le" | "contains" | "notContains" | "startsWith" | "endsWith" | "matches" | "empty" | "notEmpty"

export interface Condition {
  match: "all" | "any"
  rules: Rule[]
}

/** An action or a step that controls the flow; its ID names its output, {{steps.id.…}}. */
export interface Step {
  id: string
  kind: string
  name?: string
  disabled?: boolean
  /** Goes on with the next step if it fails. */
  continue?: boolean
  /** The settings of its kind. */
  with?: Settings
  steps?: Step[]
  else?: Step[]
  cases?: { value: string; steps: Step[] }[]
  branches?: Step[][]
}

/** Settings of steps, which differ by kind; the forms of the kinds know them. */
// oxlint-disable-next-line typescript/no-explicit-any
export type Settings = Record<string, any>

/** Servers of a step: targets, and a template whose value names servers, e.g. {{trigger.server}}. */
export interface Servers {
  targets?: TaskTarget[]
  from?: string
}

export interface Header {
  name: string
  value: string
  /** Its value is never shown again, and is used as it is. */
  secret: boolean
}

export interface Definition {
  triggers: Trigger[]
  params: Param[]
  steps: Step[]
  /** What a trigger does while the workflow runs. */
  overlap: "skip" | "queue" | "parallel"
  timeZone: string
}

export type Outcome = "running" | "succeeded" | "failed" | "cancelled" | "skipped"

/** What a step did in a run. */
export interface StepRun {
  id: string
  kind: string
  name?: string
  /** That of the item of the innermost loop. */
  index?: number
  outcome: Outcome
  startedAt: string
  endedAt?: string
  detail?: string
  output?: unknown
}

export interface Run {
  id: number
  /** What started it: a kind of trigger, manual or workflow. */
  trigger: TriggerKind | "manual" | "workflow"
  startedBy?: string
  startedAt: string
  endedAt?: string
  outcome: Outcome
  error?: string
  steps?: StepRun[]
  /** The data of its trigger and its inputs. */
  data?: { trigger?: unknown; inputs?: Record<string, unknown> }
}

export interface Workflow extends Definition {
  id: string
  name: string
  description: string
  enabled: boolean
  /** Whether a URL starts it. */
  hook: boolean
  createdAt: string
  updatedAt: string
  savedBy?: string
  lastRun?: Run
  nextRun?: string
  running: number
}

export type Draft = Pick<Workflow, "name" | "description" | "enabled"> & Definition

/** The time zone of new workflows: the user's, else the browser's. */
const localTimeZone = timeZone ?? Intl.DateTimeFormat().resolvedOptions().timeZone

export const emptyDraft: Draft = {
  name: "",
  description: "",
  enabled: true,
  triggers: [],
  params: [],
  steps: [],
  overlap: "skip",
  timeZone: localTimeZone,
}

export const draftOf = (w: Workflow): Draft => ({
  name: w.name,
  description: w.description,
  enabled: w.enabled,
  triggers: w.triggers,
  params: w.params,
  steps: w.steps,
  overlap: w.overlap,
  timeZone: w.timeZone,
})

const path = "/workflows"

export const workflowsQuery = queryOptions({
  queryKey: [path],
  queryFn: () => api<Workflow[]>(path),
  refetchInterval: (query) => (query.state.data?.some((w) => w.running > 0) ? 2_000 : 30_000),
})

export const workflowQuery = (id: string) =>
  queryOptions({
    queryKey: [path, id],
    queryFn: () => api<Workflow>(`${path}/${id}`),
  })

/** The kept runs of a workflow, newest first, without their steps. */
export const runsQuery = (id: string) =>
  queryOptions({
    queryKey: [path, id, "runs"],
    queryFn: () => api<Run[]>(`${path}/${id}/runs`),
    refetchInterval: (query) => (query.state.data?.some((r) => r.outcome === "running") ? 2_000 : 30_000),
  })

/** A run with its steps; a run in progress is followed closely. */
export const runQuery = (id: string, run: number) =>
  queryOptions({
    queryKey: [path, id, "runs", run],
    queryFn: () => api<Run>(`${path}/${id}/runs/${run}`),
    refetchInterval: (query) => (query.state.data?.outcome === "running" ? 1_000 : false),
  })

/** The runs that schedules and intervals start in the next 7 days. */
export const upcomingQuery = queryOptions({
  queryKey: [path, "upcoming"],
  queryFn: () => api<{ id: string; at: string }[]>(`${path}/upcoming`),
  refetchInterval: 60_000,
})

/** Creates a workflow, or changes it if an ID is given. */
export function useSaveWorkflow(id?: string) {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: (draft: Draft) => (id ? api<Workflow>(`${path}/${id}`, { method: "PUT", body: draft }) : api<Workflow>(path, { body: draft })),
    onSuccess: (w) => {
      queryClient.setQueryData(workflowQuery(w.id).queryKey, w)
      return queryClient.invalidateQueries({ queryKey: [path], predicate: ({ queryKey }) => queryKey[1] !== w.id || queryKey.length > 2 })
    },
  })
}

export function useDeleteWorkflow() {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: (id: string) => api(`${path}/${id}`, { method: "DELETE" }),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: [path] }),
  })
}

/** Starts a run with inputs; it goes on in the background. */
export function useRunWorkflow(id: string) {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: (inputs: Record<string, unknown>) => api<Run>(`${path}/${id}/run`, { body: { inputs } }),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: [path] }),
  })
}

export function useCancelRun(id: string) {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: (run: number) => api(`${path}/${id}/runs/${run}/cancel`, { method: "POST" }),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: [path, id] }),
  })
}

/** Gives a workflow a new URL, which replaces the one before; only this answer shows it. */
export function useNewHook(id: string) {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: () => api<{ path: string }>(`${path}/${id}/hook`, { method: "POST" }),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: [path] }),
  })
}

export function useDeleteHook(id: string) {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: () => api(`${path}/${id}/hook`, { method: "DELETE" }),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: [path] }),
  })
}
