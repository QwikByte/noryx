// Only same-site paths are accepted as redirect targets. Browsers take a backslash for a slash and leave out tabs and
// line breaks, so that /\evil.example leads elsewhere just like //evil.example. The key must be set explicitly,
// because the router merges the validated values over the raw search parameters.
export function validateRedirect(search: Record<string, unknown>): { redirect?: string } {
  const target = search.redirect
  const safe = typeof target === "string" && /^\/(?![\t\n\r]*[/\\])/.test(target)
  return { redirect: safe ? target : undefined }
}
