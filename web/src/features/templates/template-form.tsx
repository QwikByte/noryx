import { CaretDownIcon, PlusIcon, XIcon } from "@phosphor-icons/react"
import { useBlocker } from "@tanstack/react-router"
import { t } from "i18next"
import { type FormEvent, useState } from "react"
import { ConfirmDialog } from "@/components/confirm-dialog"
import { FormSection } from "@/components/form-section"
import { Pill } from "@/components/status"
import { Button } from "@/components/ui/button"
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle, DialogTrigger } from "@/components/ui/dialog"
import { Field, FieldDescription, FieldError, FieldLabel } from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import { Select, SelectContent, SelectTrigger, SelectValue } from "@/components/ui/select"
import { Textarea } from "@/components/ui/textarea"
import type { Project } from "@/features/plugins/api"
import { TracksPill } from "@/features/plugins/channel-pill"
import { PluginIcon } from "@/features/plugins/plugin-icon"
import { PluginSearch } from "@/features/plugins/plugin-search"
import { VersionMenu } from "@/features/plugins/version-menu"
import { defaults, serverType, splitOptions } from "@/features/servers/server-types"
import {
  CpuLimitField,
  HealthStartField,
  JavaFields,
  JvmOptionsField,
  MemoryField,
  RestartPolicyField,
  StopTimeoutField,
  TimeZoneField,
  VersionField,
} from "@/features/servers/settings-fields"
import { EndOfLifeNotice, SoftwareOptions } from "@/features/servers/software"
import { TagsField } from "@/features/servers/tags"
import { keptChannels, keptVersions, parseProperties, propertiesText, type TemplateDraft, type TemplateInput, type TemplatePlugin } from "./api"

/** Compares drafts regardless of the order of their properties. */
const snapshot = (d: TemplateDraft) => JSON.stringify({ ...d, name: d.name.trim(), properties: propertiesText(d.properties) })

/** Edits the settings, server.properties, tags and plugins of a template. */
export function TemplateForm({
  initial,
  submitLabel,
  pending,
  error,
  onSubmit,
}: {
  initial: TemplateDraft
  submitLabel: string
  pending: boolean
  error: Error | null
  onSubmit: (input: TemplateInput) => void
}) {
  const [form, setForm] = useState({
    ...initial,
    jvmOptions: initial.jvmOptions.join("\n"),
    properties: propertiesText(initial.properties),
  })
  const type = serverType(form.type)
  const draft: TemplateDraft = {
    ...form,
    name: form.name.trim(),
    version: type.proxy ? "LATEST" : form.version,
    jvmOptions: splitOptions(form.jvmOptions),
    properties: type.proxy ? {} : parseProperties(form.properties),
    plugins: type.addons ? form.plugins : [],
  }
  const input: TemplateInput = {
    ...draft,
    plugins: draft.plugins.map((p) => p.id),
    versions: keptVersions(draft.plugins),
    channels: keptChannels(draft.plugins),
  }
  const dirty = snapshot({ ...draft, plugins: form.plugins }) !== snapshot(initial)
  const blocker = useBlocker({ shouldBlockFn: () => dirty && !pending, enableBeforeUnload: () => dirty, withResolver: true })
  const set = (change: Partial<typeof form>) => setForm({ ...form, ...change })
  const setPlugin = (plugin: TemplatePlugin) => set({ plugins: form.plugins.map((p) => (p.id === plugin.id ? plugin : p)) })

  function submit(event: FormEvent) {
    event.preventDefault()
    onSubmit(input)
  }

  return (
    <form onSubmit={submit} className="surface rounded-xl px-5 sm:px-8">
      <FormSection title={t("General")}>
        <div className="grid gap-4 sm:grid-cols-2">
          <Field>
            <FieldLabel htmlFor="template-name">{t("Name")}</FieldLabel>
            <Input
              id="template-name"
              required
              maxLength={64}
              placeholder={t("Survival")}
              value={form.name}
              onChange={(e) => set({ name: e.target.value })}
            />
          </Field>
          <Field>
            <FieldLabel htmlFor="template-type">{t("Software")}</FieldLabel>
            <Select
              value={form.type}
              onValueChange={(next) =>
                set({ type: next, memoryMb: form.memoryMb === defaults(form.type).memoryMb ? defaults(next).memoryMb : form.memoryMb })
              }
            >
              <SelectTrigger id="template-type" className="w-full">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SoftwareOptions chosen={initial.type} />
              </SelectContent>
            </Select>
          </Field>
        </div>
        <EndOfLifeNotice type={form.type} />
        <Field>
          <FieldLabel htmlFor="template-description">{t("Description")}</FieldLabel>
          <Textarea
            id="template-description"
            rows={2}
            maxLength={500}
            placeholder={t("Survival with permissions and an economy")}
            value={form.description}
            onChange={(e) => set({ description: e.target.value })}
          />
        </Field>
        <Field>
          <FieldLabel htmlFor="template-tags">{t("Tags")}</FieldLabel>
          <TagsField id="template-tags" value={form.tags} onChange={(tags) => set({ tags })} />
          <FieldDescription>
            {t("Servers created from the template get these tags. File sets of the tags still have to be applied to them.")}
          </FieldDescription>
        </Field>
        <div className="grid gap-4 sm:grid-cols-2">
          {!type.proxy && (
            <VersionField
              id="template-version"
              value={form.version === "LATEST" ? "" : form.version}
              onChange={(version) => set({ version: version.trim() || "LATEST" })}
            />
          )}
          <MemoryField id="template-memory" value={form.memoryMb} onChange={(memoryMb) => set({ memoryMb })} />
        </div>
        <TimeZoneField id="template-time-zone" value={form.timeZone} onChange={(timeZone) => set({ timeZone })} />
      </FormSection>

      <FormSection title={t("Starting and stopping")}>
        <RestartPolicyField value={form.restartPolicy} onChange={(restartPolicy) => set({ restartPolicy })} />
        <StopTimeoutField id="template-stop-timeout" value={form.stopTimeout} onChange={(stopTimeout) => set({ stopTimeout })} />
        {!type.proxy && (
          <HealthStartField id="template-health-start" value={form.healthStart} onChange={(healthStart) => set({ healthStart })} />
        )}
      </FormSection>

      <FormSection title={t("Java")}>
        {!type.proxy && <JavaFields java={form.java} aikarFlags={form.aikarFlags} onChange={set} />}
        <JvmOptionsField value={form.jvmOptions} onChange={(jvmOptions) => set({ jvmOptions })} />
        <CpuLimitField value={form.cpuLimit} onChange={(cpuLimit) => set({ cpuLimit })} />
      </FormSection>

      {!type.proxy && (
        // i18next-instrument-ignore-next-line: the name of the file
        <FormSection title="server.properties">
          <Field>
            <FieldLabel htmlFor="template-properties">{t("Properties")}</FieldLabel>
            <Textarea
              id="template-properties"
              rows={8}
              className="font-mono"
              // i18next-instrument-ignore-next-line: an example of what to enter
              placeholder={"difficulty=hard\nmax-players=50\nmotd=Welcome!"}
              value={form.properties}
              onChange={(e) => set({ properties: e.target.value })}
            />
            <FieldDescription>
              {t("One property per line as key=value. The port, address and RCON are set by the manager and can't be changed here.")}
            </FieldDescription>
          </Field>
        </FormSection>
      )}

      {type.addons && (
        <FormSection title={type.addons.kind === "mods" ? t("Mods") : t("Plugins")}>
          {form.plugins.length > 0 && (
            <Field>
              <ul className="divide-y rounded-lg bg-muted/60">
                {form.plugins.map((p) => (
                  <li key={p.id} className="flex items-center gap-2 py-1.5 pr-1.5 pl-2 text-sm font-medium">
                    <PluginIcon src={p.icon} className="size-6 rounded-md [&>svg]:size-3.5" />
                    <span className="min-w-0 flex-1 truncate">{p.title}</span>
                    <TracksPill channel={p.channel} />
                    <VersionMenu
                      project={p.id}
                      title={p.title}
                      type={form.type}
                      version={form.version}
                      current={p.version}
                      channel={p.channel}
                      onChannel={(c) => setPlugin({ ...p, channel: c === "release" ? undefined : c })}
                      onNewest={() => setPlugin({ id: p.id, slug: p.slug, title: p.title, icon: p.icon, channel: p.channel })}
                      onPick={(v) => setPlugin({ ...p, version: v.id, versionNumber: v.number })}
                    >
                      <Button
                        type="button"
                        size="xs"
                        variant="outline"
                        title={t("Version of {{name}}", { name: p.title })}
                        className="max-w-32 bg-card"
                      >
                        <span className={p.versionNumber ? "truncate font-mono" : "truncate"}>{p.versionNumber ?? t("Newest")}</span>
                        <CaretDownIcon />
                      </Button>
                    </VersionMenu>
                    <Button
                      type="button"
                      size="icon-xs"
                      variant="ghost"
                      aria-label={t("Remove {{name}}", { name: p.title })}
                      onClick={() => set({ plugins: form.plugins.filter((x) => x.id !== p.id) })}
                    >
                      <XIcon />
                    </Button>
                  </li>
                ))}
              </ul>
              <FieldDescription>
                {t(
                  "New servers get the newest version of the channel that suits them, a release unless chosen, or the version chosen here, and keep the channel for updates. A chosen version that doesn't suit a server isn't installed on it.",
                )}
              </FieldDescription>
            </Field>
          )}
          <AddPlugins
            type={form.type}
            version={form.version}
            kind={type.addons.kind}
            chosen={form.plugins}
            onAdd={(p) => set({ plugins: [...form.plugins, p] })}
          />
        </FormSection>
      )}

      <div className="-mx-5 flex flex-wrap-reverse items-center justify-end gap-x-6 gap-y-3 rounded-b-2xl bg-muted/50 px-5 py-4 sm:-mx-8 sm:px-8">
        {error && <FieldError className="mr-auto">{error.message}</FieldError>}
        <Button type="submit" disabled={!dirty || pending}>
          {pending ? t("Saving…") : submitLabel}
        </Button>
      </div>
      <ConfirmDialog
        open={blocker.status === "blocked"}
        onOpenChange={(open) => !open && blocker.reset?.()}
        title={t("Discard your changes?")}
        description={t("Your changes to the template haven't been saved.")}
        action={t("Discard changes")}
        destructive
        onConfirm={() => blocker.proceed?.()}
      />
    </form>
  )
}

function AddPlugins({
  type,
  version,
  kind,
  chosen,
  onAdd,
}: {
  type: string
  version: string
  kind: "plugins" | "mods"
  chosen: Project[]
  onAdd: (p: Project) => void
}) {
  return (
    <Dialog>
      <DialogTrigger asChild>
        <Button type="button" variant="outline" className="w-fit">
          <PlusIcon />
          {kind === "mods" ? t("Add mods") : t("Add plugins")}
        </Button>
      </DialogTrigger>
      <DialogContent className="sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle>{kind === "mods" ? t("Add mods to the template") : t("Add plugins to the template")}</DialogTitle>
          <DialogDescription>
            {kind === "mods"
              ? t("Only mods for {{type}} are shown. What they require is installed with them.", { type: serverType(type).label })
              : t("Only plugins for {{type}} are shown. What they require is installed with them.", { type: serverType(type).label })}
          </DialogDescription>
        </DialogHeader>
        <PluginSearch
          type={type}
          version={version}
          autoFocus
          action={(hit) =>
            chosen.some((p) => p.id === hit.id) ? (
              <Pill tone="success">{t("Added")}</Pill>
            ) : (
              <Button
                type="button"
                size="sm"
                variant="outline"
                onClick={() => onAdd({ id: hit.id, slug: hit.slug, title: hit.title, icon: hit.icon })}
              >
                <PlusIcon />
                {t("Add")}
              </Button>
            )
          }
        />
      </DialogContent>
    </Dialog>
  )
}
