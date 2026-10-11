import { ListIcon, MagnifyingGlassIcon, SortAscendingIcon, SortDescendingIcon, SquaresFourIcon } from "@phosphor-icons/react"
import { t } from "i18next"
import type { ReactNode } from "react"
import { radios } from "@/components/radios"
import { Button } from "@/components/ui/button"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuLabel,
  DropdownMenuRadioGroup,
  DropdownMenuRadioItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { InputGroup, InputGroupAddon, InputGroupInput } from "@/components/ui/input-group"
import type { Order, Sorting } from "@/lib/sort"
import { cn } from "@/lib/utils"

/** Whether a list shows cards or a table. */
export type View = "grid" | "table"

/** The bar above a list: its search and filters, and how it is sorted and shown. */
export function ListToolbar({ search, children, className }: { search: ReactNode; children?: ReactNode; className?: string }) {
  return (
    <div className={cn("flex flex-wrap items-center justify-between gap-2", className)}>
      <div className="flex w-full min-w-0 items-center gap-2 sm:w-auto sm:min-w-72 sm:flex-1 lg:max-w-md">{search}</div>
      <div className="flex flex-wrap items-center gap-2">{children}</div>
    </div>
  )
}

/** Searches a list as one types. */
export function SearchField({
  label,
  value,
  onChange,
  className,
}: {
  label: string
  value?: string
  onChange: (value: string | undefined) => void
  className?: string
}) {
  return (
    <InputGroup className={className}>
      <InputGroupAddon>
        <MagnifyingGlassIcon />
      </InputGroupAddon>
      <InputGroupInput
        type="search"
        placeholder={label}
        aria-label={label}
        value={value ?? ""}
        onChange={(e) => onChange(e.target.value || undefined)}
      />
    </InputGroup>
  )
}

/** A dropdown that chooses one of a few options, showing the chosen one. */
export function MenuChoice<T extends string>({
  icon: Icon,
  label,
  value,
  options,
  onChange,
  children,
}: {
  icon: typeof SortAscendingIcon
  label: string
  value: T
  options: { value: T; label: string }[]
  onChange: (value: T) => void
  /** More choices, after the options. */
  children?: ReactNode
}) {
  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button variant="outline" aria-label={`${label}: ${options.find((o) => o.value === value)?.label}`}>
          <Icon />
          <span className="max-sm:hidden">{options.find((o) => o.value === value)?.label}</span>
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="w-44">
        <DropdownMenuLabel>{label}</DropdownMenuLabel>
        <DropdownMenuRadioGroup value={value} onValueChange={(v) => onChange(v as T)}>
          {options.map((o) => (
            <DropdownMenuRadioItem key={o.value} value={o.value}>
              {o.label}
            </DropdownMenuRadioItem>
          ))}
        </DropdownMenuRadioGroup>
        {children}
      </DropdownMenuContent>
    </DropdownMenu>
  )
}

/** Sorts a list by one of its sorts, labelled with msg, either way round. */
export function SortMenu<K extends string>({ sorting, sorts }: { sorting: Sorting<K>; sorts: Record<K, string> }) {
  return (
    <MenuChoice
      icon={sorting.order === "asc" ? SortAscendingIcon : SortDescendingIcon}
      label={t("Sort")}
      value={sorting.by}
      options={(Object.keys(sorts) as K[]).map((value) => ({ value, label: t(sorts[value]) }))}
      onChange={(sort) => sorting.sort(sort)}
    >
      <DropdownMenuSeparator />
      <DropdownMenuRadioGroup value={sorting.order} onValueChange={(order) => sorting.sort(sorting.by, order as Order)}>
        <DropdownMenuRadioItem value="asc">{t("Ascending")}</DropdownMenuRadioItem>
        <DropdownMenuRadioItem value="desc">{t("Descending")}</DropdownMenuRadioItem>
      </DropdownMenuRadioGroup>
    </MenuChoice>
  )
}

/** Switches a list between cards and a table. */
export function ViewSwitch({ value, onChange }: { value: View; onChange: (view: View) => void }) {
  const radio = radios<View>(["grid", "table"], value, onChange)
  return (
    <div role="radiogroup" aria-label={t("Layout")} className="inline-flex rounded-lg bg-muted p-0.5 ring-1 ring-border ring-inset">
      {(
        [
          ["grid", SquaresFourIcon, t("Cards")],
          ["table", ListIcon, t("Table")],
        ] as const
      ).map(([view, Icon, label]) => (
        <button
          key={view}
          {...radio(view)}
          aria-label={label}
          title={label}
          className="grid h-8 w-9 place-items-center rounded-md text-muted-foreground transition-colors outline-none hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring aria-checked:bg-card aria-checked:text-foreground aria-checked:shadow-sm"
        >
          <Icon className="size-4" weight="bold" />
        </button>
      ))}
    </div>
  )
}

/** Tells that nothing of a list matches its search. */
export function NoMatch({ children }: { children: ReactNode }) {
  return <p className="rounded-xl bg-muted/50 p-6 text-center text-sm text-muted-foreground">{children}</p>
}
