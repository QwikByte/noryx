import { t } from "i18next"
import { responseError } from "@/lib/api"
import { contentUrl, maxEditableBytes, type ServerFiles } from "./api"

/** Logs, also those BungeeCord numbers, and their archives, which open in the viewer instead of the editor. */
export const isLog = (name: string) => /\.log(\.\d+)?(\.gz)?$/i.test(name)

/** How much of a large log is read at a time, from its end. */
const part = maxEditableBytes
/** Archives are unpacked in the browser up to this size, so that a small archive can't fill its memory. */
export const maxUnpacked = 16 * 1024 ** 2

/** What the viewer shows of a log. */
export interface Log {
  text: string
  /** Where the part of the file that isn't shown ends; 0 once all of it is. */
  start: number
  /** The bytes before the text up to start: the start of its first line, which the part before completes. */
  head: Uint8Array
  /** The size of the file, or of the archive unpacked. */
  size: number
  /** Only the start of an archive was unpacked. */
  truncated?: boolean
  /** How long the text is that was added in front of it last, which ends where the text was before. */
  added?: number
}

function concat(parts: Uint8Array[]) {
  const all = new Uint8Array(parts.reduce((n, p) => n + p.length, 0))
  let at = 0
  for (const p of parts) {
    all.set(p, at)
    at += p.length
  }
  return all
}

/** Decodes bytes of a log from start in the file; a first line that may be incomplete waits for the part before. */
function decode(bytes: Uint8Array, start: number) {
  const cut = start > 0 ? bytes.indexOf(10) + 1 || bytes.length : 0
  const text = new TextDecoder().decode(bytes.subarray(cut))
  if (text.includes("\0")) throw new Error(t("This file isn't text and can't be shown here."))
  return { text, head: bytes.slice(0, cut) }
}

/** Reads the part of a log before end, or its last part, with where it starts and the size of the file. */
async function readPart(files: ServerFiles, path: string, end?: number) {
  const range = end === undefined ? `bytes=-${part}` : `bytes=${Math.max(end - part, 0)}-${end - 1}`
  const res = await fetch(contentUrl(files, path), { headers: { Range: range } })
  if (!res.ok) throw await responseError(res, t("The file could not be loaded (status {{status}}).", { status: res.status }))
  const bytes = new Uint8Array(await res.arrayBuffer())
  // An empty file comes whole, without a range.
  const within = /^bytes (\d+)-\d+\/(\d+)$/.exec(res.headers.get("Content-Range") ?? "")
  const [, start = 0, size = bytes.length] = within?.map(Number) ?? []
  return { bytes, start, size }
}

/** Reads the end of a log. */
export async function readEnd(files: ServerFiles, path: string): Promise<Log> {
  const { bytes, start, size } = await readPart(files, path)
  return { ...decode(bytes, start), start, size }
}

/** Adds the part of a log before the text shown. */
export async function readEarlier(files: ServerFiles, path: string, shown: Log): Promise<Log> {
  const before = await readPart(files, path, shown.start)
  const { text, head } = decode(concat([before.bytes, shown.head]), before.start)
  return { ...shown, text: text + shown.text, head, start: before.start, added: text.length }
}

/** Unpacks an archived log in the browser, up to maxUnpacked. */
export async function unpack(files: ServerFiles, path: string): Promise<Log> {
  const res = await fetch(contentUrl(files, path))
  if (!res.ok || !res.body) throw await responseError(res, t("The file could not be loaded (status {{status}}).", { status: res.status }))
  const reader = res.body.pipeThrough(new DecompressionStream("gzip")).getReader()
  const parts: Uint8Array[] = []
  let size = 0
  try {
    while (size <= maxUnpacked) {
      const { done, value } = await reader.read()
      if (done) break
      parts.push(value)
      size += value.length
    }
  } catch {
    throw new Error(t("This archive can't be unpacked."))
  }
  const truncated = size > maxUnpacked
  if (truncated) await reader.cancel()
  let bytes = concat(parts).subarray(0, maxUnpacked)
  // The last line of what was unpacked may be cut off.
  if (truncated) bytes = bytes.subarray(0, bytes.lastIndexOf(10) + 1)
  return { ...decode(bytes, 0), start: 0, size, truncated }
}
