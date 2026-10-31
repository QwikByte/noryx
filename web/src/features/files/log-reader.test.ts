import { describe, expect, it, vi } from "vitest"
import { maxEditableBytes } from "./api"
import { isLog, maxUnpacked, readEarlier, readEnd, unpack } from "./log-reader"

const files = { nodeId: "n1", serverId: "s1" }
const encode = (text: string) => new TextEncoder().encode(text)
const lines = (count: number, width = 20) => Array.from({ length: count }, (_, i) => `${String(i).padStart(width - 1, "0")}\n`).join("")

/** Answers the requests of the file manager for a file as an agent does: parts of it, or all of it like older agents. */
function serve(file: Uint8Array<ArrayBuffer>, { ranges = true, status = 200 } = {}) {
  const fetch = vi.fn(async (_url: string, init?: RequestInit) => {
    const range = new Headers(init?.headers).get("Range")
    if (status !== 200) return new Response('{"error":"The server has no such file."}', { status })
    if (!ranges || !range) return new Response(file, { headers: { "Content-Length": String(file.length) } })
    const [, from, to] = /^bytes=(\d*)-(\d+)$/.exec(range)!
    const start = from === "" ? Math.max(file.length - Number(to), 0) : Number(from)
    const end = from === "" ? file.length - 1 : Math.min(Number(to), file.length - 1)
    return new Response(file.slice(start, end + 1), { status: 206, headers: { "Content-Range": `bytes ${start}-${end}/${file.length}` } })
  })
  vi.stubGlobal("fetch", fetch)
  return fetch
}

async function gzip(text: string) {
  return new Uint8Array(await new Response(new Blob([text]).stream().pipeThrough(new CompressionStream("gzip"))).arrayBuffer())
}

describe("isLog", () => {
  it.each(["latest.log", "LATEST.LOG", "proxy.log.0", "2026-10-01-1.log.gz"])("opens %j in the viewer", (name) => {
    expect(isLog(name)).toBe(true)
  })

  it.each(["log.txt", "latest.log.bak", "server.properties", "logs"])("doesn't open %j in it", (name) => {
    expect(isLog(name)).toBe(false)
  })
})

describe("readEnd", () => {
  it("reads a small log whole", async () => {
    const fetch = serve(encode("[12:00:00] Starting\n[12:00:01] Done\n"))
    await expect(readEnd(files, "logs/latest.log")).resolves.toEqual({
      text: "[12:00:00] Starting\n[12:00:01] Done\n",
      head: new Uint8Array(),
      start: 0,
      size: 36,
    })
    expect(fetch).toHaveBeenCalledWith("/api/nodes/n1/servers/s1/files/content?path=logs%2Flatest.log", {
      headers: { Range: `bytes=-${maxEditableBytes}` },
    })
  })

  it("reads the end of a large log from a line on, and the part before it on demand", async () => {
    const text = lines(150_000) // 3 MB, more than one part
    const file = encode(text)
    serve(file)
    const end = await readEnd(files, "logs/latest.log")
    expect(end.size).toBe(file.length)
    expect(end.start).toBe(file.length - maxEditableBytes)
    expect(end.start + end.head.length).toBe(text.indexOf("\n", end.start) + 1)
    expect(end.text).toBe(text.slice(end.start + end.head.length))

    const all = await readEarlier(files, "logs/latest.log", end)
    expect(all).toMatchObject({ text, start: 0, size: file.length, added: end.start + end.head.length })
    expect(all.head).toHaveLength(0)
  })

  it("reads logs that older agents send whole, unless they are large", async () => {
    serve(encode("one\ntwo\n"), { ranges: false })
    await expect(readEnd(files, "latest.log")).resolves.toMatchObject({ text: "one\ntwo\n", start: 0, size: 8 })

    serve(encode(lines(150_000)), { ranges: false })
    await expect(readEnd(files, "latest.log")).rejects.toThrow("The agent of this node can't read the end of large files yet.")
  })

  it("refuses files that aren't text", async () => {
    serve(new Uint8Array([0x7f, 0x45, 0x4c, 0x46, 0, 0, 0]))
    await expect(readEnd(files, "latest.log")).rejects.toThrow("This file isn't text and can't be shown here.")
  })

  it("tells the error of the agent", async () => {
    serve(new Uint8Array(), { status: 404 })
    await expect(readEnd(files, "latest.log")).rejects.toMatchObject({ status: 404, message: "The server has no such file." })
  })
})

describe("unpack", () => {
  it("unpacks an archived log", async () => {
    serve(await gzip("[12:00:00] Stopping\n"))
    await expect(unpack(files, "logs/2026-10-01-1.log.gz")).resolves.toEqual({
      text: "[12:00:00] Stopping\n",
      head: new Uint8Array(),
      start: 0,
      size: 20,
      truncated: false,
    })
  })

  it("unpacks only the start of a large archive, up to the end of a line", async () => {
    const text = lines(1_000_000) // 20 MB, more than the browser unpacks
    serve(await gzip(text))
    const log = await unpack(files, "logs/2026-10-01-1.log.gz")
    expect(log.truncated).toBe(true)
    expect(log.size).toBeGreaterThan(maxUnpacked)
    expect(log.text.length).toBeLessThanOrEqual(maxUnpacked)
    expect(log.text).toBe(text.slice(0, text.lastIndexOf("\n", maxUnpacked - 1) + 1))
  })

  it("refuses archives that are broken", async () => {
    serve(encode("not gzip at all"))
    await expect(unpack(files, "logs/2026-10-01-1.log.gz")).rejects.toThrow("This archive can't be unpacked.")
  })
})
