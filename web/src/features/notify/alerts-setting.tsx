import { BellRingingIcon, BellZIcon, FunnelIcon } from "@phosphor-icons/react"
import { useQuery } from "@tanstack/react-query"
import { t } from "i18next"
import { type FormEvent, type ReactNode, useState } from "react"
import { Button } from "@/components/ui/button"
import { Checkbox } from "@/components/ui/checkbox"
import {
  Dialog,
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from "@/components/ui/dialog"
import { FieldDescription, FieldGroup, FieldLegend, FieldSet } from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { AccountRow } from "@/features/auth/account-row"
import { categories } from "@/features/logs/meta"
import { nodesQuery } from "@/features/nodes/api"
import { type Alerts, quiet, quietHours, useAlerts } from "@/features/preferences/api"
import { allServersQuery } from "@/features/servers/api"
import { formatDateTime, formatDuration } from "@/lib/format"
import { msg } from "@/lib/i18n"

/** Which new warnings and errors pop up as toasts and on the desktop, and pausing them. */
export function AlertsSettings() {
  const { alerts, change } = useAlerts()
  const paused = quiet(alerts)
  return (
    <>
      <AccountRow
        icon={FunnelIcon}
        tone="info"
        title={t("What pops up")}
        actions={
          <>
            <Select value={alerts.level} onValueChange={(level: Alerts["level"]) => change({ level })}>
              <SelectTrigger aria-label={t("Level")} className="w-auto min-w-44">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="warn">{t("Warnings and errors")}</SelectItem>
                <SelectItem value="error">{t("Errors only")}</SelectItem>
              </SelectContent>
            </Select>
            <ChooseDialog trigger={<Button variant="outline">{t("Choose…")}</Button>} />
          </>
        }
      >
        {describe(alerts)} {t("The bell lists all of them.")}
      </AccountRow>
      <AccountRow
        icon={paused ? BellZIcon : BellRingingIcon}
        tone={paused ? "warning" : "neutral"}
        title={t("Pause")}
        status={paused ? { tone: "warning", label: msg("Paused") } : undefined}
        actions={
          paused ? (
            <Button variant="outline" onClick={() => change({ quietUntil: undefined })}>
              {t("Pop up again")}
            </Button>
          ) : (
            <Select value="" onValueChange={(hours) => change({ quietUntil: new Date(Date.now() + Number(hours) * 3_600_000).toISOString() })}>
              <SelectTrigger aria-label={t("Pause")} className="w-auto min-w-44">
                <SelectValue placeholder={t("Pause for…")} />
              </SelectTrigger>
              <SelectContent>
                {quietHours.map((hours) => (
                  <SelectItem key={hours} value={String(hours)}>
                    {formatDuration(hours * 3_600_000)}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          )
        }
      >
        {paused
          ? t("Nothing pops up until {{time}}, also in your other browsers.", { time: formatDateTime(alerts.quietUntil!) })
          : t("Keeps warnings and errors from popping up for a while, e.g. while you work on a server.")}
      </AccountRow>
    </>
  )
}

function describe(a: Alerts) {
  const what = a.level === "error" ? t("Errors") : t("Warnings and errors")
  if (!a.only) return t("{{what}} about everything you may see pop up.", { what })
  const chosen = a.pinned || a.nodes.length + a.servers.length + a.categories.length > 0
  return chosen
    ? t("{{what}} about what you chose pop up.", { what })
    : t("Nothing pops up, as nothing is chosen.")
}

/** Chooses whether everything pops up, or only what is about pinned and chosen servers, nodes and categories. */
function ChooseDialog({ trigger }: { trigger: ReactNode }) {
  const [open, setOpen] = useState(false)
  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogTrigger asChild>{trigger}</DialogTrigger>
      <DialogContent className="sm:max-w-xl">
        <ChooseForm onClose={() => setOpen(false)} />
      </DialogContent>
    </Dialog>
  )
}

/** What pops up as the user has it each time the dialog opens. */
function ChooseForm({ onClose }: { onClose: () => void }) {
  const { alerts, change } = useAlerts()
  const [form, setForm] = useState(alerts)
  const [search, setSearch] = useState("")
  const { data: nodes = [] } = useQuery(nodesQuery)
  const { data: servers = [] } = useQuery(allServersQuery)
  const set = (change: Partial<Alerts>) => setForm({ ...form, ...change })
  const toggle = (list: "nodes" | "servers" | "categories", id: string, on: boolean) =>
    set({ [list]: on ? [...form[list], id] : form[list].filter((x) => x !== id) })
  const found = servers.filter((s) => s.name.toLowerCase().includes(search.trim().toLowerCase())).toSorted((a, b) => a.name.localeCompare(b.name))

  function submit(event: FormEvent) {
    event.preventDefault()
    // Servers and nodes that are gone since they were chosen go too.
    change({ ...form, nodes: form.nodes.filter((id) => nodes.some((n) => n.id === id)), servers: form.servers.filter((id) => servers.some((s) => s.id === id)) })
    onClose()
  }

  return (
    <form onSubmit={submit} className="grid gap-6">
      <DialogHeader>
        <DialogTitle>{t("Choose what pops up")}</DialogTitle>
        <DialogDescription>{t("New warnings and errors pop up as toasts, and on the desktop if you turned that on.")}</DialogDescription>
      </DialogHeader>
      <FieldGroup>
        <Select value={form.only ? "chosen" : "all"} onValueChange={(v) => set({ only: v === "chosen" })}>
          <SelectTrigger aria-label={t("What pops up")} className="w-full">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="all">{t("About everything I may see")}</SelectItem>
            <SelectItem value="chosen">{t("Only about what I choose")}</SelectItem>
          </SelectContent>
        </Select>
        {form.only && (
          <>
            <label className="flex cursor-pointer items-center gap-2 text-sm">
              <Checkbox checked={form.pinned} onCheckedChange={(on) => set({ pinned: on === true })} />
              {t("My pinned servers")}
            </label>
            <FieldSet>
              <FieldLegend variant="label">{t("Servers")}</FieldLegend>
              <Input type="search" value={search} onChange={(e) => setSearch(e.target.value)} placeholder={t("Search servers…")} aria-label={t("Search servers")} />
              <div className="grid max-h-44 gap-2 overflow-y-auto rounded-lg border p-3">
                {found.length === 0 && <p className="text-sm text-muted-foreground">{t("No server found.")}</p>}
                {found.map((s) => (
                  <label key={s.id} className="flex cursor-pointer items-center gap-2 text-sm">
                    <Checkbox checked={form.servers.includes(s.id)} onCheckedChange={(on) => toggle("servers", s.id, on === true)} />
                    <span className="truncate">{s.name}</span>
                    <span className="truncate text-muted-foreground">{s.nodeName}</span>
                  </label>
                ))}
              </div>
            </FieldSet>
            {nodes.length > 0 && (
              <FieldSet>
                <FieldLegend variant="label">{t("Nodes, with all their servers")}</FieldLegend>
                <div className="grid grid-cols-2 gap-x-4 gap-y-2.5 sm:grid-cols-3">
                  {nodes.map((n) => (
                    <label key={n.id} className="flex cursor-pointer items-center gap-2 text-sm">
                      <Checkbox checked={form.nodes.includes(n.id)} onCheckedChange={(on) => toggle("nodes", n.id, on === true)} />
                      <span className="truncate">{n.name}</span>
                    </label>
                  ))}
                </div>
              </FieldSet>
            )}
            <FieldSet>
              <FieldLegend variant="label">{t("Categories")}</FieldLegend>
              <div className="grid grid-cols-2 gap-x-4 gap-y-2.5 sm:grid-cols-3">
                {Object.entries(categories).map(([id, label]) => (
                  <label key={id} className="flex cursor-pointer items-center gap-2 text-sm">
                    <Checkbox checked={form.categories.includes(id)} onCheckedChange={(on) => toggle("categories", id, on === true)} />
                    {t(label)}
                  </label>
                ))}
              </div>
              <FieldDescription>{t("Also entries about no server, e.g. of sign-ins or the master itself.")}</FieldDescription>
            </FieldSet>
          </>
        )}
      </FieldGroup>
      <DialogFooter>
        <DialogClose asChild>
          <Button variant="outline">{t("Cancel")}</Button>
        </DialogClose>
        <Button type="submit">{t("Save")}</Button>
      </DialogFooter>
    </form>
  )
}
