import { queryOptions, useMutation, useQueryClient } from "@tanstack/react-query"
import { useSyncExternalStore } from "react"
import { api, ApiError, read, type RequestInit, request } from "@/lib/api"

/** A long action of the master, in progress or ended recently, as long as the settings say. */
export interface Operation {
  id: string
  /** What it does, e.g. server.create. */
  kind: string
  /** What it is about, e.g. the name of the server it creates, or a number of servers. */
  subject: string
  nodeId?: string
  serverId?: string
  networkId?: string
  /** Who started it. */
  user: string
  steps: string[]
  /** The step it is at, or failed at. */
  step: number
  /** How much of the current step is done, and of how much; total is 0 if unknown. */
  done: number
  total: number
  unit?: "bytes" | "servers" | "backups" | "files" | "minutes"
  error?: string
  result?: unknown
  startedAt: string
  finishedAt?: string
  /** The user may cancel it now. */
  cancellable?: boolean
  /** It was cancelled: it stops, or it stopped before it was done. */
  cancelled?: boolean
}

/** The operations the user may see, checked often while one is in progress. */
export const operationsQuery = queryOptions({
  queryKey: ["operations"],
  queryFn: () => api<Operation[]>("/operations"),
  refetchInterval: (query) => (query.state.data?.some((op) => !op.finishedAt) ? 2_000 : 15_000),
})

// The operations this browser follows, while they go on.
const live = new Map<string, Operation>()
const listeners = new Set<() => void>()
const subscribe = (listener: () => void) => {
  listeners.add(listener)
  return () => listeners.delete(listener)
}

function publish(op: Operation) {
  live.set(op.id, op)
  for (const listener of listeners) listener()
}

/** Tells how an operation goes on, while this browser still follows it. */
function update(op: Operation) {
  if (live.has(op.id)) publish(op)
}

/** Forgets the operations this browser follows, as the user signs out: whoever signs in next may not see them. */
export function forgetOperations() {
  live.clear()
  for (const listener of listeners) listener()
}

/** An operation this browser follows, as it goes on. */
export function useLiveOperation(id?: string) {
  return useSyncExternalStore(subscribe, () => (id ? live.get(id) : undefined))
}

let snapshot: Operation[] = []
/** All operations this browser follows. */
export function useLiveOperations() {
  return useSyncExternalStore(subscribe, () => {
    if (snapshot.length !== live.size || snapshot.some((op) => live.get(op.id) !== op)) snapshot = [...live.values()]
    return snapshot
  })
}

const sleep = (ms: number) => new Promise((resolve) => setTimeout(resolve, ms))

/**
 * Calls the API for an action that may take long. If the master runs it as an operation, it
 * answers 202 Accepted: onStart learns of the operation, which is followed until it ends, and
 * its result or error comes then. Otherwise the answer comes right away, as from api.
 */
export async function operate<T>(path: string, init: RequestInit, onStart?: (op: Operation) => void): Promise<T> {
  const res = await request(path, init)
  if (res.status !== 202) return read<T>(res)
  let op = (await res.json()) as Operation
  publish(op)
  onStart?.(op)
  while (!op.finishedAt) {
    await sleep(1_000)
    try {
      op = await api<Operation>(`/operations/${op.id}`)
    } catch (e) {
      if (!(e instanceof ApiError)) continue // the network failed for a moment; the operation goes on
      // It can't be followed any more, e.g. as the master restarted or the session ended.
      update({ ...op, finishedAt: new Date().toISOString(), error: e.message })
      throw e
    }
    update(op)
  }
  if (op.error) throw new ApiError(0, op.error, op.cancelled ? cancelledCode : undefined)
  return op.result as T
}

/** The code of the error of an operation that was cancelled. */
const cancelledCode = "cancelled"

/** Whether an error tells that an operation was cancelled, which needs no warning. */
export const isCancelled = (e: unknown) => e instanceof ApiError && e.code === cancelledCode

/** Cancels an operation; it stops at its next step that can stop. */
export function useCancelOperation() {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: (id: string) => api<Operation>(`/operations/${id}/cancel`, { method: "POST" }),
    onSuccess: (op) => {
      update(op)
      return queryClient.invalidateQueries({ queryKey: operationsQuery.queryKey })
    },
  })
}
