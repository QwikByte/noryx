import { t } from "i18next"

export class ApiError extends Error {
  readonly status: number
  /** Tells what the panel may offer, e.g. to confirm the request. */
  readonly code?: string

  constructor(status: number, message: string, code?: string) {
    super(message)
    this.status = status
    this.code = code
  }
}

export interface RequestInit {
  method?: string
  body?: unknown
}

// The master's version, which its answers to signed-in users tell: first the one the panel came
// with, then another one, e.g. after an update.
let version: string | undefined
let outdated = () => {}

/** Calls back on each answer once the master runs another version than the panel came with. */
export function onOutdated(callback: () => void) {
  outdated = callback
}

// Whether the master answered the last request. While it restarts, there is no answer, or one
// from a proxy in front of it: 502 to 504, without the version that the master's answers to
// signed-in users tell.
let reachable = true
const reachableListeners = new Set<() => void>()

/** Whether the master answers, for useSyncExternalStore. */
export const masterReachable = {
  subscribe(listener: () => void) {
    reachableListeners.add(listener)
    return () => void reachableListeners.delete(listener)
  },
  get: () => reachable,
}

function setReachable(value: boolean) {
  if (value === reachable) return
  reachable = value
  reachableListeners.forEach((listener) => listener())
}

let signedOut: (error: ApiError) => void = () => {}

/** Calls back when checkSignedIn finds that the session ended, which then is handled like a request that finds it. */
export function onSignedOut(callback: (error: ApiError) => void) {
  signedOut = callback
}

/**
 * Whether the user is still signed in, e.g. after the master refused a stream of events, which EventSource doesn't
 * tell why. While the master doesn't answer, e.g. as it restarts, the user counts as signed in.
 */
export async function checkSignedIn() {
  try {
    const res = await request("/auth/me")
    if (res.status !== 401) return true
    signedOut(await responseError(res))
    return false
  } catch {
    return true
  }
}

/** Sends a request to the master's REST API, with JSON as body. The session cookie is sent automatically. */
export async function request(path: string, init: RequestInit = {}): Promise<Response> {
  const hasBody = init.body !== undefined
  let res: Response
  try {
    res = await fetch(`/api${path}`, {
      method: init.method ?? (hasBody ? "POST" : "GET"),
      headers: hasBody ? { "Content-Type": "application/json" } : undefined,
      body: hasBody ? JSON.stringify(init.body) : undefined,
    })
  } catch (e) {
    setReachable(false)
    throw e
  }
  const answered = res.headers.get("Noryx-Version")
  setReachable(!!answered || res.status < 502 || res.status > 504)
  if (answered && answered !== (version ??= answered)) outdated()
  return res
}

/** Reads the JSON of a response, or throws the error the master sent. */
export async function read<T>(res: Response): Promise<T> {
  if (res.status === 204) return undefined as T
  if (!res.ok) throw await responseError(res)
  // A body that broke off, e.g. as the page reloads, fails the request instead of reading as {}, which lists and
  // other answers aren't.
  const text = await res.text()
  return (text ? JSON.parse(text) : undefined) as T
}

/** Calls the master's REST API. */
export async function api<T = void>(path: string, init: RequestInit = {}): Promise<T> {
  return read<T>(await request(path, init))
}

/**
 * Sends a file or form to the master's REST API with progress reports, which fetch can't give for uploads, and
 * returns the JSON of the answer, or throws the error the master sent.
 */
export function send<T>(
  method: string,
  path: string,
  body: Blob | FormData | string,
  { onProgress, signal }: { onProgress?: (fraction: number) => void; signal?: AbortSignal } = {},
): Promise<T> {
  return new Promise((resolve, reject) => {
    const xhr = new XMLHttpRequest()
    xhr.open(method, `/api${path}`)
    if (!(body instanceof FormData)) xhr.setRequestHeader("Content-Type", "application/octet-stream")
    xhr.responseType = "json"
    xhr.upload.onprogress = (e) => e.lengthComputable && onProgress?.(e.loaded / e.total)
    xhr.onload = () =>
      xhr.status >= 200 && xhr.status < 300
        ? resolve(xhr.response as T)
        : reject(
            new ApiError(xhr.status, xhr.response?.error ?? t("The upload failed with status {{status}}.", { status: xhr.status }), xhr.response?.code),
          )
    xhr.onerror = () => reject(new Error(t("The connection to the panel was lost.")))
    xhr.onabort = () => reject(new DOMException(t("The upload was cancelled."), "AbortError"))
    signal?.addEventListener("abort", () => xhr.abort())
    xhr.send(body)
  })
}

/** Reads the message the master sent with a failed response. */
export async function responseError(
  res: Response,
  fallback = t("The request failed with status {{status}}.", { status: res.status }),
): Promise<ApiError> {
  const data = await res.json().catch(() => ({}))
  return new ApiError(res.status, data.error ?? fallback, data.code)
}
