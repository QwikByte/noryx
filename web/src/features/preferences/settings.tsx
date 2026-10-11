import {
  ArrowsOutLineHorizontalIcon,
  CalendarDotsIcon,
  ClockIcon,
  ClockCounterClockwiseIcon,
  CodeIcon,
  FileCsvIcon,
  GlobeIcon,
  HouseIcon,
  KeyboardIcon,
  PaletteIcon,
  PowerIcon,
  SparkleIcon,
  TabsIcon,
  TerminalIcon,
  TerminalWindowIcon,
  TextAaIcon,
} from "@phosphor-icons/react"
import { t } from "i18next"
import { pages, startPage } from "@/components/navigation"
import { Segmented } from "@/components/segmented"
import { TimeZonePicker } from "@/components/time-zone-picker"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { useAccess } from "@/features/access/use-access"
import { AccountRow } from "@/features/auth/account-row"
import { searchChord } from "@/features/palette/shortcuts"
import { firstTabs } from "@/features/servers/tabs"
import { languageSeparator } from "@/lib/csv"
import { type Clock, clock, locale, timeWith } from "@/lib/i18n"
import { type Motion, useLook, type Width } from "@/lib/theme"
import { codeSizes, type Settings, type SettingsChange, useSettings, wraps } from "./api"

// A time in the afternoon shows what each clock means, e.g. 14:30 and 2:30 PM.
const afternoon = new Date(2000, 0, 1, 14, 30)
const browserZone = Intl.DateTimeFormat().resolvedOptions().timeZone
const count = new Intl.NumberFormat(locale)

/** A choice of a setting as a select, with an option that takes it back to the default. */
function Choice<K extends keyof Settings>({
  setting,
  label,
  options,
  fallback,
  className = "w-auto min-w-44",
}: {
  setting: K
  label: string
  options: { value: NonNullable<Settings[K]> | "default"; label: string }[]
  fallback: NonNullable<Settings[K]> | "default"
  className?: string
}) {
  const { settings, change } = useSettings()
  return (
    <Select
      value={settings[setting] ?? fallback}
      onValueChange={(value) => change({ [setting]: value === "default" ? null : value } as SettingsChange)}
    >
      <SelectTrigger aria-label={label} className={className}>
        <SelectValue />
      </SelectTrigger>
      <SelectContent>
        {options.map((o) => (
          <SelectItem key={o.value} value={o.value}>
            {o.label}
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  )
}

/** How the panel shows times, which reloads it once stored. */
export function TimesSettings() {
  const { settings, change } = useSettings()
  return (
    <>
      <AccountRow
        icon={ClockIcon}
        tone="info"
        title={t("Time format")}
        actions={
          <Segmented<Clock>
            label={t("Time format")}
            value={settings.clock ?? clock}
            options={(["24h", "12h"] as const).map((value) => ({ value, label: timeWith(value, afternoon) }))}
            onChange={(value) => change({ clock: value })}
          />
        }
      >
        {t("Times show 24 hours, or 12 hours with AM and PM.")}
      </AccountRow>
      <AccountRow
        icon={GlobeIcon}
        tone="info"
        title={t("Time zone")}
        actions={
          <>
            <Select
              value={settings.timeZone ? "chosen" : "browser"}
              onValueChange={(v) => change({ timeZone: v === "browser" ? null : browserZone })}
            >
              <SelectTrigger aria-label={t("Time zone")} className="w-auto min-w-44">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="browser">{t("The browser's")}</SelectItem>
                <SelectItem value="chosen">{t("Chosen")}</SelectItem>
              </SelectContent>
            </Select>
            {settings.timeZone && (
              <TimeZonePicker value={settings.timeZone} onChange={(zone) => change({ timeZone: zone || "UTC" })} className="w-56" />
            )}
          </>
        }
      >
        {settings.timeZone
          ? t("Times show in this time zone in all your browsers.")
          : t("Times show in the time zone of each browser, here {{zone}}.", { zone: browserZone.replaceAll("_", " ") })}
      </AccountRow>
      <AccountRow
        icon={ClockCounterClockwiseIcon}
        tone="info"
        title={t("How times show")}
        actions={
          <Segmented<NonNullable<Settings["times"]>>
            label={t("How times show")}
            value={settings.times ?? "relative"}
            options={[
              { value: "relative", label: t("How long ago") },
              { value: "absolute", label: t("Date and time") },
            ]}
            onChange={(value) => change({ times: value })}
          />
        }
      >
        {t("Where times tell how long ago something was, e.g. 5 minutes ago, or when it comes. The other shows when pointing at it.")}
      </AccountRow>
      <AccountRow
        icon={CalendarDotsIcon}
        tone="info"
        title={t("First day of the week")}
        actions={
          <Choice
            setting="weekStart"
            label={t("First day of the week")}
            fallback="default"
            options={[
              { value: "default", label: t("From the language") },
              { value: "monday", label: t("Monday") },
              { value: "sunday", label: t("Sunday") },
            ]}
          />
        }
      >
        {t("For the days of backup jobs and schedules.")}
      </AccountRow>
    </>
  )
}

/** How the console, the terminal and the editor show text. */
export function CodeSettings() {
  const { settings, change } = useSettings()
  const wrap = (key: keyof typeof wraps) => (
    <Select value={wraps[key].on(settings) ? "on" : "off"} onValueChange={(v) => change(wraps[key].set(v === "on"))}>
      <SelectTrigger aria-label={t("Long lines")} className="w-auto min-w-44">
        <SelectValue />
      </SelectTrigger>
      <SelectContent>
        <SelectItem value="on">{t("Wrap long lines")}</SelectItem>
        <SelectItem value="off">{t("Scroll sideways")}</SelectItem>
      </SelectContent>
    </Select>
  )
  return (
    <>
      <AccountRow
        icon={TextAaIcon}
        tone="violet"
        title={t("Text size")}
        actions={
          <Segmented<NonNullable<Settings["codeSize"]>>
            label={t("Text size")}
            value={settings.codeSize ?? "small"}
            options={codeSizes.map((s) => ({ value: s.value, label: t(s.label) }))}
            onChange={(value) => change({ codeSize: value })}
          />
        }
      >
        {t("Of the console, the terminal and the editor.")}
      </AccountRow>
      <AccountRow
        icon={PaletteIcon}
        tone="violet"
        title={t("Colours")}
        actions={
          <Segmented<NonNullable<Settings["codeTheme"]>>
            label={t("Colours")}
            value={settings.codeTheme ?? "dark"}
            options={[
              { value: "dark", label: t("Always dark") },
              { value: "panel", label: t("Like the theme") },
            ]}
            onChange={(value) => change({ codeTheme: value })}
          />
        }
      >
        {t("The console, the terminal and the editor stay dark, or are light in the light theme.")}
      </AccountRow>
      <AccountRow
        icon={TerminalIcon}
        tone="info"
        title={t("Console")}
        actions={
          <>
            {wrap("consoleWrap")}
            <Choice
              setting="consoleTimes"
              label={t("Times")}
              fallback="hide"
              options={[
                { value: "hide", label: t("Without times") },
                { value: "show", label: t("With the time of each line") },
              ]}
            />
            <Choice
              setting="consoleLines"
              label={t("Lines")}
              fallback="2000"
              options={(["2000", "5000", "10000"] as const).map((value) => ({ value, label: t("Keep {{lines}} lines", { lines: count.format(Number(value)) }) }))}
            />
          </>
        }
      >
        {t("The output of servers. It remembers whether it only shows warnings and errors.")}
      </AccountRow>
      <AccountRow icon={TerminalWindowIcon} tone="info" title={t("Terminal")} actions={wrap("terminalWrap")}>
        {t("It scrolls sideways unless chosen, so that tables stay aligned.")}
      </AccountRow>
      <AccountRow
        icon={CodeIcon}
        tone="info"
        title={t("Editor")}
        actions={
          <>
            {wrap("editorWrap")}
            <Choice
              setting="editorIndent"
              label={t("Indentation")}
              fallback="2"
              options={[
                { value: "2", label: t("Indent with 2 spaces") },
                { value: "4", label: t("Indent with 4 spaces") },
                { value: "tab", label: t("Indent with tabs") },
              ]}
            />
            <Choice
              setting="editorKeys"
              label={t("Keys")}
              fallback="standard"
              options={[
                { value: "standard", label: t("Standard keys") },
                { value: "vim", label: t("Vim keys") },
              ]}
            />
          </>
        }
      >
        {t("Files of servers and file sets. YAML files always indent with spaces, and Ctrl+S saves with Vim keys too.")}
      </AccountRow>
    </>
  )
}

/** How tables download as CSV files for spreadsheets. */
export function ExportSettings() {
  return (
    <AccountRow
      icon={FileCsvIcon}
      tone="success"
      title={t("CSV files")}
      actions={
        <>
          <Choice
            setting="csvSeparator"
            label={t("Separator")}
            fallback="default"
            options={[
              {
                value: "default",
                label: languageSeparator === "semicolon" ? t("As in the language: semicolons") : t("As in the language: commas"),
              },
              { value: "comma", label: t("Commas") },
              { value: "semicolon", label: t("Semicolons") },
            ]}
          />
          <Choice
            setting="csvBom"
            label={t("Byte order mark")}
            fallback="off"
            options={[
              { value: "off", label: t("Without byte order mark") },
              { value: "on", label: t("With byte order mark") },
            ]}
          />
        </>
      }
    >
      {t("Spreadsheets in languages with a decimal comma, e.g. German, expect semicolons. Excel needs the byte order mark to read umlauts and other letters.")}
    </AccountRow>
  )
}

/** Where the panel and servers open, how wide pages are and how they move, and whether shortcuts of single keys work. */
export function NavigationSettings() {
  const access = useAccess()
  const look = useLook()
  const { settings, change } = useSettings()
  return (
    <>
      <AccountRow
        icon={HouseIcon}
        tone="info"
        title={t("Start page")}
        actions={
          <Select value={startPage(access, settings.startPage)} onValueChange={(page) => change({ startPage: page })}>
            <SelectTrigger aria-label={t("Start page")} className="w-auto min-w-44">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {pages(access).map((page) => (
                <SelectItem key={page.to} value={page.to}>
                  {t(page.label)}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        }
      >
        {t("Where the panel opens, also after signing in. The overview stays in the sidebar.")}
      </AccountRow>
      <AccountRow
        icon={TabsIcon}
        tone="info"
        title={t("First tab of a server")}
        actions={
          <Choice
            setting="serverTab"
            label={t("First tab of a server")}
            fallback="console"
            options={firstTabs.map((tab) => ({ value: tab.value, label: t(tab.label) }))}
          />
        }
      >
        {t("The tab a server opens on from lists and the search. Without the permission for it, the first tab you may see.")}
      </AccountRow>
      <AccountRow
        icon={ArrowsOutLineHorizontalIcon}
        tone="violet"
        title={t("Page width")}
        actions={
          <Segmented<Width>
            label={t("Page width")}
            value={look.width}
            options={[
              { value: "limited", label: t("Limited") },
              { value: "full", label: t("Full width") },
            ]}
            onChange={(value) => change({ width: value })}
          />
        }
      >
        {t("Pages keep a width that reads well, or fill wide screens, e.g. for long tables.")}
      </AccountRow>
      <AccountRow
        icon={SparkleIcon}
        tone="violet"
        title={t("Motion")}
        actions={
          <Segmented<Motion>
            label={t("Motion")}
            value={look.motion}
            options={[
              { value: "system", label: t("System") },
              { value: "less", label: t("Less motion") },
            ]}
            onChange={(value) => change({ motion: value })}
          />
        }
      >
        {t("Lists, figures, charts and pages are animated unless your operating system asks for less motion, or you do here.")}
      </AccountRow>
      <AccountRow
        icon={KeyboardIcon}
        tone="info"
        title={t("Keyboard shortcuts")}
        actions={
          <Segmented<NonNullable<Settings["shortcuts"]>>
            label={t("Keyboard shortcuts")}
            value={settings.shortcuts ?? "on"}
            options={[
              { value: "on", label: t("On") },
              { value: "off", label: t("Off") },
            ]}
            onChange={(value) => change({ shortcuts: value })}
          />
        }
      >
        {t("Keys typed one after the other, e.g. g and then s for the servers. Turned off, they don't get in the way of speech input, and {{chord}} still searches.", {
          chord: searchChord,
        })}
      </AccountRow>
    </>
  )
}

/** Whether a single server stops or restarts right away, after asking or with a warning to its players, and when. */
export function PowerSettings() {
  const { settings } = useSettings()
  return (
    <AccountRow
      icon={PowerIcon}
      tone="warning"
      title={t("Stops and restarts")}
      actions={
        <>
          <Choice
            setting="power"
            label={t("Stops and restarts")}
            fallback="now"
            options={[
              { value: "now", label: t("Right away") },
              { value: "ask", label: t("Ask first") },
              { value: "warn", label: t("Warn the players first") },
            ]}
          />
          {settings.power && settings.power !== "now" && (
            <Choice
              setting="powerWhen"
              label={t("Which servers")}
              fallback="always"
              options={[
                { value: "always", label: t("For all servers") },
                { value: "players", label: t("For servers with players") },
              ]}
            />
          )}
        </>
      }
    >
      {t(
        "Of a single server, from its buttons and the search; selected servers always ask. Only running game servers can warn their players, so the others ask. Players count as their node measured them last; servers without a measurement count as having some.",
      )}
    </AccountRow>
  )
}
