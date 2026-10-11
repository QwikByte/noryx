/** A choice of a widget. The master takes the same keys and values (internal/master/preference). */
export type WidgetOption = ValueOption | ListOption

/** One of values, the default unless the user chose another; labels are marked with msg, and numbers have none. */
export interface ValueOption {
  key: string
  label: string
  values: { value: string; label?: string }[]
  default: string
}

/** The nodes or networks a widget shows, all unless the user chose some. */
export interface ListOption {
  key: "nodes" | "networks"
  label: string
}

/** What a widget gets: its title, its options with the defaults of those the user didn't choose, and a change of one. */
export interface WidgetProps {
  title: string
  options: Record<string, string | undefined>
  setOption: (key: string, value: string | undefined) => void
}

/** The nodes or networks a widget shows: those of its option that are still there, or else all. */
export function chosen<T extends { id: string }>(all: T[], option: string | undefined) {
  const some = all.filter((x) => option?.split(",").includes(x.id))
  return some.length > 0 ? some : all
}
