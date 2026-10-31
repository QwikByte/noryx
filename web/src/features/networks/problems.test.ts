import { describe, expect, it } from "vitest"
import type { Backend, Network } from "./api"
import type { Draft } from "./draft"
import { bedrockPortError, exposed, hostError, isValid, nameError } from "./problems"

const backend = (name: string, nodeId = "n1"): Backend => ({ nodeId, serverId: name, name, restricted: false, motd: "" })

const network: Network = {
  id: "nw1",
  name: "Survival",
  proxy: { nodeId: "n1", serverId: "proxy" },
  proxyType: "velocity",
  createdAt: "2026-10-01T00:00:00Z",
  forwarding: "modern",
  firewalled: false,
  backends: [backend("lobby")],
  try: ["lobby"],
  forcedHosts: [],
  bedrockPort: 0,
}

const draft: Draft = {
  name: "Survival",
  forwarding: "modern",
  firewalled: false,
  backends: [backend("lobby"), backend("survival", "n2")],
  try: ["n1/lobby"],
  forcedHosts: [{ host: "survival.example.com", servers: ["n2/survival"] }],
  bedrockPort: 19132,
}

const nowhere = () => false

describe("nameError", () => {
  it.each(["lobby", "survival-2", "a_b", "0", "a".repeat(32)])("accepts %j", (name) => {
    expect(nameError([backend(name)], 0)).toBeUndefined()
  })

  it.each(["", "Lobby", "-lobby", "lobby 2", "lobby.2", "a".repeat(33)])("rejects %j", (name) => {
    expect(nameError([backend(name)], 0)).toBe("Use up to 32 lower-case letters, digits, - and _.")
  })

  it("reserves try for Velocity", () => {
    expect(nameError([backend("try")], 0)).toBe("Velocity reserves try.")
  })

  it("points out the second of two servers with the same name", () => {
    const backends = [backend("lobby"), backend("lobby", "n2")]
    expect(nameError(backends, 0)).toBeUndefined()
    expect(nameError(backends, 1)).toBe("Another server has this name.")
  })
})

describe("hostError", () => {
  it.each(["example.com", "survival.example.com", "localhost", "a-b.example"])("accepts %j", (host) => {
    expect(hostError([{ host, servers: ["n1/lobby"] }], 0)).toBeUndefined()
  })

  it.each(["", "Example.com", "example.com:25565", "-a.example", "a-.example", "a..example", "*.example.com", `${"a".repeat(63)}.`.repeat(4) + "a"])(
    "rejects %j",
    (host) => {
      expect(hostError([{ host, servers: ["n1/lobby"] }], 0)).toBe("Enter a host name such as survival.example.com, without a port.")
    },
  )

  it("points out the same host name twice, and one without servers", () => {
    const hosts = [
      { host: "example.com", servers: ["n1/lobby"] },
      { host: "example.com", servers: ["n1/lobby"] },
      { host: "survival.example.com", servers: [] },
    ]
    expect(hostError(hosts, 0)).toBeUndefined()
    expect(hostError(hosts, 1)).toBe("This host name is there already.")
    expect(hostError(hosts, 2)).toBe("Choose a server for this host name.")
  })
})

describe("bedrockPortError", () => {
  it.each([0, 1024, 19132, 65535])("accepts %d", (port) => {
    expect(bedrockPortError(port)).toBeUndefined()
  })

  it.each([1, 1023, 65536, 19132.5, Number.NaN])("rejects %d", (port) => {
    expect(bedrockPortError(port)).toBe("Choose a port from 1024 to 65535.")
  })
})

describe("isValid", () => {
  it("accepts a draft without problems", () => {
    expect(isValid(network, draft, nowhere)).toBe(true)
  })

  it.each<[string, Partial<Draft>]>([
    ["without a name", { name: " " }],
    ["without servers", { backends: [] }],
    ["without a server to join", { try: [] }],
    ["with a wrong name of a server", { backends: [backend("Lobby")] }],
    ["with a long MOTD", { backends: [{ ...backend("lobby"), motd: "x".repeat(257) }] }],
    ["with a wrong host name", { forcedHosts: [{ host: "example.com:25565", servers: ["n1/lobby"] }] }],
    ["with a wrong Bedrock port", { bedrockPort: 80 }],
  ])("rejects a draft %s", (_, change) => {
    expect(isValid(network, { ...draft, ...change }, nowhere)).toBe(false)
  })

  it("requires a firewall for legacy forwarding to servers on other nodes outside the private network", () => {
    const legacy = { ...draft, forwarding: "legacy" as const }
    expect(exposed(network, legacy, nowhere)).toBe(true)
    expect(isValid(network, legacy, nowhere)).toBe(false)
    expect(isValid(network, { ...legacy, firewalled: true }, nowhere)).toBe(true)
    expect(exposed(network, legacy, () => true)).toBe(false)
    expect(exposed(network, { ...legacy, backends: [backend("lobby")] }, nowhere)).toBe(false)
    expect(exposed(network, draft, nowhere)).toBe(false)
  })
})
