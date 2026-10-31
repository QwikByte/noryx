import { afterEach, describe, expect, it, vi } from "vitest"
import { type Cell, type CsvFormat, downloadCsv, languageSeparator } from "./csv"

afterEach(() => vi.useRealTimers())

/** Downloads rows as a CSV file and returns its name and bytes. */
async function download(rows: Cell[][], format: CsvFormat) {
  const files: Blob[] = []
  vi.spyOn(URL, "createObjectURL").mockImplementation((file) => {
    files.push(file as Blob)
    return "blob:csv"
  })
  const link = { href: "", download: "", click: vi.fn() }
  vi.stubGlobal("document", { createElement: () => link })
  downloadCsv("servers", rows, format)
  expect(link.click).toHaveBeenCalledOnce()
  expect(link.href).toBe("blob:csv")
  return { name: link.download, bytes: new Uint8Array(await files[0].arrayBuffer()) }
}

const text = (bytes: Uint8Array) => new TextDecoder("utf-8", { ignoreBOM: true }).decode(bytes)

describe("downloadCsv", () => {
  it("names the file after the day in the user's time zone", async () => {
    vi.useFakeTimers({ now: new Date("2026-10-07T22:30:00Z") }) // already October 8 in Berlin
    const { name } = await download([["name"]], { separator: "comma", bom: false })
    expect(name).toBe("noryx-servers-2026-10-08.csv")
  })

  it("writes rows as lines ending in \\n, missing values empty", async () => {
    const { bytes } = await download(
      [
        ["name", "port", "tps"],
        ["lobby", 25565, 19.5],
        ["survival", undefined, undefined],
      ],
      { separator: "comma", bom: false },
    )
    expect(text(bytes)).toBe("name,port,tps\nlobby,25565,19.5\nsurvival,,\n")
  })

  it("quotes cells where Go's encoding/csv does", async () => {
    const { bytes } = await download([["a,b", 'say "hi"', "two\nlines", " space", "\\.", "a;b"]], { separator: "comma", bom: false })
    expect(text(bytes)).toBe('"a,b","say ""hi""","two\nlines"," space","\\.",a;b\n')
  })

  it("separates with semicolons and starts with a byte order mark for Excel", async () => {
    const { bytes } = await download([["a,b", "a;b", "ü"]], { separator: "semicolon", bom: true })
    expect([...bytes.slice(0, 3)]).toEqual([0xef, 0xbb, 0xbf])
    expect(text(bytes)).toBe('\uFEFFa,b;"a;b";ü\n')
  })

  it("keeps spreadsheets from running cells as formulas", async () => {
    const { bytes } = await download([["=HYPERLINK(\"x\")", "+1", "-1", "@SUM(A1)", "\tx", "\rx", "a=b"]], { separator: "comma", bom: false })
    expect(text(bytes)).toBe("\"'=HYPERLINK(\"\"x\"\")\",'+1,'-1,'@SUM(A1),'\tx,\"'\rx\",a=b\n")
  })

  it("separates with commas where numbers have a decimal point", () => {
    expect(languageSeparator).toBe("comma")
  })
})
