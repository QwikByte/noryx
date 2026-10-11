/** Whether texts match a search: each of its words is in one of them, regardless of case. */
export function matches(q: string | undefined, ...texts: (string | number | undefined)[]) {
  const text = texts.join(" ").toLowerCase()
  return (q?.toLowerCase().split(/\s+/) ?? []).every((word) => text.includes(word))
}

/** Reads the search of a list from the address, so that it can be shared and bookmarked. The key must be set explicitly, see the login route. */
export const validateQuery = (search: Record<string, unknown>): { q?: string } => ({
  q: typeof search.q === "string" && search.q ? search.q : undefined,
})
