import { EyeSlashIcon, TrashIcon } from "@phosphor-icons/react"
import { t } from "i18next"
import type { ReactNode } from "react"
import { toast } from "sonner"
import { Callout } from "@/components/callout"
import { ConfirmDialog } from "@/components/confirm-dialog"
import { NumberInput } from "@/components/number-input"
import { Button } from "@/components/ui/button"
import { Field, FieldContent, FieldDescription, FieldLabel } from "@/components/ui/field"
import { Switch } from "@/components/ui/switch"
import { useDeleteSeenPlayers } from "@/features/players/api"
import type { Retention } from "./api"

/** How long the usage, players seen and finished operations are kept, and whether players seen are recorded at all. */
export function RetentionFields({ value, onChange }: { value: Retention; onChange: (value: Retention) => void }) {
  const set = (change: Partial<Retention>) => onChange({ ...value, ...change })
  return (
    <>
      <Amount
        id="settings-usage-days"
        label={t("Keep the usage of each hour for")}
        min={30}
        max={365}
        unit={t("days")}
        value={value.usageDays}
        onChange={(usageDays) => set({ usageDays })}
      >
        {t(
          "From 30 days to a year, for the charts of 30 days, of 90 days from 90 days and of 365 days from a year. The minutes behind the charts of 24 hours and 7 days are kept for a week; a year of hours takes about as much space.",
        )}
      </Amount>
      <Field orientation="horizontal">
        <Switch id="settings-seen-players" checked={value.seenPlayers} onCheckedChange={(seenPlayers) => set({ seenPlayers })} />
        <FieldContent>
          <FieldLabel htmlFor="settings-seen-players">{t("Record players seen")}</FieldLabel>
          <FieldDescription>
            {t(
              "Every minute, the master notes who plays on which game server, for the Seen tab and the pages of players: names and times, never IP addresses. Turn it off e.g. for privacy.",
            )}
          </FieldDescription>
        </FieldContent>
      </Field>
      {!value.seenPlayers && <DeleteSeen />}
      <Amount
        id="settings-seen-days"
        label={t("Keep players seen for")}
        min={1}
        max={365}
        unit={t("days")}
        value={value.seenDays}
        onChange={(seenDays) => set({ seenDays })}
      >
        {t("From a day to a year, also once they are no longer recorded.")}
      </Amount>
      <Amount
        id="settings-operation-hours"
        label={t("List finished operations for")}
        min={1}
        max={24}
        unit={t("hours")}
        value={value.operationHours}
        onChange={(operationHours) => set({ operationHours })}
      >
        {t("Up to a day, and the last 200 at most. The master keeps them in memory, so a restart empties the list.")}
      </Amount>
    </>
  )
}

/** Offers to delete the players seen, which stay until their time is up once they are no longer recorded. */
function DeleteSeen() {
  const remove = useDeleteSeenPlayers()
  return (
    <Callout icon={EyeSlashIcon} role="status">
      <p>{t("What was recorded stays until its time is up, unless you delete it.")}</p>
      <ConfirmDialog
        trigger={
          <Button type="button" variant="outline" size="sm" className="mt-3" disabled={remove.isPending}>
            <TrashIcon />
            {t("Delete players seen")}
          </Button>
        }
        title={t("Delete all players seen?")}
        description={t("Where and when players played is deleted for all servers, also from the pages of players. This can't be undone.")}
        action={t("Delete players seen")}
        destructive
        onConfirm={() =>
          remove.mutate(undefined, {
            onSuccess: () => toast.success(t("Deleted the players seen")),
            onError: (e) => toast.error(e.message),
          })
        }
      />
    </Callout>
  )
}

/** A number of days or hours within bounds, and what it means. */
function Amount({
  id,
  label,
  min,
  max,
  unit,
  value,
  onChange,
  children,
}: {
  id: string
  label: string
  min: number
  max: number
  unit: string
  value: number
  onChange: (value: number) => void
  children: ReactNode
}) {
  return (
    <Field>
      <FieldLabel htmlFor={id}>{label}</FieldLabel>
      <NumberInput id={id} min={min} max={max} unit={unit} value={value} onChange={onChange} />
      <FieldDescription>{children}</FieldDescription>
    </Field>
  )
}
