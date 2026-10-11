import { t } from "i18next"
import { useState } from "react"
import { Field, FieldContent, FieldDescription, FieldLabel } from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import { Switch } from "@/components/ui/switch"
import type { AgentSettings } from "./api"

/** What the agent of a node applies itself: the output that containers keep, the space kept free and the interval of measurements. */
export function AgentFields({ id, value, onChange }: { id: string; value: AgentSettings; onChange: (value: AgentSettings) => void }) {
  const set = (change: Partial<AgentSettings>) => onChange({ ...value, ...change })
  const [limited, setLimited] = useState(value.logFiles > 0)
  const number = (key: keyof AgentSettings, min: number, max: number) => ({
    id: `${id}-${key}`,
    type: "number",
    required: true,
    min,
    max,
    value: value[key],
    onChange: (e: React.ChangeEvent<HTMLInputElement>) => set({ [key]: e.target.valueAsNumber || 0 }),
  })
  return (
    <>
      <Field orientation="horizontal">
        <Switch
          id={`${id}-logs`}
          checked={limited}
          onCheckedChange={(on) => {
            setLimited(on)
            set(on ? { logFileMb: 10, logFiles: 3 } : { logFileMb: 0, logFiles: 0 })
          }}
        />
        <FieldContent>
          <FieldLabel htmlFor={`${id}-logs`}>{t("Limit the output of containers")}</FieldLabel>
          <FieldDescription>
            {t(
              "Servers and datastores keep at most this much of their output, of which the console loads the last 1000 lines. A container gets the limit when it is created again, e.g. when the settings of its server are saved. Without it, the configuration of Docker or Podman decides.",
            )}
          </FieldDescription>
        </FieldContent>
      </Field>
      {limited && (
        <div className="grid gap-4 sm:grid-cols-2">
          <Field>
            <FieldLabel htmlFor={`${id}-logFiles`}>{t("Files")}</FieldLabel>
            <Input {...number("logFiles", 1, 10)} className="font-mono" />
          </Field>
          <Field>
            <FieldLabel htmlFor={`${id}-logFileMb`}>{t("Size of a file in MB")}</FieldLabel>
            <Input {...number("logFileMb", 1, 100)} className="font-mono" />
          </Field>
        </div>
      )}
      <Field>
        <FieldLabel htmlFor={`${id}-keepFreeGb`}>{t("Space kept free in GiB")}</FieldLabel>
        <Input {...number("keepFreeGb", 1, 50)} className="font-mono sm:w-40" />
        <FieldDescription>{t("From 1 to 50. Uploads and backups stop before they leave less free, so that servers can keep saving their worlds.")}</FieldDescription>
      </Field>
      <Field>
        <FieldLabel htmlFor={`${id}-statsIntervalSeconds`}>{t("Interval of measurements in seconds")}</FieldLabel>
        <Input {...number("statsIntervalSeconds", 5, 60)} className="font-mono sm:w-40" />
        <FieldDescription>{t("From 5 to 60. Longer intervals ask servers less often for their players, while the panel shows changes later.")}</FieldDescription>
      </Field>
    </>
  )
}
