import { BookmarkSimpleIcon, FloppyDiskIcon, TrashIcon } from "@phosphor-icons/react"
import { useQuery } from "@tanstack/react-query"
import { t } from "i18next"
import { type FormEvent, useState } from "react"
import { toast } from "sonner"
import { Button } from "@/components/ui/button"
import { Dialog, DialogClose, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuRadioGroup,
  DropdownMenuRadioItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { Field, FieldDescription, FieldError, FieldLabel } from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import { preferencesQuery, type SavedView, useSetViews } from "@/features/preferences/api"
import { type ServerSearch, validateServerSearch } from "./browse"

/** How many views the master keeps for a user, and how long their names are. */
const maxViews = 20
const maxName = 48

const keys = Object.keys(validateServerSearch({})) as (keyof ServerSearch)[]

/** Whether a saved view shows the servers of a search as it does, after checking the view's search again. */
const shows = (view: SavedView, search: ServerSearch) => {
  const saved = validateServerSearch(view.search)
  return keys.every((k) => saved[k] === search[k])
}

/**
 * The views of the servers page that the user saved by name, e.g. "Lobbies on node 2": choosing one shows its
 * search, filters, sort, grouping and layout, and the view shown can be saved or, once saved, deleted. The master
 * keeps them for the user, and only the user sees them.
 */
export function SavedViews({ current, onShow }: { current: ServerSearch; onShow: (search: ServerSearch) => void }) {
  const views = useQuery(preferencesQuery).data?.views ?? []
  const set = useSetViews()
  const [saving, setSaving] = useState(false)
  const active = views.find((v) => shows(v, current))
  return (
    <>
      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <Button variant="outline" aria-label={active ? t("Saved view: {{name}}", { name: active.name }) : t("Saved views")}>
            <BookmarkSimpleIcon weight={active ? "fill" : "regular"} />
            <span className="max-w-40 truncate max-sm:hidden">{active?.name ?? t("Views")}</span>
          </Button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="start" className="w-60">
          {views.length > 0 && (
            <>
              <DropdownMenuLabel>{t("Saved views")}</DropdownMenuLabel>
              <DropdownMenuRadioGroup
                value={active?.name ?? ""}
                onValueChange={(name) => onShow(validateServerSearch(views.find((v) => v.name === name)?.search ?? {}))}
              >
                {views.map((v) => (
                  <DropdownMenuRadioItem key={v.name} value={v.name}>
                    <span className="truncate">{v.name}</span>
                  </DropdownMenuRadioItem>
                ))}
              </DropdownMenuRadioGroup>
              <DropdownMenuSeparator />
            </>
          )}
          <DropdownMenuItem onSelect={() => setSaving(true)}>
            <FloppyDiskIcon />
            {t("Save view…")}
          </DropdownMenuItem>
          {active && (
            <DropdownMenuItem
              variant="destructive"
              onSelect={() =>
                set.mutate(
                  views.filter((v) => v !== active),
                  { onSuccess: () => toast.success(t("Deleted the view {{name}}", { name: active.name })) },
                )
              }
            >
              <TrashIcon />
              <span className="truncate">{t("Delete the view {{name}}", { name: active.name })}</span>
            </DropdownMenuItem>
          )}
        </DropdownMenuContent>
      </DropdownMenu>
      {saving && <SaveViewDialog views={views} search={current} onOpenChange={setSaving} />}
    </>
  )
}

/** Saves the view shown under a name; a view of the same name is replaced. */
function SaveViewDialog({ views, search, onOpenChange }: { views: SavedView[]; search: ServerSearch; onOpenChange: (open: boolean) => void }) {
  const [name, setName] = useState("")
  const set = useSetViews()
  const trimmed = name.trim()
  const replaces = views.some((v) => v.name === trimmed)
  const full = !replaces && views.length >= maxViews

  function submit(event: FormEvent) {
    event.preventDefault()
    const view = { name: trimmed, search: Object.fromEntries(Object.entries(search).filter(([, value]) => value !== undefined)) }
    set.mutate(replaces ? views.map((v) => (v.name === trimmed ? view : v)) : [...views, view], {
      onSuccess: () => {
        toast.success(t("Saved the view {{name}}", { name: trimmed }))
        onOpenChange(false)
      },
    })
  }

  return (
    <Dialog open onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-md">
        <form onSubmit={submit} className="grid gap-5">
          <DialogHeader>
            <DialogTitle>{t("Save view")}</DialogTitle>
            <DialogDescription>
              {t("The search with its filters, sort, grouping and layout. The menu of views and the search of the panel offer it to you.")}
            </DialogDescription>
          </DialogHeader>
          <Field>
            <FieldLabel htmlFor="view-name">{t("Name")}</FieldLabel>
            <Input
              id="view-name"
              autoFocus
              maxLength={maxName}
              placeholder={t("e.g. Lobbies on node 2")}
              value={name}
              onChange={(e) => setName(e.target.value)}
            />
            {replaces && <FieldDescription>{t("Replaces the view of this name.")}</FieldDescription>}
            {full && <FieldError>{t("Up to {{max}} views can be saved. Delete one first.", { max: maxViews })}</FieldError>}
          </Field>
          <DialogFooter>
            <DialogClose asChild>
              <Button variant="outline">{t("Cancel")}</Button>
            </DialogClose>
            <Button type="submit" disabled={!trimmed || full || set.isPending}>
              <FloppyDiskIcon />
              {t("Save view")}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
