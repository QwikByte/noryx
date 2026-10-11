import { t } from "i18next"
import { Field, FieldContent, FieldDescription, FieldLabel } from "@/components/ui/field"
import { Input } from "@/components/ui/input"
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
      <Field>
        <FieldLabel htmlFor="settings-update-check-hours">{t("Check every")}</FieldLabel>
        <div className="flex items-center gap-2">
          <Input
            id="settings-update-check-hours"
            type="number"
            required
            min={1}
            max={24}
            className="font-mono sm:w-32"
            disabled={!value.checkUpdates}
            value={value.updateCheckHours}
            onChange={(e) => onChange({ updateCheckHours: e.target.valueAsNumber || 0 })}
          />
          <span className="text-sm text-muted-foreground">{t("hours")}</span>
        </div>
        <FieldDescription>
          {t("From 1 to 24 hours. GitHub answers 60 such requests an hour per IP address, shared with everything behind it.")}
        </FieldDescription>
      </Field>
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
