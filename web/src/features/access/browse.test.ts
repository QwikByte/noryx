import { describe, expect, it } from "vitest"
import { validateUserSearch } from "./browse"

describe("validateUserSearch", () => {
  it("reads the list's settings from the address", () => {
    const search = { q: "ada", group: "g1", state: "nomfa", sort: "added", order: "asc" }
    expect(validateUserSearch(search)).toEqual(search)
  })

  it("drops values it doesn't know and those that aren't texts, setting every key", () => {
    const search = validateUserSearch({ q: ["ada"], group: "", state: "admin", sort: "password", order: 1 })
    expect(search).toEqual({})
    expect(Object.keys(search).sort()).toEqual(["group", "order", "q", "sort", "state"])
  })
})
