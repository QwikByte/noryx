import { CaretUpDownIcon } from "@phosphor-icons/react"
import { t } from "i18next"
import { useState } from "react"
import { Button } from "@/components/ui/button"
import { Command, CommandEmpty, CommandInput, CommandItem, CommandList } from "@/components/ui/command"
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover"
import { formatTimeZone } from "@/lib/format"
import { locale } from "@/lib/i18n"
import { cn } from "@/lib/utils"

/** How far a time zone is from UTC right now, e.g. GMT+2; empty for a zone the browser doesn't know. */
function offset(zone: string, now: Date) {
  try {
    const parts = new Intl.DateTimeFormat(locale, { timeZone: zone, timeZoneName: "shortOffset" }).formatToParts(now)
    return parts.find((p) => p.type === "timeZoneName")?.value ?? ""
  } catch {
    return ""
  }
}

/** Chooses one of the IANA time zones the browser knows, searched by name or offset. Empty stands for UTC. */
export function TimeZonePicker({
  id,
  value,
  onChange,
  className,
}: {
  id?: string
  value: string
  onChange: (zone: string) => void
  className?: string
}) {
  const [open, setOpen] = useState(false)
  const [zones, setZones] = useState<{ zone: string; offset: string }[]>([])
  const current = value || "UTC"

  // The list is made when it opens, with the offsets of hundreds of zones at that time.
  function toggle(open: boolean) {
    if (open) {
      const now = new Date()
      setZones([...new Set(["UTC", current, ...Intl.supportedValuesOf("timeZone")])].map((zone) => ({ zone, offset: offset(zone, now) })))
    }
    setOpen(open)
  }

  return (
    <Popover open={open} onOpenChange={toggle}>
      <PopoverTrigger asChild>
        {/* oxlint-disable-next-line jsx-a11y/role-has-required-aria-props -- PopoverTrigger sets aria-expanded and aria-controls */}
        <Button id={id} type="button" variant="outline" role="combobox" className={cn("justify-between font-normal", className)}>
          <span className="truncate">{formatTimeZone(current)}</span>
          <CaretUpDownIcon className="text-muted-foreground" />
        </Button>
      </PopoverTrigger>
      <PopoverContent align="start" className="w-(--radix-popover-trigger-width) min-w-72 p-0">
        <Command defaultValue={current} label={t("Time zone")}>
          <CommandInput placeholder={t("Search time zones…")} />
          <CommandList>
            <CommandEmpty>{t("No time zone found.")}</CommandEmpty>
            {zones.map(({ zone, offset }) => (
              <CommandItem
                key={zone}
                value={zone}
                keywords={[formatTimeZone(zone), offset]}
                data-checked={zone === current}
                onSelect={() => {
                  onChange(zone === "UTC" ? "" : zone)
                  setOpen(false)
                }}
              >
                <span className="min-w-0 flex-1 truncate">{formatTimeZone(zone)}</span>
                <span className="text-xs text-muted-foreground tabular-nums">{offset}</span>
              </CommandItem>
            ))}
          </CommandList>
        </Command>
      </PopoverContent>
    </Popover>
  )
}
