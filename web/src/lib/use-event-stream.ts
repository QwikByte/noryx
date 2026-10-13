import { useEffect, useRef, useState } from "react"
import { checkSignedIn, masterReachable } from "./api"

/** How a stream is doing: connecting, live, broken off and connecting again, ended by the master, or given up. */
export type StreamState = "connecting" | "live" | "lost" | "ended" | "failed"

export interface StreamHandlers {
  /** Gets each message with its ID, which may be empty. */
  onMessage: (data: string, id: string) => void
  /** Called as the stream opens, with whether it continues after the last message it got. */
  onOpen?: (resumed: boolean) => void
}

// The browser connects again by itself after about 3 seconds, which isn't worth telling.
const graceMs = 4_000
const maxDelayMs = 30_000
const maxFailures = 8

/**
 * Follows a stream of Server-Sent Events of the master at path, if any, continuing after the last message it got
 * whenever it connects again: when reconnectOn changes, after a "renew" event, with which the master checks the access
 * again, and when the connection breaks, e.g. as the master restarts behind a reverse proxy. Then it tries again at
 * once, after longer pauses up to 30 seconds and as soon as the master answers requests again, and gives up after 8
 * failures in a row, or once the session ended, which leads to the sign-in page like a request. An "end" event ends
 * it. Another path starts from its beginning.
 */
export function useEventStream(path: string | null, handlers: StreamHandlers, reconnectOn?: string): StreamState {
  const latest = useRef(handlers)
  useEffect(() => {
    latest.current = handlers
  })
  const last = useRef({ path, id: "" })
  // The state of the stream it belongs to; a new one is connecting.
  const stream = `${path}\n${reconnectOn}`
  const [state, setState] = useState<{ stream: string; state: StreamState }>()

  useEffect(() => {
    if (!path) return
    if (last.current.path !== path) last.current = { path, id: "" }
    let source: EventSource | undefined
    let failures = 0
    let retry: ReturnType<typeof setTimeout> | undefined
    let lostSoon: ReturnType<typeof setTimeout> | undefined
    let done = false
    const report = (next: StreamState) => setState({ stream, state: next })
    const settle = (next: StreamState) => {
      clearTimeout(lostSoon)
      lostSoon = undefined
      report(next)
    }
    const connect = () => {
      clearTimeout(retry)
      source?.close()
      const id = last.current.id
      const s = new EventSource(id ? `${path}${path.includes("?") ? "&" : "?"}after=${encodeURIComponent(id)}` : path)
      source = s
      s.onopen = () => {
        failures = 0
        settle("live")
        latest.current.onOpen?.(id !== "")
      }
      s.onmessage = (event: MessageEvent<string>) => {
        if (event.lastEventId) last.current.id = event.lastEventId
        latest.current.onMessage(event.data, event.lastEventId)
      }
      s.addEventListener("end", () => {
        s.close()
        settle("ended")
      })
      s.addEventListener("renew", connect)
      s.onerror = () => {
        lostSoon ??= setTimeout(() => report("lost"), graceMs)
        if (s.readyState !== EventSource.CLOSED) return // the browser connects again by itself
        void checkSignedIn().then((signedIn) => {
          if (done || source !== s) return
          if (!signedIn || ++failures >= maxFailures) settle("failed")
          else retry = setTimeout(connect, Math.min(1_000 * 2 ** (failures - 1), maxDelayMs))
        })
      }
    }
    // A master that answers again, e.g. after its update, gets the next try at once.
    const unsubscribe = masterReachable.subscribe(() => {
      if (masterReachable.get() && failures > 0) connect()
    })
    connect()
    return () => {
      done = true
      unsubscribe()
      clearTimeout(retry)
      clearTimeout(lostSoon)
      source?.close()
    }
  }, [path, stream])

  return state?.stream === stream ? state.state : "connecting"
}
