import { WarningIcon } from "@phosphor-icons/react"
import { t } from "i18next"
import type { ReactNode } from "react"
import { Callout } from "@/components/callout"
import { SelectGroup, SelectItem, SelectLabel, SelectSeparator } from "@/components/ui/select"
import { msg } from "@/lib/i18n"
import { type ServerType, serverType, serverTypes } from "./server-types"

const groups: [label: string, has: (s: ServerType) => boolean][] = [
  [msg("Java Edition"), (s) => !s.proxy && !s.bedrock],
  [msg("Bedrock Edition"), (s) => !!s.bedrock],
  [msg("Proxies for networks"), (s) => s.proxy],
]

/**
 * The software a new server or template can run, game servers first. Software at its end of life is only listed while
 * it is chosen, e.g. by a template. Software that isn't available, as the node's agent doesn't create it yet, can't be
 * chosen.
 */
export function SoftwareOptions({ chosen, available }: { chosen: string; available?: string[] }) {
  return groups.map(([label, has], i) => (
    <SelectGroup key={label}>
      {i > 0 && <SelectSeparator />}
      <SelectLabel>{t(label)}</SelectLabel>
      {serverTypes
        .filter((s) => has(s) && (!s.endOfLife || s.value === chosen))
        .map((s) => {
          const missing = available && !available.includes(s.value)
          return (
            <SelectItem key={s.value} value={s.value} disabled={missing && s.value !== chosen}>
              {s.label}
              {missing && <span className="text-muted-foreground">{t("Update the agent of the node first")}</span>}
            </SelectItem>
          )
        })}
    </SelectGroup>
  ))
}

/** Warns that software reached its end of life, and tells what to use instead; children offer a way out. */
export function EndOfLifeNotice({ type, className, children }: { type: string; className?: string; children?: ReactNode }) {
  const { label, endOfLife } = serverType(type)
  if (!endOfLife) return null
  return (
    <Callout tone="warning" icon={WarningIcon} role="note" className={className} title={t("{{software}} reached its end of life", { software: label })}>
      {t(endOfLife)}
      {children && <div className="mt-3">{children}</div>}
    </Callout>
  )
}
