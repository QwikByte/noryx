import { t } from "i18next"
import { formatSeconds } from "@/lib/format"
import { locale, msg } from "@/lib/i18n"
import type { WarningSettings } from "./api"

/** Where warnings can show: the choice in the settings, and how fields of warnings say it. */
export const warningKinds = {
  chat: { label: msg("In the chat"), shown: msg("Shown in the chat.") },
  title: { label: msg("As a title"), shown: msg("Shown as a title.") },
  actionbar: { label: msg("Above the hotbar"), shown: msg("Shown above the hotbar.") },
} satisfies Record<WarningSettings["kind"], { label: string; shown: string }>

/** Tells at which steps a warning repeats, e.g. "Again 5 minutes and 1 minute before."; empty without steps. */
export const repeats = (steps: number[]) =>
  steps.length > 0
    ? t("Again {{times}} before.", { times: new Intl.ListFormat(locale, { type: "conjunction" }).format(steps.map((m) => formatSeconds(m * 60))) })
    : ""

/** Describes a field for a warning of one's own: where it shows, as far as known, and what {minutes} becomes. */
export const warningHint = (how?: WarningSettings) =>
  [how && t(warningKinds[how.kind].shown), t("{minutes} becomes the minutes left. Proxies get no warning.")].filter(Boolean).join(" ")
