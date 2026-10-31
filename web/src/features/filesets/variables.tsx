import { CheckCircleIcon, PencilSimpleIcon, PlusIcon, SlidersHorizontalIcon, TrashIcon, WarningIcon, XIcon } from "@phosphor-icons/react"
import { useQuery } from "@tanstack/react-query"
import { t } from "i18next"
import { type FormEvent, useState } from "react"
import { Button } from "@/components/ui/button"
import { Dialog, DialogClose, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { Field, FieldDescription, FieldError, FieldLabel } from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { networksQuery } from "@/features/networks/api"
import { allServersQuery } from "@/features/servers/api"
import { msg } from "@/lib/i18n"
import type { FieldErrors } from "@/lib/use-draft"
import { type SetFile, type Value, type ValueKind, type Variable, usedVariables } from "./api"
import { PanelSection } from "./panel-section"

// An example in an empty field, which needs no translation.
const exampleName = "role"

const namePattern = /^[a-z0-9][a-z0-9_-]{0,63}$/
// A single line without quotes, backslashes and braces, as the master checks it.
const valuePattern = /^[^"'`\\{}\p{C}\p{Zl}\p{Zp}]+$/u
const maxValue = 128

const kinds: [ValueKind, string][] = [
  ["all", msg("All servers")],
  ["tag", msg("Servers with a tag")],
  ["network", msg("Servers of a network")],
  ["server", msg("A server")],
]

/** The variables of a set and those its files use, with how many values each has; they are saved with the set. */
export function VariablesSection({
  variables,
  files,
  editable,
  onChange,
  errors,
}: {
  variables: Variable[]
  files: SetFile[]
  editable: boolean
  onChange: (variables: Variable[]) => void
  /** What the master refused of the variables. */
  errors?: FieldErrors
}) {
  const [editing, setEditing] = useState<{ name?: string }>()
  const used = usedVariables(files)
  const names = [...new Set([...variables.map((v) => v.name), ...used])].sort()

  return (
    <PanelSection
      icon={SlidersHorizontalIcon}
      title={t("Variables")}
      action={
        editable && (
          <Button size="icon-sm" variant="ghost" aria-label={t("Add variable")} title={t("Add variable")} onClick={() => setEditing({})}>
            <PlusIcon />
          </Button>
        )
      }
    >
      {names.length === 0 ? (
        <p className="text-sm text-muted-foreground">
          {t("For what differs between servers, e.g. their role: use {{placeholder}} in a file and give it a value for servers, networks, tags or all servers.", {
            placeholder: "{{var:<name>}}",
          })}
        </p>
      ) : (
        <ul className="grid gap-1">
          {names.map((name) => {
            const variable = variables.find((v) => v.name === name)
            const count = variable?.values.length ?? 0
            return (
              <li key={name} className="flex items-center gap-2 rounded-lg py-1 pr-1 pl-2 ring-1 ring-border">
                {count > 0 ? (
                  <CheckCircleIcon className="size-4 shrink-0 text-success" weight="fill" aria-label={t("Has values")} />
                ) : (
                  <WarningIcon className="size-4 shrink-0 text-warning" weight="fill" aria-label={t("No value yet")} />
                )}
                <span className="min-w-0 flex-1">
                  <span className="block truncate font-mono text-xs">{name}</span>
                  <span className="block text-xs text-muted-foreground">
                    {[count > 0 ? t("{{count}} values", { count, defaultValue_one: "{{count}} value" }) : t("No value yet"), !used.includes(name) && t("unused")]
                      .filter(Boolean)
                      .join(" · ")}
                  </span>
                </span>
                {editable && (
                  <>
                    <Button size="icon-sm" variant="ghost" aria-label={t("Edit the values of {{name}}", { name })} title={t("Edit values")} onClick={() => setEditing({ name })}>
                      <PencilSimpleIcon />
                    </Button>
                    {variable && (
                      <Button
                        size="icon-sm"
                        variant="ghost"
                        aria-label={t("Delete {{name}}", { name })}
                        className="text-muted-foreground hover:bg-destructive/10 hover:text-destructive"
                        onClick={() => onChange(variables.filter((v) => v.name !== name))}
                      >
                        <TrashIcon />
                      </Button>
                    )}
                  </>
                )}
              </li>
            )
          })}
        </ul>
      )}
      <p className="text-xs text-muted-foreground">
        {t("A server gets its own value, else its network's, else that of the first of its tags in alphabetical order, else the one for all servers.")}
      </p>
      <FieldError errors={errors} />
      {editing && (
        <VariableDialog
          variable={editing.name ? (variables.find((v) => v.name === editing.name) ?? { name: editing.name, values: [] }) : undefined}
          taken={variables.map((v) => v.name)}
          onClose={() => setEditing(undefined)}
          onSave={(saved) => onChange([...variables.filter((v) => v.name !== saved.name), saved])}
        />
      )}
    </PanelSection>
  )
}

/** Edits the values of a variable, which is named first if it is new. */
export function VariableDialog({
  variable,
  taken,
  onClose,
  onSave,
}: {
  variable?: Variable
  taken: string[]
  onClose: () => void
  onSave: (variable: Variable) => void
}) {
  const [name, setName] = useState(variable?.name ?? "")
  const [values, setValues] = useState<Value[]>(variable?.values.length ? variable.values : [{ kind: "all", value: "" }])
  const [error, setError] = useState<string>()
  const change = (i: number, next: Partial<Value>) => setValues(values.map((v, j) => (j === i ? { ...v, ...next } : v)))

  function submit(event: FormEvent) {
    event.preventDefault()
    const trimmed = values.map((v) => ({ ...v, value: v.value.trim() }))
    if (!namePattern.test(name)) return setError(t("Variable names have up to 64 lower-case letters, digits, - and _."))
    if (!variable && taken.includes(name)) return setError(t("The set has a variable named {{name}} already.", { name }))
    if (trimmed.some((v) => v.kind !== "all" && !v.scope)) return setError(t("Choose the servers of each value."))
    if (trimmed.some((v) => !valuePattern.test(v.value) || [...v.value].length > maxValue))
      return setError(t("A value is a single line of up to {{max}} characters without quotes, backslashes and braces.", { max: maxValue }))
    onSave({ name, values: trimmed })
    onClose()
  }

  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent className="sm:max-w-2xl">
        <form onSubmit={submit} className="grid gap-6" autoComplete="off">
          <DialogHeader>
            <DialogTitle>{variable ? t("Values of {{name}}", { name: variable.name }) : t("Add variable")}</DialogTitle>
            <DialogDescription>
              {t("A server gets its own value, else its network's, else that of the first of its tags in alphabetical order, else the one for all servers. Changes are saved with the set.")}
            </DialogDescription>
          </DialogHeader>
          {!variable && (
            <Field>
              <FieldLabel htmlFor="variable-name">{t("Name")}</FieldLabel>
              <Input id="variable-name" className="font-mono" autoFocus required placeholder={exampleName} value={name} onChange={(e) => setName(e.target.value)} />
              <FieldDescription>{t("Files use it as {{placeholder}}.", { placeholder: `{{var:${name || "name"}}}` })}</FieldDescription>
            </Field>
          )}
          <div className="grid gap-2">
            {values.map((v, i) => (
              <ValueRow key={i} value={v} index={i} onChange={(next) => change(i, next)} onRemove={() => setValues(values.filter((_, j) => j !== i))} />
            ))}
            <Button type="button" variant="ghost" size="sm" className="justify-self-start" onClick={() => setValues([...values, { kind: "tag", scope: "", value: "" }])}>
              <PlusIcon />
              {t("Add value")}
            </Button>
            {error && <FieldError>{error}</FieldError>}
          </div>
          <DialogFooter>
            <DialogClose asChild>
              <Button variant="outline">{t("Cancel")}</Button>
            </DialogClose>
            <Button type="submit">{t("Done")}</Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

/** A value of a variable with the servers it is for. */
function ValueRow({ value: v, index, onChange, onRemove }: { value: Value; index: number; onChange: (next: Partial<Value>) => void; onRemove: () => void }) {
  const { data: networks } = useQuery(networksQuery)
  const { data: servers } = useQuery(allServersQuery)
  const tags = [...new Set(servers?.flatMap((s) => s.tags))].sort()
  const scopeLabel = t("Servers of value {{number}}", { number: index + 1 })
  // On phones, the kind and the button to remove share a line, and the servers and the value take one each.
  const wide = "col-span-2 sm:col-span-1"
  return (
    <div className="grid grid-cols-[minmax(0,1fr)_auto] items-center gap-2 rounded-lg p-2 ring-1 ring-border sm:grid-cols-[10rem_minmax(0,1fr)_minmax(0,1fr)_auto]">
      <Button type="button" size="icon-sm" variant="ghost" aria-label={t("Remove value {{number}}", { number: index + 1 })} className="col-start-2 row-start-1 sm:col-start-4" onClick={onRemove}>
        <XIcon />
      </Button>
      <Select value={v.kind} onValueChange={(kind) => onChange({ kind: kind as ValueKind, scope: "" })}>
        <SelectTrigger aria-label={t("Kind of value {{number}}", { number: index + 1 })} className="col-start-1 row-start-1 w-full">
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          {kinds.map(([kind, label]) => (
            <SelectItem key={kind} value={kind}>
              {t(label)}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
      {v.kind === "all" ? (
        <span className="hidden text-sm text-muted-foreground sm:block">{t("unless another value is for them")}</span>
      ) : v.kind === "tag" ? (
        <>
          <Input
            aria-label={scopeLabel}
            list="variable-tags"
            placeholder={t("Tag")}
            className={wide}
            value={v.scope ?? ""}
            onChange={(e) => onChange({ scope: e.target.value })}
          />
          <datalist id="variable-tags">
            {tags.map((tag) => (
              <option key={tag} value={tag} />
            ))}
          </datalist>
        </>
      ) : (
        <Select value={v.scope ?? ""} onValueChange={(scope) => onChange({ scope })}>
          <SelectTrigger aria-label={scopeLabel} className={`${wide} w-full min-w-0`}>
            <SelectValue placeholder={v.kind === "network" ? t("Choose a network") : t("Choose a server")} />
          </SelectTrigger>
          <SelectContent>
            {v.kind === "network"
              ? networks?.map((n) => (
                  <SelectItem key={n.id} value={n.id}>
                    {n.name}
                  </SelectItem>
                ))
              : servers
                  ?.toSorted((a, b) => a.name.localeCompare(b.name))
                  .map((s) => (
                    <SelectItem key={s.id} value={s.id}>
                      {s.name} · {s.nodeName}
                    </SelectItem>
                  ))}
            {v.scope && !(v.kind === "network" ? networks?.some((n) => n.id === v.scope) : servers?.some((s) => s.id === v.scope)) && (
              <SelectItem value={v.scope}>{v.kind === "network" ? t("A deleted network") : t("A deleted server")}</SelectItem>
            )}
          </SelectContent>
        </Select>
      )}
      <Input
        aria-label={t("Value {{number}}", { number: index + 1 })}
        placeholder={t("Value")}
        maxLength={maxValue}
        required
        className={wide}
        value={v.value}
        onChange={(e) => onChange({ value: e.target.value })}
      />
    </div>
  )
}
