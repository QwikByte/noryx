import {
  ArrowClockwiseIcon,
  ArrowsLeftRightIcon,
  BellRingingIcon,
  CircleNotchIcon,
  CopyIcon,
  DotsThreeIcon,
  NotePencilIcon,
  PlayIcon,
  StackIcon,
  StopIcon,
  TagIcon,
  TrashIcon,
} from "@phosphor-icons/react"
import { t } from "i18next"
import { useState } from "react"
import { ConfirmDialog } from "@/components/confirm-dialog"
import { Pill } from "@/components/status"
import { Button } from "@/components/ui/button"
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuSeparator, DropdownMenuTrigger } from "@/components/ui/dropdown-menu"
import type { Permission } from "@/features/access/permissions"
import { useAccess } from "@/features/access/use-access"
import { useOperation } from "@/features/operations/use-operation"
import { PinButton } from "@/features/preferences/pin-button"
import { SaveTemplateDialog } from "@/features/templates/save-template-dialog"
import { cn } from "@/lib/utils"
import { type Server, type ServerAction, useMove, useServerAction, type Warning } from "./api"
import { DuplicateServerDialog } from "./duplicate-server-dialog"
import { MoveServerDialog } from "./move-server-dialog"
import { NotesDialog } from "./notes"
import { canWarn, usePower } from "./power"
import { serverType } from "./server-types"
import { TagsDialog } from "./tags"

/** What a notification says while an action on a server runs. */
const pendingLabels: Record<ServerAction, (name: string) => string> = {
  start: (name) => t("Starting {{name}}…", { name }),
  stop: (name) => t("Stopping {{name}}…", { name }),
  restart: (name) => t("Restarting {{name}}…", { name }),
  delete: (name) => t("Deleting {{name}}…", { name }),
}

/**
 * Start or stop a server, also after warning its players, copy it, move it, tag it, change its notes, save it as a
 * template and delete it after confirmation. While it moves, it can't be changed. Compact actions only have icons,
 * e.g. in a table. With pin, they also pin it to the sidebar and the overview.
 */
export function ServerActions({
  nodeId,
  server,
  onDeleted,
  compact,
  pin,
}: {
  nodeId: string
  server: Server
  onDeleted?: () => void
  compact?: boolean
  pin?: boolean
}) {
  const mutation = useServerAction(nodeId)
  const operation = useOperation()
  const move = useMove(server.id)
  const power = usePower({ nodeId })
  const [dialog, setDialog] = useState<"duplicate" | "move" | "template" | "tags" | "notes">()
  const running = server.state !== "stopped"
  const dialogProps = { nodeId, server, open: true, onOpenChange: (open: boolean) => !open && setDialog(undefined) }

  // The notification follows the action, e.g. a stop that takes minutes, also if this component goes away meanwhile.
  function run(action: ServerAction, done: string, then?: () => void, warning?: Warning) {
    operation.run((onStart) => mutation.mutateAsync({ id: server.id, action, warning, onStart }), {
      title: pendingLabels[action](server.name),
      notify: true,
      done: () => ({ message: done }),
      then,
    })
  }

  // Stops and restarts go as the user chose; warn asks with the warning on, as the menu does.
  function act(action: ServerAction, done: string, warn = false) {
    if (action !== "restart" && action !== "stop") return run(action, done)
    power.request({ nodeId, server, action, run: (warning) => run(action, done, undefined, warning) }, warn)
  }

  const { can } = useAccess()
  const may = (p: Permission) => can(p, nodeId, server.id)
  const duplicate = can("servers.create", nodeId) && may("files.read")
  const saveTemplate = can("templates.manage") && (serverType(server.type).proxy || may("properties.edit"))
  // Moving deletes the server here and copies all its files.
  const movable = may("servers.delete") && may("files.read")
  const tags = may("servers.settings")
  const powers = (
    running
      ? [
          { action: "restart", icon: ArrowClockwiseIcon, label: t("Restart"), done: t("Restarted {{name}}", { name: server.name }) },
          { action: "stop", icon: StopIcon, label: t("Stop"), done: t("Stopped {{name}}", { name: server.name }) },
        ]
      : [{ action: "start", icon: PlayIcon, label: t("Start"), done: t("Started {{name}}", { name: server.name }) }]
  ).filter((p) => may(`servers.${p.action}` as Permission))
  const warnable = canWarn(server) ? powers : []
  if (move && !move.finishedAt) {
    return (
      <Pill tone="info">
        <CircleNotchIcon className="size-3.5 animate-spin motion-reduce:animate-none" />
        {t("Moving to {{node}}", { node: move.toName })}
      </Pill>
    )
  }
  const pinButton = pin && <PinButton nodeId={nodeId} server={server} />
  if (powers.length === 0 && !duplicate && !saveTemplate && !tags && !may("servers.delete")) return pinButton || null
  const menu = warnable.length > 0 || duplicate || movable || saveTemplate || tags

  return (
    <div className={cn("flex items-center", compact ? "justify-end gap-0.5" : "flex-wrap gap-2")}>
      {powers.map(({ action, icon: Icon, label, done }) => (
        <Button
          key={action}
          size={compact ? "icon-sm" : "sm"}
          variant={compact ? "ghost" : action === "start" ? "default" : "outline"}
          disabled={mutation.isPending}
          aria-label={compact ? `${label}: ${server.name}` : undefined}
          title={compact ? label : undefined}
          className={cn(compact && "text-muted-foreground", compact && action === "start" && "text-primary")}
          onClick={() => act(action as ServerAction, done)}
        >
          <Icon weight={compact ? "fill" : undefined} />
          {!compact && label}
        </Button>
      ))}
      <div className={cn("flex items-center gap-1", !compact && "ml-auto")}>
        {pinButton}
        {menu && (
          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <Button
                size="icon-sm"
                variant="ghost"
                aria-label={t("More actions for {{name}}", { name: server.name })}
                title={t("More actions")}
                className="text-muted-foreground"
              >
                <DotsThreeIcon weight="bold" />
              </Button>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end" className="w-56">
              {warnable.map(({ action, done }) => (
                <DropdownMenuItem key={action} onSelect={() => act(action as ServerAction, done, true)}>
                  <BellRingingIcon />
                  {action === "stop" ? t("Stop with a warning…") : t("Restart with a warning…")}
                </DropdownMenuItem>
              ))}
              {warnable.length > 0 && (duplicate || movable || saveTemplate || tags) && <DropdownMenuSeparator />}
              {tags && (
                <DropdownMenuItem onSelect={() => setDialog("tags")}>
                  <TagIcon />
                  {t("Tags…")}
                </DropdownMenuItem>
              )}
              {tags && (
                <DropdownMenuItem onSelect={() => setDialog("notes")}>
                  <NotePencilIcon />
                  {t("Notes…")}
                </DropdownMenuItem>
              )}
              {duplicate && (
                <DropdownMenuItem onSelect={() => setDialog("duplicate")}>
                  <CopyIcon />
                  {t("Duplicate…")}
                </DropdownMenuItem>
              )}
              {movable && (
                <DropdownMenuItem onSelect={() => setDialog("move")}>
                  <ArrowsLeftRightIcon />
                  {t("Move to another node…")}
                </DropdownMenuItem>
              )}
              {saveTemplate && (
                <DropdownMenuItem onSelect={() => setDialog("template")}>
                  <StackIcon />
                  {t("Save as template…")}
                </DropdownMenuItem>
              )}
            </DropdownMenuContent>
          </DropdownMenu>
        )}
        {may("servers.delete") && !compact && (
          <ConfirmDialog
            trigger={
              <Button
                size="icon-sm"
                variant="ghost"
                aria-label={t("Delete {{name}}", { name: server.name })}
                title={t("Delete server")}
                disabled={mutation.isPending}
                className="text-muted-foreground hover:bg-destructive/10 hover:text-destructive"
              >
                <TrashIcon />
              </Button>
            }
            title={t("Delete {{name}} on port {{port}}?", { name: server.name, port: server.port })}
            description={t("This stops the server and permanently deletes it with all worlds, plugins and settings. This can't be undone.")}
            action={t("Delete server")}
            destructive
            confirmText={server.name}
            onConfirm={() => run("delete", t("Deleted {{name}}", { name: server.name }), onDeleted)}
          />
        )}
      </div>
      {dialog === "duplicate" && <DuplicateServerDialog {...dialogProps} />}
      {dialog === "move" && <MoveServerDialog {...dialogProps} />}
      {dialog === "template" && <SaveTemplateDialog {...dialogProps} />}
      {dialog === "tags" && <TagsDialog servers={[{ ...server, nodeId }]} onOpenChange={dialogProps.onOpenChange} />}
      {dialog === "notes" && <NotesDialog nodeId={nodeId} server={server} onOpenChange={dialogProps.onOpenChange} />}
      {power.dialog}
    </div>
  )
}
