import { describe, expect, it } from "vitest"
import { validateRedirect } from "./redirect"

describe("validateRedirect", () => {
  it.each(["/", "/servers", "/servers/n1/s1?tab=console#end", '/logs?search=%22a%22', "/a//b", "/a\\b", "/%5Cevil.example"])(
    "keeps the path %j",
    (redirect) => {
      expect(validateRedirect({ redirect })).toEqual({ redirect })
    },
  )

  it.each([
    "//evil.example",
    "/\\evil.example",
    "/\t/evil.example",
    "/\r\n\\evil.example",
    "https://evil.example/",
    "javascript:alert(1)",
    "evil.example",
    "",
    " /servers",
    "servers",
  ])("drops %j, which leads elsewhere or nowhere", (redirect) => {
    expect(validateRedirect({ redirect })).toEqual({ redirect: undefined })
  })

  // What browsers make of a path, e.g. in history.pushState, decides whether it stays on the panel.
  it.each(["/servers", "//evil.example", "/\\evil.example", "/\t/evil.example", "/\t\\evil.example", "/\n/evil.example", "/a\\b", "/./x"])(
    "keeps %j only if browsers read it as a path of the panel",
    (redirect) => {
      const sameSite = new URL(redirect, "https://panel.example").origin === "https://panel.example"
      expect(validateRedirect({ redirect }).redirect !== undefined).toBe(sameSite)
    },
  )

  it.each([42, ["/servers"], { to: "/servers" }, null, undefined])("drops %j, which isn't a text", (redirect) => {
    expect(validateRedirect({ redirect })).toEqual({ redirect: undefined })
  })

  it("sets the key without a target, so that the router drops the raw one", () => {
    expect(Object.keys(validateRedirect({}))).toEqual(["redirect"])
  })
})
