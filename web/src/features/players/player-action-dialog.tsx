import { useQuery } from "@tanstack/react-query"
import { t } from "i18next"
import { type FormEvent, useState } from "react"
import { guard } from "@/components/form-dialog"
import { Button } from "@/components/ui/button"
import { Dialog, DialogClose, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { Field, FieldContent, FieldDescription, FieldError, FieldGroup, FieldLabel, FieldTitle } from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import { RadioGroup, RadioGroupItem } from "@/components/ui/radio-group"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import type { ServerRef } from "@/features/networks/api"
import { findServer, key } from "@/features/networks/servers"
import { OperationStatus } from "@/features/operations/operation-status"
import { retryAction } from "@/features/operations/retry"
import { type Done, useOperation } from "@/features/operations/use-operation"
import { allServersQuery } from "@/features/servers/api"
import { fromWallClock } from "@/lib/format"
import { playerActions, playersLabel } from "./actions"
import { type PlayerAction, type PlayerLists, type PlayerResult, playerListsQuery, useChangePlayer } from "./api"
import { type Names, namesOf, useKnownNames } from "./names"
import { NamesField } from "./names-field"

/** Servers a change can go to, e.g. the server of a player, or a whole network. */
export interface Scope {
  label: string
  servers: ServerRef[]
}

const maxReason = 256
const hour = 3_600_000

/** How long a ban lasts: for good, a while, or until a time. */
const durations = {
  ever: { label: () => t("For good"), ms: 0 },
  hour: { label: () => t("1 hour"), ms: hour },
  day: { label: () => t("1 day"), ms: 24 * hour },
  week: { label: () => t("1 week"), ms: 7 * 24 * hour },
  month: { label: () => t("30 days"), ms: 30 * 24 * hour },
  until: { label: () => t("Until…"), ms: 0 },
}
type Duration = keyof typeof durations

/** The reasons of the bans in the lists, the most frequent first, to ban others for them too; only those a ban may have. */
function banReasons(lists: PlayerLists) {
  const counts = new Map<string, number>()
  for (const { reason = "" } of lists.banned) {
    const r = reason.trim()
    if (r && [...r].length <= maxReason && !/\p{Cc}/u.test(r)) counts.set(r, (counts.get(r) ?? 0) + 1)
  }
  return [...counts.keys()].sort((a, b) => counts.get(b)! - counts.get(a)!)
}

/** Kicks, bans, pardons, whitelists or makes operator players on the servers of a scope. */
export function PlayerActionDialog({
  action,
  names: fixed,
  scopes,
  onClose,
}: {
  action: PlayerAction
  /** The players; without, the dialog asks for them. */
  names?: string[]
  scopes: Scope[]
  onClose: () => void
}) {
  const info = playerActions[action]
  const global = action === "whitelist_on" || action === "whitelist_off"
  const [typed, setTyped] = useState<Names>({ names: [], input: "" })
  const [reason, setReason] = useState("")
  const [duration, setDuration] = useState<Duration>("ever")
  const [until, setUntil] = useState("")
  const [scope, setScope] = useState(0)
  const change = useChangePlayer()
  const operation = useOperation()
  const known = useKnownNames(!fixed && !global)
  const { data: servers } = useQuery(allServersQuery)
  const { data: reasons = [] } = useQuery({ ...playerListsQuery(), enabled: action === "ban", select: banReasons })
  const names = fixed ?? namesOf(typed)
  const players = playersLabel(names)
  const title = info.title(names.length > 0 ? players : "…")
  const nameOf = (ref: ServerRef) => findServer(servers, ref)?.name ?? ref.serverId
  // When a ban ends, from the moment it is made.
  const endOf = () => (duration === "until" ? fromWallClock(until) : durations[duration].ms ? new Date(Date.now() + durations[duration].ms) : undefined)

  function done(results: PlayerResult[]): Done {
    const failed = results.filter((r) => r.error)
    const servers = new Set(results.map(key)).size
    const pending = new Set(results.filter((r) => r.pending).map(key)).size
    const lines = [
      results.length === 1 && results[0].output
        ? results[0].output
        : names.length > 1
          ? t("Done {{done}} of {{count}} times on {{servers}} servers.", { done: results.length - failed.length, count: results.length, servers })
          : t("Done on {{done}} of {{count}} servers.", { done: servers - new Set(failed.map(key)).size - pending, count: servers }),
      pending > 0 &&
        t("{{count}} stopped servers catch up when they next start.", {
          count: pending,
          defaultValue_one: "A stopped server catches up when it next starts.",
        }),
      failed.length > 0 &&
        t("Failed on {{servers}}: {{error}}", { servers: [...new Set(failed.map(nameOf))].join(", "), error: failed[0].error }),
    ]
    return {
      message: failed.length === results.length ? t("Nothing changed") : info.done(players),
      description: lines.filter(Boolean).join(" "),
      // The dialog is closed by then, so a notification follows the retry.
      action: retryAction(results, (servers) => run(servers, true)),
      warning: failed.length > 0,
    }
  }

  function run(servers: ServerRef[], notify = false) {
    operation.run(
      (onStart) =>
        change.mutateAsync({
          action,
          names: global ? undefined : names,
          reason: reason.trim() || undefined,
          until: action === "ban" ? endOf()?.toISOString() : undefined,
          servers,
          onStart,
        }),
      { title, done, then: onClose, notify },
    )
  }

  function submit(event: FormEvent) {
    event.preventDefault()
    run(scopes[scope].servers)
  }

  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent className="sm:max-w-lg" {...guard(change.isPending)}>
        {operation.live ? (
          <OperationStatus
            op={operation.live}
            title={title}
            onBackground={() => {
              operation.background(title)
              onClose()
            }}
            onBack={() => {
              operation.reset()
              change.reset()
            }}
          />
        ) : (
          <form onSubmit={submit} className="grid gap-6">
            <DialogHeader>
              <DialogTitle>{global ? info.title("") : title}</DialogTitle>
              <DialogDescription>{describe(action)}</DialogDescription>
            </DialogHeader>
            <FieldGroup>
              {fixed && fixed.length > 1 && <FieldDescription className="font-mono break-words">{fixed.join(", ")}</FieldDescription>}
              {!fixed && !global && (
                <Field>
                  <FieldLabel htmlFor="player-names">{t("Players")}</FieldLabel>
                  <NamesField id="player-names" value={typed} onChange={setTyped} known={known} />
                  <FieldDescription>
                    {t("Up to 50 players, with Enter, a space or a comma after each. Players of the Bedrock Edition have a dot in front of their name, e.g. .Steve.")}
                  </FieldDescription>
                </Field>
              )}
              {info.reason && (
                <Field>
                  <FieldLabel htmlFor="player-reason">{t("Reason")}</FieldLabel>
                  <Input
                    id="player-reason"
                    list="player-reasons"
                    autoFocus={!!fixed}
                    maxLength={maxReason}
                    placeholder={t("Optional, the player sees it")}
                    value={reason}
                    onChange={(e) => setReason(e.target.value)}
                  />
                  <datalist id="player-reasons">
                    {reasons.map((r) => (
                      <option key={r} value={r} />
                    ))}
                  </datalist>
                </Field>
              )}
              {action === "ban" && (
                <Field>
                  <FieldLabel htmlFor="player-duration">{t("Duration")}</FieldLabel>
                  <div className="flex flex-wrap gap-2">
                    <Select value={duration} onValueChange={(d) => setDuration(d as Duration)}>
                      <SelectTrigger id="player-duration" className="w-full sm:w-40">
                        <SelectValue />
                      </SelectTrigger>
                      <SelectContent>
                        {(Object.keys(durations) as Duration[]).map((d) => (
                          <SelectItem key={d} value={d}>
                            {durations[d].label()}
                          </SelectItem>
                        ))}
                      </SelectContent>
                    </Select>
                    {duration === "until" && (
                      <Input
                        type="datetime-local"
                        required
                        aria-label={t("End of the ban")}
                        className="w-full sm:w-auto sm:flex-1"
                        value={until}
                        onChange={(e) => setUntil(e.target.value)}
                      />
                    )}
                  </div>
                  <FieldDescription>
                    {duration === "ever"
                      ? t("The ban lasts until the player is pardoned.")
                      : t("The servers pardon the player at the end, also if they only run again later. Another ban or a pardon replaces the end.")}
                  </FieldDescription>
                </Field>
              )}
              {scopes.length > 1 ? (
                <RadioGroup value={String(scope)} onValueChange={(v) => setScope(Number(v))} aria-label={t("Servers")} className="gap-2">
                  {scopes.map((s, i) => (
                    <FieldLabel key={s.label} htmlFor={`player-scope-${i}`}>
                      <Field orientation="horizontal">
                        <FieldContent>
                          <FieldTitle>{s.label}</FieldTitle>
                          <FieldDescription>
                            {t("{{count}} servers", { count: s.servers.length, defaultValue_one: "{{count}} server" })}
                          </FieldDescription>
                        </FieldContent>
                        <RadioGroupItem id={`player-scope-${i}`} value={String(i)} />
                      </Field>
                    </FieldLabel>
                  ))}
                </RadioGroup>
              ) : (
                <FieldDescription>
                  {scopes[0].label} · {t("{{count}} servers", { count: scopes[0].servers.length, defaultValue_one: "{{count}} server" })}
                </FieldDescription>
              )}
              {change.error && <FieldError>{change.error.message}</FieldError>}
            </FieldGroup>
            <DialogFooter>
              <DialogClose asChild>
                <Button variant="outline" disabled={change.isPending}>
                  {t("Cancel")}
                </Button>
              </DialogClose>
              <Button
                type="submit"
                variant={info.destructive ? "destructive" : "default"}
                disabled={change.isPending || (!global && names.length === 0) || (action === "ban" && duration === "until" && !until)}
              >
                <info.icon />
                {global ? info.title("") : title}
              </Button>
            </DialogFooter>
          </form>
        )}
      </DialogContent>
    </Dialog>
  )
}

/** What an action does, beyond its name. */
function describe(action: PlayerAction) {
  switch (action) {
    case "kick":
      return t(
        "The player leaves the server. Velocity sends kicked players to another server of their network, BungeeCord disconnects them.",
      )
    case "ban":
      return t("The player can't join the servers anymore. Stopped servers ban the player once they start.")
    case "op":
      return t("Operators may run any command in the game, like the console.")
    case "whitelist_on":
      return t("Only players on the whitelist may join, and online players who aren't on it are disconnected.")
    default:
      return t("Stopped servers apply the change when they next start.")
  }
}
