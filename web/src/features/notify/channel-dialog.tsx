import { t } from "i18next"
import { type FormEvent, type ReactNode, useState } from "react"
import { toast } from "sonner"
import { TimeZonePicker } from "@/components/time-zone-picker"
import { Button } from "@/components/ui/button"
import {
  Dialog,
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from "@/components/ui/dialog"
import { Field, FieldDescription, FieldError, FieldGroup, FieldLabel, FieldLegend, FieldSet } from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import { RadioGroup, RadioGroupItem } from "@/components/ui/radio-group"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { cn } from "@/lib/utils"
import { type Channel, type ChannelKind, type MailSettings, maxEntries, maxPerMinute, useSaveChannel } from "./api"
import { kinds } from "./meta"

/** Addresses that show what a field takes; they need no translation. */
const examples = { host: "smtp.example.com", from: "noryx@example.com", to: "ops@example.com" }

const emptyMail: MailSettings = { host: "", port: 587, security: "starttls", username: "", from: "", to: [] }

function formOf(channel?: Channel) {
  const email = channel?.email ?? emptyMail
  return {
    name: channel?.name ?? "",
    kind: channel?.kind ?? ("discord" as ChannelKind),
    url: "",
    password: "",
    messagesPerMinute: channel?.messagesPerMinute ?? 1,
    entriesPerMessage: channel?.entriesPerMessage ?? 10,
    timeZone: channel?.timeZone ?? "",
    ...email,
    to: email.to.join(", "),
  }
}

/** Creates a channel, or changes one; its URL or password only changes if a new one is entered. */
export function ChannelDialog({ channel, trigger }: { channel?: Channel; trigger: ReactNode }) {
  const [open, setOpen] = useState(false)
  const [form, setForm] = useState(() => formOf(channel))
  const save = useSaveChannel()
  const set = (change: Partial<typeof form>) => setForm({ ...form, ...change })
  const email = form.kind === "email"
  // A stored password only stays with the same server and user, so that it can't go to another server.
  const keepsPassword =
    !!channel?.hasPassword && channel.email?.host === form.host.trim() && channel.email.port === form.port && channel.email.username === form.username.trim()

  function onOpenChange(next: boolean) {
    setOpen(next)
    if (next) setForm(formOf(channel))
    else save.reset()
  }

  function submit(event: FormEvent) {
    event.preventDefault()
    save.mutate(
      {
        id: channel?.id,
        name: form.name,
        kind: form.kind,
        messagesPerMinute: form.messagesPerMinute,
        entriesPerMessage: form.entriesPerMessage,
        timeZone: form.timeZone,
        url: email ? undefined : form.url.trim(),
        password: email ? form.password : undefined,
        email: email
          ? {
              host: form.host,
              port: form.port,
              security: form.security,
              username: form.username,
              from: form.from,
              to: form.to.split(/[,;\s]+/).filter(Boolean),
            }
          : undefined,
      },
      {
        onSuccess: () => {
          toast.success(channel ? t("Saved {{name}}", { name: form.name }) : t("Added {{name}}", { name: form.name }), {
            description: channel ? undefined : t("Send a test to check it, then add a rule that chooses what it gets."),
          })
          setOpen(false)
        },
      },
    )
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogTrigger asChild>{trigger}</DialogTrigger>
      <DialogContent className="sm:max-w-xl">
        <form onSubmit={submit} className="grid gap-6">
          <DialogHeader>
            <DialogTitle>{channel ? t("Change {{name}}", { name: channel.name }) : t("Add a channel")}</DialogTitle>
            <DialogDescription>
              {t("Entries of the log can hold IP addresses and names of players, which then leave the master. The URL of a webhook and the password of a mail server can't be seen again once saved.")}
            </DialogDescription>
          </DialogHeader>
          <FieldGroup>
            <Field>
              <FieldLabel htmlFor="channel-name">{t("Name")}</FieldLabel>
              <Input
                id="channel-name"
                required
                maxLength={64}
                placeholder={t("e.g. Team chat")}
                value={form.name}
                onChange={(e) => set({ name: e.target.value })}
              />
            </Field>
            <FieldSet>
              <FieldLegend variant="label">{t("Kind")}</FieldLegend>
              <RadioGroup
                value={form.kind}
                onValueChange={(kind) =>
                  set({ kind: kind as ChannelKind, entriesPerMessage: Math.min(form.entriesPerMessage, maxEntries(kind as ChannelKind)) })
                }
                disabled={!!channel}
                className="grid grid-cols-2 gap-2 sm:grid-cols-4"
              >
                {(Object.keys(kinds) as ChannelKind[]).map((kind) => {
                  const { icon: Icon, label } = kinds[kind]
                  return (
                    <label
                      key={kind}
                      className={cn(
                        "flex cursor-pointer items-center gap-2 rounded-lg p-3 text-sm font-medium ring-1 ring-foreground/10 hover:bg-muted/50 has-focus-visible:ring-2 has-focus-visible:ring-ring has-disabled:cursor-default has-data-[state=checked]:bg-primary/5 has-data-[state=checked]:ring-primary/40",
                        !!channel && form.kind !== kind && "opacity-50",
                      )}
                    >
                      <RadioGroupItem value={kind} className="sr-only" />
                      <Icon className="size-5" />
                      {t(label)}
                    </label>
                  )
                })}
              </RadioGroup>
              <FieldDescription>{t(kinds[form.kind].hint)}</FieldDescription>
            </FieldSet>
            {email ? (
              <MailFields form={form} set={set} keepsPassword={keepsPassword} />
            ) : (
              <Field>
                <FieldLabel htmlFor="channel-url">{t("URL of the webhook")}</FieldLabel>
                <Input
                  id="channel-url"
                  type="url"
                  required={!channel}
                  autoComplete="off"
                  spellCheck={false}
                  className="font-mono"
                  placeholder={channel ? t("Leave empty to keep the stored URL") : "https://…"}
                  value={form.url}
                  onChange={(e) => set({ url: e.target.value })}
                />
                <FieldDescription>{t("Only HTTPS, and only to public addresses.")}</FieldDescription>
              </Field>
            )}
            <FieldSet>
              <FieldLegend variant="label">{t("Messages")}</FieldLegend>
              <div className="grid gap-4 sm:grid-cols-3">
                <Field>
                  <FieldLabel htmlFor="channel-per-minute">{t("Per minute")}</FieldLabel>
                  <Input
                    id="channel-per-minute"
                    type="number"
                    required
                    min={1}
                    max={maxPerMinute}
                    value={form.messagesPerMinute}
                    onChange={(e) => set({ messagesPerMinute: Number(e.target.value) })}
                  />
                </Field>
                <Field>
                  <FieldLabel htmlFor="channel-entries">{t("Entries each")}</FieldLabel>
                  <Input
                    id="channel-entries"
                    type="number"
                    required
                    min={1}
                    max={maxEntries(form.kind)}
                    value={form.entriesPerMessage}
                    onChange={(e) => set({ entriesPerMessage: Number(e.target.value) })}
                  />
                </Field>
                <Field>
                  <FieldLabel htmlFor="channel-time-zone">{t("Time zone")}</FieldLabel>
                  <TimeZonePicker id="channel-time-zone" value={form.timeZone} onChange={(timeZone) => set({ timeZone })} className="w-full" />
                </Field>
              </div>
              <FieldDescription>
                {t(
                  "After 5 messages at once, the channel sends at most this many a minute, each with at most this many entries, and counts the entries it leaves out. Times are in the time zone, but those of webhooks in UTC.",
                )}
              </FieldDescription>
            </FieldSet>
            {save.error && <FieldError>{save.error.message}</FieldError>}
          </FieldGroup>
          <DialogFooter>
            <DialogClose asChild>
              <Button variant="outline">{t("Cancel")}</Button>
            </DialogClose>
            <Button type="submit" disabled={save.isPending || !form.name.trim()}>
              {save.isPending ? t("Saving…") : t("Save")}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

type Form = ReturnType<typeof formOf>

function MailFields({ form, set, keepsPassword }: { form: Form; set: (change: Partial<Form>) => void; keepsPassword: boolean }) {
  return (
    <>
      <div className="grid gap-4 sm:grid-cols-[1fr_7rem]">
        <Field>
          <FieldLabel htmlFor="channel-host">{t("Mail server")}</FieldLabel>
          <Input
            id="channel-host"
            required
            className="font-mono"
            spellCheck={false}
            placeholder={examples.host}
            value={form.host}
            onChange={(e) => set({ host: e.target.value })}
          />
        </Field>
        <Field>
          <FieldLabel htmlFor="channel-port">{t("Port")}</FieldLabel>
          <Input
            id="channel-port"
            type="number"
            required
            min={1}
            max={65535}
            value={form.port}
            onChange={(e) => set({ port: Number(e.target.value) })}
          />
        </Field>
      </div>
      <Field>
        <FieldLabel htmlFor="channel-security">{t("Encryption")}</FieldLabel>
        <Select
          value={form.security}
          onValueChange={(security: MailSettings["security"]) => set({ security, port: security === "tls" ? 465 : 587 })}
        >
          <SelectTrigger id="channel-security" className="w-full">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="starttls">{t("STARTTLS, usually at port 587")}</SelectItem>
            <SelectItem value="tls">{t("TLS, usually at port 465")}</SelectItem>
          </SelectContent>
        </Select>
        <FieldDescription>{t("Mails never go unencrypted, so that nobody reads the password on the way.")}</FieldDescription>
      </Field>
      <div className="grid gap-4 sm:grid-cols-2">
        <Field>
          <FieldLabel htmlFor="channel-username">{t("User")}</FieldLabel>
          <Input
            id="channel-username"
            autoComplete="off"
            spellCheck={false}
            value={form.username}
            onChange={(e) => set({ username: e.target.value })}
          />
          <FieldDescription>{t("Empty if the server needs no sign-in.")}</FieldDescription>
        </Field>
        <Field>
          <FieldLabel htmlFor="channel-password">{t("Password")}</FieldLabel>
          <Input
            id="channel-password"
            type="password"
            autoComplete="new-password"
            disabled={!form.username.trim()}
            required={!!form.username.trim() && !keepsPassword}
            placeholder={keepsPassword ? t("Leave empty to keep it") : undefined}
            value={form.password}
            onChange={(e) => set({ password: e.target.value })}
          />
        </Field>
      </div>
      <div className="grid gap-4 sm:grid-cols-2">
        <Field>
          <FieldLabel htmlFor="channel-from">{t("From")}</FieldLabel>
          <Input
            id="channel-from"
            type="email"
            required
            placeholder={examples.from}
            value={form.from}
            onChange={(e) => set({ from: e.target.value })}
          />
        </Field>
        <Field>
          <FieldLabel htmlFor="channel-to">{t("To")}</FieldLabel>
          <Input
            id="channel-to"
            required
            placeholder={examples.to}
            value={form.to}
            onChange={(e) => set({ to: e.target.value })}
          />
          <FieldDescription>{t("Up to 10 addresses, separated by commas.")}</FieldDescription>
        </Field>
      </div>
    </>
  )
}
