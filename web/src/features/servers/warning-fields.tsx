import { t } from "i18next"
import { useState } from "react"
import { Segmented } from "@/components/segmented"
import { Field, FieldDescription, FieldError, FieldLabel, FieldLegend, FieldSet } from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import { useAccess } from "@/features/access/use-access"
import type { ErrorsAt } from "@/lib/use-draft"
import type { WarningSettings } from "./api"
import { warningKinds } from "./warnings"

/** How the settings warn the players before restarts and stops: the texts, where they show and when. */
export function WarningFields({ value, onChange, errorAt }: { value: WarningSettings; onChange: (value: WarningSettings) => void; errorAt?: ErrorsAt }) {
  // The texts go to the console of every server they warn.
  const writable = useAccess().can("console.commands")
  // Kept as typed, so that a comma can be entered before the next number.
  const [steps, setSteps] = useState(value.steps.join(", "))
  const set = (change: Partial<WarningSettings>) => onChange({ ...value, ...change })
  return (
    <>
      <p className="text-sm text-muted-foreground">
        {t(
          "Restarts and stops by hand, also of many servers, can warn the players of game servers first. Schedules and workflows warn at the minutes they have, with these texts unless they have their own.",
        )}
      </p>
      <FieldSet>
        <FieldLegend variant="label">{t("Texts")}</FieldLegend>
        <div className="grid gap-4 sm:grid-cols-2">
          {(["restart", "stop"] as const).map((action) => (
            <Field key={action} data-invalid={!!errorAt?.(action)}>
              <FieldLabel htmlFor={`settings-warning-${action}`}>{action === "restart" ? t("Before restarts") : t("Before stops")}</FieldLabel>
              <Input
                id={`settings-warning-${action}`}
                required
                maxLength={200}
                disabled={!writable}
                aria-invalid={!!errorAt?.(action)}
                value={value[action]}
                onChange={(e) => set({ [action]: e.target.value })}
              />
              <FieldError errors={errorAt?.(action)} />
            </Field>
          ))}
        </div>
        <FieldDescription>
          {writable
            ? t("{minutes} becomes the minutes left. Write them in the language of your players.")
            : t("Changing them needs the permission to send console commands to all servers, as they go to the console of every server they warn.")}
        </FieldDescription>
      </FieldSet>
      <Field data-invalid={!!errorAt?.("kind")}>
        <FieldLabel id="settings-warning-kind">{t("Where they show")}</FieldLabel>
        <Segmented
          label={t("Where they show")}
          className="max-w-full self-start overflow-x-auto"
          value={value.kind}
          onChange={(kind) => set({ kind })}
          options={Object.entries(warningKinds).map(([kind, { label }]) => ({ value: kind as WarningSettings["kind"], label: t(label) }))}
        />
        <FieldDescription>{t("Proxies get no warning.")}</FieldDescription>
        <FieldError errors={errorAt?.("kind")} />
      </Field>
      <div className="grid gap-4 sm:grid-cols-2">
        <Field data-invalid={!!errorAt?.("maxMinutes")}>
          <FieldLabel htmlFor="settings-warning-max">{t("Longest lead time")}</FieldLabel>
          <div className="flex items-center gap-2">
            <Input
              id="settings-warning-max"
              type="number"
              required
              min={1}
              max={60}
              aria-invalid={!!errorAt?.("maxMinutes")}
              className="font-mono sm:w-32"
              value={value.maxMinutes}
              onChange={(e) => set({ maxMinutes: e.target.valueAsNumber || 0 })}
            />
            <span className="text-sm text-muted-foreground">{t("minutes")}</span>
          </div>
          <FieldDescription>{t("Up to an hour. Restarts and stops by hand offer the usual lead times up to it.")}</FieldDescription>
          <FieldError errors={errorAt?.("maxMinutes")} />
        </Field>
        <Field data-invalid={!!errorAt?.("steps")}>
          <FieldLabel htmlFor="settings-warning-steps">{t("Repeat at")}</FieldLabel>
          <Input
            id="settings-warning-steps"
            aria-invalid={!!errorAt?.("steps")}
            inputMode="numeric"
            // i18next-instrument-ignore-next-line: an example of what to enter
            placeholder="5, 1"
            className="font-mono"
            value={steps}
            onChange={(e) => {
              setSteps(e.target.value)
              set({ steps: e.target.value.split(/[\s,]+/).flatMap((m) => (/^\d+$/.test(m) ? [Number(m)] : [])) })
            }}
          />
          <FieldDescription>{t("Minutes before at which the warning repeats: up to 5, each below the longest lead time.")}</FieldDescription>
          <FieldError errors={errorAt?.("steps")} />
        </Field>
      </div>
    </>
  )
}
