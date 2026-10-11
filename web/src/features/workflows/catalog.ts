import {
  ArchiveIcon,
  ArrowCircleUpIcon,
  ArrowClockwiseIcon,
  ArrowsClockwiseIcon,
  BarricadeIcon,
  BellIcon,
  CalendarCheckIcon,
  ChatTextIcon,
  FlowArrowIcon,
  GaugeIcon,
  GitForkIcon,
  GlobeIcon,
  HandPointingIcon,
  HourglassIcon,
  type Icon,
  LightningIcon,
  ListBulletsIcon,
  MagnifyingGlassIcon,
  NotepadIcon,
  PlayIcon,
  PuzzlePieceIcon,
  RepeatIcon,
  ScrollIcon,
  ShieldCheckIcon,
  SignInIcon,
  StopCircleIcon,
  StopIcon,
  SwapIcon,
  TerminalWindowIcon,
  TimerIcon,
  TreeStructureIcon,
  UserSwitchIcon,
  UsersThreeIcon,
  BoxArrowDownIcon,
  WebhooksLogoIcon,
} from "@phosphor-icons/react"
import { t } from "i18next"
import type { Tone } from "@/components/tone"
import { defaultSelection } from "@/features/backups/api"
import { describeSchedule } from "@/features/schedules/describe"
import { msg } from "@/lib/i18n"
import type { Condition, Measure, Op, Servers, Settings, Step, Trigger, TriggerKind } from "./api"

/** What a template can name, e.g. {{trigger.server.name}}, and how the picker of data calls it. */
export interface Token {
  path: string
  label: string
}

export interface TriggerInfo {
  label: string
  description: string
  icon: Icon
  /** What the trigger tells its runs. */
  data: Token[]
  create: () => Trigger
}

const serverData: Token[] = [
  { path: "trigger.server", label: msg("The server") },
  { path: "trigger.server.name", label: msg("Name of the server") },
  { path: "trigger.node.name", label: msg("Name of the node") },
]

export const triggers: Record<TriggerKind, TriggerInfo> = {
  schedule: {
    label: msg("Schedule"),
    description: msg("At set times on chosen days, in a time zone."),
    icon: CalendarCheckIcon,
    data: [{ path: "trigger.time", label: msg("Scheduled time") }],
    create: () => ({ kind: "schedule", schedule: { days: [], times: ["04:00"], timeZone: Intl.DateTimeFormat().resolvedOptions().timeZone } }),
  },
  interval: {
    label: msg("Interval"),
    description: msg("Every few minutes or hours."),
    icon: TimerIcon,
    data: [{ path: "trigger.time", label: msg("Scheduled time") }],
    create: () => ({ kind: "interval", every: 15 }),
  },
  event: {
    label: msg("Entry of the log"),
    description: msg("When something happens that the log tells, e.g. a crash or a failed backup."),
    icon: ScrollIcon,
    data: [
      { path: "trigger.message", label: msg("Message") },
      { path: "trigger.level", label: msg("Level") },
      { path: "trigger.category", label: msg("Category") },
      { path: "trigger.user", label: msg("User") },
      ...serverData,
      { path: "trigger.attrs", label: msg("Details") },
    ],
    create: () => ({ kind: "event", level: "warn", categories: [], contains: "" }),
  },
  server: {
    label: msg("Servers and players"),
    description: msg("When servers start or stop, or players join or leave."),
    icon: SignInIcon,
    data: [{ path: "trigger.on", label: msg("What happened") }, ...serverData, { path: "trigger.player", label: msg("Name of the player") }],
    create: () => ({ kind: "server", on: "joined", players: [] }),
  },
  metric: {
    label: msg("Measure"),
    description: msg("When players, CPU, memory, TPS or data of a server pass a limit."),
    icon: GaugeIcon,
    data: [{ path: "trigger.measure", label: msg("Measure") }, { path: "trigger.value", label: msg("Value") }, ...serverData],
    create: () => ({ kind: "metric", measure: "players", value: 20, minutes: 0 }),
  },
  webhook: {
    label: msg("Webhook"),
    description: msg("When another system calls the workflow's secret URL."),
    icon: WebhooksLogoIcon,
    data: [
      { path: "trigger.body", label: msg("Body of the call") },
      { path: "trigger.query", label: msg("Values of the query") },
    ],
    create: () => ({ kind: "webhook" }),
  },
}

export const measures: Record<Measure, { label: string; unit: string }> = {
  players: { label: msg("Players"), unit: "" },
  cpu: { label: msg("CPU"), unit: "%" },
  memory: { label: msg("Memory"), unit: "%" },
  tps: { label: msg("Ticks per second"), unit: "" },
  disk: { label: msg("Data"), unit: "GB" },
}

export const serverEvents = {
  started: msg("A server started"),
  stopped: msg("A server stopped"),
  joined: msg("A player joined"),
  left: msg("A player left"),
}

/** Ready entries of the log to react to: where they are and what their message says. */
export const eventPresets = [
  { label: msg("A server crashed"), level: "warn", categories: ["servers"], contains: "crashed" },
  { label: msg("A node went offline"), level: "warn", categories: ["nodes"], contains: "went offline" },
  { label: msg("Usage passed a threshold"), level: "warn", categories: ["usage"], contains: "" },
  { label: msg("A backup failed"), level: "warn", categories: ["backups"], contains: "failed" },
  { label: msg("Any error"), level: "error", categories: [], contains: "" },
] as const

/** Describes a trigger, e.g. "Every day at 04:00" or "When a player joins". */
export function describeTrigger(tr: Trigger): string {
  switch (tr.kind) {
    case "schedule":
      return tr.schedule ? describeSchedule(tr.schedule) : t(triggers.schedule.label)
    case "interval":
      return (tr.every ?? 0) % 60 === 0
        ? t("Every {{count}} hours", { count: (tr.every ?? 60) / 60, defaultValue_one: "Every hour" })
        : t("Every {{count}} minutes", { count: tr.every, defaultValue_one: "Every minute" })
    case "event":
      return tr.contains ? t("Entry of the log with “{{text}}”", { text: tr.contains }) : t("Entry of the log of level {{level}}", { level: tr.level })
    case "server":
      return t(serverEvents[tr.on ?? "joined"])
    case "metric": {
      const m = measures[tr.measure ?? "players"]
      const value = `${tr.value ?? 0}${m.unit && ` ${m.unit}`}`
      return tr.below ? t("{{measure}} below {{value}}", { measure: t(m.label), value }) : t("{{measure}} above {{value}}", { measure: t(m.label), value })
    }
    case "webhook":
      return t("When its URL is called")
  }
}

export type Group = "servers" | "players" | "networks" | "control" | "data" | "outside"

export const groups: Record<Group, string> = {
  control: msg("Control"),
  data: msg("Data and variables"),
  servers: msg("Servers"),
  players: msg("Players"),
  networks: msg("Networks"),
  outside: msg("Notifications and the web"),
}

/** Lists of steps within a step, and what the editor calls them. */
export interface Nests {
  steps?: string
  else?: string
  cases?: boolean
  branches?: boolean
}

export interface KindInfo {
  label: string
  description: string
  icon: Icon
  tone: Tone
  group: Group
  /** The settings of a new step. */
  create?: () => Settings
  nests?: Nests
  /** What the step tells later steps, besides its outcome and error. */
  outputs: Token[]
}

const onServers: Token[] = [
  { path: "count", label: msg("Number of servers") },
  { path: "failed", label: msg("Number that failed") },
  { path: "servers", label: msg("The servers") },
  { path: "server.name", label: msg("Name of the first server") },
]

const servers = (): Servers => ({ targets: [], from: "" })

export const always: Condition = { match: "all", rules: [{ left: "", op: "eq", right: "" }] }

export const kinds: Record<string, KindInfo> = {
  if: {
    label: msg("Condition"),
    description: msg("Runs the steps of Yes or of No, depending on rules."),
    icon: GitForkIcon,
    tone: "violet",
    group: "control",
    create: () => ({ condition: structuredClone(always) }),
    nests: { steps: msg("Yes"), else: msg("No") },
    outputs: [{ path: "result", label: msg("Whether it held") }],
  },
  switch: {
    label: msg("Switch"),
    description: msg("Runs the steps of the case whose value equals a value."),
    icon: TreeStructureIcon,
    tone: "violet",
    group: "control",
    create: () => ({ value: "" }),
    nests: { cases: true, else: msg("Default") },
    outputs: [{ path: "case", label: msg("The case that ran") }],
  },
  foreach: {
    label: msg("Apply to each"),
    description: msg("Runs steps for each item of a list, e.g. each server found, a few at a time."),
    icon: ListBulletsIcon,
    tone: "violet",
    group: "control",
    create: () => ({ items: "", concurrency: 1 }),
    nests: { steps: msg("For each item") },
    outputs: [{ path: "count", label: msg("Number of items") }],
  },
  repeat: {
    label: msg("Do until"),
    description: msg("Repeats steps until a condition holds, at most a number of times."),
    icon: RepeatIcon,
    tone: "violet",
    group: "control",
    create: () => ({ until: structuredClone(always), max: 10, delay: 0 }),
    nests: { steps: msg("Repeat") },
    outputs: [
      { path: "count", label: msg("Times it ran") },
      { path: "done", label: msg("Whether the condition held") },
    ],
  },
  parallel: {
    label: msg("Parallel branches"),
    description: msg("Runs branches of steps at the same time and waits for all."),
    icon: FlowArrowIcon,
    tone: "violet",
    group: "control",
    nests: { branches: true },
    outputs: [],
  },
  try: {
    label: msg("Try and catch"),
    description: msg("Runs steps, and others if one of them fails."),
    icon: ShieldCheckIcon,
    tone: "violet",
    group: "control",
    nests: { steps: msg("Try"), else: msg("Catch") },
    outputs: [
      { path: "failed", label: msg("Whether a step failed") },
      { path: "caught.message", label: msg("The error caught") },
    ],
  },
  wait: {
    label: msg("Delay"),
    description: msg("Waits for a time, or until a time of day."),
    icon: HourglassIcon,
    tone: "neutral",
    group: "control",
    create: () => ({ for: "5", unit: "minutes" }),
    outputs: [],
  },
  call: {
    label: msg("Run a workflow"),
    description: msg("Runs another workflow with inputs, and waits for its result if you want."),
    icon: LightningIcon,
    tone: "warning",
    group: "control",
    create: () => ({ workflow: "", inputs: {}, wait: true }),
    outputs: [
      { path: "outcome", label: msg("Outcome of the run") },
      { path: "result", label: msg("Result of the run") },
    ],
  },
  terminate: {
    label: msg("Terminate"),
    description: msg("Ends the run as succeeded or failed, with a message and a result."),
    icon: StopCircleIcon,
    tone: "destructive",
    group: "control",
    create: () => ({ failed: false, message: "", result: "" }),
    outputs: [],
  },
  set: {
    label: msg("Set variable"),
    description: msg("Sets a variable, appends to a list or adds to a number."),
    icon: BoxArrowDownIcon,
    tone: "info",
    group: "data",
    create: () => ({ name: "", value: "", mode: "set" }),
    outputs: [{ path: "value", label: msg("The new value") }],
  },
  servers: {
    label: msg("Find servers"),
    description: msg("Finds servers with their state, and what they use if you want, e.g. to go through them."),
    icon: MagnifyingGlassIcon,
    tone: "info",
    group: "data",
    create: () => ({ ...servers(), state: "", usage: false }),
    outputs: [
      { path: "servers", label: msg("The servers") },
      { path: "count", label: msg("Number of servers") },
      { path: "names", label: msg("Their names") },
      { path: "server.name", label: msg("Name of the first server") },
      { path: "server.players", label: msg("Players of the first server") },
    ],
  },
  players: {
    label: msg("Get players online"),
    description: msg("Lists the players on game servers at the latest measurement."),
    icon: UsersThreeIcon,
    tone: "info",
    group: "data",
    create: servers,
    outputs: [
      { path: "players", label: msg("The players with their servers") },
      { path: "names", label: msg("Their names") },
      { path: "count", label: msg("Number of players") },
    ],
  },
  waitfor: {
    label: msg("Wait for servers"),
    description: msg("Waits until servers run, are stopped or have no players."),
    icon: HandPointingIcon,
    tone: "info",
    group: "data",
    create: () => ({ ...servers(), state: "running", minutes: 10 }),
    outputs: [{ path: "waiting", label: msg("Servers it waited for in vain") }],
  },
  start: {
    label: msg("Start servers"),
    description: msg("Starts stopped servers."),
    icon: PlayIcon,
    tone: "success",
    group: "servers",
    create: servers,
    outputs: onServers,
  },
  stop: {
    label: msg("Stop servers"),
    description: msg("Stops running servers, after warning the players if you want."),
    icon: StopIcon,
    tone: "success",
    group: "servers",
    create: () => ({ ...servers(), warnings: [], message: "" }),
    outputs: onServers,
  },
  restart: {
    label: msg("Restart servers"),
    description: msg("Restarts running servers, after warning the players if you want."),
    icon: ArrowClockwiseIcon,
    tone: "success",
    group: "servers",
    create: () => ({ ...servers(), warnings: [5, 1], message: "" }),
    outputs: onServers,
  },
  command: {
    label: msg("Run console commands"),
    description: msg("Running servers run commands one after the other."),
    icon: TerminalWindowIcon,
    tone: "success",
    group: "servers",
    create: () => ({ ...servers(), commands: [""] }),
    outputs: [{ path: "output", label: msg("Answer of the console") }, ...onServers],
  },
  message: {
    label: msg("Show a message"),
    description: msg("Shows players a message in the chat, as a title or above the hotbar."),
    icon: ChatTextIcon,
    tone: "success",
    group: "servers",
    create: () => ({ ...servers(), kind: "chat", text: "", subtitle: "", player: "" }),
    outputs: onServers,
  },
  image: {
    label: msg("Update image"),
    description: msg("Servers get the newest image of their software; running ones restart if it changed."),
    icon: ArrowCircleUpIcon,
    tone: "success",
    group: "servers",
    create: servers,
    outputs: onServers,
  },
  plugins: {
    label: msg("Update plugins"),
    description: msg("Plugins and mods get the newest version of their channel, releases unless chosen."),
    icon: PuzzlePieceIcon,
    tone: "success",
    group: "servers",
    create: servers,
    outputs: onServers,
  },
  backup: {
    label: msg("Back up servers"),
    description: msg("Backs up servers as a backup job would, keeping the newest backups of the step."),
    icon: ArchiveIcon,
    tone: "success",
    group: "servers",
    create: () => ({ ...servers(), backup: { selection: defaultSelection, location: "", keep: 7 }, label: "" }),
    outputs: onServers,
  },
  player: {
    label: msg("Change players"),
    description: msg("Kicks, bans, whitelists or ops players on game servers."),
    icon: UserSwitchIcon,
    tone: "info",
    group: "players",
    create: () => ({ ...servers(), action: "kick", player: "", reason: "", minutes: 0 }),
    outputs: [{ path: "results", label: msg("Results") }],
  },
  send: {
    label: msg("Send a player"),
    description: msg("Sends a player to another server of a network."),
    icon: SwapIcon,
    tone: "info",
    group: "players",
    create: () => ({ network: "", player: "", server: "" }),
    outputs: [],
  },
  maintenance: {
    label: msg("Maintenance"),
    description: msg("Starts or ends maintenance of a network or of one of its servers."),
    icon: BarricadeIcon,
    tone: "violet",
    group: "networks",
    create: () => ({ network: "", enabled: true, server: "", minutes: 0 }),
    outputs: [],
  },
  rolling: {
    label: msg("Restart server by server"),
    description: msg("Restarts the game servers of a network a few at a time, moving their players first."),
    icon: ArrowsClockwiseIcon,
    tone: "violet",
    group: "networks",
    create: () => ({ network: "", batch: 1 }),
    outputs: [],
  },
  notify: {
    label: msg("Send a notification"),
    description: msg("Sends a message through a channel of the notifications, e.g. to Discord."),
    icon: BellIcon,
    tone: "warning",
    group: "outside",
    create: () => ({ channel: "", level: "info", message: "" }),
    outputs: [],
  },
  http: {
    label: msg("HTTP request"),
    description: msg("Calls a URL on the internet over HTTPS and reads its answer."),
    icon: GlobeIcon,
    tone: "warning",
    group: "outside",
    create: () => ({ method: "POST", url: "https://", headers: [], body: "", anyStatus: false }),
    outputs: [
      { path: "status", label: msg("Status of the answer") },
      { path: "ok", label: msg("Whether it succeeded") },
      { path: "json", label: msg("JSON of the answer") },
      { path: "text", label: msg("Text of the answer") },
    ],
  },
  log: {
    label: msg("Write to the log"),
    description: msg("Writes an entry into the log, which notification rules can send on."),
    icon: NotepadIcon,
    tone: "neutral",
    group: "outside",
    create: () => ({ level: "info", message: "" }),
    outputs: [],
  },
}

export const ops: Record<Op, string> = {
  eq: msg("is"),
  ne: msg("is not"),
  gt: msg("is greater than"),
  ge: msg("is at least"),
  lt: msg("is less than"),
  le: msg("is at most"),
  contains: msg("contains"),
  notContains: msg("doesn't contain"),
  startsWith: msg("starts with"),
  endsWith: msg("ends with"),
  matches: msg("matches the pattern"),
  empty: msg("is empty"),
  notEmpty: msg("isn't empty"),
}

/** Whether a comparison takes a right side. */
export const binary = (op?: Op) => op !== "empty" && op !== "notEmpty"

/** A step as the editor and the runs name it: its own name or that of its kind. */
export const stepName = (s: Pick<Step, "kind" | "name">) => s.name || (kinds[s.kind] ? t(kinds[s.kind].label) : s.kind)

/** The servers of a step in a few words. */
function describeServers(s?: Settings): string {
  const parts = []
  if (s?.targets?.length) parts.push(t("{{count}} targets", { count: s.targets.length, defaultValue_one: "1 target" }))
  if (s?.from) parts.push(s.from)
  return parts.join(" + ") || t("No servers chosen")
}

/** Describes what a step does in a line, e.g. the command it runs. */
export function describeStep(s: Step): string {
  const w = s.with ?? {}
  switch (s.kind) {
    case "if":
    case "repeat": {
      const c: Condition = s.kind === "if" ? w.condition : w.until
      const first = c?.rules?.[0]
      if (!first || first.group) return t("{{count}} rules", { count: c?.rules?.length ?? 0 })
      const rest = c.rules.length - 1
      const rule = [first.left, first.op && t(ops[first.op]), binary(first.op) && first.right].filter(Boolean).join(" ")
      return rest > 0 ? t("{{rule}} and {{count}} more", { rule, count: rest }) : rule
    }
    case "switch":
      return t("{{value}} in {{count}} cases", { value: w.value, count: s.cases?.length ?? 0 })
    case "foreach":
      return w.concurrency > 1 ? t("{{items}}, {{count}} at a time", { items: w.items, count: w.concurrency }) : w.items
    case "parallel":
      return t("{{count}} branches", { count: s.branches?.length ?? 0 })
    case "wait":
      return w.until ? t("until {{time}}", { time: w.until }) : `${w.for} ${t(waitUnits[w.unit as keyof typeof waitUnits] ?? waitUnits.minutes)}`
    case "set":
      return `${w.name || "?"} ${w.mode === "append" ? "+=" : w.mode === "add" ? "+" : "="} ${w.value}`
    case "terminate":
      return w.failed ? t("as failed") : t("as succeeded")
    case "command":
      return (w.commands ?? []).filter(Boolean).join(" · ") || describeServers(w)
    case "message":
      return w.text || describeServers(w)
    case "player":
      return `${w.action} ${w.player}`
    case "send":
      return t("{{player}} to {{server}}", { player: w.player, server: w.server })
    case "maintenance":
      return w.enabled ? t("Start maintenance") : t("End maintenance")
    case "rolling":
      return t("{{count}} at a time", { count: w.batch })
    case "notify":
    case "log":
      return w.message
    case "http":
      return `${w.method} ${w.url}`
    case "call":
      return w.wait ? t("and wait for it") : t("without waiting")
  }
  return describeServers(w)
}

export const waitUnits = { seconds: msg("seconds"), minutes: msg("minutes"), hours: msg("hours") }

/** Why a step can't run as it is, e.g. without servers, for the editor to point out before saving. */
export function missing(s: Step): string | undefined {
  const w = s.with ?? {}
  const servers = ["start", "stop", "restart", "command", "message", "image", "plugins", "backup", "servers", "players", "waitfor", "player"]
  if (servers.includes(s.kind) && !w.targets?.length && !w.from) return t("Choose servers.")
  if ((s.kind === "send" || s.kind === "maintenance" || s.kind === "rolling") && !w.network) return t("Choose a network.")
  if (s.kind === "notify" && !w.channel) return t("Choose a notification channel.")
  if (s.kind === "call" && !w.workflow) return t("Choose a workflow.")
  if (s.kind === "set" && !w.name) return t("Name the variable.")
  if (s.kind === "foreach" && !w.items) return t("Choose what to go through.")
  if (s.kind === "command" && !(w.commands ?? []).some(Boolean)) return t("Enter a command.")
}
