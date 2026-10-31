import { describe, expect, it } from "vitest"
import { matches, validateQuery } from "./search"

describe("matches", () => {
  it("finds texts that hold each word of the search, regardless of case", () => {
    expect(matches("LOBBY 1.21", "lobby-2", "Paper", "1.21.10")).toBe(true)
    expect(matches("lobby paper", "lobby-2", 25565, undefined, "Paper")).toBe(true)
    expect(matches("lobby fabric", "lobby-2", "Paper")).toBe(false)
    expect(matches("25565", "lobby", 25565)).toBe(true)
  })

  it("finds everything without a search", () => {
    expect(matches(undefined, "lobby")).toBe(true)
    expect(matches("", "lobby")).toBe(true)
    expect(matches("  ", "lobby")).toBe(true)
  })
})

describe("validateQuery", () => {
  it("reads the search from the address", () => {
    expect(validateQuery({ q: "lobby" })).toEqual({ q: "lobby" })
  })

  it.each(["", 42, ["lobby"], null])("drops %j, setting the key", (q) => {
    const search = validateQuery({ q })
    expect(search).toEqual({ q: undefined })
    expect(Object.keys(search)).toEqual(["q"])
  })
})
