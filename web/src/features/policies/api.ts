import {
  ArrowCircleUpIcon,
  ArrowClockwiseIcon,
  type Icon,
  PlayIcon,
  PuzzlePieceIcon,
  StopIcon,
  TerminalWindowIcon,
} from "@phosphor-icons/react"
import { t } from "i18next"
import type { JobSettings } from "@/features/backups/api"
import { defaultSchedule, type TaskInput, taskApi } from "@/features/schedules/api"
import { msg } from "@/lib/i18n"

export type PolicyAction = "restart" | "stop" | "start" | "command" | "image" | "plugins"

/** Leaves servers with players alone: "empty" acts only on servers without players, "wait" waits for them to leave. */
export type Condition = "" | "empty" | "wait"

export interface PolicySettings {
  action: PolicyAction
  /** Minutes before a restart or stop at which players are warned. */
  warnings: number[]
  /** The warning; {minutes} is replaced by the minutes left. Empty is the text of the settings when the schedule runs. */
  message: string
  /** The console commands of the command action, sent one after the other. */
  commands?: string[]
  /** The console command of schedules saved before they could have several. */
  command?: string
  /**
   * Unless 0, a restart restarts the running game servers of networks this many at a time, so that their players
   * move to other servers first.
   */
  rolling?: number
  condition?: Condition
  /** The most minutes the condition "wait" waits for the players to leave. */
  wait?: number
  /** Backs up each server first, as a backup job with these settings would. */
  backup?: JobSettings
}

export const policies = taskApi<PolicySettings>("/policies")

export const actions: Record<PolicyAction, { label: string; description: string; icon: Icon }> = {
  restart: {
    label: msg("Restart"),
    description: msg("Running servers restart, after a countdown for the players."),
    icon: ArrowClockwiseIcon,
  },
  stop: { label: msg("Stop"), description: msg("Running servers stop, e.g. at the end of the opening hours."), icon: StopIcon },
  start: { label: msg("Start"), description: msg("Stopped servers start, e.g. when the opening hours begin."), icon: PlayIcon },
  command: {
    label: msg("Console commands"),
    description: msg("Running game servers run commands, e.g. a broadcast."),
    icon: TerminalWindowIcon,
  },
  image: {
    label: msg("Update image"),
    description: msg("Servers get the newest image of their software; running ones restart if it changed."),
    icon: ArrowCircleUpIcon,
  },
  plugins: {
    label: msg("Update plugins"),
    description: msg("Plugins and mods get their newest release, never a beta; servers load them when they restart."),
    icon: PuzzlePieceIcon,
  },
}

/** Whether the players are warned before the action, which they aren't with a condition. */
export const warns = (s: Pick<PolicySettings, "action" | "condition">) => (s.action === "restart" || s.action === "stop") && !s.condition

/** Whether an action can wait for the players to leave, and back up first. */
export const conditional = (action: PolicyAction) => action !== "start"
export const backsUp = (action: PolicyAction) => action !== "start" && action !== "command"

/** The console commands of a policy, also of one saved with a single one. */
export const commandsOf = (s: PolicySettings) => s.commands ?? (s.command ? [s.command] : [])

/** Whether a policy needs more permissions than the one to manage schedules: to back up, change settings or manage plugins. */
export const needsMore = (s: PolicySettings) => !!s.backup || s.action === "image" || s.action === "plugins"

export const emptyPolicy: TaskInput<PolicySettings> = {
  name: "",
  enabled: true,
  schedule: defaultSchedule,
  targets: [],
  settings: { action: "restart", warnings: [10, 5, 1], message: "", commands: [] },
}

/** Describes what a policy does, e.g. "Restart with warnings 10, 5, 1 min before, server by server in networks". */
export function describePolicy(s: PolicySettings): string {
  const commands = commandsOf(s)
  const action =
    s.action === "command"
      ? commands.length === 1
        ? t("Runs “{{command}}”", { command: commands[0] })
        : t("Runs {{count}} commands", { count: commands.length })
      : t(actions[s.action].label)
  const parts = [
    warns(s) && s.warnings.length > 0
      ? t("{{action}} with warnings {{minutes}} min before", { action, minutes: s.warnings.join(", ") })
      : action,
    s.action === "restart" && s.rolling && t("server by server in networks"),
    s.condition === "empty" && t("only without players"),
    s.condition === "wait" && t("once the players left, waiting up to {{minutes}} min", { minutes: s.wait }),
    s.backup && t("after a backup"),
  ]
  return parts.filter(Boolean).join(", ")
}
