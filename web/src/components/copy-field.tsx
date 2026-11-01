import { CheckIcon, CopyIcon } from "@phosphor-icons/react"
import { t } from "i18next"
import { useState } from "react"
import { toast } from "sonner"

/** A value to copy, such as a command or a link, on the console's dark surface. */
export function CopyField({ value, label, prefix }: { value: string; label: string; prefix?: string }) {
  const [copied, setCopied] = useState(false)

  async function copy() {
    try {
      await navigator.clipboard.writeText(value)
      setCopied(true)
      setTimeout(() => setCopied(false), 2000)
    } catch {
      toast.error(t("The browser didn't allow copying."))
    }
  }

  return (
    <div className="flex items-center gap-2 rounded-lg bg-console py-1 pr-1 pl-3 text-console-foreground focus-within:ring-2 focus-within:ring-ring">
      {prefix && (
        <span aria-hidden className="font-mono text-xs text-console-command">
          {prefix}
        </span>
      )}
      <input
        readOnly
        value={value}
        aria-label={label}
        onFocus={(e) => e.target.select()}
        className="h-8 min-w-0 flex-1 bg-transparent font-mono text-xs outline-none"
      />
      <button
        type="button"
        aria-label={copied ? t("Copied") : t("Copy {{label}}", { label })}
        onClick={copy}
        className="grid size-7 shrink-0 place-items-center rounded-md text-console-muted transition-colors hover:bg-white/10 hover:text-console-foreground"
      >
        {copied ? <CheckIcon className="size-4 text-console-command" /> : <CopyIcon className="size-4" />}
      </button>
    </div>
  )
}
