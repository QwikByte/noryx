import { Input } from "@/components/ui/input"

/** A required whole number between min and max, with its unit after it. */
export function NumberInput({
  id,
  min,
  max,
  unit,
  value,
  onChange,
  invalid,
}: {
  id: string
  min: number
  max: number
  unit: string
  value: number
  onChange: (value: number) => void
  /** Marks the value as refused, e.g. by the master. */
  invalid?: boolean
}) {
  return (
    <div className="flex items-center gap-2">
      <Input
        id={id}
        type="number"
        required
        min={min}
        max={max}
        aria-invalid={invalid}
        className="font-mono sm:w-32"
        value={value}
        onChange={(e) => onChange(e.target.valueAsNumber || 0)}
      />
      <span className="text-sm text-muted-foreground">{unit}</span>
    </div>
  )
}
