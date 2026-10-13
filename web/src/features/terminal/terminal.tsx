import {
  ArrowsInIcon,
  ArrowsOutIcon,
  BroomIcon,
  PaperPlaneRightIcon,
  SlidersHorizontalIcon,
  StopIcon,
  TerminalWindowIcon,
} from "@phosphor-icons/react"
import { useQuery, useQueryClient } from "@tanstack/react-query"
import { t } from "i18next"
import { type FormEvent, type KeyboardEvent, useEffect, useLayoutEffect, useRef, useState } from "react"
import { flushSync } from "react-dom"
import { Trans } from "react-i18next"
import { ConsoleButton } from "@/components/console-button"
import { useSettings, wraps } from "@/features/preferences/api"
import { CodeViewMenu } from "@/features/preferences/code-view"
import { useCommandHistory } from "@/lib/use-command-history"
import { maximizedClass, useMaximized } from "@/lib/use-maximized"
import { cn } from "@/lib/utils"
import { commandsQuery, runCommand } from "./api"
import { type Choice, commonStart, complete, insert, lookup, matching } from "./complete"

type EntryState = "running" | "done" | "failed" | "stopped"

/** A command with its output. The prompt tells where it ran. */
interface Entry {
  id: number
  prompt: string
  command: string
  output: string
  state: EntryState
  error?: string
}

/** The choices a second Tab lists, until the line changes. */
interface Listed {
  target: string
  line: string
  start: number
  end: number
  choices: Choice[]
}

const maxEntries = 100
/** Characters of output kept per command, e.g. while following a console. */
const maxOutput = 200_000
const historySteps: Partial<Record<string, number>> = { ArrowUp: -1, ArrowDown: 1 }

/** Runs commands on the master or an agent, one at a time, like a terminal. */
export function Terminal({ target, prompt }: { target: string; prompt: string }) {
  const [entries, setEntries] = useState<Entry[]>([])
  const [input, setInput] = useState("")
  const [busy, setBusy] = useState(false)
  const controller = useRef<AbortController>(undefined)
  const viewport = useRef<HTMLDivElement>(null)
  const inputRef = useRef<HTMLInputElement>(null)
  const stickToBottom = useRef(true)
  const nextId = useRef(0)
  const history = useCommandHistory(`terminal:${target}`)
  const queryClient = useQueryClient()
  const { data: commands } = useQuery(commandsQuery(target))
  const [listed, setListed] = useState<Listed>()
  const lastTab = useRef<string>(undefined)
  const shown = listed?.target === target && listed.line === input ? listed : undefined
  const wrap = wraps.terminalWrap.on(useSettings().settings)
  const [maximized, setMaximized] = useMaximized()

  // Leaving the page stops a command that is still running.
  useEffect(() => () => controller.current?.abort(), [])

  useLayoutEffect(() => {
    const el = viewport.current
    if (el && stickToBottom.current) el.scrollTop = el.scrollHeight
  }, [entries])

  const patch = (id: number, change: (entry: Entry) => Partial<Entry>) =>
    setEntries((list) => list.map((e) => (e.id === id ? { ...e, ...change(e) } : e)))

  async function run(command: string) {
    const id = nextId.current++
    setEntries((list) => [...list, { id, prompt, command, output: "", state: "running" as const }].slice(-maxEntries))
    const abort = new AbortController()
    controller.current = abort
    setBusy(true)
    // Output can arrive in many small parts, so it is rendered once per frame.
    let pending = ""
    let frame = 0
    const flush = () => {
      frame = 0
      const text = pending
      pending = ""
      patch(id, (e) => ({ output: (e.output + text).slice(-maxOutput) }))
    }
    let result: Partial<Entry>
    try {
      const error = await runCommand(
        target,
        command,
        (text) => {
          pending += text
          frame ||= requestAnimationFrame(flush)
        },
        abort.signal,
      )
      result = error ? { state: "failed", error } : { state: "done" }
    } catch (error) {
      result = abort.signal.aborted ? { state: "stopped" } : { state: "failed", error: (error as Error).message }
    }
    cancelAnimationFrame(frame)
    const rest = pending
    patch(id, (e) => ({ ...result, output: (e.output + rest).slice(-maxOutput) }))
    controller.current = undefined
    setBusy(false)
  }

  function submit(event: FormEvent) {
    event.preventDefault()
    const command = input.trim()
    if (!command || busy) return
    history.add(command)
    setInput("")
    stickToBottom.current = true
    if (command === "clear") setEntries([])
    else void run(command)
  }

  // Like in a shell: Tab completes the word before the cursor as far as it is certain, and a second Tab lists the
  // choices that are left.
  async function completeWord(el: HTMLInputElement) {
    const line = el.value
    const cursor = el.selectionStart ?? line.length
    const found = commands && complete(commands, line, cursor)
    if (!found) return
    let choices: Choice[]
    try {
      choices = matching(found, found.choices ?? (await lookup(queryClient, target, found)))
    } catch {
      return // e.g. the node is offline, so there is nothing to offer
    }
    if (el.value !== line) return // typed on meanwhile
    const shared = commonStart(choices.map((c) => c.value))
    const next =
      choices.length === 1
        ? insert(line, found.start, cursor, choices[0].value)
        : shared.length > found.word.length && shared.startsWith(found.word)
          ? insert(line, found.start, cursor, shared, true)
          : undefined
    if (next && next.line !== line) {
      flushSync(() => setInput(next.line))
      el.setSelectionRange(next.cursor, next.cursor)
      lastTab.current = next.line
    } else if (choices.length > 1 && lastTab.current === line) {
      setListed({ target, line, start: found.start, end: cursor, choices })
    } else {
      lastTab.current = line
    }
  }

  function choose(choice: Choice) {
    const el = inputRef.current
    if (!shown || !el) return
    const next = insert(input, shown.start, shown.end, choice.value)
    flushSync(() => setInput(next.line))
    el.focus()
    el.setSelectionRange(next.cursor, next.cursor)
  }

  // Like in a terminal: Tab completes, arrow keys walk through earlier commands, Ctrl+C stops one, Ctrl+L clears.
  // Tab in an empty line moves the focus on, as Shift+Tab always does.
  function onKeyDown(event: KeyboardEvent<HTMLInputElement>) {
    const el = event.currentTarget
    const step = historySteps[event.key]
    const modified = event.shiftKey || event.altKey || event.ctrlKey || event.metaKey
    if (event.ctrlKey && event.key === "c" && busy && el.selectionStart === el.selectionEnd) {
      event.preventDefault()
      controller.current?.abort()
    } else if (event.ctrlKey && event.key === "l") {
      event.preventDefault()
      setEntries([])
    } else if (event.key === "Tab" && !modified && input.trim()) {
      event.preventDefault()
      void completeWord(el)
    } else if (event.key === "Escape" && shown) {
      event.preventDefault()
      setListed(undefined)
    } else if (step) {
      event.preventDefault()
      setInput(history.step(step))
    }
  }

  return (
    <section
      data-code
      aria-labelledby="terminal-heading"
      className={cn(
        "overflow-hidden rounded-xl bg-console text-console-foreground shadow-xl ring-1 shadow-black/10 ring-black/5 [font-variant-ligatures:none] dark:ring-white/10",
        maximized && maximizedClass,
      )}
    >
      <div className="flex items-center justify-between gap-4 border-b border-console-overlay/10 py-2 pr-2 pl-4">
        <h2 id="terminal-heading" className="flex min-w-0 items-center gap-2 text-sm font-semibold">
          <TerminalWindowIcon className="size-4 shrink-0 text-console-command" />
          <span className="truncate font-mono">{prompt}</span>
        </h2>
        <div className="flex items-center gap-1">
          <ConsoleButton icon={BroomIcon} label={t("Clear")} onClick={() => setEntries([])} disabled={entries.length === 0} />
          <CodeViewMenu wrap="terminalWrap" trigger={<ConsoleButton icon={SlidersHorizontalIcon} label={t("View")} />} />
          <ConsoleButton
            icon={maximized ? ArrowsInIcon : ArrowsOutIcon}
            label={maximized ? t("Leave full window (Esc)") : t("Fill the window")}
            onClick={() => setMaximized(!maximized)}
          />
        </div>
      </div>
      {/* oxlint-disable-next-line jsx-a11y/click-events-have-key-events, jsx-a11y/no-noninteractive-element-interactions -- a click focuses the prompt, which the keyboard reaches with Tab */}
      <div
        ref={viewport}
        role="log"
        aria-label={t("Terminal output")}
        tabIndex={0}
        onScroll={(e) => {
          const el = e.currentTarget
          stickToBottom.current = el.scrollHeight - el.scrollTop - el.clientHeight < 32
        }}
        // A click into the output focuses the prompt, unless it selected text to copy.
        onClick={() => window.getSelection()?.isCollapsed && inputRef.current?.focus()}
        className={cn(
          "h-[60vh] min-h-72 space-y-3 overflow-y-auto px-4 py-3 font-mono code-text [scrollbar-color:var(--console-muted)_transparent] [scrollbar-width:thin] focus-visible:outline-2 focus-visible:-outline-offset-2 focus-visible:outline-ring",
          maximized && "h-auto min-h-0 flex-1",
        )}
      >
        {entries.length === 0 ? (
          <p className="text-console-muted">
            <Trans
              i18nKey="Type <help/> to list the commands. Tab completes them and a second Tab lists the choices, ↑ and ↓ repeat earlier commands, Ctrl+C stops a running one, and <clear/> empties the terminal."
              components={{
                help: <span className="text-console-foreground">help</span>,
                clear: <span className="text-console-foreground">clear</span>,
              }}
            />
          </p>
        ) : (
          entries.map((entry) => <EntryView key={entry.id} entry={entry} wrap={wrap} />)
        )}
      </div>
      <div aria-live="polite">
        {shown && (
          <ul
            aria-label={t("Choices")}
            className="grid max-h-40 grid-cols-[max-content_minmax(0,1fr)] gap-x-4 overflow-y-auto border-t border-console-overlay/10 px-2 py-1.5 font-mono code-text [scrollbar-color:var(--console-muted)_transparent] [scrollbar-width:thin]"
          >
            {shown.choices.map((choice) => (
              <li key={choice.value} className="col-span-2 grid grid-cols-subgrid">
                <button
                  type="button"
                  onClick={() => choose(choice)}
                  className="col-span-2 grid grid-cols-subgrid rounded-md px-2 py-1 text-left transition-colors hover:bg-console-overlay/10 focus-visible:outline-2 focus-visible:outline-ring"
                >
                  <span>{choice.value}</span>
                  <span className="truncate text-console-muted">{choice.hint}</span>
                </button>
              </li>
            ))}
          </ul>
        )}
      </div>
      <form
        onSubmit={submit}
        className="flex items-center gap-2 border-t border-console-overlay/10 bg-console-overlay/[0.03] py-1.5 pr-1.5 pl-4 focus-within:bg-console-overlay/[0.06]"
      >
        <span aria-hidden className="font-mono code-text font-bold text-console-command">
          $
        </span>
        <input
          ref={inputRef}
          value={input}
          onChange={(e) => setInput(e.target.value)}
          onKeyDown={onKeyDown}
          placeholder={busy ? t("Running… Ctrl+C stops the command") : t("Type a command, e.g. status")}
          aria-label={t("Command for {{prompt}}", { prompt })}
          autoComplete="off"
          autoCapitalize="off"
          spellCheck={false}
          maxLength={1000}
          className="h-9 min-w-0 flex-1 bg-transparent font-mono code-text outline-none placeholder:text-console-muted"
        />
        {busy ? (
          <button
            type="button"
            aria-label={t("Stop the command")}
            onClick={() => controller.current?.abort()}
            className="grid size-9 place-items-center rounded-lg bg-console-error/15 text-console-error transition-colors hover:bg-console-error/25"
          >
            <StopIcon className="size-4" weight="fill" />
          </button>
        ) : (
          <button
            type="submit"
            aria-label={t("Run the command")}
            disabled={!input.trim()}
            className="grid size-9 place-items-center rounded-lg bg-console-command/15 text-console-command transition-colors hover:bg-console-command/25 disabled:pointer-events-none disabled:opacity-30"
          >
            <PaperPlaneRightIcon className="size-4" weight="fill" />
          </button>
        )}
      </form>
    </section>
  )
}

function EntryView({ entry, wrap }: { entry: Entry; wrap: boolean }) {
  return (
    <div>
      <p className="break-words whitespace-pre-wrap">
        <span className="text-console-command">{entry.prompt}</span>
        <span className="text-console-muted"> $ </span>
        {entry.command}
      </p>
      {/* Not wrapped unless chosen, so that tables stay aligned on small screens. */}
      {entry.output && (
        <pre className={cn("font-mono", wrap ? "break-words whitespace-pre-wrap" : "overflow-x-auto [scrollbar-width:thin]")}>{entry.output}</pre>
      )}
      {entry.state === "failed" && <p className="text-console-error">{t("Error: {{error}}", { error: entry.error })}</p>}
      {entry.state === "stopped" && <p className="text-console-warn">{`^C ${t("Stopped.")}`}</p>}
      {entry.state === "running" && (
        <p aria-hidden className="animate-pulse text-console-muted">
          ▍
        </p>
      )}
    </div>
  )
}
