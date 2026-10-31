import { CaretDownIcon, ScrollIcon } from "@phosphor-icons/react"
import { t } from "i18next"
import { type Ref, useId, useLayoutEffect, useRef, useState } from "react"
import { Button } from "@/components/ui/button"
import { msg } from "@/lib/i18n"
import { type StreamState, useEventStream } from "@/lib/use-event-stream"
import { cn } from "@/lib/utils"
import type { Datastore } from "./api"

interface Line {
  id: number
  text: string
}

const maxLines = 1000

const connections: Record<StreamState, { text: string; dot: string }> = {
  connecting: { text: msg("Connecting…"), dot: "bg-console-warn animate-pulse" },
  live: { text: msg("Live"), dot: "bg-console-command shadow-[0_0_8px_var(--console-command)]" },
  ended: { text: msg("Showing the last output"), dot: "bg-console-muted" },
  lost: { text: msg("Connection lost, reconnecting…"), dot: "bg-console-warn animate-pulse" },
  failed: { text: msg("The log is not available right now."), dot: "bg-console-error" },
}

// PostgreSQL writes e.g. "ERROR:" and "WARNING:", MariaDB "[ERROR]" and "[Warning]".
function lineClass(text: string) {
  if (/\b(ERROR|FATAL|PANIC)\b/.test(text)) return "text-console-error"
  if (/\b(WARNING|Warning)\b/.test(text)) return "text-console-warn"
  return undefined
}

/** The Log section of a datastore: the log of its container, which its agent shows without passwords. */
export function DatastoreLog({
  datastore: ds,
  open,
  onOpenChange,
  ref,
}: {
  datastore: Datastore
  open: boolean
  onOpenChange: (open: boolean) => void
  ref?: Ref<HTMLElement>
}) {
  const id = useId()
  return (
    <section ref={ref} tabIndex={-1} aria-labelledby={`${id}-heading`} className="scroll-mt-24 space-y-2 outline-none">
      <div className="flex items-center justify-between gap-3">
        <h3 id={`${id}-heading`} className="text-sm font-semibold">
          {t("Log")}
        </h3>
        <Button size="sm" variant="ghost" aria-expanded={open} aria-controls={`${id}-log`} onClick={() => onOpenChange(!open)}>
          <CaretDownIcon className={cn("transition-transform", open && "rotate-180")} />
          {open ? t("Hide log") : t("Show log")}
        </Button>
      </div>
      <div id={`${id}-log`}>
        {open ? (
          <LogView datastore={ds} />
        ) : (
          <p className="text-sm text-muted-foreground">
            {t("What the datastore writes, e.g. why it doesn't start or which statements failed, with passwords hidden.")}
          </p>
        )}
      </div>
    </section>
  )
}

/** Follows the log. It connects again when the state of the datastore changes, e.g. when it starts again, and continues after the latest line. */
function LogView({ datastore: ds }: { datastore: Datastore }) {
  const [lines, setLines] = useState<Line[]>([])
  const viewport = useRef<HTMLDivElement>(null)
  const stickToBottom = useRef(true)
  const nextId = useRef(0)
  const pending = useRef<Line[]>([])
  const frame = useRef(0)

  const connection = useEventStream(`/datastores/${ds.id}/logs`, {
    reconnectOn: ds.state,
    // Lines arrive in bursts (the first ones are hundreds), so they are rendered once per frame.
    onMessage: (event) => {
      pending.current.push({ id: nextId.current++, text: event.data })
      frame.current ||= requestAnimationFrame(() => {
        frame.current = 0
        const batch = pending.current
        pending.current = []
        setLines((current) => [...current, ...batch].slice(-maxLines))
      })
    },
  })

  useLayoutEffect(() => {
    const el = viewport.current
    if (el && stickToBottom.current) el.scrollTop = el.scrollHeight
  }, [lines])

  return (
    <div
      data-code
      className="overflow-hidden rounded-lg bg-console text-console-foreground ring-1 ring-black/5 [font-variant-ligatures:none] dark:ring-white/10"
    >
      <div className="flex flex-wrap items-center justify-between gap-x-3 gap-y-1 border-b border-console-overlay/10 px-3 py-2 text-xs text-console-muted">
        <span className="flex min-w-0 items-center gap-2 font-mono">
          <ScrollIcon aria-hidden className="size-4 shrink-0 text-console-command" />
          <span className="truncate">{`noryx-db-${ds.id}`}</span>
        </span>
        <p role="status" className="flex items-center gap-2">
          <span aria-hidden className={cn("size-2 rounded-full", connections[connection].dot)} />
          {t(connections[connection].text)}
        </p>
      </div>
      <div
        ref={viewport}
        role="log"
        aria-label={t("Log of {{name}}", { name: ds.name })}
        tabIndex={0}
        onScroll={(e) => {
          const el = e.currentTarget
          stickToBottom.current = el.scrollHeight - el.scrollTop - el.clientHeight < 32
        }}
        className="h-80 overflow-y-auto px-3 py-2 font-mono code-text [scrollbar-color:var(--console-muted)_transparent] [scrollbar-width:thin] focus-visible:outline-2 focus-visible:-outline-offset-2 focus-visible:outline-ring"
      >
        {lines.length === 0 ? (
          <p className="text-console-muted">{connection === "connecting" ? t("Loading output…") : t("No output yet.")}</p>
        ) : (
          lines.map((line) => (
            <div key={line.id} className={cn("break-words whitespace-pre-wrap", lineClass(line.text))}>
              {line.text}
            </div>
          ))
        )}
      </div>
    </div>
  )
}
