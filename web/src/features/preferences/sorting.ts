import { type Order, type Sorting, sortingOf } from "@/lib/sort"
import { type Settings, type SettingsChange, useSettings } from "./api"

type SortSearch<K extends string> = { sort?: K; order?: Order }

/**
 * Sorts lists by the column in their address, or else as the user sorted lists of their kind last, which the user
 * keeps in all browsers by two settings, e.g. serverSort and serverOrder. Sorting changes the address and these. The
 * function it returns sorts one table by its columns, so that tables of different columns can share the settings,
 * e.g. the tabs of the players; a table's first column and the direction a column starts in aren't kept, so that each
 * table starts as it does by default.
 */
export function useSortings<S extends string>(
  search: SortSearch<S>,
  keys: { sort: keyof Settings; order: keyof Settings },
  onSearch: (change: SortSearch<S>) => void,
) {
  const { settings, change } = useSettings()
  // The sort chosen last applies while the address names neither a column nor an order.
  const saved = search.sort === undefined && search.order === undefined
  const shown = saved ? { sort: settings[keys.sort], order: settings[keys.order] as Order | undefined } : search
  return <K extends S>(columns: Record<K, Order>): Sorting<K> =>
    sortingOf(shown, columns, (c) => {
      change({ [keys.sort]: c.sort ?? null, [keys.order]: c.order ?? null } as SettingsChange)
      onSearch(c)
    })
}

/** Sorts a list by the column in its address, or else as the user sorted lists of its kind last, see useSortings. */
export function useSorting<K extends string>(
  search: SortSearch<K>,
  columns: Record<K, Order>,
  keys: { sort: keyof Settings; order: keyof Settings },
  onSearch: (change: SortSearch<K>) => void,
): Sorting<K> {
  return useSortings(search, keys, onSearch)(columns)
}
