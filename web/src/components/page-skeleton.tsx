import { t } from "i18next"
import { Skeleton } from "@/components/ui/skeleton"

/** Stands in for a page while its code loads, shaped like a page header above its content. */
export function PageSkeleton() {
  return (
    <div role="status" aria-busy className="animate-in fade-in">
      <span className="sr-only">{t("Loading…")}</span>
      <div aria-hidden className="mb-7 flex items-start gap-3.5">
        <Skeleton className="mt-0.5 size-10 rounded-lg" />
        <div className="flex-1 space-y-2.5 pt-1.5">
          <Skeleton className="h-7 w-56 max-w-full" />
          <Skeleton className="h-4 w-96 max-w-full" />
        </div>
      </div>
      <div aria-hidden className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
        <Skeleton className="h-28 rounded-xl" />
        <Skeleton className="h-28 rounded-xl" />
        <Skeleton className="hidden h-28 rounded-xl lg:block" />
        <Skeleton className="h-64 rounded-xl sm:col-span-2 lg:col-span-3" />
      </div>
    </div>
  )
}
