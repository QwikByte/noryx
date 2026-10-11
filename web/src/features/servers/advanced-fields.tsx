import { t } from "i18next"
import { Fold } from "@/components/fold"
import { Field, FieldDescription, FieldLabel } from "@/components/ui/field"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { formatSeconds } from "@/lib/format"
import type { CrashSettings } from "./api"

// Advanced settings shared by the settings of a server and the templates for new servers.

const minutes = (m: number) => formatSeconds(m * 60)

/** Chooses one of the usual numbers, or keeps another one that is set; the default is marked. */
function NumberSelect({
  id,
  value,
  options,
  fallback,
  label,
  onChange,
}: {
  id: string
  value: number
  options: number[]
  fallback: number
  label: (n: number) => string
  onChange: (n: number) => void
}) {
  return (
    <Select value={String(value)} onValueChange={(v) => onChange(Number(v))}>
      <SelectTrigger id={id} className="w-full sm:w-64">
        <SelectValue />
      </SelectTrigger>
      <SelectContent>
        {[...new Set([...options, value])]
          .sort((a, b) => a - b)
          .map((n) => (
            <SelectItem key={n} value={String(n)}>
              {label(n)}
              {n === fallback && <span className="text-muted-foreground">{t("Default")}</span>}
            </SelectItem>
          ))}
      </SelectContent>
    </Select>
  )
}

/** How long a game server may take to start before its failed health checks count, from 2 to 30 minutes. */
export function HealthStartField({
  id = "settings-health-start",
  value,
  onChange,
}: {
  id?: string
  value: number
  onChange: (minutes: number) => void
}) {
  return (
    <Field>
      <FieldLabel htmlFor={id}>{t("Start period")}</FieldLabel>
      <NumberSelect id={id} value={value} options={[2, 5, 10, 15, 20, 30]} fallback={2} label={minutes} onChange={onChange} />
      <FieldDescription>
        {t("How long the server may take to start before a failing health check makes it unhealthy. Large modpacks may need 10 minutes or more.")}
      </FieldDescription>
    </Field>
  )
}

/** When the agent stops a server that keeps crashing, and restarts one that stays unhealthy, folded away. */
export function CrashFields({ id, value, onChange }: { id: string; value: CrashSettings; onChange: (change: Partial<CrashSettings>) => void }) {
  const { crashLimit, stableAfter, unhealthyRestart } = value
  const summary = [
    t("Stops after {{count}} crashes in a row", { count: crashLimit, defaultValue_one: "Stops after a crash" }),
    unhealthyRestart ? t("restarts after {{time}} unhealthy", { time: minutes(unhealthyRestart) }) : t("no restarts when unhealthy"),
  ]
  return (
    <Fold title={t("Crashes")} summary={summary.join(" · ")}>
      <div className="grid gap-4 sm:grid-cols-2">
        <Field>
          <FieldLabel htmlFor={`${id}-limit`}>{t("Crashes in a row")}</FieldLabel>
          <NumberSelect
            id={`${id}-limit`}
            value={crashLimit}
            options={[1, 2, 3, 5, 10, 20]}
            fallback={5}
            label={String}
            onChange={(crashLimit) => onChange({ crashLimit })}
          />
        </Field>
        <Field>
          <FieldLabel htmlFor={`${id}-stable`}>{t("Stable after")}</FieldLabel>
          <NumberSelect
            id={`${id}-stable`}
            value={stableAfter}
            options={[1, 5, 10, 15, 30, 60, 120]}
            fallback={10}
            label={minutes}
            onChange={(stableAfter) => onChange({ stableAfter })}
          />
        </Field>
      </div>
      <FieldDescription>
        {t(
          "After this many crashes in a row, the agent stops the server, as it would be started again forever. A crash after a stable run starts a new row; large modpacks that crash late may need a longer one.",
        )}
      </FieldDescription>
      <Field>
        <FieldLabel htmlFor={`${id}-unhealthy`}>{t("Restart when unhealthy")}</FieldLabel>
        <NumberSelect
          id={`${id}-unhealthy`}
          value={unhealthyRestart}
          options={[0, 1, 2, 5, 10, 15, 30, 60]}
          fallback={0}
          label={(m) => (m ? t("After {{time}}", { time: minutes(m) }) : t("Never"))}
          onChange={(unhealthyRestart) => onChange({ unhealthyRestart })}
        />
        <FieldDescription>
          {t(
            "Restarts the server once its health check has failed for this long after its start period, e.g. as it hangs. Each restart counts as a crash; one after the server was healthy for a stable run starts a new row.",
          )}
        </FieldDescription>
      </Field>
    </Fold>
  )
}
