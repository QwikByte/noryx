import { useSyncExternalStore } from "react"

export type Theme = "light" | "dark" | "system"

/** The colours of actions, links and focus, which index.css defines and checks for contrast. */
export const accents = ["emerald", "blue", "violet", "graphite"] as const
export type Accent = (typeof accents)[number]

/** How much space the panel leaves; compact fits more on the screen. */
export type Density = "comfortable" | "compact"

/** Whether pages keep a width that reads well or fill wide screens. */
export type Width = "limited" | "full"

/** Whether the panel moves less only when the operating system asks for it, or always. */
export type Motion = "system" | "less"

/** How the panel looks in this browser. */
export interface Look {
  theme: Theme
  accent: Accent
  density: Density
  width: Width
  motion: Motion
}

// Where the browser keeps each part, which public/theme.js applies on load; none means the first value.
const parts = {
  theme: { key: "theme", values: ["system", "light", "dark"] },
  accent: { key: "noryx-accent", values: accents },
  density: { key: "noryx-density", values: ["comfortable", "compact"] },
  width: { key: "noryx-width", values: ["limited", "full"] },
  motion: { key: "noryx-motion", values: ["system", "less"] },
} as const

function read<K extends keyof Look>(part: K): Look[K] {
  const { key, values } = parts[part]
  try {
    const value = localStorage.getItem(key)
    return (values.find((v) => v === value) ?? values[0]) as Look[K]
  } catch {
    return values[0] as Look[K] // e.g. with site data blocked
  }
}

const media = matchMedia("(prefers-color-scheme: dark)")
const listeners = new Set<() => void>()
let current: Look = { theme: read("theme"), accent: read("accent"), density: read("density"), width: read("width"), motion: read("motion") }

// public/theme.js applies the look on load, before the first paint; this keeps it in sync afterwards.
function apply() {
  const root = document.documentElement
  root.classList.toggle("dark", current.theme === "dark" || (current.theme === "system" && media.matches))
  root.dataset.accent = current.accent
  root.dataset.density = current.density
  root.dataset.width = current.width
  root.dataset.motion = current.motion
}
apply()
media.addEventListener("change", () => {
  apply()
  for (const listener of listeners) listener()
})

/** Changes parts of the look and keeps them in this browser; those missing or null stay. */
export function setLook(change: { [K in keyof Look]?: Look[K] | null }) {
  const next: Look = {
    theme: change.theme ?? current.theme,
    accent: change.accent ?? current.accent,
    density: change.density ?? current.density,
    width: change.width ?? current.width,
    motion: change.motion ?? current.motion,
  }
  if (JSON.stringify(next) === JSON.stringify(current)) return
  current = next
  for (const part of Object.keys(parts) as (keyof Look)[]) {
    try {
      // Also the first value, so that the panel's defaults don't replace it (setLookDefaults).
      localStorage.setItem(parts[part].key, current[part])
    } catch {
      // The choice then only lasts until the page is reloaded.
    }
  }
  apply()
  for (const listener of listeners) listener()
}

/** Applies the panel's defaults to the parts of the look that this browser hasn't kept yet, e.g. before anyone signed in here. */
export function setLookDefaults(defaults: Partial<Look>) {
  const unkept = <K extends keyof Look>(part: K) => {
    try {
      return localStorage.getItem(parts[part].key) === null ? defaults[part] : undefined
    } catch {
      return undefined // without storage, the look stays as it is
    }
  }
  setLook({ theme: unkept("theme"), accent: unkept("accent"), density: unkept("density") })
}

export const setTheme = (theme: Theme) => setLook({ theme })

/** How the panel looks; the theme "system" follows the operating system. */
export function useLook(): Look {
  return useSyncExternalStore(
    (listener) => {
      listeners.add(listener)
      return () => listeners.delete(listener)
    },
    () => current,
  )
}

/** The colour theme chosen by the user; "system" follows the operating system. */
export const useTheme = () => useLook().theme

/** Whether the panel shows the dark theme now, also where it follows the operating system. */
export function useDark() {
  return useSyncExternalStore(
    (listener) => {
      listeners.add(listener)
      return () => listeners.delete(listener)
    },
    () => document.documentElement.classList.contains("dark"),
  )
}
