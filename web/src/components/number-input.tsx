import type { ReactNode } from "react"
import { Field, FieldDescription, FieldLabel } from "@/components/ui/field"
import { Input } from "@/components/ui/input"

interface NumberInputProps {
  id: string
  min: number
  max: number
  /** What the number counts, shown after it, e.g. "hours". */
  unit: string
  value: number
  onChange: (value: number) => void
  disabled?: boolean
}

/** A number within bounds, followed by its unit. An empty input counts as 0, which bounds above 0 refuse. */
export function NumberInput({ id, min, max, unit, value, onChange, disabled }: NumberInputProps) {
  return (
    <div className="flex items-center gap-2">
      <Input
        id={id}
        type="number"
        required
        min={min}
        max={max}
        disabled={disabled}
        className="font-mono sm:w-32"
        value={value}
        onChange={(e) => onChange(e.target.valueAsNumber || 0)}
      />
      <span className="text-sm text-muted-foreground">{unit}</span>
    </div>
  )
}

/** A NumberInput with its label and what it means, if anything. */
export function NumberField({ label, children, ...input }: NumberInputProps & { label: string; children?: ReactNode }) {
  return (
    <Field>
      <FieldLabel htmlFor={input.id}>{label}</FieldLabel>
      <NumberInput {...input} />
      {children && <FieldDescription>{children}</FieldDescription>}
    </Field>
  )
}
