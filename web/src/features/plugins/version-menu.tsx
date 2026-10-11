import { CheckIcon, NotepadIcon } from "@phosphor-icons/react"
import { useQuery } from "@tanstack/react-query"
import { t } from "i18next"
import { type ReactNode, useState } from "react"
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
import { Skeleton } from "@/components/ui/skeleton"
import { formatDate } from "@/lib/format"
import { type Channel, channelLabel, channels, newestLabel, type ProjectVersion, versionsQuery } from "./api"
import { ChangesDialog } from "./changes-dialog"
import { ChannelPill } from "./channel-pill"

/**
 * A menu of the versions of a project that run on a server type and Minecraft version, the newest first, betas and
 * alphas included, and what changed in them. With onChannel, it offers the channels to take the project in first, and
 * with onNewest the newest suitable version of the channel.
 */
export function VersionMenu({
  project,
  title,
  type,
  version,
  current,
  channel = "release",
  onChannel,
  onNewest,
  onPick,
  children,
}: {
  project: string
  /** The title of the project. */
  title: string
  type: string
  version: string
  /** The ID of the installed or chosen version. */
  current?: string
  /** The channel the project is taken in. */
  channel?: Channel
  onChannel?: (channel: Channel) => void
  onNewest?: () => void
  onPick: (version: ProjectVersion) => void
  children: ReactNode
}) {
  const [open, setOpen] = useState(false)
  const [changes, setChanges] = useState(false)
  const { data, error, isPending } = useQuery({ ...versionsQuery(project, type, version), enabled: open })
  const mark = (on: boolean) => <CheckIcon className={on ? undefined : "invisible"} />

  return (
    <>
      <DropdownMenu open={open} onOpenChange={setOpen}>
        <DropdownMenuTrigger asChild>{children}</DropdownMenuTrigger>
        <DropdownMenuContent align="end" className="max-h-80 w-72">
          {onChannel && (
            <>
              <DropdownMenuLabel>{t("Channel")}</DropdownMenuLabel>
              <DropdownMenuRadioGroup value={channel} onValueChange={(c) => onChannel(c as Channel)}>
                {channels.map((c) => (
                  <DropdownMenuRadioItem key={c} value={c}>
                    {channelLabel(c)}
                  </DropdownMenuRadioItem>
                ))}
              </DropdownMenuRadioGroup>
              <DropdownMenuSeparator />
            </>
          )}
          {onNewest && (
            <>
              <DropdownMenuItem onSelect={onNewest}>
                <span className="flex-1">{newestLabel(channel)}</span>
                {mark(!current)}
              </DropdownMenuItem>
              <DropdownMenuSeparator />
            </>
          )}
          <DropdownMenuLabel>{t("Suitable versions")}</DropdownMenuLabel>
          {isPending ? (
            [0, 1, 2].map((i) => <Skeleton key={i} className="m-1 h-6" />)
          ) : error ? (
            <p className="px-1.5 py-1 text-xs text-destructive">{error.message}</p>
          ) : data.length === 0 ? (
            <p className="px-1.5 py-1 text-xs text-muted-foreground">{t("No suitable version.")}</p>
          ) : (
            data.map((v) => (
              <DropdownMenuItem key={v.id} onSelect={() => onPick(v)}>
                <span className="truncate font-mono text-xs">{v.number}</span>
                <ChannelPill channel={v.channel} />
                <span className="ml-auto shrink-0 text-xs text-muted-foreground">{formatDate(v.published)}</span>
                {mark(v.id === current)}
              </DropdownMenuItem>
            ))
          )}
          {data && data.length > 0 && (
            <>
              <DropdownMenuSeparator />
              <DropdownMenuItem onSelect={() => setChanges(true)}>
                <NotepadIcon />
                {t("What changed")}
              </DropdownMenuItem>
            </>
          )}
        </DropdownMenuContent>
      </DropdownMenu>
      <ChangesDialog
        open={changes}
        onOpenChange={setChanges}
        title={t("What changed in {{name}}", { name: title })}
        project={project}
        type={type}
        version={version}
        current={current}
      />
    </>
  )
}
