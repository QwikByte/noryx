import { ScrollIcon } from "@phosphor-icons/react"
import { useInfiniteQuery } from "@tanstack/react-query"
import { t } from "i18next"
import { AnimatePresence, motion } from "motion/react"
import { IconTile } from "@/components/icon-tile"
import { type Level, logsQuery, useLiveLogs } from "@/features/logs/api"
import { levels } from "@/features/logs/meta"
import { formatAgo } from "@/lib/format"
import { useNow } from "@/lib/use-now"
import type { WidgetProps } from "./options"
import { Calm, Panel } from "./panel"

/** The latest entries of the log of at least a level, as they are logged; new ones slide in at the top. */
export function RecentActivity({ title, options }: WidgetProps) {
  const filter = { level: options.level as Level }
  const { data } = useInfiniteQuery(logsQuery(filter))
  useLiveLogs(filter, true)
  // Renders again now and then, so that the times stay current.
  const now = useNow(true, 30_000)
  const entries = data?.pages.flat().slice(0, Number(options.count)) ?? []
  return (
    <Panel title={title} more={{ to: "/logs", label: t("Open the log") }}>
      {data && entries.length === 0 ? (
        <Calm icon={ScrollIcon}>{t("Nothing has happened yet.")}</Calm>
      ) : (
        <ul className="divide-y">
          <AnimatePresence initial={false}>
            {entries.map((e) => (
              <motion.li
                key={e.id}
                layout
                initial={{ opacity: 0, y: -8 }}
                animate={{ opacity: 1, y: 0 }}
                exit={{ opacity: 0 }}
                className="flex items-center gap-3 px-4 py-2.5"
              >
                <IconTile icon={levels[e.level].icon} tone={levels[e.level].tone} size="sm" />
                <span className="min-w-0 flex-1">
                  <span className="block truncate text-sm">{e.message}</span>
                  <span className="block truncate text-xs text-muted-foreground">
                    {[e.serverName ?? e.nodeName, e.user, formatAgo(e.time, now)].filter(Boolean).join(" · ")}
                  </span>
                </span>
              </motion.li>
            ))}
          </AnimatePresence>
        </ul>
      )}
    </Panel>
  )
}
