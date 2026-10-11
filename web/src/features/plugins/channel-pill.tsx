import { t } from "i18next"
import { Pill } from "@/components/status"
import { type Channel, channelLabel } from "./api"

/** Marks a version that isn't a release. */
export function ChannelPill({ channel }: { channel?: Channel }) {
  if (!channel || channel === "release") return null
  return <Pill tone={channel === "beta" ? "warning" : "destructive"}>{channel === "beta" ? t("Beta") : t("Alpha")}</Pill>
}

/** Marks a project that a server or template takes betas or alphas of too. */
export function TracksPill({ channel }: { channel?: Channel }) {
  if (!channel || channel === "release") return null
  return <Pill tone="neutral">{channelLabel(channel)}</Pill>
}
