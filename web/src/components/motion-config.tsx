import { MotionConfig } from "motion/react"
import type { ReactNode } from "react"
import { useLook } from "@/lib/theme"

/** Animations only move things if neither the operating system nor the user asks for less motion. */
export function Motion({ children }: { children: ReactNode }) {
  return <MotionConfig reducedMotion={useLook().motion === "less" ? "always" : "user"}>{children}</MotionConfig>
}
