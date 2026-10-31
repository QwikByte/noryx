import { describe, expect, it } from "vitest"
import { validateNetworkSearch } from "./browse"

describe("validateNetworkSearch", () => {
  it("reads the list's settings from the address", () => {
    const search = { q: "survival", sort: "players", order: "asc" }
    expect(validateNetworkSearch(search)).toEqual(search)
  })

  it("drops values it doesn't know and those that aren't texts, setting every key", () => {
    const search = validateNetworkSearch({ q: "", sort: "cpu", order: "sideways" })
    expect(search).toEqual({})
    expect(Object.keys(search).sort()).toEqual(["order", "q", "sort"])
  })
})
