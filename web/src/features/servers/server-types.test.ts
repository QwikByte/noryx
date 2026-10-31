import { describe, expect, it } from "vitest"
import { commandsOf } from "./console-completion"
import { defaults, isBedrock, serverPorts, suggestPort, usedPorts } from "./server-types"

describe("Bedrock servers", () => {
  it("use their port and one after it for each player", () => {
    expect(serverPorts({ type: "bedrock", port: 19132, maxPlayers: 3 })).toEqual([19132, 19133, 19134])
    expect(serverPorts({ type: "bedrock", port: 19132, maxPlayers: 0 })).toHaveLength(10)
    expect(serverPorts({ type: "paper", port: 25565, maxPlayers: 20 })).toEqual([25565])
  })

  it("take ports that other servers can't", () => {
    const used = usedPorts([
      { type: "bedrock", port: 19132, maxPlayers: 2 },
      { type: "velocity", port: 25577, bedrockPort: 19140 },
    ])
    expect(used).toEqual([19132, 19133, 25577, 19140])
    expect(suggestPort(used, 19132)).toBe(19134)
  })

  it("get a port with free ones after it for their players", () => {
    expect(suggestPort([19135], 19132, 1024, 65535, 5)).toBe(19136)
    expect(suggestPort([], 19132, 19130, 19135, 5)).toBe(19130)
    expect(suggestPort([], 19132, 1024, 65535, 5)).toBe(19132)
  })

  it("start on 19132 with their own console commands", () => {
    expect(isBedrock("bedrock")).toBe(true)
    expect(isBedrock("paper")).toBe(false)
    expect(defaults("bedrock").port).toBe(19132)
    expect(commandsOf("bedrock")).toHaveProperty("allowlist")
    expect(commandsOf("bedrock")).not.toHaveProperty("whitelist")
  })
})
