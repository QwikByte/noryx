import { afterEach, describe, expect, it, vi } from "vitest"
import { timeOf, validateLogSearch } from "./search"

afterEach(() => vi.useRealTimers())

describe("validateLogSearch", () => {
  it("reads the filter from the address, with times as ISO timestamps in UTC", () => {
    expect(
      validateLogSearch({
        level: "warn",
        category: "servers",
        source: "agent",
        user: "ada",
        node: "n1",
        server: "s1",
        search: "crashed",
        range: "7d",
        from: "2026-10-01T10:00:00+02:00",
        to: "2026-10-01T09:00:00Z",
      }),
    ).toEqual({
      level: "warn",
      category: "servers",
      source: "agent",
      user: "ada",
      node: "n1",
      server: "s1",
      search: "crashed",
      range: "7d",
      from: "2026-10-01T08:00:00.000Z",
      to: "2026-10-01T09:00:00.000Z",
    })
  })

  it("drops values it doesn't know and those that aren't texts or times, setting every key", () => {
    const search = validateLogSearch({ level: "fatal", category: 1, source: "proxy", search: "", range: "1y", from: "yesterday", to: [] })
    expect(search).toEqual({})
    expect(Object.keys(search).sort()).toEqual(["category", "from", "level", "node", "range", "search", "server", "source", "to", "user"])
  })
})

describe("timeOf", () => {
  it("selects a time from one time to another, either of which may be open, over a range", () => {
    expect(timeOf({ range: "1h", from: "2026-10-01T08:00:00.000Z" })).toEqual({ since: "2026-10-01T08:00:00.000Z", until: undefined })
    expect(timeOf({ to: "2026-10-01T09:00:00.000Z" })).toEqual({ since: undefined, until: "2026-10-01T09:00:00.000Z" })
  })

  it("selects a range up to now, or all time", () => {
    vi.useFakeTimers({ now: new Date("2026-10-31T12:00:00Z") })
    expect(timeOf({ range: "24h" })).toEqual({ since: "2026-10-30T12:00:00.000Z" })
    expect(timeOf({})).toEqual({})
  })
})
