import { describe, expect, it } from "vitest"
import { validateRedirect } from "./redirect"

describe("validateRedirect", () => {
  it.each(["/", "/servers", "/servers/n1/s1?tab=console#end", '/logs?search=%22a%22', "/a//b"])("keeps the path %j", (redirect) => {
    expect(validateRedirect({ redirect })).toEqual({ redirect })
  })

  it.each(["//evil.example", "https://evil.example/", "javascript:alert(1)", "evil.example", "", " /servers", "servers"])(
    "drops %j, which leads elsewhere or nowhere",
    (redirect) => {
      expect(validateRedirect({ redirect })).toEqual({ redirect: undefined })
    },
  )

  it.each([42, ["/servers"], { to: "/servers" }, null, undefined])("drops %j, which isn't a text", (redirect) => {
    expect(validateRedirect({ redirect })).toEqual({ redirect: undefined })
  })

  it("sets the key without a target, so that the router drops the raw one", () => {
    expect(Object.keys(validateRedirect({}))).toEqual(["redirect"])
  })
})
