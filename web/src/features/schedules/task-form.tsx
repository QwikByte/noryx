import { useBlocker } from "@tanstack/react-router"
import { t } from "i18next"
import { type FormEvent, type ReactNode, useState } from "react"
import { ConfirmDialog } from "@/components/confirm-dialog"
import { FormSection } from "@/components/form-section"
import { Button } from "@/components/ui/button"
import { Field, FieldContent, FieldDescription, FieldError, FieldLabel } from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import { Switch } from "@/components/ui/switch"
import { msg } from "@/lib/i18n"
import type { TaskInput } from "./api"
import { ScheduleField } from "./schedule-field"
import { TaskTargetsField } from "./targets"

const texts = {
  job: {
    paused: msg("A paused job only runs when you start it."),
    unsaved: msg("Your changes to the job haven't been saved."),
  },
  policy: {
    paused: msg("A paused schedule only runs when you start it."),
    unsaved: msg("Your changes to the schedule haven't been saved."),
  },
}

/** The secret of a task as the form has it: unchanged if undefined, removed if empty. */
export interface SecretInput {
  value?: string
  set: (secret?: string) => void
}

/**
 * Edits a task: its name, schedule and servers, and the settings of its kind, which
 * children renders as further sections, with its secret if it keeps one.
 */
export function TaskForm<S>({
  initial,
  kind,
  submitLabel,
  pending,
  error,
  onSubmit,
  targetsOptional,
  servers = true,
  invalid = false,
  children,
}: {
  initial: TaskInput<S>
  kind: keyof typeof texts
  submitLabel: string
  pending: boolean
  error: Error | null
  onSubmit: (input: TaskInput<S>) => void
  /** Whether a task with the settings may have no servers. */
  targetsOptional?: (settings: S) => boolean
  /** Whether the task runs on servers; the backup job of the master doesn't. */
  servers?: boolean
  /** Whether what the sections hold can't be saved yet, e.g. a passphrase that isn't repeated. */
  invalid?: boolean
  children: (settings: S, set: (change: Partial<S>) => void, secret: SecretInput) => ReactNode
}) {
  const [form, setForm] = useState(initial)
  const dirty = JSON.stringify({ ...form, name: form.name.trim() }) !== JSON.stringify(initial)
  const blocker = useBlocker({ shouldBlockFn: () => dirty && !pending, enableBeforeUnload: () => dirty, withResolver: true })
  const set = (change: Partial<TaskInput<S>>) => setForm({ ...form, ...change })

  function submit(event: FormEvent) {
    event.preventDefault()
    onSubmit({ ...form, name: form.name.trim() })
  }

  return (
    <form onSubmit={submit} className="surface rounded-xl px-5 sm:px-8">
      <FormSection title={t("General")}>
        <Field>
          <FieldLabel htmlFor="task-name">{t("Name")}</FieldLabel>
          <Input id="task-name" required maxLength={64} value={form.name} onChange={(e) => set({ name: e.target.value })} />
        </Field>
        <Field orientation="horizontal">
          <Switch id="task-enabled" checked={form.enabled} onCheckedChange={(enabled) => set({ enabled })} />
          <FieldContent>
            <FieldLabel htmlFor="task-enabled">{t("Active")}</FieldLabel>
            <FieldDescription>{t(texts[kind].paused)}</FieldDescription>
          </FieldContent>
        </Field>
      </FormSection>

      {children(
        form.settings,
        (change) => set({ settings: { ...form.settings, ...change } }),
        { value: form.secret, set: (secret) => set({ secret }) },
      )}

      <FormSection title={t("When")}>
        <ScheduleField value={form.schedule} onChange={(schedule) => set({ schedule })} />
      </FormSection>

      {servers && (
        <FormSection title={t("Servers")}>
          <TaskTargetsField value={form.targets} onChange={(targets) => set({ targets })} />
        </FormSection>
      )}

      <div className="-mx-5 flex flex-wrap-reverse items-center justify-end gap-x-6 gap-y-3 rounded-b-2xl bg-muted/50 px-5 py-4 sm:-mx-8 sm:px-8">
        {error && <FieldError className="mr-auto">{error.message}</FieldError>}
        <Button
          type="submit"
          disabled={!dirty || pending || invalid || (servers && form.targets.length === 0 && !targetsOptional?.(form.settings))}
        >
          {pending ? t("Saving…") : submitLabel}
        </Button>
      </div>
      <ConfirmDialog
        open={blocker.status === "blocked"}
        onOpenChange={(open) => !open && blocker.reset?.()}
        title={t("Discard your changes?")}
        description={t(texts[kind].unsaved)}
        action={t("Discard changes")}
        destructive
        onConfirm={() => blocker.proceed?.()}
      />
    </form>
  )
}
