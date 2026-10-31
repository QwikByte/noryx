/** Whether texts match a search: each of its words is in one of them, regardless of case. */
export function matches(q: string | undefined, ...texts: (string | number | undefined)[]) {
  const text = texts.join(" ").toLowerCase()
  return (q?.toLowerCase().split(/\s+/) ?? []).every((word) => text.includes(word))
}

/**
 * Reads the parameters of a search from the address: texts that aren't empty, and values that must be one of those
 * allowed. Anything else reads as undefined. Each key must be set explicitly, see the login route.
 */
export function searchParams(search: Record<string, unknown>) {
  return {
    text: (key: string) => {
      const value = search[key]
      return typeof value === "string" && value ? value : undefined
    },
    pick: <T extends string>(key: string, allowed: readonly T[]) => allowed.find((v) => v === search[key]),
  }
}

/** Reads the search of a list from the address, so that it can be shared and bookmarked. */
export const validateQuery = (search: Record<string, unknown>): { q?: string } => ({ q: searchParams(search).text("q") })
