import { t } from "i18next"
import { msg } from "@/lib/i18n"
import type { Draft, Step } from "./api"
import { kinds, stepName, type Token, triggers } from "./catalog"

/** A list of steps: the root, or one within a step, e.g. "restart_1/else" or "check/case/0". */
export type ListKey = string

export const root: ListKey = "root"

/** Calls fn on each list of steps, the root first, and returns the steps with the lists it returned. */
function mapLists(steps: Step[], fn: (list: Step[], key: ListKey) => Step[], key: ListKey = root): Step[] {
  return fn(steps, key).map((s) => ({
    ...s,
    ...(s.steps && { steps: mapLists(s.steps, fn, `${s.id}/steps`) }),
    ...(s.else && { else: mapLists(s.else, fn, `${s.id}/else`) }),
    ...(s.cases && { cases: s.cases.map((c, i) => ({ ...c, steps: mapLists(c.steps, fn, `${s.id}/case/${i}`) })) }),
    ...(s.branches && { branches: s.branches.map((b, i) => mapLists(b, fn, `${s.id}/branch/${i}`)) }),
  }))
}

/** Each step, those within others too, in the order they appear, with the steps that hold it. */
export function walk(steps: Step[], parents: Step[] = []): { step: Step; parents: Step[] }[] {
  return steps.flatMap((s) => {
    const inner = [...parents, s]
    const lists = [s.steps, s.else, ...(s.cases ?? []).map((c) => c.steps), ...(s.branches ?? [])]
    return [{ step: s, parents }, ...lists.flatMap((list) => (list ? walk(list, inner) : []))]
  })
}

export const insert = (steps: Step[], key: ListKey, index: number, step: Step) =>
  mapLists(steps, (list, k) => (k === key ? list.toSpliced(index, 0, step) : list))

export const replace = (steps: Step[], step: Step) => mapLists(steps, (list) => list.map((s) => (s.id === step.id ? step : s)))

export const remove = (steps: Step[], id: string) => mapLists(steps, (list) => list.filter((s) => s.id !== id))

/** Moves a step up or down within its list. */
export const move = (steps: Step[], id: string, by: -1 | 1) =>
  mapLists(steps, (list) => {
    const i = list.findIndex((s) => s.id === id)
    const j = i + by
    if (i < 0 || j < 0 || j >= list.length) return list
    const out = [...list]
    ;[out[i], out[j]] = [out[j], out[i]]
    return out
  })

export const findStep = (steps: Step[], id: string) => walk(steps).find(({ step }) => step.id === id)?.step

/** A new ID of a step of a kind that no step has, e.g. restart_2. */
function newId(steps: Step[], kind: string, taken = new Set(walk(steps).map(({ step }) => step.id))) {
  const base = kind.replace(/[^a-z0-9_]/g, "") || "step"
  for (let i = 1; ; i++) {
    const id = `${base}_${i}`
    if (!taken.has(id)) {
      taken.add(id)
      return id
    }
  }
}

/** A new step of a kind, with the settings and lists of steps it starts with. */
export function newStep(steps: Step[], kind: string): Step {
  const info = kinds[kind]
  const step: Step = { id: newId(steps, kind), kind, with: info.create?.() ?? {} }
  if (info.nests?.steps) step.steps = []
  if (info.nests?.else) step.else = []
  if (info.nests?.cases) step.cases = [{ value: "", steps: [] }]
  if (info.nests?.branches) step.branches = [[], []]
  return step
}

/** A copy of a step with new IDs for it and the steps within, which the steps of the workflow don't have. */
export function copyStep(steps: Step[], step: Step): Step {
  const taken = new Set(walk(steps).map(({ step }) => step.id))
  return mapLists([structuredClone(step)], (list) => list.map((s) => ({ ...s, id: newId([], s.kind, taken) })))[0]
}

/** The groups of data that templates of a step can name, as the picker of data offers them, with translated labels. */
export interface DataGroup {
  label: string
  tokens: Token[]
}

/**
 * What a template of a step can name: what the triggers tell, the inputs, the variables, the outputs of the steps
 * before it, the item of the loops around it, the error caught around it and the server it acts on.
 */
export function dataAt(draft: Draft, stepId?: string, perServer = false): DataGroup[] {
  const all = walk(draft.steps)
  const at = all.findIndex(({ step }) => step.id === stepId)
  const here = all[at]
  const before = at < 0 ? all : all.slice(0, at)
  const groups: DataGroup[] = []
  const trigger: Token[] = [{ path: "trigger.kind", label: t("What started it") }]
  for (const tr of draft.triggers) {
    for (const token of triggers[tr.kind].data) trigger.push({ ...token, label: t(token.label) })
  }
  groups.push({ label: t("Trigger"), tokens: dedupe(trigger) })
  if (draft.params.length) {
    groups.push({ label: t("Inputs"), tokens: draft.params.map((p) => ({ path: `inputs.${p.name}`, label: p.description || p.name })) })
  }
  const local: Token[] = []
  for (const parent of here?.parents ?? []) {
    if (parent.kind === "foreach") local.push({ path: "item", label: t("Current item of {{step}}", { step: stepName(parent) }) })
    if (parent.kind === "foreach" || parent.kind === "repeat") local.push({ path: "index", label: t("Number of the time, from 0") })
  }
  if (here?.parents.some((p, i) => p.kind === "try" && p.else?.includes(here.parents[i + 1] ?? here.step))) {
    local.push({ path: "error.message", label: t("The error caught") }, { path: "error.step", label: t("ID of the step that failed") })
  }
  if (perServer) local.push({ path: "server.name", label: t("Name of the server it acts on") }, { path: "server", label: t("The server it acts on") })
  if (local.length) groups.push({ label: t("Here"), tokens: dedupe(local) })
  const vars = [...new Set(all.flatMap(({ step }) => (step.kind === "set" && step.with?.name ? [step.with.name as string] : [])))]
  if (vars.length) groups.push({ label: t("Variables"), tokens: vars.map((v) => ({ path: `vars.${v}`, label: v })) })
  for (const { step } of before) {
    if (here?.parents.includes(step)) continue // its output is only there once it ends
    const outputs = [...(kinds[step.kind]?.outputs ?? []), { path: "outcome", label: msg("Outcome") }, { path: "error", label: msg("Error") }]
    groups.push({ label: stepName(step), tokens: outputs.map((o) => ({ path: `steps.${step.id}.${o.path}`, label: t(o.label) })) })
  }
  groups.push({
    label: t("Date and time"),
    tokens: [
      { path: "now.time", label: t("Time of day, e.g. 22:00") },
      { path: "now.date", label: t("Date, e.g. 2026-12-24") },
      { path: "now.weekday", label: t("Weekday, 0 for Sunday") },
      { path: "now.hour", label: t("Hour") },
      { path: "now.iso", label: t("Date and time") },
      { path: "workflow.name", label: t("Name of the workflow") },
      { path: "run.id", label: t("Number of the run") },
    ],
  })
  return groups
}

const dedupe = (tokens: Token[]) => tokens.filter((x, i) => tokens.findIndex((y) => y.path === x.path) === i)
