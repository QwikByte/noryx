import { t } from "i18next"
import { NumberField } from "@/components/number-input"
import { Field, FieldContent, FieldDescription, FieldLabel } from "@/components/ui/field"
import { Switch } from "@/components/ui/switch"
import type { UpdateAgents } from "./api"

interface UpdateSettings {
  checkUpdates: boolean
  updateCheckHours: number
  updateAgents: UpdateAgents
}

/** How often the master looks for new releases, and whether it updates the agents after itself. */
export function UpdateFields({ value, onChange }: { value: UpdateSettings; onChange: (change: Partial<UpdateSettings>) => void }) {
  return (
    <>
      <NumberField
        id="settings-update-check-hours"
        label={t("Check every")}
        min={1}
        max={24}
        unit={t("hours")}
        disabled={!value.checkUpdates}
        value={value.updateCheckHours}
        onChange={(updateCheckHours) => onChange({ updateCheckHours })}
      >
        {t("From 1 to 24 hours. GitHub answers 60 such requests an hour per IP address, shared with everything behind it.")}
      </NumberField>
      <Field orientation="horizontal">
        <Switch
          id="settings-update-agents"
          checked={value.updateAgents === "all"}
          onCheckedChange={(all) => onChange({ updateAgents: all ? "all" : "none" })}
        />
        <FieldContent>
          <FieldLabel htmlFor="settings-update-agents">{t("Update the agents after the master")}</FieldLabel>
          <FieldDescription>
            {t(
              "Once the master runs a release installed from the panel, it updates every agent that is older. Otherwise, the agents keep their version until you update them, e.g. node by node in the Agents tab.",
            )}
          </FieldDescription>
        </FieldContent>
      </Field>
    </>
  )
}
