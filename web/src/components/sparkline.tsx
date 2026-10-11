import { motion, useReducedMotionConfig } from "motion/react"
import { cn } from "@/lib/utils"

const W = 100
const H = 24

/**
 * A small line of how a value went, without axes; its figure tells the value, so it is
 * decorative. The line draws in from the left when it appears.
 */
export function Sparkline({ values, max, className }: { values: number[]; max?: number; className?: string }) {
  const reduced = useReducedMotionConfig()
  if (values.length < 2) return <div aria-hidden className={cn("h-6", className)} />
  const top = max || Math.max(...values) || 1
  const coords = values.map((v, i) => `${((i / (values.length - 1)) * W).toFixed(1)} ${(H - (Math.min(v, top) / top) * (H - 2) - 1).toFixed(1)}`)
  const line = `M${coords.join("L")}`
  return (
    <motion.svg
      aria-hidden
      viewBox={`0 0 ${W} ${H}`}
      preserveAspectRatio="none"
      className={cn("h-6 w-full overflow-visible text-primary", className)}
      initial={reduced ? false : { clipPath: "inset(0 100% 0 0)" }}
      animate={{ clipPath: "inset(0 0% 0 0)" }}
      transition={{ duration: 0.8, ease: "easeOut" }}
    >
      <path d={`${line}L${W} ${H}L0 ${H}Z`} className="fill-current opacity-10" />
      <path d={line} fill="none" stroke="currentColor" strokeWidth={1.5} strokeLinejoin="round" vectorEffect="non-scaling-stroke" />
    </motion.svg>
  )
}
