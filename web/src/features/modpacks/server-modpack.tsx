import { ArrowCircleUpIcon, ArrowsClockwiseIcon, XIcon } from "@phosphor-icons/react"
import { useQuery } from "@tanstack/react-query"
import { t } from "i18next"
import { useState } from "react"
import { Callout, ErrorCallout } from "@/components/callout"
import { Pill } from "@/components/status"
import { Button } from "@/components/ui/button"
import { Field, FieldLabel } from "@/components/ui/field"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { useAccess } from "@/features/access/use-access"
import { useOperation } from "@/features/operations/use-operation"
import { PluginIcon } from "@/features/plugins/plugin-icon"
import type { Server } from "@/features/servers/api"
import {
  describeVersion,
  type ModpackBackup,
  type ModpackChange,
  type ModpackVersion,
  modpackVersionsQuery,
  type ServerModpack,
  serverModpackQuery,
  useUpdateModpack,
} from "./api"
import { UpdateModpackDialog } from "./update-modpack-dialog"

const published = (v: ModpackVersion) => Date.parse(v.published)

/** The modpack a server was created from, with the versions of the pack it can move to; nothing for other servers. */
export function ServerModpackSection({ nodeId, server }: { nodeId: string; server: Server }) {
  const { data: pack } = useQuery(serverModpackQuery(nodeId, server.id))
  return pack ? <ModpackSection nodeId={nodeId} server={server} pack={pack} /> : null
}

function ModpackSection({ nodeId, server, pack }: { nodeId: string; server: Server; pack: ServerModpack }) {
  const { can } = useAccess()
  const manage = can("servers.settings", nodeId, server.id) && can("plugins.manage", nodeId, server.id)
  // Versions for other mod loaders can't be installed, as the server would need another type.
  const versions = useQuery({ ...modpackVersionsQuery(pack.project.id), select: (all) => all.filter((v) => v.loaders.includes(server.type)) })
  const current = versions.data?.find((v) => v.id === pack.version)
  const others = versions.data?.filter((v) => v.id !== pack.version) ?? []
  const newest = versions.data?.find((v) => v.channel === "release") ?? versions.data?.[0]
  const newer = newest && newest.id !== pack.version && (!current || published(newest) > published(current)) ? newest : undefined
  const [chosen, setChosen] = useState<string>()
  const target = others.find((v) => v.id === (chosen ?? newer?.id))
  const older = !!target && !!current && published(target) < published(current)
  const [change, setChange] = useState<ModpackChange>()
  const update = useUpdateModpack(nodeId, server.id)
  const operation = useOperation()

  function run(version: ModpackVersion, backup: ModpackBackup) {
    setChange(undefined)
    operation.run((onStart) => update.mutateAsync({ version: version.id, ...backup, onStart }), {
      title: t("Moving {{name}} to version {{number}} of its modpack…", { name: server.name, number: version.number }),
      notify: true,
      done: (change) => {
        const kept =
          change.kept.length > 0 &&
          t("{{count}} files stay as they are, as they changed on the server.", {
            count: change.kept.length,
            defaultValue_one: "{{count}} file stays as it is, as it changed on the server.",
          })
        return {
          message: t("{{name}} has version {{number}} of its modpack now", { name: server.name, number: change.number }),
          description: [change.warning, kept].filter(Boolean).join(" ") || undefined,
          warning: !!change.warning || change.kept.length > 0,
        }
      },
      then: (change) => {
        setChange(change)
        setChosen(undefined)
      },
    })
  }

  const action = older ? t("Change version") : t("Update modpack")
  return (
    <section className="surface space-y-5 rounded-xl px-5 py-5 sm:px-8" aria-label={t("Modpack")}>
      <div className="flex flex-wrap items-center gap-x-4 gap-y-3">
        <PluginIcon src={pack.project.icon} />
        {/* Wide enough for the version, so that the note of a newer one moves below on phones. */}
        <div className="min-w-40 flex-1">
          <h2 className="heading text-base">{t("Modpack")}</h2>
          <p className="truncate text-sm text-muted-foreground">
            {t("{{pack}}, version {{number}}", { pack: pack.project.title, number: pack.number })}
          </p>
        </div>
        {newer && (
          <Pill tone="info">
            <ArrowCircleUpIcon className="size-3.5" />
            {t("Version {{number}} is available", { number: newer.number })}
          </Pill>
        )}
      </div>

      {versions.error ? (
        <ErrorCallout error={versions.error} />
      ) : (
        manage && (
          <div className="flex flex-col gap-3 sm:flex-row sm:items-end">
            <Field className="min-w-0 flex-1">
              <FieldLabel htmlFor="modpack-target">{t("Version to install")}</FieldLabel>
              {/* Radix reports "" while the options of a new value load; that is no choice. */}
              <Select value={target?.id ?? ""} onValueChange={(v) => v && setChosen(v)} disabled={others.length === 0}>
                <SelectTrigger id="modpack-target" className="w-full">
                  <SelectValue
                    placeholder={
                      versions.isPending ? t("Loading…") : others.length > 0 ? t("Choose a version") : t("No other version for this server")
                    }
                  />
                </SelectTrigger>
                <SelectContent>
                  {others.map((v) => (
                    <SelectItem key={v.id} value={v.id}>
                      {describeVersion(v)}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </Field>
            <UpdateModpackDialog
              trigger={
                <Button disabled={!target || update.isPending}>
                  <ArrowsClockwiseIcon />
                  {action}
                </Button>
              }
              nodeId={nodeId}
              server={server}
              target={target}
              older={older}
              action={action}
              onUpdate={run}
            />
          </div>
        )
      )}

      {change && <ChangeSummary change={change} onDismiss={() => setChange(undefined)} />}
    </section>
  )
}

/** What an update did, with the files that stay as they are. */
function ChangeSummary({ change, onDismiss }: { change: ModpackChange; onDismiss: () => void }) {
  return (
    <Callout
      tone={change.kept.length > 0 ? "warning" : "success"}
      role="status"
      title={t("Version {{number}} is installed", { number: change.number })}
      className="relative pr-12"
    >
      <Button variant="ghost" size="icon-sm" className="absolute top-2 right-2" aria-label={t("Dismiss")} onClick={onDismiss}>
        <XIcon />
      </Button>
      <p>
        {change.backup
          ? t("Files written: {{written}}, removed: {{removed}}. The backup “{{backup}}” has the server as it was before.", {
              written: change.written.length,
              removed: change.removed.length,
              backup: change.backup,
            })
          : t("Files written: {{written}}, removed: {{removed}}.", { written: change.written.length, removed: change.removed.length })}
      </p>
      {change.kept.length > 0 && (
        <>
          <p className="mt-2">{t("These files changed on the server, so the update left them as they are:")}</p>
          <ul className="mt-1 max-h-48 overflow-y-auto font-mono text-xs">
            {change.kept.map((path) => (
              <li key={path} className="truncate">
                {path}
              </li>
            ))}
          </ul>
        </>
      )}
    </Callout>
  )
}
