import { ArrowClockwiseIcon, StopIcon } from "@phosphor-icons/react"
import { useQuery } from "@tanstack/react-query"
import { t } from "i18next"
import { type FormEvent, useState } from "react"
import { Segmented } from "@/components/segmented"
import { Button } from "@/components/ui/button"
import { Dialog, DialogClose, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { Field, FieldContent, FieldDescription, FieldLabel } from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import { Switch } from "@/components/ui/switch"
import { type Warning, warningsQuery } from "./api"
import { repeats, warningHint, warningKinds } from "./warnings"

/** The usual lead times of warnings up to the longest one of the settings, which is offered too. */
const leadTimes = (max: number) => [...new Set([...[1, 2, 5, 10, 15, 30, 60].filter((m) => m < max), max])].map(String)

/**
 * Confirms that servers restart or stop, which can warn their players first, as the settings say: some minutes before,
 * and again at the steps of the settings. Proxies get no warning.
 */
export function PowerDialog({
  action,
  title,
  description,
  warnable,
  canMessage,
  warnFirst = false,
  onConfirm,
  onOpenChange,
}: {
  action: "restart" | "stop"
  title: string
  description: string
  /** Whether game servers run among them, whose players can be warned. */
  warnable: boolean
  /** Whether the user may write the warning, which needs the permission to send console commands. */
  canMessage: boolean
  /** Whether the warning is on when the dialog opens. */
  warnFirst?: boolean
  onConfirm: (warning?: Warning) => void
  onOpenChange: (open: boolean) => void
}) {
  const [warn, setWarn] = useState(warnFirst && warnable)
  const { data: how } = useQuery(warningsQuery)
  const [before, setBefore] = useState<string>()
  const [message, setMessage] = useState("")
  const stop = action === "stop"
  const Icon = stop ? StopIcon : ArrowClockwiseIcon
  const count = Number(before ?? Math.min(5, how?.maxMinutes ?? 5))
  const label = stop
    ? warn
      ? t("Stop in {{count}} min", { count })
      : t("Stop")
    : warn
      ? t("Restart in {{count}} min", { count })
      : t("Restart")

  function submit(event: FormEvent) {
    event.preventDefault()
    onConfirm(warn ? { minutes: count, message: message.trim() || undefined } : undefined)
    onOpenChange(false)
  }

  return (
    <Dialog open onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-lg">
        <form onSubmit={submit} className="grid gap-6">
          <DialogHeader>
            <DialogTitle>{title}</DialogTitle>
            <DialogDescription>{description}</DialogDescription>
          </DialogHeader>
          {warnable && (
            <div className="grid gap-4">
              <Field orientation="horizontal">
                <Switch id="power-warn" checked={warn} onCheckedChange={setWarn} />
                <FieldContent>
                  <FieldLabel htmlFor="power-warn">{t("Warn the players first")}</FieldLabel>
                  <FieldDescription>
                    {[how && t(warningKinds[how.kind].shown), how && repeats(how.steps.filter((m) => m < count)), t("Until then, it can be cancelled.")]
                      .filter(Boolean)
                      .join(" ")}
                  </FieldDescription>
                </FieldContent>
              </Field>
              {warn && how && (
                <>
                  <div className="flex flex-wrap items-center gap-3 text-sm">
                    <span>{t("Minutes before")}</span>
                    <Segmented
                      label={t("Minutes before")}
                      value={String(count)}
                      onChange={setBefore}
                      options={leadTimes(how.maxMinutes).map((m) => ({ value: m, label: m }))}
                    />
                  </div>
                  {canMessage && (
                    <Field>
                      <FieldLabel htmlFor="power-message">{t("Warning")}</FieldLabel>
                      <Input
                        id="power-message"
                        maxLength={200}
                        placeholder={how[action]}
                        value={message}
                        onChange={(e) => setMessage(e.target.value)}
                      />
                      <FieldDescription>{warningHint(how)}</FieldDescription>
                    </Field>
                  )}
                </>
              )}
            </div>
          )}
          <DialogFooter>
            <DialogClose asChild>
              <Button variant="outline">{t("Cancel")}</Button>
            </DialogClose>
            <Button type="submit" variant={stop ? "destructive" : "default"}>
              <Icon />
              {label}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
