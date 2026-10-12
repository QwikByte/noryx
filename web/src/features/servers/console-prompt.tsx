import { ClockCounterClockwiseIcon, PaperPlaneRightIcon, TerminalIcon, TextTIcon, UserIcon } from "@phosphor-icons/react"
import { t } from "i18next"
import { type FormEvent, type KeyboardEvent, useId, useMemo, useRef, useState } from "react"
import { Trans } from "react-i18next"
import { loadCommands, mayHoldSecret } from "@/lib/use-command-history"
import { cn } from "@/lib/utils"
import { commandsOf, type Suggestion, suggest } from "./console-completion"

const maxHistory = 50
const steps: Partial<Record<string, number>> = { ArrowUp: -1, ArrowDown: 1 }
const icons = { history: ClockCounterClockwiseIcon, command: TerminalIcon, player: UserIcon, word: TextTIcon }

/**
 * The commands typed in this tab of the browser, latest last; sessionStorage keeps them until the tab closes or the
 * user signs out. Commands that may carry a password are never kept.
 */
function useHistory(key: string) {
  const [history, setHistory] = useState(() => loadCommands(key, maxHistory))
  function add(command: string) {
    if (mayHoldSecret(command)) return history
    const next = [...history.filter((c) => c !== command), command].slice(-maxHistory)
    setHistory(next)
    try {
      sessionStorage.setItem(key, JSON.stringify(next))
    } catch {
      // without storage, the history lasts while the console is open
    }
    return next
  }
  return [history, add] as const
}

/**
 * The prompt of the console. The arrow keys walk through earlier commands, like in a terminal, and
 * while typing it suggests commands, their arguments, the names of players online and earlier
 * commands: Tab takes the first or the chosen suggestion.
 */
export function ConsolePrompt({
  historyKey,
  type,
  players,
  disabledReason,
  placeholder,
  onCommand,
}: {
  /** Where the history of the server's commands is kept. */
  historyKey: string
  type: string
  players: string[]
  disabledReason?: string
  placeholder: string
  onCommand: (command: string) => void
}) {
  const [input, setInput] = useState("")
  const [history, addToHistory] = useHistory(historyKey)
  const historyIndex = useRef(history.length)
  // Suggestions show while typing, not for a command of the history.
  const [suggesting, setSuggesting] = useState(false)
  const [active, setActive] = useState(-1)
  const list = useId()
  const commands = useMemo(() => commandsOf(type), [type])
  const suggestions = useMemo(
    () => (suggesting ? suggest(input, { history, commands, players }) : []),
    [suggesting, input, history, commands, players],
  )
  const open = suggestions.length > 0

  function change(value: string, typed: boolean) {
    setInput(value)
    setSuggesting(typed)
    setActive(-1)
  }

  // A completed word is followed by a space, so that the suggestions for the next argument show.
  const take = (s: Suggestion) => change(s.value, s.value.endsWith(" "))

  function submit(event: FormEvent) {
    event.preventDefault()
    const command = input.trim()
    if (!command) return
    historyIndex.current = addToHistory(command).length
    change("", false)
    onCommand(command)
  }

  function onKeyDown(event: KeyboardEvent<HTMLInputElement>) {
    const step = steps[event.key]
    if (event.key === "Tab" && open && !event.shiftKey) {
      event.preventDefault()
      take(suggestions[Math.max(active, 0)])
    } else if (event.key === "Enter" && open && active >= 0) {
      event.preventDefault()
      take(suggestions[active])
    } else if (event.key === "Escape" && open) {
      event.preventDefault()
      setSuggesting(false)
    } else if (step && open) {
      // The suggestions stack upwards from the prompt, so ArrowUp moves away from it.
      event.preventDefault()
      setActive((i) => Math.min(Math.max(i - step, -1), suggestions.length - 1))
    } else if (step) {
      event.preventDefault()
      historyIndex.current = Math.min(Math.max(historyIndex.current + step, 0), history.length)
      change(history[historyIndex.current] ?? "", false)
    }
  }

  return (
    <form
      onSubmit={submit}
      className="relative flex items-center gap-2 border-t border-console-overlay/10 bg-console-overlay/[0.03] py-1.5 pr-1.5 pl-4 focus-within:bg-console-overlay/[0.06]"
    >
      {open && (
        <div className="absolute bottom-full left-2 z-10 mb-1.5 w-[min(calc(100%-1rem),26rem)] overflow-hidden rounded-xl bg-console shadow-2xl ring-1 shadow-black/40 ring-console-overlay/15">
          <ul id={list} role="listbox" aria-label={t("Suggestions")} className="flex flex-col-reverse py-1">
            {suggestions.map((s, i) => {
              const Icon = icons[s.kind]
              return (
                <li
                  key={s.value}
                  id={`${list}-${i}`}
                  role="option"
                  aria-selected={i === active}
                  onMouseDown={(e) => e.preventDefault()} // keeps the focus in the prompt
                  onClick={() => take(s)}
                  className={cn(
                    "flex cursor-pointer items-center gap-2.5 px-3 py-1.5 font-mono code-text text-console-foreground/80 hover:bg-console-overlay/[0.06]",
                    i === active && "bg-console-overlay/10 text-console-foreground",
                  )}
                >
                  <Icon aria-hidden className={cn("size-3.5 shrink-0", s.kind === "player" ? "text-console-warn" : "text-console-muted")} />
                  <span className="truncate">{s.label}</span>
                </li>
              )
            })}
          </ul>
          <p aria-hidden className="border-t border-console-overlay/10 px-3 py-1.5 text-[11px] text-console-muted">
            <Trans
              i18nKey="<key>Tab</key> completes, <key>↑</key> <key>↓</key> choose"
              components={{ key: <kbd className="rounded border border-console-overlay/15 px-1 font-mono text-[10px]" /> }}
            />
          </p>
        </div>
      )}
      <span aria-hidden className="font-mono code-text font-bold text-console-command">
        &gt;
      </span>
      <input
        value={input}
        onChange={(e) => change(e.target.value, true)}
        onKeyDown={onKeyDown}
        onBlur={() => setSuggesting(false)}
        disabled={disabledReason !== undefined}
        placeholder={disabledReason ?? placeholder}
        role="combobox"
        aria-label={t("Console command")}
        aria-autocomplete="list"
        aria-expanded={open}
        aria-controls={open ? list : undefined}
        aria-activedescendant={open && active >= 0 ? `${list}-${active}` : undefined}
        autoComplete="off"
        spellCheck={false}
        maxLength={1000}
        className="h-9 min-w-0 flex-1 bg-transparent font-mono code-text outline-none placeholder:text-console-muted disabled:cursor-not-allowed"
      />
      <button
        type="submit"
        aria-label={t("Send command")}
        disabled={disabledReason !== undefined || !input.trim()}
        className="grid size-9 place-items-center rounded-lg bg-console-command/15 text-console-command transition-colors hover:bg-console-command/25 disabled:pointer-events-none disabled:opacity-30"
      >
        <PaperPlaneRightIcon className="size-4" weight="fill" />
      </button>
    </form>
  )
}
