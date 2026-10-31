import { describe, expect, it } from "vitest"
import { columnsOf, columnsSetting, validateServerSearch } from "./browse"

describe("validateServerSearch", () => {
  it("reads the list's settings from the address", () => {
    const search = {
      q: "lobby",
      state: "running",
      type: "paper",
      node: "n1",
      network: "none",
      tag: "event",
      sort: "tps",
      order: "desc",
      group: "tag",
      view: "table",
    }
    expect(validateServerSearch(search)).toEqual(search)
  })

  it("drops values it doesn't know and those that aren't texts, setting every key", () => {
    const search = validateServerSearch({
      q: "",
      state: "exploded",
      type: 1,
      node: ["n1"],
      network: { id: "x" },
      tag: null,
      sort: "constructor",
      order: "up",
      group: "toString",
      view: "list",
    })
    expect(search).toEqual({})
    expect(Object.keys(search)).toEqual(["q", "state", "type", "node", "network", "tag", "sort", "order", "group", "view"])
  })
})

describe("columns", () => {
  it("reads the chosen columns in their order, or the default ones without a choice", () => {
    expect(columnsOf(undefined)).toEqual(["node", "network", "players", "cpu", "memory"])
    expect(columnsOf("memory,tps,unknown,node")).toEqual(["node", "tps", "memory"])
    expect(columnsOf("")).toEqual([])
  })

  it("keeps a choice in the columns' order", () => {
    expect(columnsSetting(["memory", "node"])).toBe("node,memory")
    expect(columnsSetting(undefined)).toBeUndefined()
  })
})
