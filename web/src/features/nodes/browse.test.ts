import { describe, expect, it } from "vitest"
import { validateNodeSearch } from "./browse"

describe("validateNodeSearch", () => {
  it("reads the list's settings from the address", () => {
    const search = { q: "eu", sort: "memory", order: "asc", view: "grid" }
    expect(validateNodeSearch(search)).toEqual(search)
  })

  it("drops values it doesn't know and those that aren't texts, setting every key", () => {
    const search = validateNodeSearch({ q: 7, sort: "players", order: ["asc"], view: "map" })
    expect(search).toEqual({})
    expect(Object.keys(search).sort()).toEqual(["order", "q", "sort", "view"])
  })
})
