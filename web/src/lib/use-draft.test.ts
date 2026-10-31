import { describe, expect, it } from "vitest"
import { ApiError } from "./api"
import { fieldError, formError, same } from "./use-draft"

describe("same", () => {
  it("compares objects and arrays by their contents", () => {
    expect(same({ name: "Lobby", ports: [25565], world: { seed: "1" } }, { name: "Lobby", ports: [25565], world: { seed: "1" } })).toBe(true)
    expect(same({ name: "Lobby" }, { name: "Hub" })).toBe(false)
    expect(same({ ports: [25565] }, { ports: [25565, 25566] })).toBe(false)
    expect(same({ world: { seed: "1" } }, { world: { seed: "2" } })).toBe(false)
    expect(same([], {})).toBe(false)
    expect(same(Number.NaN, Number.NaN)).toBe(true)
  })

  it("leaves out fields without a value, as JSON does", () => {
    expect(same({ name: "Lobby", port: undefined }, { name: "Lobby" })).toBe(true)
    expect(same({ name: "Lobby" }, { name: "Lobby", port: 25565 })).toBe(false)
    expect(same({ port: undefined }, { other: undefined })).toBe(true)
  })

  it("compares other objects, e.g. files, by identity", () => {
    const archive = new Blob(["a"])
    expect(same({ archive }, { archive })).toBe(true)
    expect(same({ archive }, { archive: new Blob(["a"]) })).toBe(false)
    expect(same(new Date(0), new Date(0))).toBe(false)
  })
})

describe("fieldError", () => {
  const error = new ApiError(400, "Port must be between 1024 and 65535.", undefined, "port")

  it("tells the error at the field it names", () => {
    expect(fieldError(error, "port")).toEqual([{ message: "Port must be between 1024 and 65535." }])
    expect(fieldError(new ApiError(400, "Too much.", undefined, "newServers.memoryMb"), "newServers.memoryMb")).toEqual([{ message: "Too much." }])
  })

  it("tells nothing at other fields, and for errors without a field", () => {
    expect(fieldError(error, "name")).toBeUndefined()
    expect(fieldError(new ApiError(400, "Invalid."), "port")).toBeUndefined()
    expect(fieldError(new Error("The connection to the panel was lost."), "port")).toBeUndefined()
    expect(fieldError(null, "port")).toBeUndefined()
  })
})

describe("formError", () => {
  it("leaves errors to the fields of the form that they name", () => {
    expect(formError(new ApiError(400, "Invalid port.", undefined, "port"), ["name", "port"])).toBeUndefined()
  })

  it("tells the message of errors about no field or another one", () => {
    expect(formError(new ApiError(400, "Invalid port.", undefined, "port"), ["name"])).toBe("Invalid port.")
    expect(formError(new ApiError(409, "Port 25565 is already used."), ["port"])).toBe("Port 25565 is already used.")
    expect(formError(new Error("The connection to the panel was lost."), ["port"])).toBe("The connection to the panel was lost.")
    expect(formError(null, ["port"])).toBeUndefined()
  })
})
