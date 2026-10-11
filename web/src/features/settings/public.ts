import { queryOptions, useQuery } from "@tanstack/react-query"
import { t } from "i18next"
import { useEffect } from "react"
import { api } from "@/lib/api"
import { type Clock, languageName, setDefaultLanguage } from "@/lib/i18n"
import { type Accent, type Density, setLookDefaults, type Theme } from "@/lib/theme"

/**
 * The language and look of the panel for users who haven't chosen them, instead of what their browser has; ""
 * follows the browser. The keys and values are those of the users' own settings.
 */
export interface UserDefaults {
  language: string
  theme: Theme | ""
  accent: Accent | ""
  density: Density | ""
  clock: Clock | ""
}

/** What the panel shows before anyone signs in, which the settings say is public. */
export interface PublicSettings {
  /** The name of the panel: Noryx unless the settings give another one. */
  name: string
  /** Plain text for the sign-in page, e.g. whom to ask for access; empty for none. */
  notice: string
  defaults: UserDefaults
}

export const publicSettingsQuery = queryOptions({
  queryKey: ["settings", "public"],
  queryFn: () => api<PublicSettings>("/panel"),
})

/** The name of the panel, Noryx until it is loaded. */
export const usePanelName = () => useQuery(publicSettingsQuery).data?.name ?? "Noryx"

/** The defaults that the settings give, without those that follow the browser. */
export type Defaults = { [K in keyof UserDefaults]?: Exclude<UserDefaults[K], ""> }

export const defaultsQuery = queryOptions({
  ...publicSettingsQuery,
  select: (p: PublicSettings) => Object.fromEntries(Object.entries(p.defaults).filter(([, value]) => value)) as Defaults,
})

/** Applies the defaults where this browser has no language and look of its own, e.g. before anyone signed in here. */
export function useApplyDefaults() {
  const { data: defaults } = useQuery(defaultsQuery)
  useEffect(() => {
    if (!defaults) return
    setDefaultLanguage(defaults.language ?? "")
    setLookDefaults(defaults)
  }, [defaults])
}

/** What choosing no language means: the panel's default where the settings give one, else the browser's languages. */
export function useNoLanguage() {
  const language = useQuery(defaultsQuery).data?.language
  return language ? t("Default of the panel ({{language}})", { language: languageName(language) }) : t("Browser language")
}
