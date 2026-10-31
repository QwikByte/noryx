import { describe, expect, it } from "vitest"
import type { Step, Trigger, TriggerKind } from "./api"
import { binary, describeStep, describeTrigger, kinds, missing, stepName, triggers } from "./catalog"

const step = (kind: string, settings: Step["with"] = {}, more: Partial<Step> = {}): Step => ({ id: "s1", kind, with: settings, ...more })

describe("triggers", () => {
  it("create triggers of their own kind", () => {
    for (const [kind, info] of Object.entries(triggers)) expect(info.create().kind).toBe(kind as TriggerKind)
  })

  it.each<[Trigger, string]>([
    [{ kind: "interval", every: 1 }, "Every minute"],
    [{ kind: "interval", every: 15 }, "Every 15 minutes"],
    [{ kind: "interval", every: 60 }, "Every hour"],
    [{ kind: "interval", every: 180 }, "Every 3 hours"],
    [{ kind: "schedule", schedule: { days: [], times: ["04:00"], timeZone: "UTC" } }, "Every day at 4:00 AM"],
    [{ kind: "schedule", schedule: { days: [1, 2, 3, 4, 5], times: ["04:00", "16:30"], timeZone: "UTC" } }, "Weekdays at 4:00 AM and 4:30 PM"],
    [{ kind: "event", level: "warn", contains: "crashed" }, "Entry of the log with “crashed”"],
    [{ kind: "event", level: "error", contains: "" }, "Entry of the log of level error"],
    [{ kind: "server", on: "left" }, "A player left"],
    [{ kind: "metric", measure: "cpu", value: 90 }, "CPU above 90 %"],
    [{ kind: "metric", measure: "tps", value: 15, below: true }, "Ticks per second below 15"],
    [{ kind: "webhook" }, "When its URL is called"],
  ])("describes %j as %j", (trigger, text) => {
    expect(describeTrigger(trigger).replace(/\s/g, " ")).toBe(text)
  })
})

describe("steps", () => {
  it("are named by their own name, else by their kind", () => {
    expect(stepName({ kind: "restart", name: "Nightly restart" })).toBe("Nightly restart")
    expect(stepName({ kind: "restart" })).toBe("Restart servers")
    expect(stepName({ kind: "removed-kind" })).toBe("removed-kind")
  })

  it("know which comparisons take a right side", () => {
    expect(binary("eq")).toBe(true)
    expect(binary("empty")).toBe(false)
    expect(binary("notEmpty")).toBe(false)
  })

  it.each([
    [step("if", { condition: { match: "all", rules: [{ left: "{{trigger.player}}", op: "eq", right: "Notch" }] } }), "{{trigger.player}} is Notch"],
    [
      step("repeat", { until: { match: "any", rules: [{ left: "{{steps.s.count}}", op: "empty", right: "" }, { left: "a", op: "eq", right: "b" }] } }),
      "{{steps.s.count}} is empty and 1 more",
    ],
    [step("foreach", { items: "{{steps.find.servers}}", concurrency: 3 }), "{{steps.find.servers}}, 3 at a time"],
    [step("wait", { for: "5", unit: "minutes" }), "5 minutes"],
    [step("wait", { until: "04:00" }), "until 04:00"],
    [step("set", { name: "count", value: "1", mode: "add" }), "count + 1"],
    [step("command", { commands: ["say hi", "", "save-all"] }), "say hi · save-all"],
    [step("command", { commands: [""], targets: [{ kind: "server" }] }), "1 target"],
    [step("restart", { from: "{{trigger.server}}" }), "{{trigger.server}}"],
    [step("restart", {}), "No servers chosen"],
    [step("http", { method: "POST", url: "https://example.com/hook" }), "POST https://example.com/hook"],
    [step("parallel", {}, { branches: [[], []] }), "2 branches"],
    [step("terminate", { failed: true }), "as failed"],
  ])("describes %#", (s, text) => {
    expect(describeStep(s)).toBe(text)
  })

  it.each([
    [step("restart", { targets: [], from: "" }), "Choose servers."],
    [step("send", { network: "" }), "Choose a network."],
    [step("notify", { channel: "" }), "Choose a notification channel."],
    [step("call", { workflow: "" }), "Choose a workflow."],
    [step("set", { name: "" }), "Name the variable."],
    [step("foreach", { items: "" }), "Choose what to go through."],
    [step("command", { from: "{{trigger.server}}", commands: [""] }), "Enter a command."],
    [step("command", { from: "{{trigger.server}}", commands: ["say hi"] }), undefined],
    [step("wait", { for: "5", unit: "minutes" }), undefined],
  ])("points out what step %# is missing", (s, problem) => {
    expect(missing(s)).toBe(problem)
  })

  it("describe new steps of each kind without missing values", () => {
    for (const [kind, info] of Object.entries(kinds)) {
      expect(describeStep(step(kind, info.create?.()))).not.toMatch(/undefined|NaN/)
    }
  })
})
