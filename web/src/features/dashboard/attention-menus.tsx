import { EyeIcon, EyeSlashIcon } from "@phosphor-icons/react"
import { t } from "i18next"
import { Button } from "@/components/ui/button"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { useHidden } from "@/features/preferences/api"
import { formatDateTime } from "@/lib/format"
import { msg } from "@/lib/i18n"
import { fingerprint, isOf, type Problem } from "./attention"

/** How long a problem can be hidden; one hidden until it changes shows again after a week, as the master keeps it no longer. */
const hidings = [
  { label: msg("Hide for an hour"), hours: 1, changes: false },
  { label: msg("Hide for a day"), hours: 24, changes: false },
  { label: msg("Hide until it changes"), hours: 7 * 24, changes: true },
]

/** Hides a problem from the user for a while, in all their browsers. */
export function HideMenu({ problem }: { problem: Problem }) {
  const { hidden, set } = useHidden()
  const hide = (until: number, changes: boolean) =>
    set([
      ...hidden.filter((h) => h.key !== problem.key),
      { key: problem.key, state: changes ? fingerprint(problem) : undefined, until: new Date(until).toISOString() },
    ])
  return (
    <DropdownMenu>
      {/* Shows on hover of its row, and always without a pointer that hovers. */}
      <DropdownMenuTrigger asChild>
        <Button
          variant="ghost"
          size="icon-sm"
          className="shrink-0 text-muted-foreground opacity-0 transition-opacity group-focus-within:opacity-100 group-hover:opacity-100 data-[state=open]:opacity-100 pointer-coarse:opacity-100"
          aria-label={t("Hide {{title}}", { title: problem.title })}
        >
          <EyeSlashIcon />
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end">
        {hidings.map(({ label, hours, changes }) => (
          <DropdownMenuItem key={label} onSelect={() => hide(Date.now() + hours * 3_600_000, changes)}>
            {t(label)}
          </DropdownMenuItem>
        ))}
      </DropdownMenuContent>
    </DropdownMenu>
  )
}

/** The problems the user hid, each of which shows again when chosen. */
export function HiddenMenu({ problems }: { problems: Problem[] }) {
  const { hidden, set } = useHidden()
  const show = (shown: Problem[]) => set(hidden.filter((h) => !shown.some((p) => isOf(h, p))))
  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button variant="ghost" size="xs" className="text-muted-foreground">
          <EyeSlashIcon />
          {t("{{count}} hidden", { count: problems.length })}
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="w-80">
        <DropdownMenuLabel>{t("Hidden for you, in all your browsers")}</DropdownMenuLabel>
        {problems.map((p) => {
          const item = hidden.find((h) => isOf(h, p))
          return (
            <DropdownMenuItem key={p.key} onSelect={() => show([p])} aria-label={t("Show {{title}} again", { title: p.title })}>
              <EyeIcon />
              <span className="min-w-0">
                <span className="block truncate">{p.title}</span>
                <span className="block truncate text-xs text-muted-foreground">
                  {item?.state ? t("Until it changes") : item && t("Until {{time}}", { time: formatDateTime(item.until) })}
                </span>
              </span>
            </DropdownMenuItem>
          )
        })}
        {problems.length > 1 && (
          <>
            <DropdownMenuSeparator />
            <DropdownMenuItem onSelect={() => show(problems)}>
              <EyeIcon />
              {t("Show all again")}
            </DropdownMenuItem>
          </>
        )}
      </DropdownMenuContent>
    </DropdownMenu>
  )
}
