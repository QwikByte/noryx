import { ArrowsClockwiseIcon, DownloadSimpleIcon, HardDrivesIcon, SparkleIcon, WarningCircleIcon } from "@phosphor-icons/react"
import { useQuery } from "@tanstack/react-query"
import { Link } from "@tanstack/react-router"
import { t } from "i18next"
import { toast } from "sonner"
import { Callout } from "@/components/callout"
import { ConfirmDialog } from "@/components/confirm-dialog"
import { CopyField } from "@/components/copy-field"
import { Markdown } from "@/components/markdown"
import { Button } from "@/components/ui/button"
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
import { formatDate } from "@/lib/format"
import { type Release, type UpdateStatus, updateCommand, updateQuery, updating, useUpdateAction, waitsForUpdate } from "./api"
import { UpdateAgentButton } from "./update-agent-button"

const onError = { onError: (e: Error) => toast.error(e.message) }

/**
 * Tells administrators about a new release and installs it, and about agents that are older
 * than the master. While the master updates, it asks often, so that the panel reloads soon after
 * the master runs the new version.
 */
export function UpdateBanner() {
  const { data } = useQuery({
    ...updateQuery,
    retry: false, // the master is away while it restarts
    refetchInterval: (query) => (updating(query.state.data) ? 3_000 : 60_000),
  })

  if (!data || !(data.master || data.latest || data.agents.length > 0)) return null
  return (
    <div className="mb-8 space-y-4">
      {data.master ? <MasterProgress status={data} /> : data.latest && <Available status={data} release={data.latest} />}
      {data.agents.length > 0 && <OutdatedAgents status={data} />}
    </div>
  )
}

function Available({ status, release }: { status: UpdateStatus; release: Release }) {
  const install = useUpdateAction("master")
  return (
    <Callout icon={SparkleIcon} title={t("Noryx {{version}} is available", { version: release.version })}>
      <p>
        {[
          t("You're running {{version}}.", { version: status.version }),
          release.publishedAt && t("The new release was published on {{date}}.", { date: formatDate(release.publishedAt) }),
          !status.updatable && t("This master wasn't installed from a package, so update it on its host:"),
        ]
          .filter(Boolean)
          .join(" ")}
      </p>
      {!status.updatable && <CopyField label={t("Update command")} prefix="$" value={updateCommand} />}
      <div className="mt-3 flex flex-wrap gap-2">
        <ReleaseNotes release={release} />
        {status.updatable && (
          <ConfirmDialog
            trigger={
              <Button size="sm" disabled={install.isPending}>
                <DownloadSimpleIcon />
                {t("Install now")}
              </Button>
            }
            title={t("Install {{version}}?", { version: release.version })}
            description={
              status.agentsFollow
                ? t(
                    "The master installs the release and restarts, which interrupts the panel for a moment. Then it updates the agents. Minecraft servers keep running.",
                  )
                : t(
                    "The master installs the release and restarts, which interrupts the panel for a moment. The agents keep their version until you update them. Minecraft servers keep running.",
                  )
            }
            action={t("Install now")}
            onConfirm={() => install.mutate(undefined, onError)}
          />
        )}
      </div>
    </Callout>
  )
}

function MasterProgress({ status: { master, latest, agentsFollow } }: { status: UpdateStatus }) {
  const retry = useUpdateAction("master")
  if (master?.error) {
    return (
      <Callout tone="destructive" icon={WarningCircleIcon} role="alert" title={t("The update of the master failed")}>
        <p>{master.error}</p>
        {latest && (
          <Button size="sm" variant="outline" className="mt-3" disabled={retry.isPending} onClick={() => retry.mutate(undefined, onError)}>
            {t("Try again")}
          </Button>
        )}
      </Callout>
    )
  }
  return (
    <Callout
      icon={ArrowsClockwiseIcon}
      role="status"
      title={latest ? t("Installing {{version}}…", { version: latest.version }) : t("Installing the update…")}
    >
      {agentsFollow
        ? t("The master restarts on the new release, then the panel reloads and the agents are updated. Minecraft servers keep running.")
        : t("The master restarts on the new release, then the panel reloads. Minecraft servers keep running.")}
    </Callout>
  )
}

function OutdatedAgents({ status: { version, agents } }: { status: UpdateStatus }) {
  const update = useUpdateAction("agents")
  const waiting = agents.filter(waitsForUpdate).length
  return (
    <Callout
      tone="warning"
      icon={HardDrivesIcon}
      title={t("{{count}} agents are older than the master", {
        count: agents.length,
        defaultValue_one: "An agent is older than the master",
      })}
    >
      <ul className="space-y-2">
        {agents.map((a) => (
          <li key={a.nodeId} className="flex flex-wrap items-center justify-between gap-x-3 gap-y-1">
            <span className="min-w-0">
              <Link to="/nodes/$nodeId" params={{ nodeId: a.nodeId }} className="font-medium hover:underline">
                {a.name}
              </Link>{" "}
              <span className="font-mono text-xs">{a.version}</span>
              {a.update && (a.update.error ? <span className="text-destructive"> · {a.update.error}</span> : ` · ${t("updating…")}`)}
            </span>
            {waitsForUpdate(a) && <UpdateAgentButton agent={a} version={version} />}
          </li>
        ))}
      </ul>
      {waiting > 1 && (
        <Button size="sm" className="mt-3" disabled={update.isPending} onClick={() => update.mutate(undefined, onError)}>
          <DownloadSimpleIcon />
          {t("Update all to {{version}}", { version })}
        </Button>
      )}
    </Callout>
  )
}

function ReleaseNotes({ release }: { release: Release }) {
  return (
    <Dialog>
      <DialogTrigger asChild>
        <Button size="sm" variant="outline">
          {t("What's new")}
        </Button>
      </DialogTrigger>
      <DialogContent className="sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle>{t("What's new in {{version}}", { version: release.version })}</DialogTitle>
          {release.publishedAt && (
            <DialogDescription>{t("Published on {{date}}.", { date: formatDate(release.publishedAt) })}</DialogDescription>
          )}
        </DialogHeader>
        <div className="max-h-[60vh] overflow-y-auto rounded-lg bg-muted/50 p-4">
          <Markdown text={release.notes.trim() || t("Read the notes of this release on GitHub.")} />
        </div>
        <DialogFooter>
          <Button variant="outline" asChild>
            <a href={release.url} target="_blank" rel="noreferrer">
              {t("Open on GitHub")}
            </a>
          </Button>
          <DialogClose asChild>
            <Button>{t("Close")}</Button>
          </DialogClose>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
