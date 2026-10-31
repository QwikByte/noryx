import { PlusIcon, XIcon } from "@phosphor-icons/react"
import { useQuery } from "@tanstack/react-query"
import { t } from "i18next"
import { Button } from "@/components/ui/button"
import { Field, FieldContent, FieldDescription, FieldLabel, FieldLegend, FieldSet, FieldTitle } from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import { RadioGroup, RadioGroupItem } from "@/components/ui/radio-group"
import { Switch } from "@/components/ui/switch"
import { defaultSelection, type JobSettings } from "@/features/backups/api"
import { LocationField, RetentionField, SelectionField } from "@/features/backups/backup-fields"
import { nodesQuery } from "@/features/nodes/api"
import { msg } from "@/lib/i18n"
import type { Condition, PolicySettings } from "./api"

const maxCommands = 20
const maxWait = 360

/** The console commands of a schedule, sent one after the other. */
export function CommandsField({ value, onChange }: { value: string[]; onChange: (commands: string[]) => void }) {
  const list = value.length > 0 ? value : [""]
  return (
    <FieldSet>
      <FieldLegend variant="label">{t("Commands")}</FieldLegend>
      <ol className="grid gap-2">
        {list.map((command, i) => (
          // The commands have no identity of their own; the inputs are controlled.
          <li key={i} className="flex gap-2">
            <Input
              aria-label={t("Command {{number}}", { number: i + 1 })}
              required
              maxLength={1000}
              placeholder={i === 0 ? t("say Vote for us!") : undefined}
              className="font-mono"
              value={command}
              onChange={(e) => onChange(list.with(i, e.target.value))}
            />
            <Button
              type="button"
              size="icon"
              variant="ghost"
              aria-label={t("Remove command {{number}}", { number: i + 1 })}
              disabled={list.length === 1}
              onClick={() => onChange(list.toSpliced(i, 1))}
            >
              <XIcon />
            </Button>
          </li>
        ))}
      </ol>
      <Button
        type="button"
        variant="outline"
        className="w-fit"
        disabled={list.length >= maxCommands}
        onClick={() => onChange([...list, ""])}
      >
        <PlusIcon />
        {t("Add command")}
      </Button>
      <FieldDescription>
        {t("Console commands without a slash, which go one after the other to the game servers, not to proxies.")}
      </FieldDescription>
    </FieldSet>
  )
}

const conditions: { value: Condition; label: string; description: string }[] = [
  { value: "", label: msg("Always"), description: msg("Whether players are online or not.") },
  { value: "empty", label: msg("Only without players"), description: msg("Servers with players online are left alone this time.") },
  {
    value: "wait",
    label: msg("Once the players left"),
    description: msg("Each server waits for its players to leave, and is left alone if they don't in time."),
  },
]

/** Whether servers with players are left alone, and how long they wait for the players to leave. */
export function ConditionField({ value, onChange }: { value: PolicySettings; onChange: (change: Partial<PolicySettings>) => void }) {
  // The radio group can't have an empty value, which is "always".
  const chosen = value.condition || "always"
  return (
    <>
      <RadioGroup
        value={chosen}
        onValueChange={(c) =>
          onChange({ condition: c === "always" ? "" : (c as Condition), wait: c === "wait" ? (value.wait ?? 30) : undefined })
        }
        aria-label={t("Players")}
        className="gap-3 lg:grid-cols-3"
      >
        {conditions.map(({ value: condition, label, description }) => (
          <FieldLabel key={condition || "always"} htmlFor={`condition-${condition || "always"}`}>
            <Field orientation="horizontal" className="items-start">
              <FieldContent>
                <FieldTitle>{t(label)}</FieldTitle>
                <FieldDescription>{t(description)}</FieldDescription>
              </FieldContent>
              <RadioGroupItem id={`condition-${condition || "always"}`} value={condition || "always"} />
            </Field>
          </FieldLabel>
        ))}
      </RadioGroup>
      {value.condition === "wait" && (
        <Field>
          <FieldLabel htmlFor="policy-wait">{t("Wait at most")}</FieldLabel>
          <div className="flex items-center gap-2">
            <Input
              id="policy-wait"
              type="number"
              min={1}
              max={maxWait}
              required
              className="w-24 font-mono"
              value={value.wait ?? 30}
              onChange={(e) => onChange({ wait: e.target.valueAsNumber || 0 })}
            />
            <span className="text-sm text-muted-foreground">{t("minutes")}</span>
          </div>
        </Field>
      )}
      <FieldDescription>
        {t(
          "Players aren't warned then, as nobody is on the servers it acts on. A proxy counts the players of its whole network; game servers that restart server by server in networks restart anyway, as their players move to another server first.",
        )}
      </FieldDescription>
    </>
  )
}

const defaultBackup: JobSettings = { selection: defaultSelection, datastores: [], location: "", keep: 7, keepDays: 0, keepWeeks: 0, keepMonths: 0 }

/** Whether a schedule backs up each server first, and what and where, as a backup job would. */
export function BackupFields({ value, onChange }: { value?: JobSettings; onChange: (backup?: JobSettings) => void }) {
  const { data: nodes = [] } = useQuery(nodesQuery)
  const locations = nodes.flatMap((n) => n.info?.storage.map((l) => l.name) ?? [])
  return (
    <>
      <Field orientation="horizontal">
        <Switch id="policy-backup" checked={!!value} onCheckedChange={(on) => onChange(on ? defaultBackup : undefined)} />
        <FieldContent>
          <FieldLabel htmlFor="policy-backup">{t("Back up first")}</FieldLabel>
          <FieldDescription>
            {t(
              "Each server is backed up before the action, as a backup job would. One that can't be backed up is left alone. With warnings, the servers are backed up while the players are warned.",
            )}
          </FieldDescription>
        </FieldContent>
      </Field>
      {value && (
        <>
          <SelectionField value={value.selection} onChange={(selection) => onChange({ ...value, selection })} />
          <LocationField locations={locations} value={value.location} onChange={(location) => onChange({ ...value, location })} />
          <RetentionField value={value} onChange={(change) => onChange({ ...value, ...change })} />
        </>
      )}
    </>
  )
}
