import { t } from "i18next"
import { useSyncExternalStore } from "react"
import type { LogEntry } from "@/features/logs/api"

/**
 * Notifications of the operating system about new warnings and errors while the panel's tab is in the background.
 * Each browser opts in on its own; the browser only asks for its permission when the user turns them on.
 */
const key = "noryx-desktop-notifications"

export const desktopSupported = typeof window !== "undefined" && "Notification" in window

const listeners = new Set<() => void>()
const changed = () => listeners.forEach((listener) => listener())

function readOn() {
  try {
    return localStorage.getItem(key) === "on"
  } catch {
    return false
  }
}

function writeOn(on: boolean) {
  try {
    if (on) localStorage.setItem(key, "on")
    else localStorage.removeItem(key)
  } catch {
    // nothing to remember then
  }
  changed()
}

/** Whether this browser shows them, and whether it may: "granted", "denied" or "default" while it hasn't asked. */
function snapshot() {
  return `${readOn()}:${desktopSupported ? Notification.permission : "denied"}`
}

/** Whether desktop notifications are on in this browser, and what the browser allows. */
export function useDesktopNotifications() {
  const [on, permission] = useSyncExternalStore(
    (listener) => {
      listeners.add(listener)
      // The permission can change in the browser's settings while the panel is open.
      window.addEventListener("focus", listener)
      return () => {
        listeners.delete(listener)
        window.removeEventListener("focus", listener)
      }
    },
    snapshot,
  ).split(":")
  return {
    on: on === "true" && permission === "granted",
    permission: permission as NotificationPermission,
    /** Asks the browser for its permission, which only works on a click, and turns them on if it gives it. */
    turnOn: async () => {
      const result = await Notification.requestPermission()
      writeOn(result === "granted")
      return result === "granted"
    },
    turnOff: () => writeOn(false),
  }
}

// Entries since the tab went to the background, which one notification counts.
let unseen = 0
if (typeof document !== "undefined") {
  document.addEventListener("visibilitychange", () => {
    if (document.visibilityState === "visible") unseen = 0
  })
}

/**
 * Shows a new warning or error as a notification of the operating system, if they are on and the panel's tab is in
 * the background. Later ones replace it and count how many came, so that a burst can't flood the screen.
 */
export function notifyDesktop(entry: LogEntry, details: string, onClick: () => void) {
  if (!desktopSupported || document.visibilityState !== "hidden" || !readOn() || Notification.permission !== "granted") return
  unseen++
  try {
    const n = new Notification(unseen > 1 ? t("{{number}} new warnings and errors", { number: unseen }) : entry.message, {
      body: unseen > 1 ? entry.message : details,
      tag: "noryx-log",
      icon: "/favicon.svg",
    })
    n.onclick = () => {
      window.focus()
      onClick()
      n.close()
    }
  } catch {
    // Some browsers, e.g. on Android, only show notifications through a service worker, which the panel has none of.
  }
}
