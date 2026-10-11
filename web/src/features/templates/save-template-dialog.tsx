import { useQuery } from "@tanstack/react-query"
import { useNavigate } from "@tanstack/react-router"
import { t } from "i18next"
import { type FormEvent, useState } from "react"
import { toast } from "sonner"
import { Button } from "@/components/ui/button"
import { Dialog, DialogClose, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { Field, FieldError, FieldGroup, FieldLabel } from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import { Textarea } from "@/components/ui/textarea"
import { pluginsQuery } from "@/features/plugins/api"
import { propertiesQuery } from "@/features/properties/api"
import type { Server } from "@/features/servers/api"
import { displayVersion, serverType } from "@/features/servers/server-types"
import { formatMegabytes } from "@/lib/format"
import { useSaveTemplate } from "./api"

/**
 * Saves the settings, server.properties, tags and the plugins from Modrinth or Hangar of a server as a template.
 * Other plugin files, plugin configurations and worlds are left out.
 */
export function SaveTemplateDialog({
  nodeId,
  server,
  open,
  onOpenChange,
}: {
  nodeId: string
  server: Server
  open: boolean
  onOpenChange: (open: boolean) => void
}) {
  const [name, setName] = useState(server.name)
  const [description, setDescription] = useState("")
  const type = serverType(server.type)
  const properties = useQuery({ ...propertiesQuery(nodeId, server.id), enabled: open && !type.proxy })
  const plugins = useQuery({ ...pluginsQuery({ nodeId, serverId: server.id }), enabled: open && !!type.addons })
  const save = useSaveTemplate()
  const navigate = useNavigate()

  const locked = new Set(properties.data?.locked.map((l) => l.key))
  const props = Object.fromEntries(Object.entries(properties.data?.properties ?? {}).filter(([key]) => !locked.has(key)))
  // Turned-off plugins stay out of the template, as the server doesn't load them.
  const enabled = plugins.data?.plugins.filter((p) => !p.disabled) ?? []
  const projects = enabled.flatMap((p) => (p.project ? [p.project.id] : []))
  const others = enabled.length - projects.length
  const software = type.proxy ? type.label : `${type.label} ${displayVersion(server.version)}`
  const count = projects.length
  const summary = [
    t("{{software}} with {{memory}} and the server's settings", { software, memory: formatMegabytes(server.memoryMb) }),
    !type.proxy &&
      t("{{count}} properties of server.properties", {
        count: Object.keys(props).length,
        defaultValue_one: "{{count}} property of server.properties",
      }),
    type.addons &&
      (type.addons.kind === "mods"
        ? t("{{count}} mods from Modrinth", { count, defaultValue_one: "{{count}} mod from Modrinth" })
        : t("{{count}} plugins from Modrinth or Hangar", { count, defaultValue_one: "{{count}} plugin from Modrinth or Hangar" })),
    server.tags.length > 0 && t("The tags {{tags}}", { tags: server.tags.join(", ") }),
    type.addons &&
      others > 0 &&
      t("{{count}} other files are left out", { count: others, defaultValue_one: "{{count}} other file is left out" }),
  ].filter(Boolean)

  function submit(event: FormEvent) {
    event.preventDefault()
    const { version, memoryMb, java, restartPolicy, aikarFlags, jvmOptions, cpuLimit, stopTimeout, timeZone } = server
    save.mutate(
      {
        name: name.trim(),
        description,
        type: server.type,
        version,
        memoryMb,
        java,
        restartPolicy,
        aikarFlags,
        jvmOptions,
        cpuLimit,
        stopTimeout,
        timeZone,
        healthStart: server.healthStart,
        crashLimit: server.crashLimit,
        stableAfter: server.stableAfter,
        unhealthyRestart: server.unhealthyRestart,
        properties: props,
        tags: server.tags,
        plugins: projects,
        versions: {},
        channels: Object.fromEntries(enabled.flatMap((p) => (p.project && p.tracks ? [[p.project.id, p.tracks]] : []))),
      },
      {
        onSuccess: (template) => {
          toast.success(t("Saved {{server}} as the template {{template}}", { server: server.name, template: template.name }))
          onOpenChange(false)
          void navigate({ to: "/templates/$templateId", params: { templateId: template.id } })
        },
      },
    )
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-lg">
        <form onSubmit={submit} className="grid gap-6">
          <DialogHeader>
            <DialogTitle>{t("Save as template")}</DialogTitle>
            <DialogDescription>
              {t("New servers can start with the setup of {{name}}. Worlds and plugin configurations aren't included.", {
                name: server.name,
              })}
            </DialogDescription>
          </DialogHeader>
          <FieldGroup>
            <Field>
              <FieldLabel htmlFor="template-name">{t("Name")}</FieldLabel>
              <Input id="template-name" required maxLength={64} value={name} onChange={(e) => setName(e.target.value)} />
            </Field>
            <Field>
              <FieldLabel htmlFor="template-description">{t("Description")}</FieldLabel>
              <Textarea
                id="template-description"
                rows={2}
                maxLength={500}
                value={description}
                onChange={(e) => setDescription(e.target.value)}
              />
            </Field>
            <ul className="grid gap-1.5 rounded-lg bg-muted/60 px-4 py-3 text-sm">
              {summary.map((line) => (
                <li key={String(line)} className="list-inside list-disc">
                  {line}
                </li>
              ))}
            </ul>
            {(properties.error || plugins.error) && <FieldError>{(properties.error ?? plugins.error)?.message}</FieldError>}
            {save.error && <FieldError>{save.error.message}</FieldError>}
          </FieldGroup>
          <DialogFooter>
            <DialogClose asChild>
              <Button variant="outline">{t("Cancel")}</Button>
            </DialogClose>
            <Button type="submit" disabled={save.isPending || properties.isLoading || plugins.isLoading}>
              {save.isPending ? t("Saving…") : t("Save template")}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
