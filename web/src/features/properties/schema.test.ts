import { describe, expect, it } from "vitest"
import { bedrockDefinitions, check, checkValue, definition, definitions, definitionsOf, groups, iconMatches } from "./schema"

describe("definition", () => {
  it("knows the properties of Minecraft", () => {
    expect(definition("view-distance")).toMatchObject({ label: "View distance", group: "World", kind: { type: "number", min: 3, max: 32 } })
  })

  it("puts unknown properties under Advanced, as a switch if they look boolean", () => {
    expect(definition("paper-feature", "true")).toEqual({ label: "paper-feature", description: "", group: "Advanced", kind: { type: "boolean" } })
    expect(definition("paper-feature", "false").kind).toEqual({ type: "boolean" })
    expect(definition("paper-limit", "10").kind).toEqual({ type: "text", mono: true })
    expect(definition("paper-limit").kind).toEqual({ type: "text", mono: true })
  })

  it("puts each property into a group, with options that differ", () => {
    for (const { group, kind } of Object.values(definitions)) {
      expect(groups).toContain(group)
      if (kind.type === "select") expect(new Set(kind.options.map(([value]) => value)).size).toBe(kind.options.length)
    }
  })
})

describe("check", () => {
  it.each([
    ["view-distance", "10", undefined],
    ["view-distance", "3", undefined],
    ["view-distance", "2", "The minimum is 3."],
    ["view-distance", "33", "The maximum is 32."],
    ["view-distance", "", "Enter a whole number."],
    ["view-distance", " ", "Enter a whole number."],
    ["view-distance", "10.5", "Enter a whole number."],
    ["view-distance", "ten", "Enter a whole number."],
    // Minecraft would read these as the default.
    ["max-players", "1e3", "Enter a whole number."],
    ["max-players", "10.0", "Enter a whole number."],
    ["max-players", "10 ", "Enter a whole number."],
    ["max-tick-time", "-1", undefined],
    ["max-tick-time", "-2", "The minimum is -1."],
    ["pause-when-empty-seconds", "-60", undefined],
    ["query.port", "65536", "The maximum is 65535."],
    ["motd", "anything", undefined],
    ["pvp", "maybe", undefined],
    ["unknown", "anything", undefined],
  ])("checks %s=%j", (key, value, error) => {
    expect(check(key, value)).toBe(error)
  })

  it("checks values of a kind", () => {
    expect(checkValue({ type: "number" }, "-5")).toBeUndefined()
    expect(checkValue({ type: "text" }, "")).toBeUndefined()
  })
})

describe("bedrockDefinitions", () => {
  it("knows the properties of Bedrock Dedicated Server, which differ from Minecraft's", () => {
    const known = definitionsOf("bedrock")
    expect(known).toBe(bedrockDefinitions)
    expect(definitionsOf("paper")).toBe(definitions)
    expect(definition("allow-list", "true", known)).toMatchObject({ label: "Allowlist", group: "Players", kind: { type: "boolean" } })
    expect(definition("level-type", "DEFAULT", known).kind).toMatchObject({
      type: "select",
      options: [
        ["DEFAULT", "Default"],
        ["FLAT", "Superflat"],
        ["LEGACY", "Old, limited world"],
      ],
    })
    expect(definition("motd", "x", known).group).toBe("Advanced")
    expect(check("tick-distance", "13", known)).toBe("The maximum is 12.")
    expect(check("view-distance", "4", known)).toBe("The minimum is 5.")
    for (const { group } of Object.values(known)) expect(groups).toContain(group)
  })
})

describe("iconMatches", () => {
  it("finds the server icon by its file and its name", () => {
    expect(iconMatches("")).toBe(true)
    expect(iconMatches("icon")).toBe(true)
    expect(iconMatches("server-icon.png")).toBe(true)
    expect(iconMatches("motd")).toBe(false)
  })
})
