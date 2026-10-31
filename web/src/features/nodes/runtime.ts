import { t } from "i18next"
import type { NodeInfo, Runtime } from "./api"

/** The name of a runtime as texts write it. */
export const runtimeTitle = (runtime: Runtime) => (runtime === "podman" ? "Podman" : "Docker")

/**
 * The runtime of a node with its version, e.g. "Podman 5.4.2", or that the agent can't reach it;
 * undefined without the permission to see the node.
 */
export function runtimeLabel(info: NodeInfo) {
  if (!info.agentVersion) return undefined
  const title = runtimeTitle(info.runtimeName)
  return info.runtimeVersion ? `${title} ${info.runtimeVersion}` : t("{{runtime}} isn't reachable", { runtime: title })
}
