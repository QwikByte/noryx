import type { Icon } from "@phosphor-icons/react"
import { Link } from "@tanstack/react-router"
import type { ReactNode } from "react"
import { cn } from "@/lib/utils"
import { type Tone, toneText } from "./tone"

/**
 * Key figures side by side on one card, divided by hairlines; className sets its columns, e.g.
 * grid-cols-2 lg:grid-cols-4.
 */
export function StatStrip({ className, children, label }: { className?: string; children: ReactNode; label?: string }) {
  return (
    <section aria-label={label} className={cn("surface grid overflow-hidden rounded-xl", className)}>
      {children}
    </section>
  )
}

/**
 * A key figure in a StatStrip, e.g. how many servers run, with an optional detail below it; with
 * to, it opens their page.
 */
export function StatCard({
  icon: Icon,
  tone = "success",
  label,
  value,
  children,
  to,
  search,
}: {
  icon: Icon
  tone?: Tone
  label: string
  value: ReactNode
  children?: ReactNode
  to?: "/servers" | "/nodes" | "/players"
  /** The search of the page it opens, e.g. the players online of one network. */
  search?: { network?: string; tab?: "online" }
}) {
  // The hairlines to the right and below; those at the edges of the strip are cut off.
  const className = "flex min-w-0 flex-col gap-2.5 bg-card p-3.5 shadow-[1px_0_0_0_var(--border),0_1px_0_0_var(--border)] sm:p-5"
  const content = (
    <>
      <p className="eyebrow flex items-center gap-2 text-muted-foreground">
        <Icon aria-hidden className={cn("size-4 shrink-0", toneText[tone])} />
        <span className="truncate">{label}</span>
      </p>
      <div className="figure min-w-0 text-[1.625rem] break-words">{value}</div>
      {children && <div className="mt-auto text-xs wrap-break-word text-muted-foreground">{children}</div>}
    </>
  )
  return to ? (
    <Link to={to} search={search} className={cn(className, "transition-colors outline-none hover:bg-muted/40 focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-inset")}>
      {content}
    </Link>
  ) : (
    <div className={className}>{content}</div>
  )
}
