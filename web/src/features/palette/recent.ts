import { useQuery } from "@tanstack/react-query"
import { useLocation } from "@tanstack/react-router"
import { useEffect } from "react"
import { meQuery } from "@/features/auth/api"

// What each user opened last is a convenience of this browser; without storage, the palette
// offers nothing recent. Only addresses are kept, and the palette shows those of them that lead
// to something the user may still see.
const limit = 10
const storageKey = (user: number) => `noryx-recent:${user}`

/** The part of an address that names what it shows, e.g. a server for any of its tabs; lists and new ones have none. */
export const openedPath = (pathname: string) =>
  /^\/(?:nodes\/[^/]+(?:\/servers\/[^/]+)?|(?:networks|templates|filesets|backups|policies)\/(?!new(?:\/|$))[^/]+)/.exec(pathname)?.[0]

/** The addresses the user opened last, the latest first. */
export function readRecent(user: number): string[] {
  try {
    const list: unknown = JSON.parse(localStorage.getItem(storageKey(user)) ?? "[]")
    return Array.isArray(list) ? list.filter((path): path is string => typeof path === "string") : []
  } catch {
    return []
  }
}

/** Remembers what the user opens, wherever they open it from, for the palette. */
export function useRememberOpened() {
  const pathname = useLocation({ select: (l) => l.pathname })
  const { data: me } = useQuery(meQuery)
  useEffect(() => {
    const path = openedPath(pathname)
    if (!me || !path) return
    try {
      localStorage.setItem(storageKey(me.id), JSON.stringify([path, ...readRecent(me.id).filter((p) => p !== path)].slice(0, limit)))
    } catch {
      // nothing to remember then
    }
  }, [me, pathname])
}
