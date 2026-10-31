// Only same-site paths are accepted as redirect targets. The key must be set explicitly,
// because the router merges the validated values over the raw search parameters.
export function validateRedirect(search: Record<string, unknown>): { redirect?: string } {
  const target = search.redirect
  const safe = typeof target === "string" && target.startsWith("/") && !target.startsWith("//")
  return { redirect: safe ? target : undefined }
}
