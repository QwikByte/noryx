import { LockSimpleIcon, PlusIcon, XIcon } from "@phosphor-icons/react"
import { useQuery } from "@tanstack/react-query"
import { t } from "i18next"
import type { ReactNode } from "react"
import { Chip } from "@/components/chip"
import { Segmented } from "@/components/segmented"
import { Button } from "@/components/ui/button"
import { Field, FieldContent, FieldDescription, FieldLabel, FieldLegend, FieldSet } from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { Switch } from "@/components/ui/switch"
import { useAccess } from "@/features/access/use-access"
import { LocationField, RetentionField, SelectionField } from "@/features/backups/backup-fields"
import { networksQuery } from "@/features/networks/api"
import { nodesQuery } from "@/features/nodes/api"
import { notificationsQuery } from "@/features/notify/api"
import { TaskTargetsField } from "@/features/schedules/targets"
import { warningsQuery } from "@/features/servers/api"
import type { Header, Servers, Settings, Step } from "./api"
import { workflowsQuery } from "./api"
import { waitUnits } from "./catalog"
import { ConditionField } from "./condition-field"
import { TemplateInput } from "./template-input"

type Set = (change: Settings) => void

/** A labelled field of a step. */
function Row({ id, label, description, children }: { id?: string; label: string; description?: ReactNode; children: ReactNode }) {
  return (
    <Field>
      <FieldLabel htmlFor={id}>{label}</FieldLabel>
      {children}
      {description && <FieldDescription>{description}</FieldDescription>}
    </Field>
  )
}

/** A labelled template of a step. */
function Text({ id, label, description, value, onChange, multiline, placeholder }: {
  id: string
  label: string
  description?: ReactNode
  value?: string
  onChange: (value: string) => void
  multiline?: boolean
  placeholder?: string
}) {
  return (
    <Row id={id} label={label} description={description}>
      <TemplateInput id={id} value={value ?? ""} onChange={onChange} multiline={multiline} placeholder={placeholder} />
    </Row>
  )
}

function Num({ id, label, value, onChange, min = 0, max, suffix }: {
  id: string
  label: string
  value?: number
  onChange: (n: number) => void
  min?: number
  max?: number
  suffix?: string
}) {
  return (
    <Row id={id} label={label}>
      <div className="flex items-center gap-2">
        <Input id={id} type="number" min={min} max={max} className="w-28 font-mono" value={value ?? 0} onChange={(e) => onChange(e.target.valueAsNumber || 0)} />
        {suffix && <span className="text-sm text-muted-foreground">{suffix}</span>}
      </div>
    </Row>
  )
}

function Choice<T extends string>({ id, label, value, options, onChange }: {
  id: string
  label: string
  value: T
  options: { value: T; label: string }[]
  onChange: (value: T) => void
}) {
  return (
    <Row id={id} label={label}>
      <Select value={value} onValueChange={(v) => onChange(v as T)}>
        <SelectTrigger id={id} className="w-full">
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          {options.map((o) => (
            <SelectItem key={o.value} value={o.value}>
              {o.label}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
    </Row>
  )
}

function Toggle({ id, label, description, checked, onChange }: { id: string; label: string; description?: string; checked: boolean; onChange: (on: boolean) => void }) {
  return (
    <Field orientation="horizontal">
      <Switch id={id} checked={checked} onCheckedChange={onChange} />
      <FieldContent>
        <FieldLabel htmlFor={id}>{label}</FieldLabel>
        {description && <FieldDescription>{description}</FieldDescription>}
      </FieldContent>
    </Field>
  )
}

/** Ready templates that name servers, e.g. the server of the trigger. */
const serverSources = () => [
  { label: t("Server of the trigger"), value: "{{trigger.server}}" },
  { label: t("Current item"), value: "{{item}}" },
]

/** Chooses the servers of a step: nodes, servers, tags and networks, or servers from data, e.g. the server of the trigger. */
export function ServersField({ value, onChange, finds }: { value: Servers; onChange: (servers: Servers) => void; finds: string[] }) {
  const mode = value.from !== undefined && value.from !== "" ? "data" : "chosen"
  return (
    <FieldSet>
      <FieldLegend variant="label">{t("Servers")}</FieldLegend>
      <Segmented
        label={t("Servers")}
        value={mode}
        className="w-fit"
        onChange={(m) => onChange(m === "data" ? { targets: [], from: "{{trigger.server}}" } : { targets: [], from: "" })}
        options={[
          { value: "chosen", label: t("Chosen") },
          { value: "data", label: t("From data") },
        ]}
      />
      {mode === "chosen" ? (
        <TaskTargetsField value={value.targets ?? []} onChange={(targets) => onChange({ ...value, targets })} />
      ) : (
        <>
          <TemplateInput aria-label={t("Servers from data")} value={value.from ?? ""} onChange={(from) => onChange({ ...value, from })} />
          <div className="flex flex-wrap gap-1.5">
            {[...serverSources(), ...finds.map((id) => ({ label: t("Servers of {{step}}", { step: id }), value: `{{steps.${id}.servers}}` }))].map((s) => (
              <button key={s.value} type="button" onClick={() => onChange({ ...value, from: s.value })}>
                <Chip className="font-normal hover:bg-muted">{s.label}</Chip>
              </button>
            ))}
          </div>
          <FieldDescription>{t("A server of the data, a list of them, or IDs as node/server.")}</FieldDescription>
        </>
      )}
    </FieldSet>
  )
}

/** A list of templates, e.g. console commands, one per line. */
function Lines({ label, values, onChange, placeholder, max }: { label: string; values: string[]; onChange: (v: string[]) => void; placeholder?: string; max: number }) {
  const list = values.length ? values : [""]
  return (
    <FieldSet>
      <FieldLegend variant="label">{label}</FieldLegend>
      <ol className="grid gap-2">
        {list.map((v, i) => (
          // The lines have no identity of their own; the inputs are controlled.
          <li key={i} className="flex gap-2">
            <div className="min-w-0 flex-1">
              <TemplateInput aria-label={`${label} ${i + 1}`} value={v} placeholder={i === 0 ? placeholder : undefined} onChange={(x) => onChange(list.with(i, x))} />
            </div>
            <Button type="button" size="icon" variant="ghost" aria-label={t("Remove line {{number}}", { number: i + 1 })} disabled={list.length === 1} onClick={() => onChange(list.toSpliced(i, 1))}>
              <XIcon />
            </Button>
          </li>
        ))}
      </ol>
      <Button type="button" size="sm" variant="outline" className="w-fit" disabled={list.length >= max} onClick={() => onChange([...list, ""])}>
        <PlusIcon />
        {t("Line")}
      </Button>
    </FieldSet>
  )
}

/** The minutes before at which the players are warned of a restart or stop, and the warning. */
function Warnings({ action, value, onChange }: { action: "restart" | "stop"; value: Settings; onChange: Set }) {
  const { data: how } = useQuery(warningsQuery)
  return (
    <div className="grid gap-4 sm:grid-cols-[10rem_1fr]">
      <Row id="step-warnings" label={t("Warn the players")} description={t("Minutes before, up to 60.")}>
        <Input
          id="step-warnings"
          inputMode="numeric"
          // i18next-instrument-ignore-next-line: an example of what to enter
          placeholder="10, 5, 1"
          className="font-mono"
          defaultValue={(value.warnings ?? []).join(", ")}
          onChange={(e) => onChange({ warnings: e.target.value.split(/[\s,]+/).flatMap((m) => (/^\d+$/.test(m) ? [Number(m)] : [])) })}
        />
      </Row>
      <Row id="step-warning" label={t("Warning")} description={t("{minutes} becomes the minutes left. A warning of your own is a console command.")}>
        <Input id="step-warning" maxLength={200} value={value.message ?? ""} placeholder={how?.[action]} onChange={(e) => onChange({ message: e.target.value })} />
      </Row>
    </div>
  )
}

function Headers({ value, onChange }: { value: Header[]; onChange: (h: Header[]) => void }) {
  return (
    <FieldSet>
      <FieldLegend variant="label">{t("Headers")}</FieldLegend>
      {value.length > 0 && (
        <ul className="grid gap-2">
          {value.map((h, i) => (
            // Headers have no identity of their own; the inputs are controlled.
            <li key={i} className="grid grid-cols-[9rem_1fr_auto_auto] items-center gap-2">
              <Input aria-label={t("Name")} className="font-mono text-[0.8125rem]" placeholder={t("Name")} value={h.name} onChange={(e) => onChange(value.with(i, { ...h, name: e.target.value }))} />
              {h.secret ? (
                <Input
                  type="password"
                  aria-label={t("Value")}
                  autoComplete="off"
                  placeholder={t("Kept as it is")}
                  value={h.value}
                  onChange={(e) => onChange(value.with(i, { ...h, value: e.target.value }))}
                />
              ) : (
                <TemplateInput aria-label={t("Value")} value={h.value} onChange={(v) => onChange(value.with(i, { ...h, value: v }))} />
              )}
              <Button
                type="button"
                size="icon"
                variant={h.secret ? "secondary" : "ghost"}
                aria-pressed={h.secret}
                aria-label={t("Secret")}
                title={t("Secret: never shown again, used as it is")}
                onClick={() => onChange(value.with(i, { ...h, secret: !h.secret, value: "" }))}
              >
                <LockSimpleIcon />
              </Button>
              <Button type="button" size="icon" variant="ghost" aria-label={t("Remove header {{name}}", { name: h.name })} onClick={() => onChange(value.toSpliced(i, 1))}>
                <XIcon />
              </Button>
            </li>
          ))}
        </ul>
      )}
      <Button type="button" size="sm" variant="outline" className="w-fit" onClick={() => onChange([...value, { name: "", value: "", secret: false }])}>
        <PlusIcon />
        {t("Header")}
      </Button>
      <FieldDescription>{t("The value of a secret header is never shown again. Changing the URL needs it once more.")}</FieldDescription>
    </FieldSet>
  )
}

function NetworkSelect({ value, onChange }: { value: string; onChange: (id: string) => void }) {
  const access = useAccess()
  const { data: networks = [] } = useQuery({ ...networksQuery, enabled: access.can("networks.view") })
  return <Choice id="step-network" label={t("Network")} value={value} onChange={onChange} options={networks.map((n) => ({ value: n.id, label: n.name }))} />
}

const playerActions = () => [
  { value: "kick", label: t("Kick") },
  { value: "ban", label: t("Ban") },
  { value: "pardon", label: t("Pardon") },
  { value: "whitelist_add", label: t("Add to the whitelist") },
  { value: "whitelist_remove", label: t("Remove from the whitelist") },
  { value: "op", label: t("Make operator") },
  { value: "deop", label: t("Take operator away") },
  { value: "whitelist_on", label: t("Turn the whitelist on") },
  { value: "whitelist_off", label: t("Turn the whitelist off") },
]

const levelOptions = () => [
  { value: "info", label: t("Info") },
  { value: "warn", label: t("Warning") },
  { value: "error", label: t("Error") },
]

/** The settings of a step of any kind. finds are the IDs of the steps that find servers. */
export function StepFields({ step, onChange, workflowId, finds }: { step: Step; onChange: (step: Step) => void; workflowId?: string; finds: string[] }) {
  const w = step.with ?? {}
  const set: Set = (change) => onChange({ ...step, with: { ...w, ...change } })
  const servers = <ServersField value={w} onChange={(s) => set(s)} finds={finds} />
  switch (step.kind) {
    case "if":
      return <ConditionField value={w.condition} onChange={(condition) => set({ condition })} />
    case "switch":
      return <Cases step={step} onChange={onChange} />
    case "foreach":
      return (
        <>
          <Text id="step-items" label={t("Items")} value={w.items} onChange={(items) => set({ items })} description={t("A list from data, e.g. the servers found, or values separated by commas.")} />
          <Num id="step-concurrency" label={t("At a time")} min={1} max={10} value={w.concurrency} onChange={(concurrency) => set({ concurrency })} />
        </>
      )
    case "repeat":
      return (
        <>
          <FieldSet>
            <FieldLegend variant="label">{t("Until")}</FieldLegend>
            <ConditionField value={w.until} onChange={(until) => set({ until })} />
          </FieldSet>
          <div className="grid gap-4 sm:grid-cols-2">
            <Num id="step-max" label={t("At most")} min={1} max={1000} value={w.max} onChange={(max) => set({ max })} suffix={t("times")} />
            <Num id="step-delay" label={t("Wait between")} max={3600} value={w.delay} onChange={(delay) => set({ delay })} suffix={t(waitUnits.seconds)} />
          </div>
        </>
      )
    case "parallel":
      return <Branches step={step} onChange={onChange} />
    case "try":
      return <p className="text-sm text-muted-foreground">{t("If a step under Try fails, the steps under Catch run, with the error as {{error}}.", { error: "{{error.message}}" })}</p>
    case "wait": {
      const mode = w.until !== undefined && w.until !== "" ? "until" : "for"
      return (
        <>
          <Segmented
            label={t("Delay")}
            value={mode}
            className="w-fit"
            onChange={(m) => set(m === "until" ? { for: "", unit: "", until: "22:00" } : { for: "5", unit: "minutes", until: "" })}
            options={[
              { value: "for", label: t("For a time") },
              { value: "until", label: t("Until a time of day") },
            ]}
          />
          {mode === "for" ? (
            <div className="grid gap-4 sm:grid-cols-[1fr_10rem]">
              <Text id="step-for" label={t("Wait")} value={w.for} onChange={(v) => set({ for: v })} />
              <Choice id="step-unit" label={t("Unit")} value={w.unit || "minutes"} onChange={(unit) => set({ unit })} options={Object.entries(waitUnits).map(([value, label]) => ({ value, label: t(label) }))} />
            </div>
          ) : (
            <Text id="step-until" label={t("Until")} value={w.until} onChange={(until) => set({ until })} description={t("A time of day like 22:00, in the time zone of the workflow. Up to 24 hours.")} />
          )}
        </>
      )
    }
    case "set":
      return (
        <>
          <Row id="step-var" label={t("Variable")} description={t("Later steps read it as {{example}}.", { example: `{{vars.${w.name || "name"}}}` })}>
            <Input id="step-var" className="font-mono" value={w.name ?? ""} maxLength={32} onChange={(e) => set({ name: e.target.value.replace(/[^A-Za-z0-9_]/g, "") })} />
          </Row>
          <Choice
            id="step-mode"
            label={t("How")}
            value={w.mode || "set"}
            onChange={(mode) => set({ mode })}
            options={[
              { value: "set", label: t("Set it") },
              { value: "append", label: t("Append to the list") },
              { value: "add", label: t("Add to the number") },
            ]}
          />
          <Text id="step-value" label={t("Value")} value={w.value} onChange={(value) => set({ value })} multiline />
        </>
      )
    case "terminate":
      return (
        <>
          <Toggle id="step-failed" label={t("As failed")} checked={!!w.failed} onChange={(failed) => set({ failed })} />
          <Text id="step-message" label={t("Message")} value={w.message} onChange={(message) => set({ message })} />
          <Text id="step-result" label={t("Result")} value={w.result} onChange={(result) => set({ result })} description={t("What a workflow that runs this one gets.")} />
        </>
      )
    case "call":
      return <Call value={w} set={set} workflowId={workflowId} />
    case "servers":
      return (
        <>
          {servers}
          <Choice
            id="step-state"
            label={t("State")}
            value={w.state || "any"}
            onChange={(state) => set({ state: state === "any" ? "" : state })}
            options={[
              { value: "any", label: t("Any") },
              { value: "running", label: t("Running") },
              { value: "stopped", label: t("Stopped") },
            ]}
          />
          <Toggle id="step-usage" label={t("With usage and players")} description={t("Adds CPU, memory, TPS, data and the players from the latest measurement.")} checked={!!w.usage} onChange={(usage) => set({ usage })} />
        </>
      )
    case "players":
    case "start":
    case "image":
    case "plugins":
      return servers
    case "waitfor":
      return (
        <>
          {servers}
          <Choice
            id="step-state"
            label={t("Until they")}
            value={w.state}
            onChange={(state) => set({ state })}
            options={[
              { value: "running", label: t("run") },
              { value: "stopped", label: t("are stopped") },
              { value: "empty", label: t("have no players") },
            ]}
          />
          <Num id="step-minutes" label={t("At most")} min={1} max={1440} value={w.minutes} onChange={(minutes) => set({ minutes })} suffix={t(waitUnits.minutes)} />
        </>
      )
    case "stop":
    case "restart":
      return (
        <>
          {servers}
          <Warnings action={step.kind} value={w} onChange={set} />
        </>
      )
    case "command":
      return (
        <>
          {servers}
          <Lines label={t("Commands")} values={w.commands ?? []} onChange={(commands) => set({ commands })} max={20} placeholder={t("say Hello {{player}}", { player: "{{trigger.player}}" })} />
          <FieldDescription>{t("{{server}} is the server each command runs on.", { server: "{{server.name}}" })}</FieldDescription>
        </>
      )
    case "message":
      return (
        <>
          {servers}
          <Choice
            id="step-kind"
            label={t("Where")}
            value={w.kind || "chat"}
            onChange={(kind) => set({ kind })}
            options={[
              { value: "chat", label: t("In the chat") },
              { value: "title", label: t("As a title") },
              { value: "actionbar", label: t("Above the hotbar") },
            ]}
          />
          <Text id="step-text" label={t("Message")} value={w.text} onChange={(text) => set({ text })} />
          {w.kind === "title" && <Text id="step-subtitle" label={t("Subtitle")} value={w.subtitle} onChange={(subtitle) => set({ subtitle })} />}
          <Text id="step-player" label={t("Only to the player")} value={w.player} onChange={(player) => set({ player })} description={t("Empty for everyone on the servers.")} />
        </>
      )
    case "backup":
      return <Backup value={w} set={set} servers={servers} />
    case "player":
      return (
        <>
          {servers}
          <Choice id="step-action" label={t("Action")} value={w.action} onChange={(action) => set({ action })} options={playerActions()} />
          {!String(w.action).startsWith("whitelist_o") && (
            <Text id="step-player" label={t("Player")} value={w.player} onChange={(player) => set({ player })} description={t("A name, or a list of names from data.")} />
          )}
          {(w.action === "kick" || w.action === "ban") && <Text id="step-reason" label={t("Reason")} value={w.reason} onChange={(reason) => set({ reason })} />}
          {w.action === "ban" && <Num id="step-minutes" label={t("For")} value={w.minutes} max={525600} onChange={(minutes) => set({ minutes })} suffix={t("minutes, 0 for ever")} />}
        </>
      )
    case "send":
      return (
        <>
          <NetworkSelect value={w.network} onChange={(network) => set({ network })} />
          <Text id="step-player" label={t("Player")} value={w.player} onChange={(player) => set({ player })} />
          <Text id="step-server" label={t("To the server")} value={w.server} onChange={(server) => set({ server })} description={t("The name of a game server of the network.")} />
        </>
      )
    case "maintenance":
      return (
        <>
          <NetworkSelect value={w.network} onChange={(network) => set({ network })} />
          <Toggle id="step-enabled" label={t("Start maintenance")} description={t("Off ends it.")} checked={!!w.enabled} onChange={(enabled) => set({ enabled })} />
          <Text id="step-server" label={t("Only the server")} value={w.server} onChange={(server) => set({ server })} description={t("The name of a game server of the network; empty for the whole network.")} />
          {w.enabled && <Num id="step-minutes" label={t("Ends after")} value={w.minutes} onChange={(minutes) => set({ minutes })} suffix={t("minutes, 0 until it is ended")} />}
        </>
      )
    case "rolling":
      return (
        <>
          <NetworkSelect value={w.network} onChange={(network) => set({ network })} />
          <Num id="step-batch" label={t("Servers at a time")} min={1} max={10} value={w.batch} onChange={(batch) => set({ batch })} />
        </>
      )
    case "notify":
      return <Notify value={w} set={set} />
    case "http":
      return (
        <>
          <div className="grid gap-4 sm:grid-cols-[8rem_1fr]">
            <Choice id="step-method" label={t("Method")} value={w.method} onChange={(method) => set({ method })} options={["GET", "POST", "PUT", "PATCH", "DELETE"].map((m) => ({ value: m, label: m }))} />
            <Text id="step-url" label={t("URL")} value={w.url} onChange={(url) => set({ url })} />
          </div>
          <Headers value={w.headers ?? []} onChange={(headers) => set({ headers })} />
          {w.method !== "GET" && <Text id="step-body" label={t("Body")} value={w.body} onChange={(body) => set({ body })} multiline description={t("Sent as JSON if it is JSON, else as text.")} />}
          <Toggle id="step-any" label={t("Any status")} description={t("Succeeds whatever the status of the answer; otherwise it has to be 2xx.")} checked={!!w.anyStatus} onChange={(anyStatus) => set({ anyStatus })} />
          <FieldDescription>{t("Only HTTPS to public addresses, without following redirects.")}</FieldDescription>
        </>
      )
    case "log":
      return (
        <>
          <Choice id="step-level" label={t("Level")} value={w.level || "info"} onChange={(level) => set({ level })} options={levelOptions()} />
          <Text id="step-message" label={t("Message")} value={w.message} onChange={(message) => set({ message })} />
        </>
      )
  }
  return null
}

function Cases({ step, onChange }: { step: Step; onChange: (step: Step) => void }) {
  const cases = step.cases ?? []
  return (
    <>
      <Text id="step-value" label={t("Value")} value={step.with?.value} onChange={(value) => onChange({ ...step, with: { ...step.with, value } })} />
      <FieldSet>
        <FieldLegend variant="label">{t("Cases")}</FieldLegend>
        <ol className="grid gap-2">
          {cases.map((c, i) => (
            // Cases have no identity of their own; the inputs are controlled.
            <li key={i} className="flex gap-2">
              <div className="min-w-0 flex-1">
                <TemplateInput aria-label={t("Case {{number}}", { number: i + 1 })} value={c.value} onChange={(value) => onChange({ ...step, cases: cases.with(i, { ...c, value }) })} />
              </div>
              <Button
                type="button"
                size="icon"
                variant="ghost"
                aria-label={t("Remove case {{number}}", { number: i + 1 })}
                disabled={cases.length === 1}
                onClick={() => onChange({ ...step, cases: cases.toSpliced(i, 1) })}
              >
                <XIcon />
              </Button>
            </li>
          ))}
        </ol>
        <Button type="button" size="sm" variant="outline" className="w-fit" disabled={cases.length >= 20} onClick={() => onChange({ ...step, cases: [...cases, { value: "", steps: [] }] })}>
          <PlusIcon />
          {t("Case")}
        </Button>
        <FieldDescription>{t("Removing a case removes its steps too.")}</FieldDescription>
      </FieldSet>
    </>
  )
}

function Branches({ step, onChange }: { step: Step; onChange: (step: Step) => void }) {
  const branches = step.branches ?? []
  return (
    <div className="flex flex-wrap items-center gap-3 text-sm">
      <span>{t("{{count}} branches", { count: branches.length })}</span>
      <Button type="button" size="sm" variant="outline" disabled={branches.length >= 10} onClick={() => onChange({ ...step, branches: [...branches, []] })}>
        <PlusIcon />
        {t("Branch")}
      </Button>
      <Button type="button" size="sm" variant="ghost" disabled={branches.length <= 2} onClick={() => onChange({ ...step, branches: branches.slice(0, -1) })}>
        <XIcon />
        {t("Remove the last branch")}
      </Button>
    </div>
  )
}

function Call({ value, set, workflowId }: { value: Settings; set: Set; workflowId?: string }) {
  const { data: workflows = [] } = useQuery(workflowsQuery)
  const target = workflows.find((w) => w.id === value.workflow)
  const inputs: Record<string, string> = value.inputs ?? {}
  return (
    <>
      <Choice
        id="step-workflow"
        label={t("Workflow")}
        value={value.workflow}
        onChange={(workflow) => set({ workflow, inputs: {} })}
        options={workflows.map((w) => ({ value: w.id, label: w.id === workflowId ? t("{{name}} (this one)", { name: w.name }) : w.name }))}
      />
      {target?.params.map((p) => (
        <Text
          key={p.name}
          id={`step-input-${p.name}`}
          label={p.name}
          description={p.description}
          value={inputs[p.name]}
          onChange={(v) => set({ inputs: { ...inputs, [p.name]: v } })}
        />
      ))}
      <Toggle id="step-wait" label={t("Wait for it")} description={t("Waits until it ends, so that its outcome and result are known; fails if it fails.")} checked={!!value.wait} onChange={(wait) => set({ wait })} />
    </>
  )
}

function Notify({ value, set }: { value: Settings; set: Set }) {
  const access = useAccess()
  const { data } = useQuery({ ...notificationsQuery, enabled: access.can("notifications.manage") && access.can("logs.view") })
  return (
    <>
      <Choice id="step-channel" label={t("Channel")} value={value.channel} onChange={(channel) => set({ channel })} options={(data?.channels ?? []).map((c) => ({ value: c.id, label: c.name }))} />
      <Choice id="step-level" label={t("Level")} value={value.level || "info"} onChange={(level) => set({ level })} options={levelOptions()} />
      <Text id="step-message" label={t("Message")} value={value.message} onChange={(message) => set({ message })} multiline />
      <FieldDescription>{t("The channel sends it like an entry of the log, a few at once and then one a minute at most.")}</FieldDescription>
    </>
  )
}

function Backup({ value, set, servers }: { value: Settings; set: Set; servers: ReactNode }) {
  const { data: nodes = [] } = useQuery(nodesQuery)
  const locations = nodes.flatMap((n) => n.info?.storage.map((l) => l.name) ?? [])
  const backup = value.backup
  return (
    <>
      {servers}
      <Text id="step-label" label={t("Label")} value={value.label} onChange={(label) => set({ label })} description={t("Empty for the name of the workflow.")} />
      <SelectionField value={backup.selection} onChange={(selection) => set({ backup: { ...backup, selection } })} />
      <LocationField locations={locations} value={backup.location} onChange={(location) => set({ backup: { ...backup, location } })} />
      <RetentionField value={backup} onChange={(change) => set({ backup: { ...backup, ...change } })} />
    </>
  )
}
