import { GearSixIcon } from "@phosphor-icons/react"
import { useQuery } from "@tanstack/react-query"
import { t } from "i18next"
import { Fragment } from "react"
import { Button } from "@/components/ui/button"
import {
  DropdownMenu,
  DropdownMenuCheckboxItem,
  DropdownMenuContent,
  DropdownMenuLabel,
  DropdownMenuRadioGroup,
  DropdownMenuRadioItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { networksQuery } from "@/features/networks/api"
import { nodesQuery } from "@/features/nodes/api"
import type { ListOption, WidgetOption, WidgetProps } from "./options"

/** The options of a widget while the overview is customized; each applies right away. */
export function WidgetOptions({ title, defs, ...props }: WidgetProps & { defs: WidgetOption[] }) {
  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button
          variant="ghost"
          size="icon-xs"
          className="rounded-full"
          aria-label={t("Options of {{title}}", { title })}
          title={t("Options of {{title}}", { title })}
        >
          <GearSixIcon />
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="w-56">
        {defs.map((o, i) => (
          <Fragment key={o.key}>
            {i > 0 && <DropdownMenuSeparator />}
            {"values" in o ? (
              <>
                <DropdownMenuLabel>{t(o.label)}</DropdownMenuLabel>
                <DropdownMenuRadioGroup value={props.options[o.key]} onValueChange={(value) => props.setOption(o.key, value)}>
                  {o.values.map((v) => (
                    // The menu stays open for the other options.
                    <DropdownMenuRadioItem key={v.value} value={v.value} onSelect={(event) => event.preventDefault()}>
                      {v.label ? t(v.label) : v.value}
                    </DropdownMenuRadioItem>
                  ))}
                </DropdownMenuRadioGroup>
              </>
            ) : (
              <Chosen option={o} {...props} />
            )}
          </Fragment>
        ))}
      </DropdownMenuContent>
    </DropdownMenu>
  )
}

/** Chooses the nodes or networks a widget shows; those that are gone since they were chosen go too. */
function Chosen({ option, options, setOption }: Omit<WidgetProps, "title"> & { option: ListOption }) {
  const { data: nodes = [] } = useQuery({ ...nodesQuery, enabled: option.key === "nodes" })
  const { data: networks = [] } = useQuery({ ...networksQuery, enabled: option.key === "networks" })
  const all = option.key === "nodes" ? nodes : networks
  const ids = all.filter((x) => options[option.key]?.split(",").includes(x.id)).map((x) => x.id)
  const set = (next: string[]) => setOption(option.key, next.length > 0 ? next.join(",") : undefined)
  return (
    <>
      <DropdownMenuLabel>{t(option.label)}</DropdownMenuLabel>
      <DropdownMenuCheckboxItem checked={ids.length === 0} onCheckedChange={() => set([])} onSelect={(event) => event.preventDefault()}>
        {option.key === "nodes" ? t("All nodes") : t("All networks")}
      </DropdownMenuCheckboxItem>
      {all.map((x) => (
        <DropdownMenuCheckboxItem
          key={x.id}
          checked={ids.includes(x.id)}
          onCheckedChange={(on) => set(on ? [...ids, x.id] : ids.filter((id) => id !== x.id))}
          onSelect={(event) => event.preventDefault()}
        >
          <span className="truncate">{x.name}</span>
        </DropdownMenuCheckboxItem>
      ))}
    </>
  )
}
