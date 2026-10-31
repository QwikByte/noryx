import { describe, expect, it } from "vitest"
import {
  dayOf,
  formatAgo,
  formatAgoTitle,
  formatBytes,
  formatDate,
  formatDuration,
  formatElapsed,
  formatHours,
  formatMegabytes,
  formatMinutes,
  formatSeconds,
  formatTimeZone,
  fromWallClock,
  toWallClock,
  zoneOffset,
} from "./format"

// The tests run in American English and in the time zone of Berlin, which is 2 hours ahead of UTC in summer and 1
// in winter; ICU separates times with narrow spaces, which these compare as spaces.
const spaced = (text: string) => text.replace(/\s/g, " ")

describe("sizes", () => {
  it.each([
    [0, "0 B"],
    [512, "512 B"],
    [1536, "1.5 KiB"],
    [31.3 * 1024 ** 3, "31.3 GiB"],
    [1024 ** 5, "1,024 TiB"],
  ])("formats %d bytes as %j", (bytes, text) => {
    expect(formatBytes(bytes)).toBe(text)
  })

  it("formats mebibytes", () => {
    expect(formatMegabytes(2048)).toBe("2 GiB")
    expect(formatMegabytes(512)).toBe("512 MiB")
  })
})

describe("durations", () => {
  it.each([
    [0, "0:00"],
    [59_999, "0:59"],
    [65_000, "1:05"],
    [3_723_000, "1:02:03"],
    [-5_000, "0:00"],
  ])("formats %d ms as elapsed %j", (ms, text) => {
    expect(formatElapsed(ms)).toBe(text)
  })

  it.each([
    [30_000, "0 minutes"],
    [5 * 60_000, "5 minutes"],
    [90 * 60_000, "1 hour"],
    [3 * 86_400_000 + 3_600_000, "3 days"],
  ])("formats %d ms in its largest unit as %j", (ms, text) => {
    expect(formatDuration(ms)).toBe(text)
  })

  it("formats seconds, hours and minutes in whole larger units where they are", () => {
    expect(formatSeconds(30)).toBe("30 seconds")
    expect(formatSeconds(90)).toBe("90 seconds")
    expect(formatSeconds(120)).toBe("2 minutes")
    expect(formatHours(24)).toBe("1 day")
    expect(formatHours(36)).toBe("36 hours")
    expect(formatMinutes(45)).toBe("45 min")
    expect(formatMinutes(120)).toBe("2 hr")
    expect(spaced(formatMinutes(725))).toBe("12 hr 5 min")
  })
})

describe("times", () => {
  it("formats dates and days in the user's time zone", () => {
    expect(formatDate("2026-10-07T22:30:00Z")).toBe("Oct 8, 2026")
    expect(dayOf("2026-10-07T21:59:00Z")).toBe("2026-10-07")
    expect(dayOf("2026-10-07T22:00:00Z")).toBe("2026-10-08")
    expect(dayOf("2026-12-07T22:30:00Z")).toBe("2026-12-07")
  })

  it("knows how far the time zone is ahead of UTC, with daylight saving time", () => {
    expect(zoneOffset(Date.parse("2026-07-01T12:00:00.250Z"))).toBe(2 * 3_600_000)
    expect(zoneOffset(Date.parse("2026-12-01T12:00:00Z"))).toBe(3_600_000)
  })

  it.each([
    ["2026-10-08T14:30", "2026-10-08T12:30:00.000Z"],
    ["2026-12-08T14:30", "2026-12-08T13:30:00.000Z"],
    ["2026-10-25T12:00", "2026-10-25T11:00:00.000Z"], // the day daylight saving time ends
    ["2026-03-29T12:00", "2026-03-29T10:00:00.000Z"], // and the day it starts
  ])("reads %j of a datetime-local input in the user's time zone", (wall, iso) => {
    expect(fromWallClock(wall).toISOString()).toBe(iso)
    expect(toWallClock(iso)).toBe(wall)
  })

  it("tells how long ago or until a time is, with the date and time as its title", () => {
    const now = Date.parse("2026-10-31T12:00:00Z")
    expect(formatAgo("2026-10-31T11:59:30Z", now)).toBe("30 seconds ago")
    expect(formatAgo("2026-10-31T11:57:00Z", now)).toBe("3 minutes ago")
    expect(formatAgo("2026-10-31T14:00:00Z", now)).toBe("in 2 hours")
    expect(formatAgo("2026-10-30T12:00:00Z", now)).toBe("yesterday")
    expect(spaced(formatAgoTitle("2026-10-31T11:57:00Z", now))).toBe("Oct 31, 2026, 12:57 PM")
  })

  it("names time zones as people read them", () => {
    expect(formatTimeZone("America/New_York")).toBe("America/New York")
    expect(formatTimeZone("")).toBe("UTC")
  })
})
