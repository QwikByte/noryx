import { DownloadSimpleIcon, MemoryIcon, PencilSimpleIcon, PlusIcon, StackIcon, TrashIcon } from "@phosphor-icons/react"
import { useQuery } from "@tanstack/react-query"
import { getRouteApi, Link } from "@tanstack/react-router"
import { t } from "i18next"
import { toast } from "sonner"
import { ErrorCallout } from "@/components/callout"
import { Chip } from "@/components/chip"
import { ConfirmDialog } from "@/components/confirm-dialog"
import { EmptyState } from "@/components/empty-state"
import { TabIntro } from "@/components/hub-layout"
import { IconTile } from "@/components/icon-tile"
import { NoMatch, SearchField } from "@/components/list-toolbar"
import { Button } from "@/components/ui/button"
import { Skeleton } from "@/components/ui/skeleton"
import { useAccess } from "@/features/access/use-access"
import { PluginIcon } from "@/features/plugins/plugin-icon"
import { CreateServerDialog } from "@/features/servers/create-server-dialog"
import { displayVersion, serverLook, serverType } from "@/features/servers/server-types"
import { TagList } from "@/features/servers/tags"
import { formatMegabytes } from "@/lib/format"
import { matches } from "@/lib/search"
import { exportUrl, type Template, templatesQuery, useDeleteTemplate } from "./api"
import { ImportTemplateDialog } from "./import-template-dialog"

const route = getRouteApi("/_app/_library/templates")

function NewTemplate() {
  return (
    <>
      <ImportTemplateDialog />
      <Button asChild>
        <Link to="/templates/new">
          <PlusIcon />
          {t("New template")}
        </Link>
      </Button>
    </>
  )
}

/** The templates, searched by the address. */
export function TemplatesPage() {
  const manage = useAccess().can("templates.manage")
  const { data: templates, isPending, error } = useQuery(templatesQuery)
  const { q } = route.useSearch()
  const navigate = route.useNavigate()
  const shown = (templates ?? []).filter((tpl) =>
    matches(q, tpl.name, tpl.description, serverType(tpl.type).label, tpl.version, ...tpl.tags, ...tpl.plugins.map((p) => p.title)),
  )
  return (
    <>
      <TabIntro actions={manage && <NewTemplate />}>
        {t("Reusable server setups: software, version, memory, settings and plugins. New servers start from them in a few clicks.")}
      </TabIntro>
      {isPending ? (
        <div className="grid gap-4 md:grid-cols-2">
          {[0, 1].map((i) => (
            <Skeleton key={i} className="h-52 rounded-xl" />
          ))}
        </div>
      ) : error ? (
        <ErrorCallout error={error} />
      ) : templates.length === 0 ? (
        <EmptyState
          icon={StackIcon}
          tone="info"
          title={t("No templates yet")}
          description={manage && t("Create a template from scratch, import one, or save an existing server as a template from its page.")}
        >
          {manage && <NewTemplate />}
        </EmptyState>
      ) : (
        <>
          <SearchField
            label={t("Search templates")}
            value={q}
            onChange={(q) => navigate({ search: { q }, replace: true })}
            className="mb-5 sm:max-w-xs"
          />
          {shown.length === 0 ? (
            <NoMatch>{t("Nothing matches your search.")}</NoMatch>
          ) : (
            <ul className="grid gap-4 md:grid-cols-2">
              {shown.map((template) => (
                <TemplateCard key={template.id} template={template} />
              ))}
            </ul>
          )}
        </>
      )}
    </>
  )
}

function TemplateCard({ template }: { template: Template }) {
  const { can, canSomewhere } = useAccess()
  const remove = useDeleteTemplate()
  const type = serverType(template.type)
  const extras = [
    template.java && t("Java {{version}}", { version: template.java }),
    template.aikarFlags && t("Aikar's flags"),
    Object.keys(template.properties).length > 0 &&
      t("{{count}} properties", { count: Object.keys(template.properties).length, defaultValue_one: "{{count}} property" }),
  ].filter(Boolean)

  return (
    <li className="surface flex flex-col gap-4 rounded-xl p-5">
      <div className="flex items-start gap-3">
        <IconTile {...serverLook(template.type)} />
        <div className="min-w-0 flex-1">
          <Link to="/templates/$templateId" params={{ templateId: template.id }} className="block truncate font-semibold hover:underline">
            {template.name}
          </Link>
          <p className="truncate text-xs text-muted-foreground">
            {type.label} {type.proxy ? "" : displayVersion(template.version)}
          </p>
        </div>
        <Chip icon={MemoryIcon}>{formatMegabytes(template.memoryMb)}</Chip>
      </div>
      {template.description && <p className="line-clamp-2 text-sm text-muted-foreground">{template.description}</p>}
      <TagList tags={template.tags} />
      {(extras.length > 0 || template.plugins.length > 0) && (
        <div className="flex flex-wrap items-center gap-1.5">
          {template.plugins.map((p) => (
            <span key={p.id} title={p.title}>
              <PluginIcon src={p.icon} className="size-7 rounded-lg [&>svg]:size-4" />
            </span>
          ))}
          {extras.map((e) => (
            <Chip key={String(e)} className="font-normal">
              {e}
            </Chip>
          ))}
        </div>
      )}
      <div className="mt-auto flex flex-wrap items-center gap-2 border-t pt-4">
        {canSomewhere("servers.create") && (
          <CreateServerDialog
            template={template}
            trigger={
              <Button size="sm">
                <PlusIcon />
                {t("Create server")}
              </Button>
            }
          />
        )}
        {can("templates.manage") && (
          <Button asChild size="sm" variant="outline">
            <Link to="/templates/$templateId" params={{ templateId: template.id }}>
              <PencilSimpleIcon />
              {t("Edit")}
            </Link>
          </Button>
        )}
        <Button asChild size="icon-sm" variant="ghost" className="ml-auto text-muted-foreground">
          <a href={exportUrl(template.id)} download aria-label={t("Export {{name}}", { name: template.name })} title={t("Export")}>
            <DownloadSimpleIcon />
          </a>
        </Button>
        {can("templates.manage") && (
          <ConfirmDialog
            trigger={
              <Button
                size="icon-sm"
                variant="ghost"
                aria-label={t("Delete {{name}}", { name: template.name })}
                title={t("Delete template")}
                className="text-muted-foreground hover:bg-destructive/10 hover:text-destructive"
              >
                <TrashIcon />
              </Button>
            }
            title={t("Delete {{name}}?", { name: template.name })}
            description={t("Servers created from the template stay as they are.")}
            action={t("Delete template")}
            destructive
            onConfirm={() =>
              remove.mutate(template.id, {
                onSuccess: () => toast.success(t("Deleted {{name}}", { name: template.name })),
                onError: (e) => toast.error(e.message),
              })
            }
          />
        )}
      </div>
    </li>
  )
}
