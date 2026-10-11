import { ArrowsClockwiseIcon } from "@phosphor-icons/react"
import { useQuery } from "@tanstack/react-query"
import { getRouteApi, useBlocker } from "@tanstack/react-router"
import { t } from "i18next"
import { type FormEvent, useState } from "react"
import { Trans } from "react-i18next"
import { ConfirmDialog } from "@/components/confirm-dialog"
import { FormSection } from "@/components/form-section"
import { Button } from "@/components/ui/button"
import { Field, FieldDescription, FieldLabel } from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import { useAccess } from "@/features/access/use-access"
import { ServerModpackSection } from "@/features/modpacks/server-modpack"
import { nodeQuery } from "@/features/nodes/api"
import { useOperation } from "@/features/operations/use-operation"
import { UsageWarnings } from "@/features/usage/thresholds"
import { cn } from "@/lib/utils"
import { CrashFields, HealthStartField, initialHeap, ResourceFields } from "./advanced-fields"
import { freeMemoryMb, type Server, type ServerSettings, serversQuery, useServer, useUpdateImage, useUpdateServer } from "./api"
import { isModded, serverType, splitOptions } from "./server-types"
import {
  CpuLimitField,
  JavaFields,
  JvmOptionsField,
  MemoryField,
  RestartPolicyField,
  StopTimeoutField,
  TimeZoneField,
  VersionField,
} from "./settings-fields"

const route = getRouteApi("/_app/nodes/$nodeId/servers/$serverId/settings")

/** The Settings tab of a server. */
export function SettingsPage() {
  const { nodeId, serverId } = route.useParams()
  const { server } = useServer(nodeId, serverId)
  const { can } = useAccess()
  if (!server) return null
  // Remounting on save resets the form to what the agent applied.
  return (
    <div className="space-y-6">
      <SettingsForm key={JSON.stringify(settingsOf(server))} nodeId={nodeId} server={server} />
      <ServerModpackSection nodeId={nodeId} server={server} />
      <UsageWarnings nodeId={nodeId} serverId={server.id} editable={can("servers.settings", nodeId, server.id)} />
      <UpdateImage nodeId={nodeId} server={server} />
    </div>
  )
}

/** Servers keep the image they were created with, until it is updated here. */
function UpdateImage({ nodeId, server }: { nodeId: string; server: Server }) {
  const update = useUpdateImage(nodeId, server.id)
  const operation = useOperation()
  const running = server.state !== "stopped"
  function run() {
    operation.run((onStart) => update.mutateAsync({ onStart }), {
      title: t("Update the image of {{name}}", { name: server.name }),
      notify: true,
      done: ({ updated }) => ({
        message: !updated
          ? t("{{name}} has the newest image already", { name: server.name })
          : running
            ? t("Updated and restarted {{name}}", { name: server.name })
            : t("Updated {{name}}", { name: server.name }),
      }),
    })
  }
  const button = (
    <Button variant="outline" disabled={update.isPending} onClick={running ? undefined : run}>
      <ArrowsClockwiseIcon />
      {t("Update image")}
    </Button>
  )
  return (
    <section className="surface flex flex-wrap items-center justify-between gap-4 rounded-xl px-5 py-5 sm:px-8" aria-label={t("Image")}>
      <h2 className="heading text-base">{t("Image")}</h2>
      {running ? (
        <ConfirmDialog
          trigger={button}
          title={t("Update the image of {{name}}?", { name: server.name })}
          description={t("If there is a newer image, the server's container is created again with the same data, so the server restarts.")}
          action={t("Update image")}
          onConfirm={run}
        />
      ) : (
        button
      )}
    </section>
  )
}

function settingsOf(s: Server): ServerSettings {
  const { name, version, memoryMb, port, java, restartPolicy, aikarFlags, jvmOptions, cpuLimit, loaderVersion, stopTimeout, timeZone } = s
  const settings = { name, version, memoryMb, port, java, restartPolicy, aikarFlags, jvmOptions, cpuLimit, loaderVersion, stopTimeout, timeZone }
  const { healthStart, crashLimit, stableAfter, unhealthyRestart, processLimit, initMemoryMb } = s
  return { ...settings, healthStart, crashLimit, stableAfter, unhealthyRestart, processLimit, initMemoryMb }
}

function SettingsForm({ nodeId, server }: { nodeId: string; server: Server }) {
  const initial = settingsOf(server)
  const [form, setForm] = useState({ ...initial, jvmOptions: initial.jvmOptions.join("\n") })
  const update = useUpdateServer(nodeId, server.id)
  const operation = useOperation()
  const { data: node } = useQuery(nodeQuery(nodeId))
  const { data: servers } = useQuery(serversQuery(nodeId))
  const game = !serverType(server.type).proxy
  const modded = isModded(server.type)
  const settings: ServerSettings = { ...form, jvmOptions: splitOptions(form.jvmOptions), initMemoryMb: initialHeap(form) }
  const dirty = JSON.stringify(settings) !== JSON.stringify(initial)
  const blocker = useBlocker({ shouldBlockFn: () => dirty && !update.isPending, enableBeforeUnload: () => dirty, withResolver: true })
  const set = (change: Partial<typeof form>) => setForm({ ...form, ...change })

  function submit(event: FormEvent) {
    event.preventDefault()
    operation.run((onStart) => update.mutateAsync({ settings, onStart }), {
      title: server.state === "stopped" ? t("Saving…") : t("Saving and restarting {{name}}…", { name: server.name }),
      notify: true,
      done: ({ warning }) => ({ message: t("Saved the settings of {{name}}", { name: form.name }), description: warning, warning: !!warning }),
    })
  }

  return (
    <form onSubmit={submit} className="surface rounded-xl px-5 sm:px-8">
      <FormSection title={t("General")}>
        <Field>
          <FieldLabel htmlFor="settings-name">{t("Name")}</FieldLabel>
          <Input id="settings-name" required maxLength={32} value={form.name} onChange={(e) => set({ name: e.target.value })} />
        </Field>
        <div className={cn("grid gap-4", modded ? "sm:grid-cols-2" : "sm:grid-cols-3")}>
          {game && (
            <VersionField
              id="settings-version"
              value={form.version === "LATEST" ? "" : form.version}
              onChange={(version) => set({ version: version.trim() || "LATEST" })}
            />
          )}
          {modded && (
            <Field>
              <FieldLabel htmlFor="settings-loader">{t("Loader version")}</FieldLabel>
              <Input
                id="settings-loader"
                placeholder={t("Newest")}
                className="font-mono"
                value={form.loaderVersion}
                onChange={(e) => set({ loaderVersion: e.target.value.trim() })}
              />
            </Field>
          )}
          <MemoryField
            id="settings-memory"
            value={form.memoryMb}
            onChange={(memoryMb) => set({ memoryMb })}
            freeMb={freeMemoryMb(node, servers, server)}
          />
          <Field>
            <FieldLabel htmlFor="settings-port">{t("Port")}</FieldLabel>
            <Input
              id="settings-port"
              type="number"
              min={1024}
              max={65535}
              required
              className="font-mono"
              value={form.port}
              onChange={(e) => set({ port: e.target.valueAsNumber || 0 })}
            />
          </Field>
        </div>
        {game && form.version !== initial.version && (
          <FieldDescription>
            {t("Worlds can't be opened by older Minecraft versions. Make a backup before you downgrade.")}
            {modded && form.loaderVersion && ` ${t("Empty the loader version unless it suits the new Minecraft version.")}`}
          </FieldDescription>
        )}
        <TimeZoneField value={form.timeZone} onChange={(timeZone) => set({ timeZone })} />
      </FormSection>

      <FormSection title={t("Starting and stopping")}>
        <RestartPolicyField value={form.restartPolicy} onChange={(restartPolicy) => set({ restartPolicy })} />
        <StopTimeoutField value={form.stopTimeout} onChange={(stopTimeout) => set({ stopTimeout })} />
        {game && <HealthStartField value={form.healthStart} onChange={(healthStart) => set({ healthStart })} />}
        <CrashFields id="settings-crashes" value={form} onChange={set} />
      </FormSection>

      <FormSection title={t("Java")}>
        {game && <JavaFields java={form.java} aikarFlags={form.aikarFlags} onChange={set} />}
        <JvmOptionsField value={form.jvmOptions} onChange={(jvmOptions) => set({ jvmOptions })} />
      </FormSection>

      <FormSection title={t("Resources")}>
        <CpuLimitField value={form.cpuLimit} onChange={(cpuLimit) => set({ cpuLimit })} cpus={node?.info?.cpuCount} />
        <ResourceFields id="settings-resources" value={form} onChange={set} />
        <p className="text-sm text-muted-foreground">
          <Trans
            i18nKey="The data is kept in the storage location <name/>."
            components={{ name: <span className="font-medium text-foreground">{server.storage}</span> }}
          />
        </p>
      </FormSection>

      <div className="-mx-5 flex flex-wrap-reverse items-center justify-end gap-x-6 gap-y-3 rounded-b-2xl bg-muted/50 px-5 py-4 sm:-mx-8 sm:px-8">
        <p className="text-sm text-muted-foreground">
          {server.state === "stopped"
            ? t("The server's container is created again with the same data.")
            : t("The server's container is created again with the same data, so the server restarts.")}
        </p>
        <Button type="submit" disabled={!dirty || update.isPending}>
          {update.isPending ? t("Saving…") : t("Save settings")}
        </Button>
      </div>
      <ConfirmDialog
        open={blocker.status === "blocked"}
        onOpenChange={(open) => !open && blocker.reset?.()}
        title={t("Discard your changes?")}
        description={t("Your changes to the settings haven't been saved.")}
        action={t("Discard changes")}
        destructive
        onConfirm={() => blocker.proceed?.()}
      />
    </form>
  )
}
