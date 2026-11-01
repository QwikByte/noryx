import { t } from "i18next"
import { SelectGroup, SelectItem, SelectLabel, SelectSeparator } from "@/components/ui/select"
import { msg } from "@/lib/i18n"
import { type ServerType, serverTypes } from "./server-types"

const groups: [label: string, has: (s: ServerType) => boolean][] = [
  [msg("Java Edition"), (s) => !s.proxy && !s.bedrock],
  [msg("Bedrock Edition"), (s) => !!s.bedrock],
  [msg("Proxies for networks"), (s) => s.proxy],
]

/**
 * The software a new server or template can run, game servers first. Software that isn't available, as the node's agent
 * doesn't create it yet, can't be chosen.
 */
export function SoftwareOptions({ chosen, available }: { chosen: string; available?: string[] }) {
  return groups.map(([label, has], i) => (
    <SelectGroup key={label}>
      {i > 0 && <SelectSeparator />}
      <SelectLabel>{t(label)}</SelectLabel>
      {serverTypes
        .filter(has)
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
