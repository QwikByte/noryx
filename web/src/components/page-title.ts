import { useMatch, useMatches } from "@tanstack/react-router"
import { t } from "i18next"
import { useEffect, useSyncExternalStore } from "react"
import { usePanelName } from "@/features/settings/public"

declare module "@tanstack/react-router" {
  interface StaticDataRouteOption {
    /** What the route shows, untranslated (msg), e.g. "Servers"; the browser's tab is titled after it. */
    title?: string
  }
}

// The names pages give themselves, e.g. that of their server, by the match of their route.
const names = new Map<string, string>()
const listeners = new Set<() => void>()
let version = 0

function changed() {
  version++
  for (const listener of listeners) listener()
}

function subscribe(listener: () => void) {
  listeners.add(listener)
  return () => void listeners.delete(listener)
}

/** Puts what a page shows, e.g. the name of its server, into the title of the browser's tab, before the title of its route. */
export function usePageName(name: string | undefined) {
  const id = useMatch({ strict: false, select: (match) => match.id })
  useEffect(() => {
    if (!name) return
    names.set(id, name)
    changed()
    return () => {
      names.delete(id)
      changed()
    }
  }, [id, name])
}

/**
 * The titles and names of the open page's routes from the outside in, each with the path that opens it, e.g.
 * Servers, lobby and Files; a title that repeats the one before it is left out.
 */
export function usePageTrail() {
  useSyncExternalStore(subscribe, () => version)
  const matches = useMatches()
  const trail: { label: string; to: string }[] = []
  for (const match of matches) {
    for (const label of [match.staticData.title && t(match.staticData.title), names.get(match.id)]) {
      if (label && trail.at(-1)?.label !== label) trail.push({ label, to: match.pathname })
    }
  }
  return trail
}

/** Titles the browser's tab after the open page, from the inside out, and the panel, e.g. "Files · lobby · Servers · Noryx". */
export function DocumentTitle() {
  useSyncExternalStore(subscribe, () => version)
  const matches = useMatches()
  const panel = usePanelName()
  const title = [
    ...matches.flatMap((match) => [match.staticData.title && t(match.staticData.title), names.get(match.id)]).reverse(),
    panel,
  ]
    .filter(Boolean)
    .join(" · ")
  useEffect(() => {
    document.title = title
  }, [title])
  return null
}
