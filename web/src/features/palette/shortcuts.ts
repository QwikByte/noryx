import { CubeIcon, GraphIcon, HardDrivesIcon, UserIcon } from "@phosphor-icons/react"
import { useNavigate } from "@tanstack/react-router"
import { useEffect, useEffectEvent, useRef } from "react"
import { pages } from "@/components/navigation"
import { type Access, useAccess } from "@/features/access/use-access"
import { useSettings } from "@/features/preferences/api"
import { msg } from "@/lib/i18n"

export type Creation = "server" | "network" | "node"

/** What a shortcut opens besides a page: the search, the list of shortcuts or a dialog that creates something. */
export type Opens = "search" | "shortcuts" | Creation

export const mac = /mac|iphone|ipad/i.test(navigator.userAgent)

/** Opens the search from anywhere, also while typing; its keys are pressed together. */
export const searchChord = mac ? "⌘K" : "Ctrl K"

export const account = { to: "/account", label: msg("Your account"), icon: UserIcon } as const

/** What the palette creates, and c followed by the key, for those who may. */
export const creations = [
  { opens: "server", key: "s", label: msg("Create server"), icon: CubeIcon, allowed: (a: Access) => a.canSomewhere("servers.create") },
  { opens: "network", key: "n", label: msg("Create network"), icon: GraphIcon, allowed: (a: Access) => a.can("networks.manage") },
  { opens: "node", key: "m", label: msg("Add node"), icon: HardDrivesIcon, allowed: (a: Access) => a.can("nodes.enroll") },
] as const

/** The key that follows g to open a page, e.g. g s for the servers; m is for machines, as n is the networks'. */
export const pageKeys: Partial<Record<string, string>> = {
  "/": "o",
  "/servers": "s",
  "/networks": "n",
  "/players": "p",
  "/nodes": "m",
  "/templates": "t",
  "/filesets": "f",
  "/plugins": "e",
  "/backups": "b",
  "/policies": "c",
  "/logs": "l",
  "/settings": ",",
  "/account": "a",
}

interface Shortcut {
  /** Typed one after the other. */
  keys: string[]
  label: string
  to?: ReturnType<typeof pages>[number]["to"] | typeof account.to
  opens?: Opens
}

const search: Shortcut = { keys: [searchChord], label: msg("Search, also while typing"), opens: "search" }

/** Whether the user takes shortcuts of single keys, e.g. g s; WCAG 2.1.4 asks that they can be turned off. */
export const useSingleKeys = () => useSettings().settings.shortcuts !== "off"

/** The shortcuts the user may use, in the groups that list them; without those of single keys, only the search's. */
export function shortcutGroups(access: Access, singleKeys = true) {
  if (!singleKeys) return [{ heading: msg("General"), shortcuts: [search] }]
  const groups: { heading: string; shortcuts: Shortcut[] }[] = [
    {
      heading: msg("General"),
      shortcuts: [
        search,
        { keys: ["/"], label: msg("Search"), opens: "search" },
        { keys: ["?"], label: msg("Show the keyboard shortcuts"), opens: "shortcuts" },
      ],
    },
    {
      heading: msg("Create"),
      shortcuts: creations.filter((c) => c.allowed(access)).map((c) => ({ keys: ["c", c.key], label: c.label, opens: c.opens })),
    },
    {
      heading: msg("Go to"),
      shortcuts: [...pages(access), account].flatMap(({ to, label }) => {
        const key = pageKeys[to]
        return key ? [{ keys: ["g", key], label, to }] : []
      }),
    },
  ]
  return groups.filter((group) => group.shortcuts.length > 0)
}

/** How long the second key of a sequence may wait, in milliseconds. */
const sequenceTime = 1500

/** Whether typed keys belong to something else: a field, the editor, a menu, or a dialog that is open. */
function elsewhere(event: KeyboardEvent) {
  const target = event.target instanceof Element ? event.target : undefined
  return (
    (target instanceof HTMLElement && target.isContentEditable) ||
    !!target?.closest('input, textarea, select, .cm-editor, [role="combobox"], [role="menu"], [role="listbox"]') ||
    !!document.querySelector('[role="dialog"], [role="alertdialog"]')
  )
}

/** Follows the keyboard shortcuts, those of single keys if the user takes them: pages open here, open is told about the rest. */
export function useShortcuts(open: (what: Opens) => void) {
  const access = useAccess()
  const singleKeys = useSingleKeys()
  const navigate = useNavigate()
  const pending = useRef({ key: "", at: 0 })

  const onKey = useEffectEvent((event: KeyboardEvent) => {
    const key = event.key.toLowerCase()
    if ((event.ctrlKey || event.metaKey) && !event.altKey && !event.shiftKey && key === "k") {
      event.preventDefault()
      return open("search")
    }
    if (!singleKeys) return
    // Modifiers alone, e.g. Shift for ?, don't break a sequence.
    if (event.key.length !== 1) return
    const first = event.timeStamp - pending.current.at < sequenceTime ? pending.current.key : ""
    pending.current = { key: "", at: 0 }
    if (event.ctrlKey || event.metaKey || event.altKey || event.repeat || event.isComposing || event.defaultPrevented || elsewhere(event)) return

    const shortcuts = shortcutGroups(access).flatMap((group) => group.shortcuts)
    const find = (typed: string) => shortcuts.find((s) => s.keys.join(" ") === typed)
    const shortcut = (first && find(`${first} ${key}`)) || find(key)
    if (shortcut) {
      event.preventDefault()
      if (shortcut.to) void navigate({ to: shortcut.to })
      else if (shortcut.opens) open(shortcut.opens)
    } else if (shortcuts.some((s) => s.keys.length > 1 && s.keys[0] === key)) {
      pending.current = { key, at: event.timeStamp }
    }
  })

  useEffect(() => {
    const listener = (event: KeyboardEvent) => onKey(event)
    document.addEventListener("keydown", listener)
    return () => document.removeEventListener("keydown", listener)
  }, [])
}
