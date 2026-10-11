import { useQueries, useQueryClient } from "@tanstack/react-query"
import { t } from "i18next"
import { useState } from "react"
import { useAccess } from "@/features/access/use-access"
import { useSettings } from "@/features/preferences/api"
import { usageQuery } from "@/features/usage/api"
import type { Server, Warning } from "./api"
import { PowerDialog } from "./power-dialog"
import { serverType } from "./server-types"

/** Whether the players of a server can be warned before it stops or restarts: those of running game servers. */
export const canWarn = (server: Pick<Server, "state" | "type">) => server.state === "running" && !serverType(server.type).proxy

/** A stop or restart of a server; run does it, with the warning chosen. */
interface Request {
  nodeId: string
  server: Pick<Server, "id" | "name" | "state" | "type">
  action: "restart" | "stop"
  run: (warning?: Warning) => void
}

/**
 * Stops and restarts single servers as the user chose: right away, after asking, or with the warning of the players on,
 * which only running game servers have, so that the others ask. Those who chose so are only asked about servers with
 * players by the latest count of their node, or whose count isn't known, which stays loaded for the servers of nodeId.
 * dialog asks, and onClose follows each request once it acted or was asked, e.g. to close the search.
 */
export function usePower({ nodeId, onClose }: { nodeId?: string; onClose?: () => void } = {}) {
  const { settings } = useSettings()
  const { can } = useAccess()
  const queryClient = useQueryClient()
  const [asking, setAsking] = useState<Request & { warn: boolean }>()
  const byPlayers = (settings.power ?? "now") !== "now" && settings.powerWhen === "players"
  useQueries({ queries: (byPlayers && nodeId ? [nodeId] : []).map(usageQuery) })

  /** Stops or restarts a server, or asks first; warn always asks with the warning on, e.g. from a menu. */
  function request(request: Request, warn = false) {
    const usage = queryClient.getQueryData(usageQuery(request.nodeId).queryKey)
    const players = usage?.servers.find((s) => s.id === request.server.id)?.players?.online
    const choice = warn ? "warn" : byPlayers && players === 0 ? "now" : (settings.power ?? "now")
    if (choice !== "now") return setAsking({ ...request, warn: choice === "warn" })
    request.run()
    onClose?.()
  }

  const dialog = asking && (
    <PowerDialog
      action={asking.action}
      title={
        asking.action === "stop" ? t("Stop {{name}}?", { name: asking.server.name }) : t("Restart {{name}}?", { name: asking.server.name })
      }
      description={t("Its players are disconnected.")}
      warnable={canWarn(asking.server)}
      warnFirst={asking.warn}
      canMessage={can("console.commands", asking.nodeId, asking.server.id)}
      onConfirm={asking.run}
      onOpenChange={(open) => {
        if (open) return
        setAsking(undefined)
        onClose?.()
      }}
    />
  )
  return { request, dialog }
}
