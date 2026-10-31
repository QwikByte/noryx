import { describe, expect, it } from "vitest"
import { sftpCommand } from "./sftp"

describe("sftpCommand", () => {
  it("opens the folder of the server", () => {
    expect(sftpCommand("alice", "panel.example.com", { port: 2022, path: "/node-1/Lobby" })).toBe(
      "sftp -P 2022 alice@panel.example.com:/node-1/Lobby/",
    )
  })

  it("quotes names that a shell would split or expand", () => {
    expect(sftpCommand("alice", "[2001:db8::1]", { port: 2022, path: "/Bob's node/Lobby 1" })).toBe(
      `sftp -P 2022 'alice@[2001:db8::1]:/Bob'\\''s node/Lobby 1/'`,
    )
  })
})
