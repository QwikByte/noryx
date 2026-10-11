import { motion, useReducedMotionConfig, useSpring, useTransform } from "motion/react"
import { useEffect } from "react"
import { formatNumber } from "@/features/usage/format"

/** A number that counts up to its value, and on to each new one, instead of jumping, unless the system or the user asks for less motion. */
export function AnimatedNumber({ value, format = (v) => formatNumber(Math.round(v)) }: { value: number; format?: (value: number) => string }) {
  const reduced = useReducedMotionConfig()
  const spring = useSpring(0, { stiffness: 90, damping: 20 })
  const text = useTransform(spring, format)
  useEffect(() => {
    if (reduced) spring.jump(value)
    else spring.set(value)
  }, [spring, value, reduced])
  // Screen readers get the value itself, not the steps in between.
  return (
    <span className="tabular-nums">
      <span className="sr-only">{format(value)}</span>
      <motion.span aria-hidden>{text}</motion.span>
    </span>
  )
}
