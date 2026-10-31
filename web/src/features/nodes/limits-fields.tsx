import { t } from "i18next"
import { Field, FieldContent, FieldDescription, FieldError, FieldLabel, FieldLegend, FieldSet } from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import { Switch } from "@/components/ui/switch"
import { formatMegabytes } from "@/lib/format"
import type { ErrorsAt } from "@/lib/use-draft"
import type { LimitsForm } from "./limits"

/** Port range and memory limit of a node; memoryMb is the node's memory, if known. */
export function LimitsFields({
  id,
  form,
  onChange,
  memoryMb,
  errorAt,
}: {
  id: string
  form: LimitsForm
  onChange: (change: Partial<LimitsForm>) => void
  memoryMb?: number
  errorAt?: ErrorsAt
}) {
  const reserve = Number(form.memoryReserveMb) || 0
  // The master refuses the range at its first port.
  const portsInvalid = !!errorAt?.("portMin")
  return (
    <>
      <FieldSet>
        <FieldLegend variant="label">{t("Port range")}</FieldLegend>
        <div className="flex items-center gap-2">
          <Input
            aria-label={t("First port")}
            aria-invalid={portsInvalid}
            type="number"
            min={1024}
            max={65535}
            placeholder="1024"
            className="font-mono"
            value={form.portMin}
            onChange={(e) => onChange({ portMin: e.target.value })}
          />
          <span className="text-muted-foreground">–</span>
          <Input
            aria-label={t("Last port")}
            aria-invalid={portsInvalid}
            type="number"
            min={1024}
            max={65535}
            placeholder="65535"
            className="font-mono"
            value={form.portMax}
            onChange={(e) => onChange({ portMax: e.target.value })}
          />
        </div>
        <FieldDescription>{t("New servers get the first free port of the range. Leave both empty to allow any port.")}</FieldDescription>
        <FieldError errors={errorAt?.("portMin")} />
      </FieldSet>
      <Field orientation="horizontal">
        <Switch id={`${id}-limit`} checked={form.limitMemory} onCheckedChange={(limitMemory) => onChange({ limitMemory })} />
        <FieldContent>
          <FieldLabel htmlFor={`${id}-limit`}>{t("Limit memory")}</FieldLabel>
          <FieldDescription>
            {t("Servers together can't get more memory than the node has, minus a reserve for the system.")}
          </FieldDescription>
        </FieldContent>
      </Field>
      {form.limitMemory && (
        <Field data-invalid={!!errorAt?.("memoryReserveMb")}>
          <FieldLabel htmlFor={`${id}-reserve`}>{t("Reserve in MiB")}</FieldLabel>
          <Input
            id={`${id}-reserve`}
            aria-invalid={!!errorAt?.("memoryReserveMb")}
            type="number"
            min={0}
            className="font-mono sm:w-40"
            value={form.memoryReserveMb}
            onChange={(e) => onChange({ memoryReserveMb: e.target.value })}
          />
          <FieldDescription>
            {memoryMb
              ? `${t("Servers can get up to {{usable}} of the node's {{total}}.", {
                  usable: formatMegabytes(Math.max(0, memoryMb - reserve)),
                  total: formatMegabytes(memoryMb),
                })} `
              : ""}
            {t("Each server counts with Java's overhead on top of its memory: about a quarter more, plus 256 MiB.")}
          </FieldDescription>
          <FieldError errors={errorAt?.("memoryReserveMb")} />
        </Field>
      )}
    </>
  )
}
