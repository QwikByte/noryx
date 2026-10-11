import { LockOpenIcon } from "@phosphor-icons/react"
import { t } from "i18next"
import type { ReactNode } from "react"
import { Trans } from "react-i18next"
import { Callout } from "@/components/callout"
import { LanguageMenu } from "@/components/language-menu"
import { Logo } from "@/components/logo"
import { ThemeToggle } from "@/components/theme-toggle"
import { useApplyDefaults, usePanelName } from "@/features/settings/public"
import { chosenLanguage } from "@/lib/i18n"
import { cn } from "@/lib/utils"

/**
 * The frame of the pages before signing in: on large screens a dark panel with the logo beside
 * the form, which has a title. They follow the panel's defaults where this browser has no choice.
 */
export function AuthLayout({ title, description, children }: { title: string; description?: ReactNode; children: ReactNode }) {
  useApplyDefaults()
  return (
    <div className="grid min-h-svh lg:grid-cols-[minmax(0,5fr)_minmax(0,6fr)]">
      <BrandPanel />
      <main className="relative flex min-h-svh flex-col items-center justify-center px-4 py-20">
        <div className="absolute top-4 right-4 flex items-center gap-2">
          <LanguageMenu value={chosenLanguage()} />
          <ThemeToggle className="w-28" />
        </div>
        <div className="w-full max-w-sm">
          <div className="mb-8 space-y-1.5">
            <Logo className="mb-7 size-10 lg:hidden" />
            <h1 className="heading text-[1.625rem]">{title}</h1>
            {description && <p className="text-sm text-muted-foreground">{description}</p>}
          </div>
          {!window.isSecureContext && <InsecureNotice />}
          {children}
        </div>
      </main>
    </div>
  )
}

// Which blocks of the field run, start or rest; fixed, so that the field looks the same on every visit.
const field = Array.from({ length: 96 }, (_, i) => {
  const n = (Math.sin(i * 12.9898 + 4.1414) * 43758.5453) % 1
  const r = n < 0 ? n + 1 : n
  return r < 0.24 ? "running" : r < 0.27 ? "starting" : r < 0.55 ? "stopped" : "empty"
})

/** The dark side of the sign-in page: the logo with the name of the panel, a field of servers as blocks, and what Noryx is for. */
function BrandPanel() {
  const name = usePanelName()
  return (
    <div className="dark relative hidden flex-col justify-between overflow-hidden border-r bg-sidebar p-12 text-foreground lg:flex">
      <div className="flex items-center gap-3">
        <Logo className="size-9" />
        <span className="heading min-w-0 text-xl break-words">{name}</span>
      </div>
      <div aria-hidden className="grid w-fit grid-cols-12 gap-2 [mask-image:radial-gradient(ellipse_at_30%_50%,black_30%,transparent_75%)]">
        {field.map((state, i) => (
          <span
            key={i}
            style={{ animationDelay: `${(i % 12) * 40 + Math.floor(i / 12) * 60}ms` }}
            className={cn(
              "size-7 animate-in rounded-[4px] fade-in zoom-in-75 fill-mode-both duration-500 motion-reduce:animate-none xl:size-8",
              state === "running" && "bg-primary",
              state === "starting" && "bg-warning",
              state === "stopped" && "bg-white/[0.06] ring-1 ring-white/10 ring-inset",
              state === "empty" && "bg-white/[0.02]",
            )}
          />
        ))}
      </div>
      <p className="max-w-sm text-sm text-muted-foreground">
        {t("Servers, proxies and whole networks of Minecraft, on all your nodes, from one panel.")}
      </p>
    </div>
  )
}

/** Browsers drop the secure session cookie over plain HTTP, except at localhost, so signing in can't work here. */
function InsecureNotice() {
  return (
    <Callout tone="warning" icon={LockOpenIcon} role="note" className="mb-6" title={t("Signing in needs HTTPS")}>
      <Trans
        i18nKey="Browsers only keep you signed in over HTTPS or on localhost. Sign in through an SSH tunnel instead: run <tunnel/> and open <local/>. Then turn on HTTPS under Settings → General."
        components={{
          tunnel: <span className="font-mono break-all">{`ssh -L 8080:${location.host} <user>@${location.hostname}`}</span>,
          local: <span className="font-mono">http://localhost:8080</span>,
        }}
      />
    </Callout>
  )
}
