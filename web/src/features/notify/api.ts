import { queryOptions, useMutation, useQueryClient } from "@tanstack/react-query"
import { api } from "@/lib/api"
import type { Level } from "@/features/logs/api"

export type ChannelKind = "discord" | "slack" | "webhook" | "email"

/** Settings of a mail channel besides its password. */
export interface MailSettings {
  host: string
  port: number
  /** TLS from the start, e.g. at port 465, or STARTTLS, e.g. at port 587. */
  security: "tls" | "starttls"
  username: string
  from: string
  to: string[]
}

/** Where notifications go. Its secret, the URL of a webhook or the password of the mail server, is never shown. */
export interface Channel {
  id: string
  name: string
  kind: ChannelKind
  /** The host of a webhook's URL, or the mail server. */
  host: string
  email?: MailSettings
  hasPassword?: boolean
  /** How many messages it sends a minute once the first few went, and how many entries each has at most. */
  messagesPerMinute: number
  entriesPerMessage: number
  /** The IANA time zone of the times in its texts; empty is UTC. Webhooks get UTC. */
  timeZone: string
  createdAt: string
  /** When it last sent, and why it last failed, since the master started. */
  sentAt?: string
  problem?: string
}

/** A new or changed channel; an empty url or password keeps the stored one. */
export interface ChannelInput {
  name: string
  kind: ChannelKind
  url?: string
  email?: MailSettings
  password?: string
  messagesPerMinute: number
  entriesPerMessage: number
  timeZone: string
}

/** The most messages a minute and entries a message a channel may send; Discord takes 10 entries a message. */
export const maxPerMinute = 10
export const maxEntries = (kind: ChannelKind) => (kind === "discord" ? 10 : 25)

/** Sends the new entries of the log of at least a level, of some categories, and of a node, a server, a tag or a network, to a channel. */
export interface Rule {
  id: string
  channelId: string
  enabled: boolean
  level: Exclude<Level, "debug">
  /** None means all. */
  categories: string[]
  nodeId: string
  serverId: string
  /** The servers with the tag, or the proxy and game servers of the network, when an entry comes. */
  tag: string
  networkId: string
  createdAt: string
}

export type RuleInput = Omit<Rule, "id" | "createdAt">

export interface Notifications {
  channels: Channel[]
  rules: Rule[]
}

export const notificationsQuery = queryOptions({
  queryKey: ["notifications"],
  queryFn: () => api<Notifications>("/notifications"),
  // Channels tell when they sent last and why they failed.
  refetchInterval: 30_000,
})

function useChange<T>(fn: (input: T) => Promise<unknown>) {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: fn,
    onSuccess: () => queryClient.invalidateQueries({ queryKey: notificationsQuery.queryKey }),
  })
}

export const useSaveChannel = () =>
  useChange(({ id, ...input }: ChannelInput & { id?: string }) =>
    id
      ? api<Channel>(`/notifications/channels/${id}`, { method: "PUT", body: input })
      : api<Channel>("/notifications/channels", { body: input }),
  )

export const useDeleteChannel = () => useChange((id: string) => api(`/notifications/channels/${id}`, { method: "DELETE" }))

export const useTestChannel = () =>
  useMutation({ mutationFn: (id: string) => api(`/notifications/channels/${id}/test`, { method: "POST" }) })

export const useSaveRule = () =>
  useChange(({ id, ...input }: RuleInput & { id?: string }) =>
    id ? api<Rule>(`/notifications/rules/${id}`, { method: "PUT", body: input }) : api<Rule>("/notifications/rules", { body: input }),
  )

export const useDeleteRule = () => useChange((id: string) => api(`/notifications/rules/${id}`, { method: "DELETE" }))
