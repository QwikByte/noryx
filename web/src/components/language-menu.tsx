import { TranslateIcon } from "@phosphor-icons/react"
import { t } from "i18next"
import { Button } from "@/components/ui/button"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuLabel,
  DropdownMenuRadioGroup,
  DropdownMenuRadioItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { useNoLanguage } from "@/features/settings/public"
import { chooseLanguage, languageName, languages } from "@/lib/i18n"

/** Chooses the panel's language; "" follows the panel's default or the browser. onChoose stores the choice, e.g. for the signed-in user. */
export function LanguageMenu({ value, onChoose = chooseLanguage }: { value: string; onChoose?: (language: string) => void }) {
  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button variant="ghost" size="icon-sm" aria-label={t("Language")} title={t("Language")} className="shrink-0 text-muted-foreground">
          <TranslateIcon />
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="w-48">
        <DropdownMenuLabel>{t("Language")}</DropdownMenuLabel>
        <DropdownMenuSeparator />
        <LanguageChoices value={value} onChoose={onChoose} />
      </DropdownMenuContent>
    </DropdownMenu>
  )
}

/** The languages as items of a menu. */
export function LanguageChoices({ value, onChoose }: { value: string; onChoose: (language: string) => void }) {
  const none = useNoLanguage()
  return (
    <DropdownMenuRadioGroup value={value} onValueChange={onChoose}>
      <DropdownMenuRadioItem value="">{none}</DropdownMenuRadioItem>
      {languages.map((code) => (
        <DropdownMenuRadioItem key={code} value={code} lang={code}>
          {languageName(code)}
        </DropdownMenuRadioItem>
      ))}
    </DropdownMenuRadioGroup>
  )
}
