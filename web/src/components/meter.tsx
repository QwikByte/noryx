import { motion, useReducedMotionConfig } from "motion/react"
import { cn } from "@/lib/utils"

/** A bar for how much of something is used; it fills up when it shows and turns amber and then red as it fills up. */
export function Meter({ value, label, className }: { value: number; label: string; className?: string }) {
  const used = Math.min(Math.max(value, 0), 1)
  const reduced = useReducedMotionConfig()
  return (
    <div
      role="meter"
      aria-label={label}
      aria-valuemin={0}
      aria-valuemax={100}
      aria-valuenow={Math.round(used * 100)}
      className={cn("h-1.5 overflow-hidden rounded-full bg-muted", className)}
    >
      <motion.div
        className={cn("h-full rounded-full transition-colors", used > 0.9 ? "bg-destructive" : used > 0.75 ? "bg-warning" : "bg-primary")}
        initial={reduced ? false : { width: 0 }}
        animate={{ width: `${used * 100}%` }}
        transition={{ duration: 0.6, ease: "easeOut" }}
      />
    </div>
  )
}
