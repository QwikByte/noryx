import {
  ArrowDownIcon,
  ArrowUpIcon,
  ArrowsInIcon,
  ArrowsOutIcon,
  BroomIcon,
  DownloadSimpleIcon,
  MagnifyingGlassIcon,
  SlidersHorizontalIcon,
  TerminalIcon,
  WarningIcon,
} from "@phosphor-icons/react"
import { Link } from "@tanstack/react-router"
import { t } from "i18next"
import { memo, type ReactNode, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react"
import { ConsoleButton } from "@/components/console-button"
import { useAccess } from "@/features/access/use-access"
import { useSettings } from "@/features/preferences/api"
import { CodeViewMenu } from "@/features/preferences/code-view"
import { useServerUsage } from "@/features/usage/api"
import { dayOf } from "@/lib/format"
import { locale, msg, timeZone } from "@/lib/i18n"
import { type StreamState, useEventStream } from "@/lib/use-event-stream"
import { maximizedClass, useMaximized } from "@/lib/use-maximized"
import { cn } from "@/lib/utils"
import { earlierOutput, type Server, useSendCommand } from "./api"
import { levelOf, literal, type Run, runs } from "./console-format"
import { ConsolePrompt } from "./console-prompt"
import { serverType } from "./server-types"

type LineKind = "log" | "command" | "output" | "error"
interface Line {
  id: number
  kind: LineKind
  /** The line without colours, which search and downloads use. */
  text: string
  runs: Run[]
  level?: "error" | "warn"
  /** The ID of a line of the server's output: when it was written, in nanoseconds since 1970. */
  time?: string
}

/** When a line was written, with seconds, in the panel's locale and time zone. */
const writtenAt = new Intl.DateTimeFormat(locale, { timeStyle: "medium", timeZone })
const formatWritten = (time: string) => writtenAt.format(Number(time.slice(0, -6)))

const connections: Record<StreamState, { text: string; dot: string }> = {
  connecting: { text: msg("Connecting…"), dot: "bg-console-warn animate-pulse" },
  live: { text: msg("Live"), dot: "bg-console-command shadow-[0_0_8px_var(--console-command)]" },
  ended: { text: msg("Showing the last output"), dot: "bg-console-muted" },
  lost: { text: msg("Connection lost, reconnecting…"), dot: "bg-console-warn animate-pulse" },
  failed: { text: msg("The console is not available right now."), dot: "bg-console-error" },
}

const levelClass = { error: "text-console-error", warn: "text-console-warn" }

/**
 * Live console of a server: its output as it happens, which can be searched, filtered, downloaded
 * and extended by earlier output, and a prompt for commands.
 */
export function Console({ nodeId, server }: { nodeId: string; server: Server }) {
  const { can } = useAccess()
  const { settings, change } = useSettings()
  const [lines, setLines] = useState<Line[]>([])
  const [query, setQuery] = useState("")
  const [maximized, setMaximized] = useMaximized()
  const problems = settings.consoleFilter === "problems"
  const wrap = settings.consoleWrap !== "scroll"
  const times = settings.consoleTimes === "show"
  // How many lines the console keeps, which a ref gives the batches that add lines.
  const maxLines = useRef(2000)
  useEffect(() => {
    maxLines.current = Number(settings.consoleLines ?? 2000)
  })
  // Lines added while scrolled up, which the button to the end counts.
  const [unseen, setUnseen] = useState(0)
  const [earlier, setEarlier] = useState<"idle" | "loading" | "done">("idle")
  const send = useSendCommand(nodeId, server.id)
  const players = useServerUsage(nodeId, server.id).usage?.players?.names
  const viewport = useRef<HTMLDivElement>(null)
  const stickToBottom = useRef(true)
  // The distance to the end of the output to keep after earlier lines were added at its start.
  const keepFromEnd = useRef<number>(undefined)
  const nextId = useRef(0)
  const pending = useRef<Line[]>([])
  const frame = useRef(0)
  // Commands are sent one after the other, so that they run and answer in the order typed.
  const queue = useRef(Promise.resolve())

  const live = server.state !== "stopped"
  const proxy = serverType(server.type).proxy

  function line(kind: LineKind, text: string, time?: string): Line {
    const parts = kind === "log" || kind === "output" ? runs(text) : [{ text }]
    const plain = parts.map((r) => r.text).join("")
    return { id: nextId.current++, kind, text: plain, runs: parts, level: kind === "error" ? "error" : levelOf(plain), time }
  }

  // Lines arrive in bursts (the initial tail has hundreds), so they are rendered once per frame.
  function append(...added: Line[]) {
    pending.current.push(...added)
    frame.current ||= requestAnimationFrame(() => {
      frame.current = 0
      const batch = pending.current
      pending.current = []
      setLines((current) => [...current, ...batch].slice(-maxLines.current))
      if (!stickToBottom.current) setUnseen((n) => n + batch.length)
    })
  }

  // Connects again when the state of the server changes, e.g. when it starts again after a crash,
  // and continues after the latest line; without one, it shows the last lines afresh.
  const connection = useEventStream(
    `/api/nodes/${nodeId}/servers/${server.id}/logs?colors=true`,
    {
      onOpen: (resumed) => {
        if (resumed) return
        pending.current = []
        setLines([])
        setEarlier("idle")
      },
      onMessage: (data, id) => append(line("log", data, id || undefined)),
    },
    server.state,
  )

  useLayoutEffect(() => {
    const el = viewport.current
    if (!el) return
    if (keepFromEnd.current !== undefined) el.scrollTop = el.scrollHeight - keepFromEnd.current
    else if (stickToBottom.current) el.scrollTop = el.scrollHeight
    keepFromEnd.current = undefined
  }, [lines])

  function run(command: string) {
    scrollToEnd()
    append(line("command", `> ${command}`))
    queue.current = queue.current.then(() =>
      send.mutateAsync(command).then(
        ({ output, formatted }) =>
          append(
            ...(formatted || output)
              .split("\n")
              .filter((l) => l.trim())
              .map((l) => line("output", l)),
          ),
        (error: Error) => append(line("error", error.message)),
      ),
    )
  }

  // The output reaches back as far as the agent keeps the server's lines; one load gets all of them.
  async function loadEarlier() {
    const first = lines.find((l) => l.time)
    if (!first?.time) return
    setEarlier("loading")
    try {
      const { lines: older } = await earlierOutput(nodeId, server.id, first.time)
      const el = viewport.current
      if (el) keepFromEnd.current = el.scrollHeight - el.scrollTop
      setLines((current) => [...older.map((l) => line("log", l.text, l.id)), ...current])
      setEarlier("done")
    } catch (e) {
      append(line("error", (e as Error).message))
      setEarlier("idle")
    }
  }

  function scrollToEnd() {
    stickToBottom.current = true
    setUnseen(0)
    viewport.current?.scrollTo({ top: viewport.current.scrollHeight })
  }

  function clear() {
    pending.current = []
    setLines([])
    setUnseen(0)
    setEarlier("idle")
  }

  const search = useMemo(() => (query ? new RegExp(literal(query), "gi") : undefined), [query])
  const shown = useMemo(() => {
    const matches = search && new RegExp(search.source, "i")
    return lines.filter((l) => (!problems || l.level) && (!matches || matches.test(l.text)))
  }, [lines, search, problems])
  const filtered = shown.length !== lines.length

  function download() {
    const text = shown.map((l) => `${times && l.time ? `${formatWritten(l.time)} ` : ""}${l.text}\n`).join("")
    const url = URL.createObjectURL(new Blob([text], { type: "text/plain" }))
    const name = `${server.name}-console-${dayOf(Date.now())}.txt`
    Object.assign(document.createElement("a"), { href: url, download: name }).click()
    setTimeout(() => URL.revokeObjectURL(url))
  }

  const disabledReason = !can("console.commands", nodeId, server.id)
    ? t("Your groups don't let you send commands")
    : !live
      ? t("Start the server to send commands")
      : undefined

  return (
    <section
      data-code
      aria-labelledby="console-heading"
      className={cn(
        "overflow-hidden rounded-xl bg-console text-console-foreground shadow-xl ring-1 shadow-black/10 ring-black/5 [font-variant-ligatures:none] dark:ring-white/10",
        maximized && maximizedClass,
      )}
    >
      <div className="flex flex-wrap items-center gap-x-4 gap-y-2 border-b border-console-overlay/10 py-2 pr-2 pl-4">
        <h2 id="console-heading" className="flex items-center gap-2 text-sm font-semibold">
          <TerminalIcon className="size-4 text-console-command" />
          {t("Console")}
        </h2>
        <p role="status" className="mr-auto flex items-center gap-2 text-xs text-console-muted">
          <span aria-hidden className={cn("size-2 rounded-[2px]", connections[connection].dot)} />
          {t(connections[connection].text)}
        </p>
        <div className="flex w-full items-center gap-1 sm:w-auto">
          <div className="relative min-w-0 flex-1 sm:w-60 sm:flex-none">
            <MagnifyingGlassIcon
              aria-hidden
              className="pointer-events-none absolute top-1/2 left-2.5 size-3.5 -translate-y-1/2 text-console-muted"
            />
            <input
              type="search"
              value={query}
              onChange={(e) => setQuery(e.target.value)}
              placeholder={t("Search output")}
              aria-label={t("Search the output")}
              className={cn(
                "h-8 w-full rounded-lg bg-console-overlay/[0.06] pr-2 pl-8 text-xs outline-none placeholder:text-console-muted focus-visible:ring-2 focus-visible:ring-ring [&::-webkit-search-cancel-button]:hidden",
                // Room for how many lines match.
                filtered && "pr-16",
              )}
            />
            {filtered && (
              <span className="pointer-events-none absolute top-1/2 right-2.5 -translate-y-1/2 text-[11px] text-console-muted tabular-nums">
                {t("{{shown}} of {{total}}", { shown: shown.length, total: lines.length })}
              </span>
            )}
          </div>
          <ConsoleButton
            icon={WarningIcon}
            label={t("Only warnings and errors")}
            aria-pressed={problems}
            onClick={() => change({ consoleFilter: problems ? "all" : "problems" })}
          />
          <ConsoleButton icon={DownloadSimpleIcon} label={t("Download as text file")} disabled={!shown.length} onClick={download} />
          <ConsoleButton icon={BroomIcon} label={t("Clear the output")} disabled={!lines.length} onClick={clear} />
          <CodeViewMenu wrap="consoleWrap" times trigger={<ConsoleButton icon={SlidersHorizontalIcon} label={t("View")} />} />
          <ConsoleButton
            icon={maximized ? ArrowsInIcon : ArrowsOutIcon}
            label={maximized ? t("Leave full window (Esc)") : t("Fill the window")}
            onClick={() => setMaximized(!maximized)}
          />
        </div>
      </div>
      <div className={cn("relative", maximized && "min-h-0 flex-1")}>
        {/* Ligatures are off so that output like "<--" shows exactly what the server printed. */}
        <div
          ref={viewport}
          role="log"
          aria-label={t("Console output of {{name}}", { name: server.name })}
          tabIndex={0}
          onScroll={(e) => {
            const el = e.currentTarget
            stickToBottom.current = el.scrollHeight - el.scrollTop - el.clientHeight < 32
            if (stickToBottom.current) setUnseen(0)
          }}
          className={cn(
            "h-[60vh] min-h-72 overflow-auto px-4 py-3 font-mono code-text [scrollbar-color:var(--console-muted)_transparent] [scrollbar-width:thin] focus-visible:outline-2 focus-visible:-outline-offset-2 focus-visible:outline-ring",
            maximized && "h-full min-h-0",
          )}
        >
          {lines.some((l) => l.time) && <Earlier state={earlier} onLoad={loadEarlier} nodeId={nodeId} server={server} />}
          {lines.length === 0 ? (
            <p className="text-console-muted">{connection === "connecting" ? t("Loading output…") : t("No output yet.")}</p>
          ) : shown.length === 0 ? (
            <p className="text-console-muted">{t("No lines match.")}</p>
          ) : (
            shown.map((l) => <OutputLine key={l.id} line={l} search={search} wrap={wrap} times={times} />)
          )}
        </div>
        {unseen > 0 && (
          <button
            type="button"
            onClick={scrollToEnd}
            className="absolute bottom-3 left-1/2 flex -translate-x-1/2 items-center gap-1.5 rounded-full bg-console-command px-3 py-1.5 text-xs font-semibold text-console shadow-lg shadow-black/40 transition-transform hover:scale-105 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-ring"
          >
            <ArrowDownIcon className="size-3.5" weight="bold" />
            {t("{{count}} new lines", { count: unseen, defaultValue_one: "{{count}} new line" })}
          </button>
        )}
      </div>
      <ConsolePrompt
        historyKey={`noryx-history:console:${nodeId}/${server.id}`}
        type={server.type}
        players={players ?? noPlayers}
        disabledReason={disabledReason}
        placeholder={proxy ? t("Type a command, e.g. glist") : t("Type a command, e.g. say Hello")}
        onCommand={run}
      />
    </section>
  )
}

const noPlayers: string[] = []

/** A line of the output, with when it was written if times shows; it renders again only when it or how it shows changes. */
const OutputLine = memo(function OutputLine({ line, search, wrap, times }: { line: Line; search?: RegExp; wrap: boolean; times: boolean }) {
  return (
    <div
      className={cn(
        wrap ? "break-words whitespace-pre-wrap" : "w-max whitespace-pre",
        line.kind === "command" ? "text-console-command" : line.level && levelClass[line.level],
      )}
    >
      {times && (
        <span className="mr-3 text-console-muted tabular-nums select-none">
          {line.time ? formatWritten(line.time) : <span className="invisible">{blankTime}</span>}
        </span>
      )}
      <Highlighted runs={line.runs} search={search} />
    </div>
  )
})

// A time as wide as most, for the room of lines without one, e.g. commands.
const blankTime = writtenAt.format(Date.UTC(2000, 0, 1, 22, 58, 58))

/** The runs of a line, with what search found in it marked; search must be global. */
function Highlighted({ runs, search }: { runs: Run[]; search?: RegExp }) {
  const text = runs.map((r) => r.text).join("")
  const found = search ? [...text.matchAll(search)].map((m) => [m.index, m.index + m[0].length]) : []
  let at = 0
  return runs.map((run, i) => {
    const start = at
    at += run.text.length
    const parts: ReactNode[] = []
    let pos = 0
    for (const [from, to] of found) {
      const a = Math.max(from - start, pos)
      const b = Math.min(to - start, run.text.length)
      if (b <= a) continue
      if (a > pos) parts.push(run.text.slice(pos, a))
      parts.push(
        <mark key={a} className="rounded-xs bg-console-warn/30 text-inherit">
          {run.text.slice(a, b)}
        </mark>,
      )
      pos = b
    }
    if (pos < run.text.length) parts.push(run.text.slice(pos))
    return (
      <span key={i} className={run.className}>
        {parts}
      </span>
    )
  })
}

/** Loads the earlier output once; afterwards, it points to the server's log files. */
function Earlier({
  state,
  onLoad,
  nodeId,
  server,
}: {
  state: "idle" | "loading" | "done"
  onLoad: () => void
  nodeId: string
  server: Server
}) {
  const readable = useAccess().can("files.read", nodeId, server.id)
  if (state !== "done")
    return (
      <button
        type="button"
        onClick={onLoad}
        disabled={state === "loading"}
        className="mx-auto mb-2 flex items-center gap-1.5 rounded-full px-3 py-1 text-[11px] text-console-muted ring-1 ring-console-overlay/10 transition-colors hover:bg-console-overlay/10 hover:text-console-foreground disabled:animate-pulse"
      >
        <ArrowUpIcon className="size-3" weight="bold" />
        {state === "loading" ? t("Loading earlier output…") : t("Load earlier output")}
      </button>
    )
  return (
    <p className="mb-2 border-b border-dashed border-console-overlay/10 pb-2 text-center text-[11px] text-console-muted">
      {t("This is as far back as the console reaches.")}{" "}
      {readable && (
        <Link
          to="/nodes/$nodeId/servers/$serverId/files"
          params={{ nodeId, serverId: server.id }}
          // BungeeCord writes its log into the server's folder, the others into logs.
          search={{ path: server.type === "bungeecord" ? undefined : "logs" }}
          className="text-console-foreground underline underline-offset-2 hover:text-console-command"
        >
          {t("Older output is in the log files.")}
        </Link>
      )}
    </p>
  )
}
