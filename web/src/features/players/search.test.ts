import { describe, expect, it, vi } from "vitest"
import { validatePlayerSearch } from "./search"

// The hook that remembers sortings loads the panel's theme, which needs a browser; the validator doesn't use it.
vi.mock("@/features/preferences/sorting", () => ({ useSortings: vi.fn() }))

describe("validatePlayerSearch", () => {
  it("reads the page's search from the address, with the sorts of each of its tables", () => {
    expect(validatePlayerSearch({ q: "Notch", tab: "seen", network: "nw1", sort: "playtime", order: "desc" })).toEqual({
      q: "Notch",
      tab: "seen",
      network: "nw1",
      sort: "playtime",
      order: "desc",
    })
    expect(validatePlayerSearch({ tab: "banned", sort: "servers" })).toMatchObject({ tab: "banned", sort: "servers" })
  })

  it("drops values it doesn't know and those that aren't texts, setting every key", () => {
    const search = validatePlayerSearch({ q: "", tab: "admins", network: 3, sort: "ip", order: "random" })
    expect(search).toEqual({})
    expect(Object.keys(search).sort()).toEqual(["network", "order", "q", "sort", "tab"])
  })
})
