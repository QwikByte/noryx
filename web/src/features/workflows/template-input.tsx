import { BracketsCurlyIcon } from "@phosphor-icons/react"
import { t } from "i18next"
import { useContext, useRef, useState } from "react"
import { Command, CommandEmpty, CommandGroup, CommandInput, CommandItem, CommandList } from "@/components/ui/command"
import { InputGroup, InputGroupAddon, InputGroupButton, InputGroupInput, InputGroupTextarea } from "@/components/ui/input-group"
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover"
import { cn } from "@/lib/utils"
import { DataContext } from "./data"

/**
 * A text that may hold templates such as {{trigger.server.name}}, with a picker of the data the step can name, which
 * inserts them where the cursor is.
 */
export function TemplateInput({
  value,
  onChange,
  multiline,
  className,
  ...props
}: {
  value: string
  onChange: (value: string) => void
  multiline?: boolean
  id?: string
  placeholder?: string
  required?: boolean
  className?: string
  "aria-label"?: string
}) {
  const field = useRef<HTMLInputElement & HTMLTextAreaElement>(null)
  const [open, setOpen] = useState(false)

  function pick(path: string) {
    const el = field.current
    const token = `{{${path}}}`
    const start = el?.selectionStart ?? value.length
    const end = el?.selectionEnd ?? value.length
    onChange(value.slice(0, start) + token + value.slice(end))
    setOpen(false)
    requestAnimationFrame(() => {
      el?.focus()
      el?.setSelectionRange(start + token.length, start + token.length)
    })
  }

  const control = {
    ref: field,
    value,
    onChange: (e: { target: { value: string } }) => onChange(e.target.value),
    spellCheck: false,
    className: cn("font-mono text-[0.8125rem]", className),
    ...props,
  }
  return (
    <InputGroup className={cn(multiline && "items-start")}>
      {multiline ? <InputGroupTextarea rows={3} {...control} /> : <InputGroupInput {...control} />}
      <InputGroupAddon align="inline-end" className={cn(multiline && "pt-1.5")}>
        <DataPicker open={open} onOpenChange={setOpen} onPick={pick} />
      </InputGroupAddon>
    </InputGroup>
  )
}

/** A button that lists the data a step can name, to insert one. */
function DataPicker({ open, onOpenChange, onPick }: { open: boolean; onOpenChange: (open: boolean) => void; onPick: (path: string) => void }) {
  const data = useContext(DataContext)
  return (
    <Popover open={open} onOpenChange={onOpenChange}>
      <PopoverTrigger asChild>
        <InputGroupButton size="icon-xs" aria-label={t("Insert data")} title={t("Insert data")} className="text-primary">
          <BracketsCurlyIcon />
        </InputGroupButton>
      </PopoverTrigger>
      <PopoverContent align="end" className="w-80 p-0">
        <Command>
          <CommandInput placeholder={t("Search data…")} />
          <CommandList className="max-h-80">
            <CommandEmpty>{t("Nothing found.")}</CommandEmpty>
            {data.map((group) => (
              <CommandGroup key={group.label} heading={group.label}>
                {group.tokens.map((token) => (
                  <CommandItem key={token.path} value={`${group.label} ${token.label} ${token.path}`} onSelect={() => onPick(token.path)}>
                    <span className="min-w-0 flex-1 truncate">{token.label}</span>
                    <code className="max-w-[45%] truncate font-mono text-[0.6875rem] text-muted-foreground">{token.path}</code>
                  </CommandItem>
                ))}
              </CommandGroup>
            ))}
          </CommandList>
        </Command>
      </PopoverContent>
    </Popover>
  )
}
