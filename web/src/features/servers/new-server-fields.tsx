import { useQuery } from "@tanstack/react-query"
import { t } from "i18next"
import { Field, FieldDescription, FieldLabel } from "@/components/ui/field"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { useAccess } from "@/features/access/use-access"
import { templatesQuery } from "@/features/templates/api"
import type { NewServerSettings } from "./api"
import { serverType } from "./server-types"
import { JavaField, MemoryField, StopTimeoutField, TimeZoneField } from "./settings-fields"
import { SoftwareOptions } from "./software"

const none = "none"

/** What Create server starts with: a template, or else the software, memory, Java, stop timeout and time zone. */
export function NewServerFields({ value, onChange }: { value: NewServerSettings; onChange: (value: NewServerSettings) => void }) {
  const canSeeTemplates = useAccess().can("templates.view")
  const { data: templates } = useQuery({ ...templatesQuery, enabled: canSeeTemplates })
  const set = (change: Partial<NewServerSettings>) => onChange({ ...value, ...change })
  // A template that was deleted is ignored, as if none was chosen.
  const template = templates?.find((candidate) => candidate.id === value.template)
  return (
    <>
      <Field>
        <FieldLabel htmlFor="settings-new-template">{t("Template")}</FieldLabel>
        <Select
          value={template?.id ?? none}
          onValueChange={(id) => id && set({ template: id === none ? "" : id })}
          disabled={!templates}
        >
          <SelectTrigger id="settings-new-template" className="w-full sm:w-72">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value={none}>{t("No template")}</SelectItem>
            {templates?.map((option) => (
              <SelectItem key={option.id} value={option.id}>
                {option.name}
                <span className="text-muted-foreground">{serverType(option.type).label}</span>
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        <FieldDescription>
          {canSeeTemplates
            ? t("Create server chooses it for those who may see templates. The settings below apply to the others, and without a template.")
            : t("Choosing a template needs the permission to see templates.")}
        </FieldDescription>
      </Field>
      <div className="grid gap-4 sm:grid-cols-3">
        <Field>
          <FieldLabel htmlFor="settings-new-type">{t("Software")}</FieldLabel>
          <Select value={value.type} onValueChange={(type) => type && set({ type })}>
            <SelectTrigger id="settings-new-type" className="w-full">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SoftwareOptions chosen={value.type} />
            </SelectContent>
          </Select>
        </Field>
        <MemoryField id="settings-new-memory" label={t("Memory of game servers")} value={value.memoryMb} onChange={(memoryMb) => set({ memoryMb })} />
        <MemoryField
          id="settings-new-proxy-memory"
          label={t("Memory of proxies")}
          value={value.proxyMemoryMb}
          onChange={(proxyMemoryMb) => set({ proxyMemoryMb })}
        />
      </div>
      <FieldDescription>{t("Modpacks get at least 4 GiB.")}</FieldDescription>
      <JavaField id="settings-new-java" value={value.java} onChange={(java) => set({ java })} />
      <StopTimeoutField id="settings-new-stop-timeout" value={value.stopTimeout} onChange={(stopTimeout) => set({ stopTimeout })} />
      <TimeZoneField id="settings-new-time-zone" value={value.timeZone} onChange={(timeZone) => set({ timeZone })} />
      <p className="text-sm text-muted-foreground">
        {t("New servers start on the node each user created a server on last, while it is online, or else on the first online node.")}
      </p>
    </>
  )
}
