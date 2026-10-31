import { describe, expect, it, vi } from "vitest"
import { type Order, sortBy, sortingOf } from "./sort"

const columns: Record<"name" | "cpu" | "port", Order> = { name: "asc", cpu: "desc", port: "asc" }

describe("sortingOf", () => {
  it("sorts by the first column in its direction without a sort in the address", () => {
    expect(sortingOf({}, columns, vi.fn())).toMatchObject({ by: "name", order: "asc" })
  })

  it("sorts by the column in the address, in its own direction unless the address says another", () => {
    expect(sortingOf({ sort: "cpu" }, columns, vi.fn())).toMatchObject({ by: "cpu", order: "desc" })
    expect(sortingOf({ sort: "cpu", order: "asc" }, columns, vi.fn())).toMatchObject({ by: "cpu", order: "asc" })
    expect(sortingOf({ order: "desc" }, columns, vi.fn())).toMatchObject({ by: "name", order: "desc" })
  })

  it("ignores the sort of another table", () => {
    expect(sortingOf({ sort: "players", order: "desc" }, columns, vi.fn())).toMatchObject({ by: "name", order: "asc" })
  })

  it("leaves the first column and a column's own direction out of the address", () => {
    const onChange = vi.fn()
    const { sort } = sortingOf({ sort: "cpu", order: "asc" }, columns, onChange)
    sort("cpu") // keeps the direction
    sort("cpu", "desc")
    sort("port")
    sort("name", "desc")
    expect(onChange.mock.calls.map(([change]) => change)).toEqual([
      { sort: "cpu", order: "asc" },
      { sort: "cpu", order: undefined },
      { sort: "port", order: undefined },
      { sort: undefined, order: "desc" },
    ])
  })
})

describe("sortBy", () => {
  interface Server {
    name: string
    cpu?: number
    version?: string
  }
  const servers: Server[] = [
    { name: "lobby-10", cpu: 5 },
    { name: "survival", version: "1.21.10" },
    { name: "lobby-2", cpu: 5, version: "1.21.9" },
    { name: "creative", cpu: 50, version: "1.21.10" },
  ]
  const names = (sorted: Server[]) => sorted.map((s) => s.name)

  it("sorts by numbers, ties by name as people read them, and items without a value last in either order", () => {
    expect(names(sortBy(servers, "asc", (s) => s.cpu, (s) => s.name))).toEqual(["lobby-2", "lobby-10", "creative", "survival"])
    expect(names(sortBy(servers, "desc", (s) => s.cpu, (s) => s.name))).toEqual(["creative", "lobby-2", "lobby-10", "survival"])
  })

  it("sorts texts with their numbers as numbers", () => {
    expect(names(sortBy(servers, "asc", (s) => s.version, (s) => s.name))).toEqual(["lobby-2", "creative", "survival", "lobby-10"])
  })

  it("leaves the items as they were", () => {
    const before = [...servers]
    sortBy(servers, "desc", (s) => s.name, (s) => s.name)
    expect(servers).toEqual(before)
  })
})
