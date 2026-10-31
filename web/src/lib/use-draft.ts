import { useCallback, useLayoutEffect, useRef, useState } from "react"
import { ApiError } from "./api"

/**
 * The draft of a form. It starts as initial and follows it, e.g. as the settings it starts from load, until it is changed;
 * changed back, it follows initial again. It is dirty while it differs from initial.
 */
export function useDraft<T extends object>(initial: T) {
  const [changed, setChanged] = useState<T>()
  const latest = useRef(initial)
  useLayoutEffect(() => {
    latest.current = initial
  })
  const value = changed !== undefined && !same(changed, initial) ? changed : initial
  const dirty = value !== initial
  if (changed !== undefined && !dirty) setChanged(undefined)
  /** Changes fields of the draft, given as such or by a function of the draft. */
  const set = useCallback(
    (change: Partial<T> | ((draft: T) => Partial<T>)) =>
      setChanged((draft = latest.current) => ({ ...draft, ...(typeof change === "function" ? change(draft) : change) })),
    [],
  )
  /** Starts the draft again from initial, or from next. */
  const reset = useCallback((next?: T) => setChanged(next), [])
  return { value, dirty, set, reset }
}

/** Whether a and b are equal as data: plain objects without their undefined fields and arrays by their contents, anything else by identity. */
export function same(a: unknown, b: unknown): boolean {
  if (Object.is(a, b)) return true
  if (Array.isArray(a) || Array.isArray(b)) return Array.isArray(a) && Array.isArray(b) && a.length === b.length && a.every((v, i) => same(v, b[i]))
  if (!plain(a) || !plain(b)) return false
  const keys = Object.keys(a).filter((key) => a[key] !== undefined)
  return keys.length === Object.keys(b).filter((key) => b[key] !== undefined).length && keys.every((key) => same(a[key], b[key]))
}

const plain = (v: unknown): v is Record<string, unknown> => typeof v === "object" && v !== null && Object.getPrototypeOf(v) === Object.prototype

/** Errors at a field, as FieldError shows them. */
export type FieldErrors = { message: string }[]

/** The error of a failed request at the field it names by its JSON key, e.g. port or newServers.memoryMb. */
export function fieldError(error: unknown, field: string): FieldErrors | undefined {
  return error instanceof ApiError && error.field === field ? [{ message: error.message }] : undefined
}

/** The errors at the fields of a form, by their JSON keys. */
export type ErrorsAt = (field: string) => FieldErrors | undefined

/** The errors of a failed request at the fields of a form, or of the object at the key within, e.g. newServers. */
export const errorsAt =
  (error: unknown, within?: string): ErrorsAt =>
  (field) =>
    fieldError(error, within ? `${within}.${field}` : field)

/** The message of a failed request, unless it names one of the fields that a form shows it at. */
export function formError(error: unknown, fields: readonly string[]): string | undefined {
  if (error instanceof ApiError && error.field && fields.includes(error.field)) return undefined
  return error instanceof Error ? error.message : undefined
}
