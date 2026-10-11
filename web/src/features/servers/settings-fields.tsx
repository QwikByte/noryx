import { CaretUpDownIcon } from "@phosphor-icons/react"
import { useQuery } from "@tanstack/react-query"
import { t } from "i18next"
import { useState } from "react"
import { TimeZonePicker } from "@/components/time-zone-picker"
import { Button } from "@/components/ui/button"
import { Command, CommandEmpty, CommandInput, CommandItem, CommandList } from "@/components/ui/command"
import { Field, FieldContent, FieldDescription, FieldLabel, FieldTitle } from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover"
import { RadioGroup, RadioGroupItem } from "@/components/ui/radio-group"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { Switch } from "@/components/ui/switch"
import { Textarea } from "@/components/ui/textarea"
import { gameVersionsQuery } from "@/features/plugins/api"
import { formatMegabytes, formatSeconds } from "@/lib/format"
import { msg } from "@/lib/i18n"
import type { RestartPolicy } from "./api"
import { containerMemoryMb, maxMemoryMb, memoryOptionsMb, minMemoryMb } from "./server-types"

// Fields shared by the settings of a server and the templates for new servers.

const javaVersions: [value: string, label: string][] = [
  ["", msg("Newest")],
  ["25", "Java 25"],
  ["21", "Java 21"],
  ["17", "Java 17"],
  ["11", "Java 11"],
  ["8", "Java 8"],
]

const restartPolicies: [RestartPolicy, string, string][] = [
  ["always", msg("Always running"), msg("Starts with the node and after a crash, unless you stopped it or it crashed 5 times in a row.")],
  ["on_crash", msg("After a crash"), msg("Starts again when it crashes, up to 5 times in a row.")],
  ["never", msg("Only manually"), msg("Starts only when you start it.")],
]

/** The Minecraft version of a game server, with the releases as suggestions; empty is the latest. */
export function VersionField({ id, value, onChange }: { id: string; value: string; onChange: (version: string) => void }) {
  const { data: releases = [] } = useQuery(gameVersionsQuery)
  return (
    <Field>
      <FieldLabel htmlFor={id}>{t("Minecraft version")}</FieldLabel>
      <Input
        id={id}
        list={`${id}-releases`}
        autoComplete="off"
        placeholder={t("Latest")}
        value={value}
        onChange={(e) => onChange(e.target.value)}
      />
      <datalist id={`${id}-releases`}>
        {releases.map((release) => (
          <option key={release} value={release} />
        ))}
      </datalist>
    </Field>
  )
}

/**
 * Chooses the memory of a server: one of the usual steps, or any other amount from 512 MiB to 64 GiB that is entered.
 * freeMb, if the node limits it, leaves out what doesn't fit.
 */
export function MemoryField({
  id,
  label = t("Memory"),
  value,
  onChange,
  freeMb,
}: {
  id: string
  label?: string
  value: number
  onChange: (mb: number) => void
  freeMb?: number
}) {
  const [open, setOpen] = useState(false)
  const [search, setSearch] = useState("")
  const fits = (mb: number) => freeMb === undefined || containerMemoryMb(mb) <= freeMb
  const entered = Number(search)
  const other = Number.isInteger(entered) && entered >= minMemoryMb && entered <= maxMemoryMb ? [entered] : []
  return (
    <Field>
      <FieldLabel htmlFor={id}>{label}</FieldLabel>
      <Popover
        open={open}
        onOpenChange={(open) => {
          setOpen(open)
          setSearch("")
        }}
      >
        <PopoverTrigger asChild>
          <Button id={id} type="button" variant="outline" role="combobox" className="w-full justify-between font-normal">
            {formatMegabytes(value)}
            <CaretUpDownIcon className="text-muted-foreground" />
          </Button>
        </PopoverTrigger>
        <PopoverContent align="start" className="w-(--radix-popover-trigger-width) min-w-60 p-0">
          <Command label={label}>
            <CommandInput inputMode="numeric" placeholder={t("Other amount in MiB")} value={search} onValueChange={setSearch} />
            <CommandList>
              <CommandEmpty>{t("Enter 512 to 65536 MiB.")}</CommandEmpty>
              {[...new Set([...memoryOptionsMb, value, ...other])]
                .sort((a, b) => a - b)
                .map((mb) => (
                  <CommandItem
                    key={mb}
                    value={String(mb)}
                    keywords={[formatMegabytes(mb)]}
                    disabled={!fits(mb)}
                    data-checked={mb === value}
                    onSelect={() => {
                      onChange(mb)
                      setOpen(false)
                    }}
                  >
                    <span className="flex-1">{formatMegabytes(mb)}</span>
                    <span className="text-xs text-muted-foreground tabular-nums">{fits(mb) ? `${mb} MiB` : t("More than the node has left")}</span>
                  </CommandItem>
                ))}
            </CommandList>
          </Command>
        </PopoverContent>
      </Popover>
      {freeMb !== undefined && (
        <FieldDescription>
          {t("{{free}} left on the node for the server and Java's overhead", { free: formatMegabytes(Math.max(0, freeMb)) })}
        </FieldDescription>
      )}
    </Field>
  )
}

export function RestartPolicyField({ value, onChange }: { value: RestartPolicy; onChange: (policy: RestartPolicy) => void }) {
  return (
    <RadioGroup
      value={value}
      onValueChange={(v) => onChange(v as RestartPolicy)}
      aria-label={t("When the server starts")}
      className="gap-3 sm:grid-cols-3"
    >
      {restartPolicies.map(([policy, label, description]) => (
        <FieldLabel key={policy} htmlFor={`restart-${policy}`}>
          <Field orientation="horizontal" className="items-start">
            <FieldContent>
              <FieldTitle>{t(label)}</FieldTitle>
              <FieldDescription>{t(description)}</FieldDescription>
            </FieldContent>
            <RadioGroupItem id={`restart-${policy}`} value={policy} />
          </Field>
        </FieldLabel>
      ))}
    </RadioGroup>
  )
}

/** The Java version of a game server; empty is the newest. */
export function JavaField({ id = "settings-java", value, onChange }: { id?: string; value: string; onChange: (java: string) => void }) {
  return (
    <Field>
      <FieldLabel htmlFor={id}>{t("Java version")}</FieldLabel>
      <Select value={value || "newest"} onValueChange={(v) => onChange(v === "newest" ? "" : v)}>
        <SelectTrigger id={id} className="w-full sm:w-64">
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          {javaVersions.map(([value, label]) => (
            <SelectItem key={label} value={value || "newest"}>
              {t(label)}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
      <FieldDescription>
        {t("Minecraft 1.20.5 and newer needs Java 21 or newer, 1.18 to 1.20.4 Java 17, and versions up to 1.16 run best on Java 8 or 11.")}
      </FieldDescription>
    </Field>
  )
}

/** Java version and Aikar's flags, which only game servers have. */
export function JavaFields({
  java,
  aikarFlags,
  onChange,
}: {
  java: string
  aikarFlags: boolean
  onChange: (change: { java?: string; aikarFlags?: boolean }) => void
}) {
  return (
    <>
      <JavaField value={java} onChange={(java) => onChange({ java })} />
      <Field orientation="horizontal">
        <Switch id="settings-aikar" checked={aikarFlags} onCheckedChange={(on) => onChange({ aikarFlags: on })} />
        <FieldContent>
          <FieldLabel htmlFor="settings-aikar">{t("Aikar's flags")}</FieldLabel>
          <FieldDescription>{t("Garbage collector tuning recommended for Paper and its forks, which reduces lag spikes.")}</FieldDescription>
        </FieldContent>
      </Field>
    </>
  )
}

/** JVM options, one per line. */
export function JvmOptionsField({ value, onChange }: { value: string; onChange: (lines: string) => void }) {
  return (
    <Field>
      <FieldLabel htmlFor="settings-jvm">{t("JVM options")}</FieldLabel>
      <Textarea
        id="settings-jvm"
        rows={3}
        className="font-mono"
        // i18next-instrument-ignore-next-line: an example of what to enter
        placeholder="-Dfile.encoding=UTF-8"
        value={value}
        onChange={(e) => onChange(e.target.value)}
      />
      <FieldDescription>
        {t("One option per line. The memory is set above, not here. Options that run code, such as Java agents, aren't allowed.")}
      </FieldDescription>
    </Field>
  )
}

const stopTimeouts = [30, 60, 120, 180, 300, 600]

/** How long a server may take to stop, from 30 seconds to 10 minutes. */
export function StopTimeoutField({
  id = "settings-stop-timeout",
  value,
  onChange,
}: {
  id?: string
  value: number
  onChange: (seconds: number) => void
}) {
  return (
    <Field>
      <FieldLabel htmlFor={id}>{t("Stop timeout")}</FieldLabel>
      <Select value={String(value)} onValueChange={(v) => onChange(Number(v))}>
        <SelectTrigger id={id} className="w-full sm:w-64">
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          {[...new Set([...stopTimeouts, value])]
            .sort((a, b) => a - b)
            .map((seconds) => (
              <SelectItem key={seconds} value={String(seconds)}>
                {formatSeconds(seconds)}
                {seconds === 60 && <span className="text-muted-foreground">{t("Default")}</span>}
              </SelectItem>
            ))}
        </SelectContent>
      </Select>
      <FieldDescription>
        {t("How long the server may take to save its worlds when it stops or restarts before it is killed. Large modded worlds may need longer.")}
      </FieldDescription>
    </Field>
  )
}

export function TimeZoneField({
  id = "settings-time-zone",
  value,
  onChange,
}: {
  id?: string
  value: string
  onChange: (zone: string) => void
}) {
  return (
    <Field>
      <FieldLabel htmlFor={id}>{t("Time zone")}</FieldLabel>
      <TimeZonePicker id={id} value={value} onChange={onChange} className="w-full sm:w-72" />
      <FieldDescription>{t("The time of the server's log and of plugins that work with times, e.g. for daily rewards.")}</FieldDescription>
    </Field>
  )
}

export function CpuLimitField({ value, onChange, cpus }: { value: number; onChange: (cores: number) => void; cpus?: number }) {
  return (
    <Field>
      <FieldLabel htmlFor="settings-cpu">{t("CPU limit")}</FieldLabel>
      <Input
        id="settings-cpu"
        type="number"
        min={0}
        max={cpus}
        step={0.5}
        className="w-full font-mono sm:w-40"
        value={value}
        onChange={(e) => onChange(e.target.valueAsNumber || 0)}
      />
      <FieldDescription>
        {cpus
          ? t("CPU cores the server may use of the node's {{count}}. 0 means no limit, which suits most servers.", { count: cpus })
          : t("CPU cores the server may use. 0 means no limit, which suits most servers.")}
      </FieldDescription>
    </Field>
  )
}
