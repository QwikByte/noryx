import { useEffect, useRef, useState } from "react"
import { request, signedOut } from "./api"

/** How a stream is: connecting for the first time, live, lost and reconnecting, ended by the master, or given up. */
export type StreamState = "connecting" | "live" | "lost" | "ended" | "failed"

// Waits between attempts to connect again, the last one repeated until it gives up.
const backoff = [1_000, 2_000, 5_000, 10_000, 30_000]
const maxFailures = 10

interface Options {
  /** Called with each event; its ID is where the stream continues when it connects again. */
  onMessage: (event: MessageEvent<string>) => void
  /** Called when the stream opens, with whether it continues after an event it had. */
  onOpen?: (resumed: boolean) => void
  /** A value that makes the stream connect anew when it changes, e.g. the state of a server, continuing after the last event. */
  reconnectOn?: unknown
  /** How long the stream must be gone before it counts as lost, e.g. to let the browser reconnect first. */
  graceMs?: number
}

/**
 * Follows the Server-Sent Events of the master at path while it is set, and tells how the stream is. The stream
 * continues after the last event it got whenever it connects again: when the master renews it to check the access
 * again, when the browser reconnects after a lost connection, and when the browser gives up after an answer that
 * isn't a stream, e.g. from a proxy while the master restarts, or from the master while the node of a server is
 * offline. Then it asks again why and connects again with growing waits, signs the user in again if the session
 * expired, and gives up after repeated failures or a final answer, e.g. that the user may no longer see it. A
 * "renew" event connects again at once; an "end" event ends the stream, e.g. as the server stopped.
 */
export function useEventStream(path: string | undefined, { onMessage, onOpen, reconnectOn, graceMs = 0 }: Options): StreamState {
  const handlers = useRef({ onMessage, onOpen })
  useEffect(() => {
    handlers.current = { onMessage, onOpen }
  })
  // The ID of the last event of the stream at path, which a new path starts afresh.
  const last = useRef({ path, id: "" })
  const [state, setState] = useState<StreamState>("connecting")

  useEffect(() => {
    if (path === undefined) return
    if (last.current.path !== path) last.current = { path, id: "" }
    let source: EventSource | undefined
    let probe: AbortController | undefined
    let again: ReturnType<typeof setTimeout> | undefined
    let lostSoon: ReturnType<typeof setTimeout> | undefined
    let failures = 0
    let closed = false
    const url = () => (last.current.id ? `${path}${path.includes("?") ? "&" : "?"}after=${encodeURIComponent(last.current.id)}` : path)
    const settle = (next: StreamState) => {
      clearTimeout(lostSoon)
      lostSoon = undefined
      setState(next)
    }

    const connect = () => {
      const s = new EventSource(`/api${url()}`)
      source = s
      s.onopen = () => {
        failures = 0
        settle("live")
        handlers.current.onOpen?.(last.current.id !== "")
      }
      s.onmessage = (event: MessageEvent<string>) => {
        last.current.id = event.lastEventId
        handlers.current.onMessage(event)
      }
      s.addEventListener("end", () => {
        s.close()
        settle("ended")
      })
      s.addEventListener("renew", () => {
        s.close()
        connect()
      })
      s.onerror = () => {
        lostSoon ??= setTimeout(() => setState("lost"), graceMs)
        if (s.readyState === EventSource.CLOSED) void retry()
      }
    }

    // The browser gave up, and doesn't tell why: the same request does, and the stream it may open goes right away.
    const retry = async () => {
      probe = new AbortController()
      let status = 0 // the master can't be reached
      try {
        status = (await request(url(), { signal: probe.signal })).status
        probe.abort()
      } catch {
        // as status tells
      }
      if (closed) return
      if (status === 401) {
        settle("failed")
        signedOut()
        return
      }
      const passing = status === 0 || status === 200 || (status >= 502 && status <= 504)
      if (!passing || ++failures > maxFailures) {
        settle("failed")
        return
      }
      again = setTimeout(connect, status === 200 ? 0 : backoff[Math.min(failures, backoff.length) - 1])
    }

    connect()
    return () => {
      closed = true
      clearTimeout(again)
      clearTimeout(lostSoon)
      probe?.abort()
      source?.close()
    }
  }, [path, reconnectOn, graceMs])

  return state
}
