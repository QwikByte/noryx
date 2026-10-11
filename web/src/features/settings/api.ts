import { queryOptions, useMutation, useQueryClient } from "@tanstack/react-query"
import { t } from "i18next"
import type { NodeLimits } from "@/features/nodes/api"
import type { NewServerSettings, WarningSettings } from "@/features/servers/api"
import type { ThresholdDefaults } from "@/features/usage/api"
import { api } from "@/lib/api"
import { publicSettingsQuery, type UserDefaults } from "./public"

/** The certificate the panel serves: self-signed, of Let's Encrypt, or none for plain HTTP, e.g. behind a reverse proxy. */
export type PanelHTTPS = "" | "self-signed" | "letsencrypt"

/** Settings of the master; they apply right away, the panel's address and HTTPS when the master starts again. */
export interface MasterSettings {
  /** IP:port the panel listens at from the next start; empty means the address from the command line. */
  panelAddr: string
  panelHttps: PanelHTTPS
  /** Domain name that Let's Encrypt certifies and the self-signed certificate includes. */
  panelDomain: string
  /** host:port join tokens tell agents to enroll at; empty means the address from the command line. */
  enrollAddr: string
  sessionHours: number
  joinTokenMinutes: number
  /** Limits new nodes get. */
  nodeDefaults: NodeLimits
  /** How long log entries are kept. */
  logDays: number
  /** How many MiB the log may take; the oldest entries beyond it are deleted before their time. */
  logSizeMb: number
  /** Whether the master looks for new releases, which administrators can install. */
  checkUpdates: boolean
  /** Who has to use two-factor authentication: all users, or the members of the groups with these IDs. */
  requireMfa: { all: boolean; groups: string[] }
  /** When the usage of servers and nodes warns, unless they have their own thresholds. */
  thresholds: ThresholdDefaults

  /** The name of the panel, empty for Noryx, and the notice of the sign-in page; both are plain text and public. */
  panelName: string
  signInNotice: string
  /** The language and look of users who haven't chosen them, which are public too. */
  userDefaults: UserDefaults
  /** The IDs of the groups that inviting a user preselects; deleted ones are left out. */
  inviteGroups: string[]
  /** What the panel creates servers with, unless a template is chosen. */
  newServers: NewServerSettings
  /** How the players are warned before restarts and stops. */
  warnings: WarningSettings
}

/** The certificate the panel serves. */
export interface PanelCertificate {
  selfSigned: boolean
  /** Domain names and IP addresses it is valid for. */
  names: string[]
  expiresAt: string
  /** SHA-256, to compare with the one browsers show. */
  fingerprint: string
  /** Why Let's Encrypt issued none, so that the self-signed one is served. */
  error?: string
}

/** The running master; apart from its certificates, it only changes with a restart. */
export interface Master {
  version: string
  startedAt: string
  /** Where the panel listens. */
  panelAddr: string
  /** The certificate the panel serves, "files" for the one of the command line. */
  panelHttps: PanelHTTPS | "files"
  panelDomain: string
  panelCertificate?: PanelCertificate
  /** The panel's address from the command line, used while the settings name none. */
  panelDefaultAddr: string
  /** Why the panel didn't listen at the address from the settings when the master started. */
  panelAddrError?: string
  enrollListenAddr: string
  /** The enrollment address from the command line. */
  enrollAddr: string
  caFingerprint: string
  certificateExpiresAt: string
  /** Whether administrators can restart the master from the panel. */
  restartable: boolean
}

export interface SettingsView {
  settings: MasterSettings
  master: Master
}

export const settingsQuery = queryOptions({
  queryKey: ["settings"],
  queryFn: () => api<SettingsView>("/settings"),
})

export function useUpdateSettings() {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: (settings: MasterSettings) => api<SettingsView>("/settings", { method: "PUT", body: settings }),
    onSuccess: (view) => {
      queryClient.setQueryData(settingsQuery.queryKey, view)
      void queryClient.invalidateQueries({ queryKey: publicSettingsQuery.queryKey })
    },
  })
}

/** Where the panel can be reached: its address, its certificate and the domain of it. */
export interface PanelTarget {
  addr: string
  https: Master["panelHttps"]
  domain: string
}

/** The panel as it runs now. */
export const currentPanel = (master: Master): PanelTarget => ({
  addr: master.panelAddr,
  https: master.panelHttps,
  domain: master.panelDomain,
})

/** The panel after the next start of the master; a certificate of the command line takes precedence over the settings. */
export const nextPanel = (settings: MasterSettings, master: Master): PanelTarget => {
  const addr = settings.panelAddr || master.panelDefaultAddr
  if (master.panelHttps === "files") return { addr, https: "files", domain: "" }
  return { addr, https: settings.panelHttps, domain: settings.panelHttps ? settings.panelDomain : "" }
}

export const samePanel = (a: PanelTarget, b: PanelTarget) => a.addr === b.addr && a.https === b.https && a.domain === b.domain

export const panelURL = ({ addr, https }: PanelTarget) => `${https ? "https" : "http"}://${addr}`

/** Key of the restart, so that every restart button tells while one runs. */
export const restartKey = ["restart-master"]

/**
 * Restarts the master and waits until it answers again, which takes a few seconds. next is
 * where the panel is then: if it changes between HTTP and HTTPS, the browser goes there, as
 * it can't ask the new address before; otherwise it is named in the error if the master doesn't come back here.
 */
export function useRestartMaster() {
  const queryClient = useQueryClient()
  return useMutation({
    mutationKey: restartKey,
    mutationFn: async ({ master, next }: { master: Master; next: PanelTarget }) => {
      await api("/master/restart", { method: "POST" })
      if (!!next.https !== (location.protocol === "https:")) {
        const url = new URL(location.href)
        url.protocol = next.https ? "https:" : "http:"
        // Let's Encrypt only certifies the domain.
        if (next.https === "letsencrypt") url.hostname = next.domain
        url.port = next.addr.slice(next.addr.lastIndexOf(":") + 1)
        // systemd starts the master again after 5 seconds.
        await new Promise((resolve) => setTimeout(resolve, 8_000))
        location.assign(url)
        return new Promise<never>(() => {})
      }
      for (let attempt = 0; attempt < 30; attempt++) {
        await new Promise((resolve) => setTimeout(resolve, 2_000))
        const view = await api<SettingsView>("/settings").catch(() => undefined)
        if (view && view.master.startedAt !== master.startedAt) return view
      }
      throw new Error(
        samePanel(next, currentPanel(master))
          ? t("The master hasn't answered for a minute. See: journalctl -u noryx-master")
          : t(
              "The master hasn't answered here for a minute. It listens at {{address}} now: open the panel there, or point your reverse proxy to it.",
              {
                address: panelURL(next),
              },
            ),
      )
    },
    onSuccess: async (view) => {
      queryClient.setQueryData(settingsQuery.queryKey, view)
      await queryClient.invalidateQueries()
    },
  })
}
