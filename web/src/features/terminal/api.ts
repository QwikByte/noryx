import { queryOptions } from "@tanstack/react-query"
import { t } from "i18next"
import { api, responseError } from "@/lib/api"

/** The target of the master's own commands; other targets are node IDs. */
export const masterTarget = "master"

/** A command as the terminal completes it: its subcommands, or its arguments and flags. */
export interface Command {
  name: string
  short: string
  args?: Arg[]
  flags?: Flag[]
  commands?: Command[]
}

/** An argument of a command; kind is what it names, e.g. server, backup or datastore. */
interface Arg {
  name: string
  kind: string
  optional?: boolean
  repeated?: boolean
}

/** A flag of a command; value is the kind of value it takes, e.g. server, and missing for switches such as --follow. */
export interface Flag {
  name: string
  shorthand?: string
  usage: string
  value?: string
}

/** The commands of the master or of a node's agent that the user may run. */
export const commandsQuery = (target: string) =>
  queryOptions({
    queryKey: ["terminal", target, "commands"],
    queryFn: () => api<Command[]>(`/terminal/commands?${new URLSearchParams({ target })}`),
  })

interface TerminalEvent {
  output?: string
  done?: boolean
  error?: string
}

/**
 * Runs a command line for the master or the agent of a node. Its output is passed to onOutput as it arrives.
 * Resolves with the error message if the command failed; aborting the signal stops the command.
 */
export async function runCommand(
  target: string,
  command: string,
  onOutput: (text: string) => void,
  signal: AbortSignal,
): Promise<string | undefined> {
  const res = await fetch("/api/terminal", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ target, command }),
    signal,
  })
  if (!res.ok || !res.body) throw await responseError(res)
  const reader = res.body.pipeThrough(new TextDecoderStream()).getReader()
  let buffer = ""
  for (;;) {
    const { done, value } = await reader.read()
    if (done) throw new Error(t("The connection to the master was lost."))
    buffer += value
    for (let end = buffer.indexOf("\n"); end >= 0; end = buffer.indexOf("\n")) {
      const event: TerminalEvent = JSON.parse(buffer.slice(0, end))
      buffer = buffer.slice(end + 1)
      if (event.output) onOutput(event.output)
      if (event.done) return event.error
    }
  }
}
