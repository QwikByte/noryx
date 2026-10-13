/** Whether texts match a search: each of its words is in one of them, regardless of case. */
export function matches(q: string | undefined, ...texts: (string | number | undefined)[]) {
  const text = texts.join(" ").toLowerCase()
  return (q?.toLowerCase().split(/\s+/) ?? []).every((word) => text.includes(word))
}

/**
 * Reads the values of the search of an address that are valid: text that isn't empty, or one of the values allowed,
 * e.g. of a sort. Anything else is undefined, as addresses can be edited and shared.
 */
export function params(search: Record<string, unknown>) {
  return {
    text: (key: string) => (typeof search[key] === "string" && search[key] ? search[key] : undefined),
    pick: <T extends string>(key: string, allowed: readonly T[]) => allowed.find((v) => v === search[key]),
  }
}

/** Reads the search of a list from the address, so that it can be shared and bookmarked. The key must be set explicitly, see the login route. */
export const validateQuery = (search: Record<string, unknown>): { q?: string } => ({ q: params(search).text("q") })
