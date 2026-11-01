import { isBedrock, isPaper } from "./server-types"

/** An argument of a command: the words it takes, or the name of a player online. */
type Arg = readonly string[] | "player"

const player = "player"
const gameModes = ["survival", "creative", "adventure", "spectator"]

// Minecraft's commands for the console, with the arguments worth completing.
const vanilla: Record<string, Arg[]> = {
  advancement: [["grant", "revoke"], player],
  ban: [player],
  "ban-ip": [player],
  banlist: [["ips", "players"]],
  clear: [player],
  defaultgamemode: [gameModes],
  deop: [player],
  difficulty: [["peaceful", "easy", "normal", "hard"]],
  effect: [["give", "clear"], player],
  enchant: [player],
  execute: [],
  experience: [["add", "set", "query"], player],
  gamemode: [gameModes, player],
  gamerule: [],
  give: [player],
  help: [],
  kick: [player],
  kill: [player],
  list: [["uuids"]],
  locate: [["structure", "biome", "poi"]],
  msg: [player],
  op: [player],
  pardon: [player],
  "pardon-ip": [],
  recipe: [["give", "take"], player],
  reload: [],
  "save-all": [["flush"]],
  "save-off": [],
  "save-on": [],
  say: [],
  scoreboard: [["objectives", "players"]],
  seed: [],
  setworldspawn: [],
  spawnpoint: [player],
  stop: [],
  summon: [],
  team: [["add", "empty", "join", "leave", "list", "modify", "remove"]],
  teleport: [player, player],
  tell: [player],
  tellraw: [player],
  tick: [["freeze", "query", "rate", "sprint", "step", "unfreeze"]],
  time: [["add", "query", "set"]],
  title: [player, ["actionbar", "clear", "reset", "subtitle", "times", "title"]],
  tp: [player, player],
  transfer: [player],
  weather: [["clear", "rain", "thunder"]],
  whitelist: [["add", "list", "off", "on", "reload", "remove"], player],
  worldborder: [["add", "center", "damage", "get", "set", "warning"]],
  xp: [["add", "set", "query"], player],
}

// Paper and its forks add these, also through Bukkit and the profiler spark.
const paper: Record<string, Arg[]> = {
  mspt: [],
  paper: [["dumpitem", "dumpplugins", "entity", "heap", "mobcaps", "reload", "version"]],
  plugins: [],
  restart: [],
  spark: [["gc", "health", "heapsummary", "profiler", "tps"]],
  tps: [],
  version: [],
}

const proxies: Record<string, Record<string, Arg[]>> = {
  velocity: {
    end: [],
    glist: [["all"]],
    send: [player],
    server: [],
    shutdown: [],
    velocity: [["dump", "heap", "info", "plugins", "reload", "version"]],
  },
  bungeecord: {
    alert: [],
    bungee: [],
    end: [],
    find: [player],
    glist: [],
    greload: [],
    ip: [player],
    perms: [],
    send: [player],
    server: [],
  },
}

// The console commands of Bedrock Dedicated Server.
const bedrock: Record<string, Arg[]> = {
  allowlist: [["add", "list", "off", "on", "reload", "remove"], player],
  changesetting: [["allow-cheats", "difficulty"]],
  clear: [player],
  deop: [player],
  difficulty: [["peaceful", "easy", "normal", "hard"]],
  effect: [player],
  gamemode: [["survival", "creative", "adventure", "spectator"], player],
  gamerule: [],
  give: [player],
  help: [],
  kick: [player],
  kill: [player],
  list: [],
  op: [player],
  permission: [["list", "reload"]],
  reload: [],
  save: [["hold", "query", "resume"]],
  say: [],
  stop: [],
  tell: [player],
  tellraw: [player],
  time: [["add", "query", "set"]],
  title: [player, ["actionbar", "clear", "reset", "subtitle", "times", "title"]],
  tp: [player, player],
  weather: [["clear", "rain", "thunder"]],
}

/** The commands of a type of server that the console completes; Bedrock servers have their own. */
export function commandsOf(type: string): Record<string, Arg[]> {
  if (isBedrock(type)) return bedrock
  return proxies[type] ?? (isPaper(type) ? { ...vanilla, ...paper } : vanilla)
}

export interface Suggestion {
  /** The command line once the suggestion is taken. */
  value: string
  /** What is shown: a command, a word or a whole command line of the history. */
  label: string
  kind: "history" | "command" | "player" | "word"
}

const maxSuggestions = 8
const startsWith = (text: string, prefix: string) => text.toLowerCase().startsWith(prefix.toLowerCase())

/**
 * Completes a command line from the commands typed before (latest last), the commands of the server
 * and the players online: the command, or the word at the end with what that argument takes. Players
 * are offered for the arguments of commands the console doesn't know, e.g. those of plugins.
 */
export function suggest(
  input: string,
  { history, commands, players }: { history: string[]; commands: Record<string, Arg[]>; players: string[] },
): Suggestion[] {
  if (!input.trim()) return []
  const typed = history
    .toReversed()
    .filter((line) => line.length > input.length && startsWith(line, input))
    .slice(0, 3)
    .map((line): Suggestion => ({ value: line, label: line, kind: "history" }))

  const words = input.split(" ")
  const word = words.pop() ?? ""
  const head = words.length ? `${words.join(" ")} ` : ""
  let options: string[]
  let kind: Suggestion["kind"]
  if (!words.length) {
    const slash = word.startsWith("/") ? "/" : ""
    options = Object.keys(commands)
      .filter((name) => startsWith(name, word.slice(slash.length)))
      .sort()
      .map((name) => slash + name)
    kind = "command"
  } else {
    const name = words[0].replace(/^\//, "").toLowerCase()
    const command = Object.hasOwn(commands, name) ? commands[name] : undefined
    const arg: Arg | undefined = command ? command[words.filter(Boolean).length - 1] : word ? player : undefined
    options = (arg === player ? players : (arg ?? [])).filter((option) => startsWith(option, word)).sort()
    kind = arg === player ? "player" : "word"
  }
  const fromHistory = new Set(typed.map((s) => s.value))
  const completed = options
    .filter((option) => option !== word && !fromHistory.has(head + option))
    .map((option): Suggestion => ({ value: `${head}${option} `, label: option, kind }))
  return [...typed, ...completed].slice(0, maxSuggestions)
}
