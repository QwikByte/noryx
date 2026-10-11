import { BellSimpleIcon, PaperPlaneTiltIcon, PencilSimpleIcon, PlusIcon, ShareNetworkIcon, TrashIcon } from "@phosphor-icons/react"
import { useQuery } from "@tanstack/react-query"
import { t } from "i18next"
import { toast } from "sonner"
import { Callout, ErrorCallout } from "@/components/callout"
import { ConfirmDialog } from "@/components/confirm-dialog"
import { EmptyState } from "@/components/empty-state"
import { TabIntro } from "@/components/hub-layout"
import { Section } from "@/components/section"
import { Button } from "@/components/ui/button"
import { Skeleton } from "@/components/ui/skeleton"
import { Switch } from "@/components/ui/switch"
import { useAccess } from "@/features/access/use-access"
import { AccountRow } from "@/features/auth/account-row"
import { networksQuery } from "@/features/networks/api"
import { nodesQuery } from "@/features/nodes/api"
import { allServersQuery } from "@/features/servers/api"
import { formatAgo } from "@/lib/format"
import { msg } from "@/lib/i18n"
import { useNow } from "@/lib/use-now"
import { type Channel, notificationsQuery, type Rule, useDeleteChannel, useDeleteRule, useSaveRule, useTestChannel } from "./api"
import { ChannelDialog } from "./channel-dialog"
import { describeCategories, kinds, levelLabel } from "./meta"
import { RuleDialog } from "./rule-dialog"

/** The Notifications tab: the channels that warnings and errors go to, and the rules that choose them. */
export function NotificationsSettingsPage() {
  const { data, isPending, error } = useQuery(notificationsQuery)
  if (isPending) return <Skeleton className="h-64 rounded-xl" />
  if (error) return <ErrorCallout error={error} />
  const { channels, rules } = data
  return (
    <>
      <TabIntro>
        {t("Warnings and errors reach you in Discord, Slack, another service through a webhook or by mail, also while nobody looks at the panel.")}
      </TabIntro>
      <Callout title={t("What leaves the master")}>
        {t("Entries of the log can hold IP addresses and names of players. Never the secrets of servers and networks, which aren't logged.")}
      </Callout>
      <Section
        title={t("Channels")}
        description={t("Where notifications go.")}
        actions={
          <ChannelDialog
            trigger={
              <Button>
                <PlusIcon /> {t("Add channel")}
              </Button>
            }
          />
        }
      >
        {channels.length === 0 ? (
          <EmptyState icon={ShareNetworkIcon} tone="info" title={t("No channels yet")} description={t("Add Discord, Slack, a webhook or a mail server.")} />
        ) : (
          <div className="grid gap-3">
            {channels.map((channel) => (
              <ChannelRow key={channel.id} channel={channel} rules={rules.filter((r) => r.channelId === channel.id).length} />
            ))}
          </div>
        )}
      </Section>
      <Section
        title={t("Rules")}
        description={t("Which entries go to which channel.")}
        actions={
          channels.length > 0 && (
            <RuleDialog
              channels={channels}
              trigger={
                <Button>
                  <PlusIcon /> {t("Add rule")}
                </Button>
              }
            />
          )
        }
      >
        {rules.length === 0 ? (
          <EmptyState
            icon={BellSimpleIcon}
            tone="warning"
            title={t("No rules yet")}
            description={channels.length === 0 ? t("Add a channel first.") : t("Without rules, the channels get nothing but tests.")}
          />
        ) : (
          <div className="grid gap-3">
            {rules.map((rule) => (
              <RuleRow key={rule.id} rule={rule} channels={channels} />
            ))}
          </div>
        )}
      </Section>
    </>
  )
}

function ChannelRow({ channel, rules }: { channel: Channel; rules: number }) {
  const test = useTestChannel()
  const remove = useDeleteChannel()
  const now = useNow(true, 60_000)
  const { icon, tone, label } = kinds[channel.kind]
  const sent = channel.sentAt && t("Sent {{time}}", { time: formatAgo(channel.sentAt, now) })
  return (
    <AccountRow
      icon={icon}
      tone={tone}
      title={channel.name}
      status={channel.problem ? { tone: "destructive", label: msg("Failing") } : undefined}
      actions={
        <>
          <Button
            variant="outline"
            disabled={test.isPending}
            onClick={() =>
              test.mutate(channel.id, {
                onSuccess: () => toast.success(t("Sent a test to {{name}}", { name: channel.name })),
                onError: (e) => toast.error(t("The test to {{name}} failed", { name: channel.name }), { description: e.message }),
              })
            }
          >
            <PaperPlaneTiltIcon /> {test.isPending ? t("Sending…") : t("Send test")}
          </Button>
          <ChannelDialog
            channel={channel}
            trigger={
              <Button variant="ghost" size="icon" aria-label={t("Change {{name}}", { name: channel.name })} title={t("Change")}>
                <PencilSimpleIcon />
              </Button>
            }
          />
          <ConfirmDialog
            trigger={
              <Button
                variant="ghost"
                size="icon"
                aria-label={t("Delete {{name}}", { name: channel.name })}
                title={t("Delete")}
                disabled={remove.isPending}
              >
                <TrashIcon />
              </Button>
            }
            title={t("Delete {{name}}?", { name: channel.name })}
            description={
              rules > 0
                ? t("Its {{count}} rules are deleted with it.", { count: rules, defaultValue_one: "Its rule is deleted with it." })
                : t("It gets no more notifications.")
            }
            action={t("Delete")}
            destructive
            onConfirm={() =>
              remove.mutate(channel.id, {
                onSuccess: () => toast.success(t("Deleted {{name}}", { name: channel.name })),
                onError: (e) => toast.error(e.message),
              })
            }
          />
        </>
      }
    >
      <p className="break-all">
        {t(label)} · <span className="font-mono">{channel.host}</span>
        {channel.email && ` → ${channel.email.to.join(", ")}`}
      </p>
      {channel.problem ? (
        <p className="text-destructive">{channel.problem}</p>
      ) : (
        <p>{[sent, t("{{count}} rules", { count: rules, defaultValue_one: "{{count}} rule" })].filter(Boolean).join(" · ")}</p>
      )}
    </AccountRow>
  )
}

function RuleRow({ rule, channels }: { rule: Rule; channels: Channel[] }) {
  const save = useSaveRule()
  const remove = useDeleteRule()
  const { data: nodes } = useQuery(nodesQuery)
  const { data: servers } = useQuery({ ...allServersQuery, enabled: !!rule.serverId })
  const access = useAccess()
  const { data: networks } = useQuery({ ...networksQuery, enabled: !!rule.networkId && access.can("networks.view") })
  const channel = channels.find((c) => c.id === rule.channelId)
  const node = rule.nodeId && (nodes?.find((n) => n.id === rule.nodeId)?.name ?? rule.nodeId)
  const server = rule.serverId && (servers?.find((s) => s.nodeId === rule.nodeId && s.id === rule.serverId)?.name ?? rule.serverId)
  const network = rule.networkId && (networks?.find((n) => n.id === rule.networkId)?.name ?? t("A network"))
  const where = server
    ? t("{{server}} on {{node}}", { server, node })
    : node
      ? t("Node {{node}}", { node })
      : rule.tag
        ? t("Servers with the tag {{tag}}", { tag: rule.tag })
        : network
          ? t("Servers of {{network}}", { network })
          : t("All nodes and servers")
  const kind = channel ? kinds[channel.kind] : kinds.webhook
  return (
    <AccountRow
      icon={kind.icon}
      tone={rule.enabled ? kind.tone : "neutral"}
      title={t("{{entries}} to {{channel}}", { entries: levelLabel(rule.level), channel: channel?.name ?? rule.channelId })}
      status={rule.enabled ? undefined : { tone: "neutral", label: msg("Off") }}
      actions={
        <>
          <Switch
            aria-label={t("Send entries that match")}
            className="mx-2"
            checked={rule.enabled}
            disabled={save.isPending}
            onCheckedChange={(enabled) => {
              const { id, channelId, level, categories, nodeId, serverId, tag, networkId } = rule
              save.mutate({ id, channelId, enabled, level, categories, nodeId, serverId, tag, networkId }, { onError: (e) => toast.error(e.message) })
            }}
          />
          <RuleDialog
            rule={rule}
            channels={channels}
            trigger={
              <Button variant="ghost" size="icon" aria-label={t("Change the rule")} title={t("Change")}>
                <PencilSimpleIcon />
              </Button>
            }
          />
          <ConfirmDialog
            trigger={
              <Button variant="ghost" size="icon" aria-label={t("Delete the rule")} title={t("Delete")} disabled={remove.isPending}>
                <TrashIcon />
              </Button>
            }
            title={t("Delete the rule?")}
            description={t("Its channel gets no more of these entries.")}
            action={t("Delete")}
            destructive
            onConfirm={() => remove.mutate(rule.id, { onError: (e) => toast.error(e.message) })}
          />
        </>
      }
    >
      {describeCategories(rule.categories)} · {where}
    </AccountRow>
  )
}
