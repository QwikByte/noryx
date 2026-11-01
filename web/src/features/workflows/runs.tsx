import { CaretRightIcon, ClockCounterClockwiseIcon, StopIcon, UserIcon } from "@phosphor-icons/react"
import { useQuery } from "@tanstack/react-query"
import { t } from "i18next"
import { useState } from "react"
import { toast } from "sonner"
import { ErrorCallout } from "@/components/callout"
import { IconTile } from "@/components/icon-tile"
import { Section } from "@/components/section"
import { Button } from "@/components/ui/button"
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { Field, FieldDescription, FieldGroup, FieldLabel } from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import { Skeleton } from "@/components/ui/skeleton"
import { Switch } from "@/components/ui/switch"
import { Textarea } from "@/components/ui/textarea"
import { formatDateTime, formatElapsed } from "@/lib/format"
import { cn } from "@/lib/utils"
import { type Param, type Run, runQuery, runsQuery, type StepRun, useCancelRun, useRunWorkflow, type Workflow } from "./api"
import { kinds, stepName } from "./catalog"
import { outcomes, startedBy } from "./status"

/** How long a run or step took, in milliseconds while under a second. */
function took(run: Pick<Run, "startedAt" | "endedAt">) {
  const ms = (run.endedAt ? Date.parse(run.endedAt) : Date.now()) - Date.parse(run.startedAt)
  return ms < 1000 ? t("{{ms}} ms", { ms: Math.max(0, ms) }) : formatElapsed(ms)
}

/** The kept runs of a workflow, newest first; each opens with its steps. */
export function Runs({ workflow, manage }: { workflow: Workflow; manage: boolean }) {
  const { data: runs, isPending, error } = useQuery(runsQuery(workflow.id))
  const [open, setOpen] = useState<number>()
  return (
    <Section title={t("Runs")} description={t("The latest 100 runs are kept, with each step they ran.")}>
      {isPending ? (
        <Skeleton className="h-32 rounded-xl" />
      ) : error ? (
        <ErrorCallout error={error} />
      ) : runs.length === 0 ? (
        <p className="surface flex items-center gap-3 rounded-xl px-4 py-5 text-sm text-muted-foreground">
          <ClockCounterClockwiseIcon className="size-5" />
          {t("It hasn't run yet.")}
        </p>
      ) : (
        <ol className="surface divide-y rounded-xl">
          {runs.map((run) => {
            const outcome = outcomes[run.outcome] ?? outcomes.succeeded
            return (
              <li key={run.id}>
                <button type="button" onClick={() => setOpen(run.id)} className="flex w-full items-center gap-3 px-4 py-3 text-left outline-none hover:bg-muted/40 focus-visible:bg-muted/40">
                  <IconTile icon={outcome.icon} tone={outcome.tone} size="sm" className={cn(run.outcome === "running" && "[&>svg]:animate-spin")} />
                  <span className="min-w-0 flex-1">
                    <span className="flex flex-wrap items-center gap-x-3 text-sm">
                      <time dateTime={run.startedAt} className="font-semibold">
                        {formatDateTime(run.startedAt)}
                      </time>
                      <span className="text-xs text-muted-foreground">{[t(outcome.label), startedBy(run), t("took {{time}}", { time: took(run) })].join(" · ")}</span>
                      {run.startedBy && (
                        <span className="flex items-center gap-1 text-xs text-muted-foreground">
                          <UserIcon className="size-3.5" />
                          {run.startedBy}
                        </span>
                      )}
                    </span>
                    {run.error && <span className="line-clamp-2 text-sm break-words text-destructive">{run.error}</span>}
                  </span>
                  <CaretRightIcon className="size-4 text-muted-foreground" />
                </button>
              </li>
            )
          })}
        </ol>
      )}
      {open !== undefined && <RunDialog workflow={workflow} run={open} manage={manage} onClose={() => setOpen(undefined)} />}
    </Section>
  )
}

/** A run with each step it ran, what they told and the data of its trigger; one in progress can be cancelled. */
function RunDialog({ workflow, run: id, manage, onClose }: { workflow: Workflow; run: number; manage: boolean; onClose: () => void }) {
  const { data: run, error } = useQuery(runQuery(workflow.id, id))
  const cancel = useCancelRun(workflow.id)
  const outcome = run && (outcomes[run.outcome] ?? outcomes.succeeded)
  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent className="max-h-[90dvh] overflow-y-auto sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle>{t("Run {{number}} of {{name}}", { number: id, name: workflow.name })}</DialogTitle>
          <DialogDescription>
            {run ? [formatDateTime(run.startedAt), startedBy(run), outcome && t(outcome.label), t("took {{time}}", { time: took(run) })].join(" · ") : " "}
          </DialogDescription>
        </DialogHeader>
        {error && <ErrorCallout error={error} />}
        {run?.error && <p className="rounded-lg bg-destructive/10 p-3 text-sm break-words whitespace-pre-line text-destructive">{run.error}</p>}
        {run?.outcome === "running" && manage && (
          <Button
            type="button"
            variant="outline"
            className="w-fit"
            disabled={cancel.isPending}
            onClick={() => cancel.mutate(id, { onSuccess: () => toast.success(t("Cancelling the run")), onError: (e) => toast.error(e.message) })}
          >
            <StopIcon />
            {t("Cancel run")}
          </Button>
        )}
        {run?.steps && (
          <ol className="grid gap-1.5">
            {run.steps.map((s, i) => (
              // Records have no identity of their own: a step in a loop has several.
              <StepRecord key={i} step={s} />
            ))}
          </ol>
        )}
        {run?.data && (
          <details className="rounded-lg ring-1 ring-border">
            <summary className="cursor-pointer px-3 py-2 text-sm font-medium">{t("Data of the trigger and the inputs")}</summary>
            <pre className="max-h-72 overflow-auto border-t bg-muted/40 p-3 font-mono text-xs">{JSON.stringify(run.data, null, 2)}</pre>
          </details>
        )}
      </DialogContent>
    </Dialog>
  )
}

function StepRecord({ step }: { step: StepRun }) {
  const outcome = outcomes[step.outcome] ?? outcomes.succeeded
  const info = kinds[step.kind]
  return (
    <li className="rounded-lg px-3 py-2 ring-1 ring-border">
      <details>
        <summary className="flex cursor-pointer list-none items-center gap-2 text-sm [&::-webkit-details-marker]:hidden">
          <outcome.icon className={cn("size-4 shrink-0", outcome.color, step.outcome === "running" && "animate-spin")} aria-label={t(outcome.label)} />
          {info && <info.icon className="size-4 shrink-0 text-muted-foreground" aria-hidden />}
          <span className="min-w-0 flex-1 truncate font-medium">
            {stepName(step)}
            {step.index !== undefined && <span className="ml-2 text-xs font-normal text-muted-foreground">{t("item {{number}}", { number: step.index + 1 })}</span>}
          </span>
          <span className="shrink-0 font-mono text-xs text-muted-foreground">{took({ startedAt: step.startedAt, endedAt: step.endedAt })}</span>
        </summary>
        {step.output !== undefined && step.output !== null && (
          <pre className="mt-2 max-h-60 overflow-auto rounded-md bg-muted/40 p-2 font-mono text-xs">{JSON.stringify(step.output, null, 2)}</pre>
        )}
      </details>
      {step.detail && <p className={cn("mt-1 pl-6 text-xs break-words", step.outcome === "failed" ? "text-destructive" : "text-muted-foreground")}>{step.detail}</p>}
    </li>
  )
}

/** Starts a workflow by hand, asking for its inputs if it has any. */
export function RunNowDialog({ workflow, open, onOpenChange }: { workflow: Workflow; open: boolean; onOpenChange: (open: boolean) => void }) {
  const run = useRunWorkflow(workflow.id)
  const [values, setValues] = useState<Record<string, unknown>>({})
  function submit(e: React.FormEvent) {
    e.preventDefault()
    run.mutate(values, {
      onSuccess: () => {
        toast.success(t("Started {{name}}", { name: workflow.name }))
        onOpenChange(false)
        setValues({})
      },
      onError: (err) => toast.error(err.message),
    })
  }
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t("Run {{name}}", { name: workflow.name })}</DialogTitle>
          <DialogDescription>
            {workflow.savedBy
              ? t("It runs with the permissions of {{user}}, who saved it last.", { user: workflow.savedBy })
              : t("The user who saved it last was deleted or disabled, so its steps that need permissions fail. Save it again.")}
          </DialogDescription>
        </DialogHeader>
        <form onSubmit={submit} className="grid gap-5">
          <FieldGroup>
            {workflow.params.map((p) => (
              <ParamField key={p.name} param={p} value={values[p.name]} onChange={(v) => setValues({ ...values, [p.name]: v })} />
            ))}
          </FieldGroup>
          <Button type="submit" disabled={run.isPending} className="justify-self-end">
            {t("Run now")}
          </Button>
        </form>
      </DialogContent>
    </Dialog>
  )
}

function ParamField({ param, value, onChange }: { param: Param; value: unknown; onChange: (v: unknown) => void }) {
  const id = `input-${param.name}`
  if (param.type === "boolean") {
    return (
      <Field orientation="horizontal">
        <Switch id={id} checked={value === undefined ? param.default === "true" : !!value} onCheckedChange={onChange} />
        <FieldLabel htmlFor={id}>{param.name}</FieldLabel>
      </Field>
    )
  }
  const control = { id, required: param.required && !param.default, placeholder: param.default, value: (value as string) ?? "", onChange: (e: { target: { value: string } }) => onChange(e.target.value) }
  return (
    <Field>
      <FieldLabel htmlFor={id}>{param.name}</FieldLabel>
      {param.type === "data" ? <Textarea {...control} className="font-mono text-xs" /> : <Input {...control} type={param.type === "number" ? "number" : "text"} step="any" />}
      {param.description && <FieldDescription>{param.description}</FieldDescription>}
    </Field>
  )
}
