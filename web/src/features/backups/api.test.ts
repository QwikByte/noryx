import { describe, expect, it } from "vitest"
import { settingsOf, type Storage } from "./api"

describe("settingsOf", () => {
  // The master refuses fields it doesn't take, such as the number of copies of a listed storage.
  it("keeps only what the master takes of a listed storage", () => {
    const listed: Storage = {
      id: "s1",
      copies: 3,
      name: "Offsite",
      endpoint: "s3.example.com",
      region: "eu-central-1",
      bucket: "backups",
      prefix: "noryx",
      accessKey: "key",
      pathStyle: false,
      encrypt: true,
    }
    const { id: _, copies: __, ...settings } = listed
    expect(settingsOf(listed)).toEqual(settings)
  })
})
