import { useQuery } from "@tanstack/react-query"
import { t } from "i18next"
import { ErrorCallout } from "@/components/callout"
import { Field, FieldDescription, FieldLabel, FieldLegend, FieldSet } from "@/components/ui/field"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { Skeleton } from "@/components/ui/skeleton"
import { groupsQuery } from "@/features/access/api"
import { GroupPicker } from "@/features/access/group-picker"
import { useAccess } from "@/features/access/use-access"
import { languageName, languages, msg, timeWith } from "@/lib/i18n"
import { accents } from "@/lib/theme"
import type { UserDefaults } from "./public"

// A time in the afternoon shows what each clock means, e.g. 14:30 and 2:30 PM.
const afternoon = new Date(2000, 0, 1, 14, 30)

const accentNames = { emerald: msg("Emerald"), blue: msg("Blue"), violet: msg("Violet"), graphite: msg("Graphite") }

/** The language and look of users who haven't chosen them, each with the choices users have, or from the browser. */
export function UserDefaultsFields({ value, onChange }: { value: UserDefaults; onChange: (value: UserDefaults) => void }) {
  const choice = <K extends keyof UserDefaults>(key: K, label: string, options: { value: UserDefaults[K]; label: string }[]) => (
    <Field>
      <FieldLabel htmlFor={`settings-default-${key}`}>{label}</FieldLabel>
      <Select value={value[key] || "browser"} onValueChange={(v) => onChange({ ...value, [key]: v === "browser" ? "" : v })}>
        <SelectTrigger id={`settings-default-${key}`} className="w-full">
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          <SelectItem value="browser">{t("From the browser")}</SelectItem>
          {options.map((o) => (
            <SelectItem key={o.value} value={o.value}>
              {o.label}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
    </Field>
  )
  return (
    <>
      <div className="grid gap-x-4 gap-y-5 sm:grid-cols-2 xl:grid-cols-3">
        {choice(
          "language",
          t("Language"),
          languages.map((code) => ({ value: code, label: languageName(code) })),
        )}
        {choice("theme", t("Colour theme"), [
          { value: "light", label: t("Light") },
          { value: "dark", label: t("Dark") },
          { value: "system", label: t("System") },
        ])}
        {choice(
          "accent",
          t("Accent colour"),
          accents.map((accent) => ({ value: accent, label: t(accentNames[accent]) })),
        )}
        {choice("density", t("Density"), [
          { value: "comfortable", label: t("Comfortable") },
          { value: "compact", label: t("Compact") },
        ])}
        {choice(
          "clock",
          t("Time format"),
          (["24h", "12h"] as const).map((clock) => ({ value: clock, label: timeWith(clock, afternoon) })),
        )}
      </div>
      <FieldDescription>
        {t(
          "Users who haven't chosen these on their account page get them instead of what their browser has, and so does the sign-in page in browsers that have none of their own yet. They are public, as the sign-in page shows them.",
        )}
      </FieldDescription>
    </>
  )
}

/** The groups that inviting a user preselects; choosing them needs the permission to see users and groups. */
export function InviteGroupsField({ value, onChange }: { value: string[]; onChange: (groups: string[]) => void }) {
  const canSeeGroups = useAccess().can("users.view")
  const groups = useQuery({ ...groupsQuery, enabled: canSeeGroups })
  return (
    <FieldSet>
      <FieldLegend variant="label">{t("Groups of invited users")}</FieldLegend>
      <FieldDescription>
        {t(
          "Inviting a user preselects these groups. Whoever invites can still change them, and only give groups within their own permissions.",
        )}
      </FieldDescription>
      {!canSeeGroups ? (
        <FieldDescription>
          {t("{{count}} groups chosen. Choosing others needs the permission to see users and groups.", {
            count: value.length,
            defaultValue_one: "{{count}} group chosen. Choosing others needs the permission to see users and groups.",
          })}
        </FieldDescription>
      ) : groups.error ? (
        <ErrorCallout error={groups.error} />
      ) : groups.data ? (
        <GroupPicker groups={groups.data} value={value} onChange={onChange} />
      ) : (
        <Skeleton className="h-24 rounded-xl" />
      )}
    </FieldSet>
  )
}
