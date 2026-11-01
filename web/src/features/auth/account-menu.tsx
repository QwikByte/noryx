import { PaletteIcon, SignOutIcon, TranslateIcon, UserCircleIcon } from "@phosphor-icons/react"
import { useQuery } from "@tanstack/react-query"
import { Link, useNavigate } from "@tanstack/react-router"
import { t } from "i18next"
import { toast } from "sonner"
import { LanguageChoices } from "@/components/language-menu"
import { ThemeChoices } from "@/components/theme-toggle"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuSub,
  DropdownMenuSubContent,
  DropdownMenuSubTrigger,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { useAccess } from "@/features/access/use-access"
import { useSettings } from "@/features/preferences/api"
import { chosenLanguage } from "@/lib/i18n"
import { cn } from "@/lib/utils"
import { meQuery, useLogout, useSetLanguage } from "./api"

/**
 * The signed-in user, whose menu holds their account, the colour theme and language of the
 * panel, and signing out. folded shows only the avatar, e.g. in a folded sidebar.
 */
export function AccountMenu({ folded, className }: { folded?: boolean; className?: string }) {
  const { data: user } = useQuery(meQuery)
  const { admin } = useAccess()
  const logout = useLogout()
  const navigate = useNavigate()
  const setLanguage = useSetLanguage()
  const { change } = useSettings()
  const name = user?.username ?? ""
  const role = admin ? t("Administrator") : t("Your account")

  return (
    <DropdownMenu>
      <DropdownMenuTrigger
        aria-label={t("Account of {{name}}", { name })}
        title={folded ? name : undefined}
        className={cn(
          "group flex min-w-0 items-center gap-2.5 rounded-xl p-1.5 text-left transition-colors outline-none hover:bg-muted focus-visible:ring-2 focus-visible:ring-ring data-[state=open]:bg-muted",
          className,
        )}
      >
        <span
          aria-hidden
          className="grid size-8 shrink-0 place-items-center rounded-full bg-linear-to-br from-violet-500 to-sky-500 text-xs font-bold text-white uppercase"
        >
          {name.charAt(0)}
        </span>
        {/* On small screens, only the avatar shows. */}
        {!folded && (
          <span className="min-w-0 flex-1 leading-tight max-md:hidden">
            <span className="block truncate text-sm font-semibold">{name}</span>
            <span className="block truncate text-xs text-muted-foreground">{role}</span>
          </span>
        )}
      </DropdownMenuTrigger>
      <DropdownMenuContent side={folded ? "right" : "top"} align={folded ? "end" : "start"} className="w-60">
        <DropdownMenuLabel className="leading-tight">
          <span className="block truncate text-sm font-semibold text-foreground">{name}</span>
          <span className="block truncate text-xs font-normal">{role}</span>
        </DropdownMenuLabel>
        <DropdownMenuSeparator />
        <DropdownMenuItem asChild>
          <Link to="/account">
            <UserCircleIcon />
            {t("Your account")}
          </Link>
        </DropdownMenuItem>
        <DropdownMenuSub>
          <DropdownMenuSubTrigger>
            <PaletteIcon />
            {t("Colour theme")}
          </DropdownMenuSubTrigger>
          <DropdownMenuSubContent className="w-40">
            <ThemeChoices onChoose={(theme) => change({ theme })} />
          </DropdownMenuSubContent>
        </DropdownMenuSub>
        <DropdownMenuSub>
          <DropdownMenuSubTrigger>
            <TranslateIcon />
            {t("Language")}
          </DropdownMenuSubTrigger>
          <DropdownMenuSubContent className="w-44">
            <LanguageChoices value={user?.language ?? chosenLanguage()} onChoose={(language) => setLanguage.mutate(language, { onError: (error) => toast.error(error.message) })} />
          </DropdownMenuSubContent>
        </DropdownMenuSub>
        <DropdownMenuSeparator />
        <DropdownMenuItem variant="destructive" onSelect={() => logout.mutate(undefined, { onSettled: () => navigate({ to: "/login" }) })}>
          <SignOutIcon />
          {t("Sign out")}
        </DropdownMenuItem>
      </DropdownMenuContent>
    </DropdownMenu>
  )
}
