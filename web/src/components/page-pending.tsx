import { t } from "i18next"
import { Skeleton } from "@/components/ui/skeleton"

/**
 * Shown in place of a page while its code or data load for longer than a moment, e.g. over a slow connection: the shape
 * of a page's header and content, so that the area doesn't stay blank.
 */
export function PagePending() {
  return (
    <div role="status" aria-label={t("Loading…")}>
      <div className="mb-7 flex items-start gap-3.5">
        <Skeleton className="size-10 shrink-0 rounded-lg" />
        <div className="min-w-0 flex-1 space-y-2.5 pt-1">
          <Skeleton className="h-7 w-48 max-w-full" />
          <Skeleton className="h-4 w-80 max-w-full" />
        </div>
      </div>
      <div className="space-y-4">
        <Skeleton className="h-24 rounded-xl" />
        <Skeleton className="h-44 rounded-xl" />
      </div>
    </div>
  )
}
