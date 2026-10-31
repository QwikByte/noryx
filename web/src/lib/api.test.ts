import { beforeEach, describe, expect, it, vi } from "vitest"

// The module keeps whether the master answers and the version it runs, so each test loads it afresh.
let api: typeof import("./api")
beforeEach(async () => {
  vi.resetModules()
  api = await import("./api")
})

/** An answer of the master, or of a proxy in front of it without a version. */
const answer = (status: number, { body, version }: { body?: string; version?: string } = {}) =>
  new Response(body ?? null, { status, headers: version ? { "Noryx-Version": version } : {} })

/** Lets fetch answer each request with the next of answers, or fail with an error. */
function serve(...answers: (Response | Error)[]) {
  const fetch = vi.fn(async (_url: string, _init?: RequestInit) => {
    const next = answers.shift()
    if (!next) throw new Error("no more answers")
    if (next instanceof Error) throw next
    return next
  })
  vi.stubGlobal("fetch", fetch)
  return fetch
}

describe("api", () => {
  it("sends JSON to the master's API and reads its answer", async () => {
    const fetch = serve(answer(200, { body: '{"id":"s1"}' }))
    await expect(api.api("/servers", { body: { name: "lobby" } })).resolves.toEqual({ id: "s1" })
    expect(fetch).toHaveBeenCalledWith("/api/servers", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: '{"name":"lobby"}',
      signal: undefined,
    })
  })

  it("reads answers without content as undefined", async () => {
    serve(answer(204), answer(200, { body: "" }))
    await expect(api.api("/a", { method: "DELETE" })).resolves.toBeUndefined()
    await expect(api.api("/b")).resolves.toBeUndefined()
  })

  it("throws the error of a 401, which the router signs in again after, with the master reachable", async () => {
    serve(answer(401, { body: '{"error":"Sign in again.","code":"unauthenticated"}' }))
    const error = await api.api("/me").catch((e: unknown) => e)
    expect(error).toBeInstanceOf(api.ApiError)
    expect(error).toMatchObject({ status: 401, message: "Sign in again.", code: "unauthenticated" })
    expect(api.masterReachable.get()).toBe(true)
  })

  it("tells the field of the request that an error is about", async () => {
    serve(answer(400, { body: '{"error":"Port must be between 1024 and 65535.","field":"port"}' }), answer(400, { body: '{"error":"Invalid."}' }))
    await expect(api.api("/nodes/n1/servers", { body: { port: 80 } })).rejects.toMatchObject({
      status: 400,
      message: "Port must be between 1024 and 65535.",
      field: "port",
    })
    await expect(api.api("/nodes/n1/servers", { body: {} })).rejects.toMatchObject({ field: undefined })
  })

  it("tells the status of errors without a message", async () => {
    serve(answer(500, { body: "<html>Internal Server Error</html>" }))
    await expect(api.api("/servers")).rejects.toMatchObject({ status: 500, message: "The request failed with status 500." })
  })

  it("calls back when the session expired outside of queries", () => {
    api.signedOut() // nothing listens yet
    const callback = vi.fn()
    api.onSignedOut(callback)
    api.signedOut()
    expect(callback).toHaveBeenCalledOnce()
  })
})

describe("send", () => {
  /** Lets XMLHttpRequest answer an upload with status and the JSON body. */
  function answerUploads(status: number, body: unknown) {
    vi.stubGlobal(
      "XMLHttpRequest",
      class {
        status = 0
        response: unknown
        upload = {}
        onload = () => {}
        open() {}
        setRequestHeader() {}
        send() {
          this.status = status
          this.response = body
          this.onload()
        }
      },
    )
  }

  it("reads the answer of an upload", async () => {
    answerUploads(201, { id: "s1" })
    await expect(api.send("POST", "/nodes/n1/servers/import", "archive")).resolves.toEqual({ id: "s1" })
  })

  it("throws the error of the master with its code and field", async () => {
    answerUploads(409, { error: "Port 25565 is already used.", code: "used", field: "port" })
    const error = await api.send("POST", "/nodes/n1/servers/import", "archive").catch((e: unknown) => e)
    expect(error).toBeInstanceOf(api.ApiError)
    expect(error).toMatchObject({ status: 409, message: "Port 25565 is already used.", code: "used", field: "port" })
  })
})

describe("masterReachable", () => {
  it("tells listeners when the master stops and starts answering", async () => {
    serve(new TypeError("Failed to fetch"), new TypeError("Failed to fetch"), answer(200, { body: "[]" }))
    const listener = vi.fn()
    const unsubscribe = api.masterReachable.subscribe(listener)
    expect(api.masterReachable.get()).toBe(true)

    await expect(api.request("/servers")).rejects.toThrow("Failed to fetch")
    expect(api.masterReachable.get()).toBe(false)
    await expect(api.request("/servers")).rejects.toThrow()
    expect(listener).toHaveBeenCalledOnce()

    await api.request("/servers")
    expect(api.masterReachable.get()).toBe(true)
    expect(listener).toHaveBeenCalledTimes(2)

    unsubscribe()
    serve(new TypeError("Failed to fetch"))
    await expect(api.request("/servers")).rejects.toThrow()
    expect(listener).toHaveBeenCalledTimes(2)
  })

  it.each([502, 503, 504])("counts a %i of a proxy as the master being away, but not one of the master", async (status) => {
    serve(answer(status), answer(status, { version: "1.0.0" }))
    await api.request("/servers")
    expect(api.masterReachable.get()).toBe(false)
    await api.request("/servers")
    expect(api.masterReachable.get()).toBe(true)
  })

  it.each([400, 401, 403, 404, 500])("counts a %i as an answer of the master", async (status) => {
    serve(answer(status))
    await api.request("/servers")
    expect(api.masterReachable.get()).toBe(true)
  })

  it("doesn't count a cancelled request against the master", async () => {
    const cancel = new AbortController()
    cancel.abort()
    serve(new DOMException("The operation was aborted.", "AbortError"))
    await expect(api.request("/servers", { signal: cancel.signal })).rejects.toThrow("aborted")
    expect(api.masterReachable.get()).toBe(true)
  })
})

describe("onOutdated", () => {
  it("calls back on each answer of another version than the first one", async () => {
    serve(
      answer(200, { version: "1.0.0" }),
      answer(200, { version: "1.0.0" }),
      answer(401), // to users who aren't signed in, the master doesn't tell its version
      answer(200, { version: "1.1.0" }),
      answer(200, { version: "1.1.0" }),
    )
    const outdated = vi.fn()
    api.onOutdated(outdated)
    for (let i = 0; i < 3; i++) await api.request("/me")
    expect(outdated).not.toHaveBeenCalled()
    await api.request("/me")
    await api.request("/me")
    expect(outdated).toHaveBeenCalledTimes(2)
  })
})
