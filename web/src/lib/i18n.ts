import i18next from "i18next"
import { initReactI18next } from "react-i18next"

// The panel's English texts are the keys of their translations. src/locales/en.json lists them
// (npm run i18n); a language is a copy of it with the texts translated, e.g. de.json. Texts that
// aren't translated stay English.
const files = import.meta.glob<Record<string, string>>(["../locales/*.json", "!../locales/en.json"], { import: "default" })

/** The codes of the panel's languages: English and those with a file in src/locales. */
export const languages = ["en", ...Object.keys(files).map((path) => path.slice(path.lastIndexOf("/") + 1, -".json".length))]

/** The name of a language in itself, e.g. Deutsch. */
export function languageName(code: string) {
  const name = new Intl.DisplayNames([code], { type: "language" }).of(code) ?? code
  return name[0].toUpperCase() + name.slice(1)
}

// The language chosen in this browser, so that the panel shows it before anyone signs in. The
// panel also stores the choice of the signed-in user, which then applies in every browser.
// Without a choice, the default of the panel's settings applies, which the browser keeps too.
const storageKey = "noryx-language"
const defaultKey = "noryx-default-language"

function stored(key: string) {
  try {
    return localStorage.getItem(key) ?? ""
  } catch {
    return "" // e.g. with site data blocked
  }
}

/** The language chosen in this browser, or "" to follow the panel's default, else the browser's languages. */
export const chosenLanguage = () => stored(storageKey)

/** The language of a tag without its script or region, e.g. pt for pt-BR. */
const base = (tag: string) => tag.split("-")[0].toLowerCase()

/**
 * The language the panel shows for a choice: the chosen one, else the panel's default, else the first of the
 * browser's languages the panel has, fully (pt-BR) or by its language alone (pt, then pt-PT), else English.
 */
function resolve(choice: string) {
  const chosen = [choice, stored(defaultKey)].find((l) => languages.includes(l))
  if (chosen) return chosen
  for (const tag of navigator.languages) {
    const match =
      languages.find((l) => l.toLowerCase() === tag.toLowerCase()) ??
      languages.find((l) => l === base(tag)) ??
      languages.find((l) => base(l) === base(tag))
    if (match) return match
  }
  return "en"
}

/** Chooses a language, "" for the panel's default or the browser's, and reloads the panel if it shows another one. */
export function chooseLanguage(choice: string) {
  try {
    if (choice) localStorage.setItem(storageKey, choice)
    else localStorage.removeItem(storageKey)
  } catch {
    return // the panel would show the same after reloading, again and again for a signed-in user
  }
  if (resolve(choice) !== language) location.reload()
}

/** Keeps the panel's default language, "" for none, and reloads the panel if it shows another one then. */
export function setDefaultLanguage(code: string) {
  if (code === stored(defaultKey)) return
  try {
    if (code) localStorage.setItem(defaultKey, code)
    else localStorage.removeItem(defaultKey)
  } catch {
    return
  }
  if (resolve(chosenLanguage()) !== language) location.reload()
}

// The language the panel shows, chosen before anything renders.
const language = resolve(chosenLanguage())

/** Whether times have 12 or 24 hours. */
export type Clock = "12h" | "24h"

const hourCycles = { "12h": "h12", "24h": "h23" } as const

/**
 * How the signed-in user wants times: with 12 or 24 hours, in an IANA time zone rather than the
 * browser's, as how long ago they were or as dates and times, and weeks starting on Monday or Sunday.
 */
export interface Formats {
  clock?: Clock
  timeZone?: string
  times?: "relative" | "absolute"
  weekStart?: "monday" | "sunday"
}

// The formats the signed-in user chose, which this browser keeps to show them from the start.
const formatKeys = { clock: "noryx-clock", timeZone: "noryx-time-zone", times: "noryx-times", weekStart: "noryx-week-start" } as const

/** Leaves out what the panel can't show, e.g. a time zone this browser doesn't know. */
function known(f: Partial<Record<keyof Formats, string | null>>): Formats {
  const zone = (z?: string | null) => {
    try {
      return z && Intl.DateTimeFormat("en", { timeZone: z }).resolvedOptions().timeZone ? z : undefined
    } catch {
      return undefined // a RangeError for zones the browser doesn't know
    }
  }
  const of = <T extends string>(value: string | null | undefined, values: T[]) => values.find((v) => v === value)
  return {
    clock: of(f.clock, ["12h", "24h"]),
    timeZone: zone(f.timeZone),
    times: of(f.times, ["relative", "absolute"]),
    weekStart: of(f.weekStart, ["monday", "sunday"]),
  }
}

function storedFormats() {
  try {
    return known(Object.fromEntries(Object.entries(formatKeys).map(([part, key]) => [part, localStorage.getItem(key)])))
  } catch {
    return {}
  }
}

const regional = language.includes("-") ? language : (navigator.languages.find((l) => base(l) === language && valid(l)) ?? language)
const chosen = storedFormats()

/**
 * The locale of dates and numbers: the panel's language, in the browser's region for it if there
 * is one, e.g. de-AT. A language with its own script or region, e.g. pt-BR, keeps it. A chosen
 * clock replaces the region's, e.g. en-US-u-hc-h23.
 */
export const locale = chosen.clock ? new Intl.Locale(regional, { hourCycle: hourCycles[chosen.clock] }).toString() : regional

/** The time zone of times, or undefined for the browser's. */
export const timeZone = chosen.timeZone

/** Whether times that tell how long ago something was show that, rather than the date and time. */
export const relativeTimes = chosen.times !== "absolute"

/** Formats a time with the panel's locale and a clock, e.g. to show what choosing it means. */
export const timeWith = (clock: Clock, date: Date) =>
  date.toLocaleTimeString(new Intl.Locale(locale, { hourCycle: hourCycles[clock] }).toString(), { timeStyle: "short" })

/** The clock the panel shows: the chosen one, else that of the locale. */
export const clock: Clock = ["h11", "h12"].includes(new Intl.DateTimeFormat(locale, { hour: "numeric" }).resolvedOptions().hourCycle ?? "")
  ? "12h"
  : "24h"

/** The first day of the week, 0 for Sunday to 6 for Saturday: the chosen one, else the language's, else Monday. */
export const firstDay = chosen.weekStart ? (chosen.weekStart === "sunday" ? 0 : 1) : languageFirstDay()

function languageFirstDay() {
  // getWeekInfo replaced the property weekInfo; browsers without either start on Monday.
  const l = new Intl.Locale(locale) as Intl.Locale & { getWeekInfo?: () => { firstDay: number }; weekInfo?: { firstDay: number } }
  try {
    return ((l.getWeekInfo?.() ?? l.weekInfo)?.firstDay ?? 1) % 7
  } catch {
    return 1
  }
}

/** Chooses how times show, and reloads the panel if it shows them otherwise; what a user never chose follows the browser. */
export function chooseFormats(formats: Formats) {
  const next = known(formats)
  try {
    for (const [part, key] of Object.entries(formatKeys)) {
      const value = next[part as keyof Formats]
      if (value) localStorage.setItem(key, value)
      else localStorage.removeItem(key)
    }
  } catch {
    return // the panel would show the same after reloading
  }
  if (JSON.stringify(next) !== JSON.stringify(chosen)) location.reload()
}

/** Whether Intl takes a locale; some browsers report ones it doesn't, e.g. en-US@posix. */
function valid(l: string) {
  try {
    return Intl.getCanonicalLocales(l).length > 0
  } catch {
    return false
  }
}

/**
 * Marks a text outside of components for translation, which the components do with t. t itself
 * can't run there, as modules are loaded before the language is chosen.
 */
export const msg = <T extends string>(text: T) => text

/**
 * Chooses the language before the panel renders, so components can use i18next's t directly.
 * Changing it later means reloading the panel.
 */
export async function setUpI18n() {
  const resources = language === "en" ? {} : { [language]: { translation: await files[`../locales/${language}.json`]() } }
  document.documentElement.lang = language
  await i18next.use(initReactI18next).init({
    lng: language,
    fallbackLng: "en",
    // Texts not translated yet are empty in the files of the languages.
    returnEmptyString: false,
    resources,
    keySeparator: false,
    nsSeparator: false,
    // React escapes the texts. Values that users control go into components of <Trans>, never
    // into its values, which it would parse as markup.
    interpolation: { escapeValue: false },
    react: { transKeepBasicHtmlNodesFor: [] },
  })
}
