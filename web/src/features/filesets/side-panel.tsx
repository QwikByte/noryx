import { CrosshairIcon, NotePencilIcon, PlusIcon, XIcon } from "@phosphor-icons/react"
import { useQuery } from "@tanstack/react-query"
import { t } from "i18next"
import { useState } from "react"
import { Button } from "@/components/ui/button"
import { Dialog, DialogClose, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { Field, FieldError, FieldLabel } from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import { Textarea } from "@/components/ui/textarea"
import { networksQuery } from "@/features/networks/api"
import { allServersQuery } from "@/features/servers/api"
import { NetworkField, TagField } from "@/features/servers/group-fields"
import type { ErrorsAt, FieldErrors } from "@/lib/use-draft"
import type { FileSet, SetInput, Target } from "./api"
import { targetKey, targetLabel } from "./labels"
import { PanelSection } from "./panel-section"
import { SecretsSection } from "./secrets"
import { VariablesSection } from "./variables"

/** What a set is for and what it needs besides its files: its targets, variables, secrets and details. */
export function SidePanel({
  set,
  draft,
  editable,
  onChange,
  errorAt,
}: {
  set: FileSet
  draft: SetInput
  editable: boolean
  onChange: (change: Partial<SetInput>) => void
  errorAt?: ErrorsAt
}) {
  return (
    <div className="grid content-start items-start gap-4 lg:grid-cols-2 2xl:grid-cols-1">
      <TargetsSection targets={draft.targets} editable={editable} onChange={(targets) => onChange({ targets })} errors={errorAt?.("targets")} />
      <VariablesSection
        variables={draft.variables}
        files={draft.files}
        editable={editable}
        onChange={(variables) => onChange({ variables })}
        errors={errorAt?.("variables")}
      />
      <SecretsSection set={set} files={draft.files} editable={editable} />
      <PanelSection icon={NotePencilIcon} title={t("Details")}>
        <Field data-invalid={!!errorAt?.("name")}>
          <FieldLabel htmlFor="fileset-name">{t("Name")}</FieldLabel>
          <Input
            id="fileset-name"
            required
            maxLength={64}
            disabled={!editable}
            aria-invalid={!!errorAt?.("name")}
            value={draft.name}
            onChange={(e) => onChange({ name: e.target.value })}
          />
          <FieldError errors={errorAt?.("name")} />
        </Field>
        <Field data-invalid={!!errorAt?.("description")}>
          <FieldLabel htmlFor="fileset-description">{t("Description")}</FieldLabel>
          <Textarea
            id="fileset-description"
            maxLength={500}
            rows={3}
            disabled={!editable}
            aria-invalid={!!errorAt?.("description")}
            value={draft.description}
            onChange={(e) => onChange({ description: e.target.value })}
          />
          <FieldError errors={errorAt?.("description")} />
        </Field>
      </PanelSection>
    </div>
  )
}

/** The tags and networks whose servers a set is for, with how many servers each covers. */
function TargetsSection({
  targets,
  editable,
  onChange,
  errors,
}: {
  targets: Target[]
  editable: boolean
  onChange: (targets: Target[]) => void
  errors?: FieldErrors
}) {
  const { data: networks } = useQuery(networksQuery)
  const { data: servers } = useQuery(allServersQuery)
  const [adding, setAdding] = useState(false)
  const add = (target: Target) => {
    if (!targets.some((other) => targetKey(other) === targetKey(target))) onChange([...targets, target])
    setAdding(false)
  }
  const covered = (target: Target) => {
    if (target.kind === "tag") return servers?.filter((s) => s.tags.includes(target.value)).length
    const network = networks?.find((n) => n.id === target.value)
    return network && (target.role === "proxy" ? 1 : network.backends.length)
  }

  return (
    <PanelSection
      icon={CrosshairIcon}
      title={t("Targets")}
      action={
        editable && (
          <Button size="icon-sm" variant="ghost" aria-label={t("Add target")} title={t("Add target")} onClick={() => setAdding(true)}>
            <PlusIcon />
          </Button>
        )
      }
    >
      {targets.length === 0 ? (
        <p className="text-sm text-muted-foreground">{t("No targets yet. Add a tag or a network, whose servers get the files when the set is applied.")}</p>
      ) : (
        <ul className="grid gap-1">
          {targets.map((target) => {
            const { icon: Icon, label } = targetLabel(target, networks)
            const count = covered(target)
            return (
              <li key={targetKey(target)} className="flex items-center gap-2 rounded-lg py-1 pr-1 pl-2 ring-1 ring-border">
                <Icon className="size-4 shrink-0 text-muted-foreground" />
                <span className="min-w-0 flex-1">
                  <span className="block truncate text-sm font-medium">{label}</span>
                  {count !== undefined && (
                    <span className="block text-xs text-muted-foreground">{t("{{count}} servers", { count, defaultValue_one: "{{count}} server" })}</span>
                  )}
                </span>
                {editable && (
                  <Button
                    size="icon-sm"
                    variant="ghost"
                    aria-label={t("Remove {{name}}", { name: label })}
                    onClick={() => onChange(targets.filter((other) => targetKey(other) !== targetKey(target)))}
                  >
                    <XIcon />
                  </Button>
                )}
              </li>
            )
          })}
        </ul>
      )}
      <FieldError errors={errors} />
      {adding && (
        <Dialog open onOpenChange={setAdding}>
          <DialogContent className="sm:max-w-lg">
            <div className="grid gap-6">
              <DialogHeader>
                <DialogTitle>{t("Add target")}</DialogTitle>
                <DialogDescription>
                  {t(
                    "Applying puts the files on all servers of the targets. A server that stops being a target loses the files with secrets right away, and the others with the next apply, unless they changed on the server.",
                  )}
                </DialogDescription>
              </DialogHeader>
              <TagField tags={[...new Set(servers?.flatMap((s) => s.tags))].sort()} onAdd={(value) => add({ kind: "tag", value })} />
              <NetworkField
                networks={networks ?? []}
                roles={[
                  { value: "servers", label: t("Game servers") },
                  { value: "proxy", label: t("Proxy") },
                ]}
                onAdd={(value, role) => add({ kind: "network", value, role })}
              />
              <DialogFooter>
                <DialogClose asChild>
                  <Button variant="outline">{t("Cancel")}</Button>
                </DialogClose>
              </DialogFooter>
            </div>
          </DialogContent>
        </Dialog>
      )}
    </PanelSection>
  )
}
