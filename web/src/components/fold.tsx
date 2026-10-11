import { CaretRightIcon } from "@phosphor-icons/react"
import { type ReactNode, useId, useState } from "react"
import { cn } from "@/lib/utils"

/** Fields of a form that fold away behind their title and a summary of their values, e.g. in a dialog. */
export function Fold({ title, summary, className, children }: { title: string; summary: string; className?: string; children: ReactNode }) {
  const [open, setOpen] = useState(false)
  const id = useId()
  return (
    <div className="grid grid-cols-1 gap-4">
      <button
        type="button"
        aria-expanded={open}
        aria-controls={id}
        onClick={() => setOpen(!open)}
        className="flex items-center gap-2 rounded-md text-left text-sm outline-none focus-visible:ring-2 focus-visible:ring-ring"
      >
        <CaretRightIcon className={cn("size-4 shrink-0 transition-transform motion-reduce:transition-none", open && "rotate-90")} />
        <span className="shrink-0 font-medium">{title}</span>
        <span className="truncate text-muted-foreground">{summary}</span>
      </button>
      {open && (
        <div id={id} className={cn("grid gap-4", className)}>
          {children}
        </div>
      )}
    </div>
  )
}
