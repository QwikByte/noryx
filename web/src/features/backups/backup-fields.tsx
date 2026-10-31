import { FolderOpenIcon, XIcon } from "@phosphor-icons/react"
import { t } from "i18next"
import { useState } from "react"
import { Trans } from "react-i18next"
import { Button } from "@/components/ui/button"
import { Checkbox } from "@/components/ui/checkbox"
import { Field, FieldContent, FieldDescription, FieldError, FieldLabel, FieldLegend, FieldSet, FieldTitle } from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import type { ServerFiles } from "@/features/files/api"
import { PickDialog } from "@/features/files/path-picker"
import { msg } from "@/lib/i18n"
import type { ErrorsAt, FieldErrors } from "@/lib/use-draft"
import { type JobSettings, pathsError, type Selection } from "./api"

const choices: [key: keyof Omit<Selection, "paths" | "exclude">, label: string, description: string][] = [
  ["everything", msg("Everything"), msg("The whole folder of the server, including the server software.")],
  ["worlds", msg("Worlds"), msg("All worlds, including those added later.")],
  ["plugins", msg("Plugins and mods"), msg("With their settings.")],
  ["config", msg("Configuration"), msg("server.properties, whitelist and other settings, without jars and logs.")],
]

/** Chooses what of a server, or of the servers of a job, is backed up; errorAt tells what the master refused of its paths. */
export function SelectionField({
  value,
  server,
  onChange,
  errorAt,
}: {
  value: Selection
  server?: ServerFiles
  onChange: (selection: Selection) => void
  errorAt?: ErrorsAt
}) {
  return (
    <>
      <FieldSet>
        <FieldLegend variant="label">{t("What to back up")}</FieldLegend>
        <div className="grid gap-3 sm:grid-cols-2">
          {choices.map(([key, label, description]) => {
            const implied = key !== "everything" && value.everything
            return (
              <FieldLabel key={key} htmlFor={`selection-${key}`}>
                <Field orientation="horizontal" data-disabled={implied}>
                  <Checkbox
                    id={`selection-${key}`}
                    checked={value[key] || implied}
                    disabled={implied}
                    onCheckedChange={(on) => onChange({ ...value, [key]: on === true })}
                  />
                  <FieldContent>
                    <FieldTitle>{t(label)}</FieldTitle>
                    <FieldDescription>{t(description)}</FieldDescription>
                  </FieldContent>
                </Field>
              </FieldLabel>
            )
          })}
        </div>
      </FieldSet>
      {!value.everything && (
        <PathsField
          id="selection-path"
          label={t("More files and folders")}
          description={t("Choose them on a server, or type a path. Paths a server doesn't have are skipped.")}
          pick={t("Files and folders to back up")}
          example="plugins/LuckPerms"
          paths={value.paths}
          server={server}
          onChange={(paths) => onChange({ ...value, paths })}
          errors={errorAt?.("paths")}
        />
      )}
      <PathsField
        id="selection-exclude"
        label={t("Leave out")}
        description={t("Folders that are big and easy to replace, like the tiles of a map plugin or logs. Restoring leaves them as they are.")}
        pick={t("Files and folders to leave out")}
        example="plugins/dynmap/web/tiles"
        paths={value.exclude ?? []}
        server={server}
        onChange={(exclude) => onChange({ ...value, exclude })}
        errors={errorAt?.("exclude")}
      />
    </>
  )
}

/** Files and folders of a selection: picked on a server, or typed for those other servers have. */
function PathsField({
  id,
  label,
  description,
  pick,
  example,
  paths,
  server,
  onChange,
  errors,
}: {
  id: string
  label: string
  description: string
  /** The title of the dialog to pick them in. */
  pick: string
  /** An example in the empty field, which needs no translation. */
  example: string
  paths: string[]
  server?: ServerFiles
  onChange: (paths: string[]) => void
  /** What the master refused of the paths. */
  errors?: FieldErrors
}) {
  const [browsing, setBrowsing] = useState(false)
  const [typed, setTyped] = useState("")
  const invalid = pathsError(paths)
  const error = invalid ? [{ message: invalid }] : errors
  const add = (more: string[]) => onChange([...new Set([...paths, ...more])])
  const addTyped = () => {
    if (typed.trim()) add([typed.trim().replace(/^\/+|\/+$/g, "")])
    setTyped("")
  }
  return (
    <Field data-invalid={!!error}>
      <FieldLabel htmlFor={id}>{label}</FieldLabel>
      {paths.length > 0 && (
        <ul aria-label={label} className="flex flex-wrap gap-1.5">
          {paths.map((p) => (
            <li key={p} className="flex items-center gap-1 rounded-md bg-muted py-0.5 pr-0.5 pl-2 font-mono text-xs">
              {p}
              <button
                type="button"
                aria-label={t("Remove {{name}}", { name: p })}
                onClick={() => onChange(paths.filter((other) => other !== p))}
                className="grid size-5 place-items-center rounded text-muted-foreground hover:bg-background hover:text-foreground"
              >
                <XIcon className="size-3" />
              </button>
            </li>
          ))}
        </ul>
      )}
      <div className="flex flex-wrap gap-2">
        <Button type="button" variant="outline" size="sm" onClick={() => setBrowsing(true)}>
          <FolderOpenIcon />
          {t("Browse…")}
        </Button>
        <Input
          id={id}
          className="h-8 max-w-64 font-mono text-xs"
          placeholder={example}
          aria-invalid={!!error}
          value={typed}
          onChange={(e) => setTyped(e.target.value)}
          onBlur={addTyped}
          onKeyDown={(e) => {
            if (e.key !== "Enter") return
            e.preventDefault()
            addTyped()
          }}
        />
      </div>
      {error ? <FieldError errors={error} /> : <FieldDescription>{description}</FieldDescription>}
      {browsing && (
        <PickDialog
          title={pick}
          description={!server && t("Browse one of the servers; the others are backed up with the same paths.")}
          server={server}
          action={t("Add")}
          onClose={() => setBrowsing(false)}
          onPick={(_, picked) => add(picked.map((p) => p.path))}
        />
      )}
    </Field>
  )
}

type Retention = "keep" | "keepDays" | "keepWeeks" | "keepMonths"

const retention: [key: Retention, label: string][] = [
  ["keep", msg("Newest")],
  ["keepDays", msg("Daily")],
  ["keepWeeks", msg("Weekly")],
  ["keepMonths", msg("Monthly")],
]

/** Chooses which backups a job keeps: its newest ones, and the newest of each of the last days, weeks and months. */
export function RetentionField({ value, onChange }: { value: JobSettings; onChange: (change: Partial<JobSettings>) => void }) {
  return (
    <FieldSet>
      <FieldLegend variant="label">{t("Backups to keep")}</FieldLegend>
      <div className="grid grid-cols-2 gap-3 sm:grid-cols-4">
        {retention.map(([key, label]) => (
          <Field key={key}>
            <FieldLabel htmlFor={`job-${key}`}>{t(label)}</FieldLabel>
            <Input
              id={`job-${key}`}
              type="number"
              min={0}
              max={1000}
              className="font-mono"
              value={value[key] ?? 0}
              onChange={(e) => onChange({ [key]: e.target.valueAsNumber || 0 } as Partial<JobSettings>)}
            />
          </Field>
        ))}
      </div>
      <FieldDescription>
        {t(
          "Per server and datastore, the job keeps its newest backups, and the newest of each of the last days, weeks and months that have backups, in its time zone. It deletes the others; with 0 everywhere, it keeps all. Backups made by hand and kept ones are never deleted.",
        )}
      </FieldDescription>
    </FieldSet>
  )
}

/** Chooses the storage location for backups among those of the nodes; empty means the default one. */
export function LocationField({
  locations,
  value,
  onChange,
}: {
  locations: string[]
  value: string
  onChange: (location: string) => void
}) {
  const options = [...new Set(["default", ...locations, value || "default"])]
  return (
    <Field>
      <FieldLabel htmlFor="backup-location">{t("Storage location")}</FieldLabel>
      <Select value={value || "default"} onValueChange={(v) => onChange(v === "default" ? "" : v)}>
        <SelectTrigger id="backup-location" className="w-full sm:w-64">
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          {options.map((name) => (
            <SelectItem key={name} value={name}>
              {name}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
      <FieldDescription>
        <Trans
          i18nKey="Backups stay on the server's node, out of the server's reach. The node's administrator adds locations, e.g. on another disk, with <command/>."
          components={{ command: <code className="font-mono text-xs">noryx-agent storage add</code> }}
        />
      </FieldDescription>
    </Field>
  )
}
