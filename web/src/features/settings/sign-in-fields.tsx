import { useQuery } from "@tanstack/react-query"
import { t } from "i18next"
import { type ReactNode, useState } from "react"
import { Field, FieldContent, FieldDescription, FieldLabel } from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import { Switch } from "@/components/ui/switch"
import { type MasterSettings, settingsQuery } from "./api"

type Limits = Pick<MasterSettings, "sessionHours" | "minPasswordLength" | "setupLinkHours" | "maxTokenDays" | "sessionIdleHours">

/**
 * How many characters new passwords need, how long setup links are valid, and the limits of API tokens and unused
 * sessions, which are off unless chosen. Passwords never need fewer characters than they always did.
 */
export function SignInFields({ value, onChange }: { value: Limits; onChange: (change: Partial<Limits>) => void }) {
  // The fields stay while they are empty, until the limits are switched off.
  const [idle, setIdle] = useState(value.sessionIdleHours > 0)
  const [tokens, setTokens] = useState(value.maxTokenDays > 0)
  const saved = useQuery(settingsQuery).data
  // The master counts the tokens over the saved limit.
  const over = saved && value.maxTokenDays === saved.settings.maxTokenDays ? saved.tokensOverLimit : 0
  return (
    <>
      <Field orientation="horizontal">
        <Switch
          id="settings-idle"
          checked={idle}
          onCheckedChange={(on) => {
            setIdle(on)
            onChange({ sessionIdleHours: on ? Math.min(2, value.sessionHours) : 0 })
          }}
        />
        <FieldContent>
          <FieldLabel htmlFor="settings-idle">{t("End unused sessions")}</FieldLabel>
          <FieldDescription>
            {t(
              "Sessions also end once they weren't used for a while, e.g. in a browser that was closed. A panel that stays open keeps using its session.",
            )}
          </FieldDescription>
        </FieldContent>
      </Field>
      {idle && (
        <NumberField
          id="settings-idle-hours"
          label={t("Sessions end after")}
          min={1}
          max={value.sessionHours}
          unit={t("hours without use")}
          value={value.sessionIdleHours}
          onChange={(sessionIdleHours) => onChange({ sessionIdleHours })}
        >
          {t("Up to as long as sessions last. Applies to all sessions right away.")}
        </NumberField>
      )}
      <div className="grid gap-4 sm:grid-cols-2">
        <NumberField
          id="settings-password-length"
          label={t("Passwords need at least")}
          min={12}
          max={64}
          unit={t("characters")}
          value={value.minPasswordLength}
          onChange={(minPasswordLength) => onChange({ minPasswordLength })}
        >
          {t("From 12 to 64. Applies to the passwords set from now on; those set before keep working.")}
        </NumberField>
        <NumberField
          id="settings-setup-link"
          label={t("Setup links are valid for")}
          min={1}
          max={168}
          unit={t("hours")}
          value={value.setupLinkHours}
          onChange={(setupLinkHours) => onChange({ setupLinkHours })}
        >
          {t("Up to a week. Applies to the links created from now on.")}
        </NumberField>
      </div>
      <Field orientation="horizontal">
        <Switch
          id="settings-token-limit"
          checked={tokens}
          onCheckedChange={(on) => {
            setTokens(on)
            onChange({ maxTokenDays: on ? 90 : 0 })
          }}
        />
        <FieldContent>
          <FieldLabel htmlFor="settings-token-limit">{t("Limit the lifetime of API tokens")}</FieldLabel>
          <FieldDescription>
            {t("New tokens then have to expire within this time. Tokens created before keep their expiry.")}
          </FieldDescription>
        </FieldContent>
      </Field>
      {tokens && (
        <NumberField
          id="settings-token-days"
          label={t("API tokens last at most")}
          min={1}
          max={3650}
          unit={t("days")}
          value={value.maxTokenDays}
          onChange={(maxTokenDays) => onChange({ maxTokenDays })}
        >
          {over > 0 &&
            t("{{count}} existing tokens expire later or never. Their users can revoke them on their account page.", {
              count: over,
              defaultValue_one: "{{count}} existing token expires later or never. Its user can revoke it on their account page.",
            })}
        </NumberField>
      )}
    </>
  )
}

function NumberField({
  id,
  label,
  min,
  max,
  unit,
  value,
  onChange,
  children,
}: {
  id: string
  label: string
  min: number
  max: number
  unit: string
  value: number
  onChange: (value: number) => void
  children?: ReactNode
}) {
  return (
    <Field>
      <FieldLabel htmlFor={id}>{label}</FieldLabel>
      <div className="flex items-center gap-2">
        <Input
          id={id}
          type="number"
          required
          min={min}
          max={max}
          className="font-mono sm:w-32"
          value={value}
          onChange={(e) => onChange(e.target.valueAsNumber || 0)}
        />
        <span className="text-sm text-muted-foreground">{unit}</span>
      </div>
      {children && <FieldDescription>{children}</FieldDescription>}
    </Field>
  )
}
