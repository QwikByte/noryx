import { useRef } from "react"

const prefix = "noryx-history:"
const maxCommands = 50

/**
 * A word of a command after which a password or another secret may follow, e.g. in "authme register Steve
 * <password>" or "login <password>". The logs of the master and the agents leave out what follows it by the same
 * rule (internal/logging/command.go).
 */
const secretWord = /^\/?(l|log|login|reg|register|unregister|unreg|cp|pw|.*pass.*|.*secret.*|.*token.*|.*key)$/i

/** Whether a command may carry a password or another secret, which no history keeps. */
export function mayHoldSecret(command: string) {
  return command.trim().split(/\s+/).slice(0, -1).some((word) => secretWord.test(word))
}

/** The commands of a history kept in the browser tab, without those that may carry a secret. */
export function loadCommands(key: string, max = maxCommands): string[] {
  try {
    const stored: unknown = JSON.parse(sessionStorage.getItem(key) ?? "[]")
    return Array.isArray(stored)
      ? stored.filter((c): c is string => typeof c === "string" && !mayHoldSecret(c)).slice(-max)
      : []
  } catch {
    return []
  }
}

const load = (key: string) => loadCommands(prefix + key)

/**
 * The commands typed into a prompt, e.g. of a console, which ↑ and ↓ step through. The browser tab keeps the
 * latest ones per key until it is closed or the user signs out, so they outlast leaving the page; commands that
 * may carry a password are never kept.
 */
export function useCommandHistory(key: string) {
  const state = useRef<{ key: string; commands: string[]; index: number }>(undefined)
  const current = () => {
    if (state.current?.key !== key) {
      const commands = load(key)
      state.current = { key, commands, index: commands.length }
    }
    return state.current
  }
  return {
    /** Adds a command as the latest one and steps past it. */
    add(command: string) {
      const s = current()
      s.index = s.commands.length
      if (mayHoldSecret(command)) return
      s.commands = [...s.commands.filter((c) => c !== command), command].slice(-maxCommands)
      s.index = s.commands.length
      try {
        sessionStorage.setItem(prefix + key, JSON.stringify(s.commands))
      } catch {
        // The commands then only last as long as the page.
      }
    },
    /** Steps back (-1) or forward (1) and returns the command there; past the latest one, "". */
    step(delta: number) {
      const s = current()
      s.index = Math.min(Math.max(s.index + delta, 0), s.commands.length)
      return s.commands[s.index] ?? ""
    },
  }
}

/** Forgets the commands of all prompts, so that whoever signs in next in the tab doesn't see them. */
export function forgetCommandHistories() {
  try {
    for (const key of Object.keys(sessionStorage)) if (key.startsWith(prefix)) sessionStorage.removeItem(key)
  } catch {
    // Without storage, there is nothing to forget.
  }
}

const ownerKey = `${prefix}owner`

/**
 * Keeps the commands of the prompts for the user who signed in, and forgets them if another user typed them, e.g.
 * when a session expired and someone else signs in in the same tab.
 */
export function ownCommandHistories(userId: number) {
  try {
    if (sessionStorage.getItem(ownerKey) === String(userId)) return
    forgetCommandHistories()
    sessionStorage.setItem(ownerKey, String(userId))
  } catch {
    // Without storage, there are no commands to keep.
  }
}
