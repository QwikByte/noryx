import { useQuery } from "@tanstack/react-query"
import { useBlocker } from "@tanstack/react-router"
import { t } from "i18next"
import { type FormEvent, useState } from "react"
import { Trans } from "react-i18next"
import { toast } from "sonner"
import { ErrorCallout } from "@/components/callout"
import { ConfirmDialog } from "@/components/confirm-dialog"
import { FormSection } from "@/components/form-section"
import { Button } from "@/components/ui/button"
import { Checkbox } from "@/components/ui/checkbox"
import { Field, FieldContent, FieldDescription, FieldError, FieldLabel } from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import { Skeleton } from "@/components/ui/skeleton"
import { Switch } from "@/components/ui/switch"
import { msg } from "@/lib/i18n"
import type { ErrorsAt } from "@/lib/use-draft"
import { cn } from "@/lib/utils"
import { type Measure, type Threshold, type Thresholds, type ThresholdsView, thresholdsQuery, useUpdateThresholds } from "./api"

/** The measures of servers and nodes, and what their values are relative to. The labels are translated with t. */
const measures: Record<"servers" | "nodes", { measure: Measure; label: string; description: string }[]> = {
  servers: [
    { measure: "cpu", label: msg("CPU"), description: msg("In percent of its CPU limit, or of all cores of its node if it has none.") },
    { measure: "memory", label: msg("Memory"), description: msg("In percent of its memory limit, including Java's overhead.") },
    {
      measure: "tps",
      label: msg("Ticks per second"),
      description: msg("Over the last minute, 20 at best. Only Paper and its forks except Folia tell them."),
    },
  ],
  nodes: [
    { measure: "cpu", label: msg("CPU"), description: msg("In percent of all its cores.") },
    { measure: "memory", label: msg("Memory"), description: msg("In percent of its memory, without the page cache.") },
    { measure: "storage", label: msg("Storage"), description: msg("In percent of each of its storage locations.") },
  ],
}

/** What the checks of thresholds do. */
export function ThresholdsHelp() {
  return (
    <p className="text-sm text-muted-foreground">
      {t(
        "The master checks every minute and logs a warning once a value has stayed beyond its threshold for as many minutes (0 warns at once), and an entry once it is fine again. Warnings show up in the bell and under Needs attention on the overview.",
      )}
    </p>
  )
}

/** The thresholds of servers or nodes. With defaults, a measure uses its default while value has none for it. */
export function ThresholdFields({
  id,
  kind,
  value,
  defaults,
  onChange,
  errorAt,
}: {
  id: string
  kind: "servers" | "nodes"
  value: Thresholds
  defaults?: Thresholds
  onChange: (value: Thresholds) => void
  errorAt?: ErrorsAt
}) {
  return (
    <ul className="divide-y rounded-xl border">
      {measures[kind].map(({ measure, label, description }) => {
        const own = value[measure]
        const threshold = own ?? defaults?.[measure]
        if (!threshold) return null
        const set = (next?: Threshold) => onChange({ ...value, [measure]: next })
        const change = (c: Partial<Threshold>) => set({ ...threshold, ...c })
        const fixed = !!defaults && !own
        const key = `${id}-${measure}`
        const errors = errorAt?.(measure)
        const inputs = {
          value: (
            <Input
              type="number"
              required
              min={1}
              max={measure === "tps" ? 20 : 100}
              step="any"
              disabled={fixed}
              aria-invalid={!!errors}
              aria-label={t("Threshold of {{measure}}", { measure: t(label) })}
              className="w-20 font-mono"
              value={threshold.value}
              onChange={(e) => change({ value: e.target.valueAsNumber || 0 })}
            />
          ),
          minutes: (
            <Input
              type="number"
              required
              min={0}
              max={1440}
              disabled={fixed}
              aria-invalid={!!errors}
              aria-label={t("Minutes of {{measure}}", { measure: t(label) })}
              className="w-20 font-mono"
              value={threshold.minutes}
              onChange={(e) => change({ minutes: e.target.valueAsNumber || 0 })}
            />
          ),
        }
        return (
          <li key={measure} className="space-y-3 p-4">
            <div className="flex flex-wrap items-start justify-between gap-x-6 gap-y-3">
              <Field orientation="horizontal" className="w-auto min-w-0 flex-[1_1_14rem]">
                <Switch id={`${key}-on`} checked={!threshold.off} disabled={fixed} onCheckedChange={(on) => change({ off: !on })} />
                <FieldContent>
                  <FieldLabel htmlFor={`${key}-on`}>{t(label)}</FieldLabel>
                  <FieldDescription>{t(description)}</FieldDescription>
                </FieldContent>
              </Field>
              {defaults && (
                <Field orientation="horizontal" className="w-auto">
                  <Checkbox
                    id={`${key}-default`}
                    checked={fixed}
                    onCheckedChange={(checked) => set(checked ? undefined : { ...threshold })}
                  />
                  <FieldLabel htmlFor={`${key}-default`}>{t("Use default")}</FieldLabel>
                </Field>
              )}
            </div>
            {threshold.off ? (
              <p className="text-sm text-muted-foreground">{t("Doesn't warn.")}</p>
            ) : (
              <p className="flex flex-wrap items-center gap-2 text-sm text-muted-foreground">
                {measure === "tps" ? (
                  <Trans i18nKey="Warns below <value/> for <minutes/> minutes" components={inputs} />
                ) : (
                  <Trans i18nKey="Warns at <value/> % or more for <minutes/> minutes" components={inputs} />
                )}
              </p>
            )}
            <FieldError errors={errors} />
          </li>
        )
      })}
    </ul>
  )
}

/** The thresholds of a node, or of one of its servers, which only those who may change it change. */
export function UsageWarnings({
  nodeId,
  serverId,
  editable,
  className,
}: {
  nodeId: string
  serverId?: string
  editable: boolean
  className?: string
}) {
  const { data, error } = useQuery(thresholdsQuery(nodeId, serverId))
  if (error) return <ErrorCallout error={error} className={className} />
  if (!data) return <Skeleton className={cn("h-64 rounded-xl", className)} />
  // Remounting on save resets the form to what the master stored.
  return (
    <ThresholdsForm key={JSON.stringify(data.own)} nodeId={nodeId} serverId={serverId} view={data} editable={editable} className={className} />
  )
}

function ThresholdsForm({
  nodeId,
  serverId,
  view,
  editable,
  className,
}: {
  nodeId: string
  serverId?: string
  view: ThresholdsView
  editable: boolean
  className?: string
}) {
  const [own, setOwn] = useState(view.own)
  const update = useUpdateThresholds(nodeId, serverId)
  const dirty = JSON.stringify(own) !== JSON.stringify(view.own)
  const blocker = useBlocker({ shouldBlockFn: () => dirty && !update.isPending, enableBeforeUnload: () => dirty, withResolver: true })

  function submit(event: FormEvent) {
    event.preventDefault()
    toast.promise(update.mutateAsync(own), {
      loading: t("Saving…"),
      success: t("Saved the usage warnings"),
      error: (e: Error) => e.message,
    })
  }

  return (
    <form onSubmit={submit} className={cn("surface rounded-xl px-5 sm:px-8", className)}>
      <fieldset disabled={!editable} className="contents">
        <FormSection title={t("Usage warnings")}>
          <ThresholdsHelp />
          <ThresholdFields
            id={serverId ? "server-thresholds" : "node-thresholds"}
            kind={serverId ? "servers" : "nodes"}
            value={own}
            defaults={view.defaults}
            onChange={setOwn}
          />
        </FormSection>
      </fieldset>
      <div
        className="-mx-5 flex flex-wrap-reverse items-center justify-end gap-x-6 gap-y-3 rounded-b-2xl bg-muted/50 px-5 py-4 sm:-mx-8 sm:px-8"
        hidden={!editable}
      >
        <p className="text-sm text-muted-foreground">
          {t("Applies from the next check, without a restart. The defaults are in the settings of the master.")}
        </p>
        <Button type="submit" disabled={!dirty || update.isPending}>
          {update.isPending ? t("Saving…") : t("Save warnings")}
        </Button>
      </div>
      <ConfirmDialog
        open={blocker.status === "blocked"}
        onOpenChange={(open) => !open && blocker.reset?.()}
        title={t("Discard your changes?")}
        description={t("Your changes to the usage warnings haven't been saved.")}
        action={t("Discard changes")}
        destructive
        onConfirm={() => blocker.proceed?.()}
      />
    </form>
  )
}
