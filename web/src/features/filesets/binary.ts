import type { SetFile } from "./api"

/** Limits of a file of a set, which the master checks. */
export const maxFileBytes = 1 << 20

// Images the panel previews, by extension. The Content Security Policy allows them as data: URLs.
const images: Record<string, string> = {
  png: "image/png",
  jpg: "image/jpeg",
  jpeg: "image/jpeg",
  gif: "image/gif",
  webp: "image/webp",
  bmp: "image/bmp",
  ico: "image/x-icon",
  avif: "image/avif",
}

/** The type of an image the panel can preview, by the extension of its path, or undefined. */
export const imageType = (path: string) => images[path.split(".").pop()?.toLowerCase() ?? ""]

function toBase64(bytes: Uint8Array) {
  let binary = ""
  for (let i = 0; i < bytes.length; i += 0x8000) binary += String.fromCharCode(...bytes.subarray(i, i + 0x8000))
  return btoa(binary)
}

const fromBase64 = (data: string) => Uint8Array.from(atob(data), (c) => c.charCodeAt(0))

/** The size of a file of a set in bytes. */
export const sizeOf = (file: SetFile) => (file.data !== undefined ? fromBase64(file.data).length : new TextEncoder().encode(file.content).length)

// The starts of archives (also .jar files), Java classes and Linux programs, which the master refuses under any name.
const code = ["PK\x03\x04", "PK\x05\x06", "PK\x07\x08", "\xca\xfe\xba\xbe", "\x7fELF"]

/** Whether a set can't have a file as it holds code, like the master tells by its name or its start. */
export function holdsCode(path: string, bytes: Uint8Array) {
  if (/\.(jar|zip|class)$/i.test(path)) return true
  return code.some((start) => [...start].every((c, i) => bytes[i] === c.charCodeAt(0)))
}

/** A file as a set keeps it: text if it is UTF-8 without NUL bytes, binary otherwise. */
export function setFileOf(path: string, bytes: Uint8Array): SetFile {
  try {
    const content = new TextDecoder("utf-8", { fatal: true }).decode(bytes)
    if (!content.includes("\0")) return { path, content, onlyIfMissing: false }
  } catch {
    // not UTF-8
  }
  return { path, content: "", data: toBase64(bytes), onlyIfMissing: false }
}

/** Saves a file of a set on the computer. */
export function download(file: SetFile) {
  const body = file.data !== undefined ? fromBase64(file.data) : file.content
  const url = URL.createObjectURL(new Blob([body], { type: "application/octet-stream" }))
  Object.assign(document.createElement("a"), { href: url, download: file.path.split("/").pop() }).click()
  setTimeout(() => URL.revokeObjectURL(url))
}
