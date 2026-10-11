import type { Status } from "@/components/status"
import { msg } from "@/lib/i18n"
import type { DatastoreState, Engine } from "./api"

export const states: Record<DatastoreState, Status> = {
  running: { tone: "success", label: msg("Running") },
  starting: { tone: "info", label: msg("Starting"), pulse: true },
  unhealthy: { tone: "destructive", label: msg("Unhealthy") },
  stopped: { tone: "neutral", label: msg("Stopped") },
  unknown: { tone: "warning", label: msg("Unknown") },
}

export const engines: Record<Engine, { label: string; description: string; buffer: string }> = {
  mariadb: { label: "MariaDB", description: msg("For plugins that speak MySQL, e.g. CoreProtect and LiteBans."), buffer: msg("Buffer pool (MB)") },
  postgres: { label: "PostgreSQL", description: msg("For plugins that support it, e.g. LuckPerms and Plan."), buffer: msg("Shared buffers (MB)") },
}
