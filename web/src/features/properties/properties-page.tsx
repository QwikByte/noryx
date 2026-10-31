import { FileTextIcon, MagnifyingGlassIcon } from "@phosphor-icons/react"
import { useQuery } from "@tanstack/react-query"
import { getRouteApi, useBlocker } from "@tanstack/react-router"
import { t } from "i18next"
import { useState } from "react"
import { toast } from "sonner"
import { ErrorCallout } from "@/components/callout"
import { ConfirmDialog } from "@/components/confirm-dialog"
import { EmptyState } from "@/components/empty-state"
import { Button } from "@/components/ui/button"
import { InputGroup, InputGroupAddon, InputGroupInput } from "@/components/ui/input-group"
import { Skeleton } from "@/components/ui/skeleton"
import { useAccess } from "@/features/access/use-access"
import { useOperation } from "@/features/operations/use-operation"
import { type Server, useServer, useServerAction } from "@/features/servers/api"
import { propertiesQuery, type ServerProperties, useUpdateProperties } from "./api"
import { PropertyField } from "./property-field"
import { isBedrock } from "@/features/servers/server-types"
import { check, definition, definitionsOf, groups, iconMatches } from "./schema"
import { ServerIcon } from "./server-icon"

const route = getRouteApi("/_app/nodes/$nodeId/servers/$serverId/properties")

/** The Properties tab of a server: server.properties as a form. */
export function PropertiesPage() {
  const { nodeId, serverId } = route.useParams()
  const { server } = useServer(nodeId, serverId)
  const { data, isPending, error } = useQuery(propertiesQuery(nodeId, serverId))

  if (!server || isPending) return <Skeleton className="h-96 rounded-xl" />
  if (error) return <ErrorCallout error={error} />
  if (!data.exists)
    return (
      <EmptyState
        icon={FileTextIcon}
        tone="neutral"
        title={t("No server.properties yet")}
        description={t("The server creates it when it starts for the first time. Start the server once, then come back.")}
      />
    )
  return <PropertiesForm nodeId={nodeId} server={server} data={data} />
}

function PropertiesForm({ nodeId, server, data }: { nodeId: string; server: Server; data: ServerProperties }) {
  const { can } = useAccess()
  const [changes, setChanges] = useState<Record<string, string>>({})
  const [search, setSearch] = useState("")
  const update = useUpdateProperties(nodeId, server.id)
  const restart = useServerAction(nodeId)
  const operation = useOperation()
  const locked = new Map(data.locked.map((l) => [l.key, l.reason]))
  // Bedrock Dedicated Server has properties of its own, and no icon in the server list.
  const known = definitionsOf(server.type)
  const order = Object.keys(known)
  const setting = (key: string) => definition(key, data.properties[key], known)
  const count = Object.keys(changes).length
  const errors = Object.fromEntries(
    Object.entries(changes).flatMap(([key, value]) => (check(key, value, known) ? [[key, check(key, value, known)]] : [])),
  )
  const blocker = useBlocker({ shouldBlockFn: () => count > 0, enableBeforeUnload: () => count > 0, withResolver: true })

  function set(key: string, value: string) {
    setChanges((current) => {
      const next = { ...current, [key]: value }
      if (value === data.properties[key]) delete next[key] // back to the saved value
      return next
    })
  }

  const term = search.trim().toLowerCase()
  const keys = Object.keys(data.properties)
    .filter((key) => !term || [key, t(setting(key).label), t(setting(key).description)].some((s) => s.toLowerCase().includes(term)))
    .sort((a, b) => (order.indexOf(a) + 1 || order.length + 1) - (order.indexOf(b) + 1 || order.length + 1) || a.localeCompare(b))
  const icon = can("files.write", nodeId, server.id) && !isBedrock(server.type) && iconMatches(term)

  function save(andRestart: boolean) {
    update.mutate(changes, {
      onSuccess: () => {
        setChanges({})
        if (!andRestart) return toast.success(t("Saved. The changes apply when the server restarts."))
        operation.run((onStart) => restart.mutateAsync({ id: server.id, action: "restart", onStart }), {
          title: t("Restarting {{name}}…", { name: server.name }),
          notify: true,
          done: () => ({ message: t("Saved and restarted {{name}}", { name: server.name }) }),
        })
      },
      onError: (e) => toast.error(e.message),
    })
  }

  return (
    <div className="pb-24">
      <InputGroup className="mb-8 w-full sm:max-w-xs">
        <InputGroupAddon>
          <MagnifyingGlassIcon />
        </InputGroupAddon>
        <InputGroupInput
          placeholder={t("Search settings")}
          aria-label={t("Search settings")}
          value={search}
          onChange={(e) => setSearch(e.target.value)}
        />
      </InputGroup>
      {keys.length === 0 && !icon && <p className="text-sm text-muted-foreground">{t("No setting matches your search.")}</p>}
      {groups.map((group) => {
        const inGroup = keys.filter((key) => setting(key).group === group)
        const withIcon = icon && group === "Server list"
        if (inGroup.length === 0 && !withIcon) return null
        return (
          <section key={group} className="surface mb-6 rounded-xl p-5 sm:p-6" aria-label={t(group)}>
            <h2 className="heading mb-5 text-base">{t(group)}</h2>
            <div className="grid gap-x-10 gap-y-6 md:grid-cols-2">
              {withIcon && <ServerIcon nodeId={nodeId} serverId={server.id} />}
              {inGroup.map((key) => (
                <PropertyField
                  key={key}
                  name={key}
                  setting={setting(key)}
                  value={changes[key] ?? data.properties[key]}
                  locked={locked.get(key)}
                  error={errors[key]}
                  changed={key in changes}
                  onChange={(value) => set(key, value)}
                />
              ))}
            </div>
          </section>
        )
      })}
      {count > 0 && (
        <div className="sticky bottom-4 z-10 flex flex-wrap items-center justify-between gap-3 rounded-xl bg-popover/90 px-4 py-3 shadow-2xl ring-1 ring-foreground/10 backdrop-blur-xl">
          <p className="flex items-center gap-2.5 text-sm font-medium">
            <span aria-hidden className="size-2 rounded-full bg-warning" />
            {t("{{count}} unsaved changes", { count, defaultValue_one: "{{count}} unsaved change" })}
          </p>
          <div className="flex gap-2">
            <Button variant="ghost" onClick={() => setChanges({})}>
              {t("Discard")}
            </Button>
            <Button
              variant={server.state === "stopped" ? "default" : "outline"}
              disabled={update.isPending || Object.keys(errors).length > 0}
              onClick={() => save(false)}
            >
              {t("Save")}
            </Button>
            {server.state !== "stopped" && can("servers.restart", nodeId, server.id) && (
              <Button disabled={update.isPending || Object.keys(errors).length > 0} onClick={() => save(true)}>
                {t("Save and restart")}
              </Button>
            )}
          </div>
        </div>
      )}
      <ConfirmDialog
        open={blocker.status === "blocked"}
        onOpenChange={(open) => !open && blocker.reset?.()}
        title={t("Discard your changes?")}
        description={t("Your changes to the settings haven't been saved.")}
        action={t("Discard changes")}
        destructive
        onConfirm={() => blocker.proceed?.()}
      />
    </div>
  )
}
