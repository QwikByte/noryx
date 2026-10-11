// The node each user created a server on last is a convenience of this browser; without storage,
// new servers start on the first online node.
const storageKey = (user: number) => `noryx.last-node.${user}`

/** The node the user created a server on last, as far as this browser remembers. */
export function lastNode(user: number): string | undefined {
  try {
    return localStorage.getItem(storageKey(user)) ?? undefined
  } catch {
    return undefined
  }
}

/** Remembers the node the user creates a server on, for the next one. */
export function rememberNode(user: number, nodeId: string) {
  try {
    localStorage.setItem(storageKey(user), nodeId)
  } catch {
    // nothing to remember then
  }
}
