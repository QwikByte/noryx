import { t } from "i18next"
import { Field, FieldDescription, FieldError, FieldLabel } from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import { Textarea } from "@/components/ui/textarea"
import type { ErrorsAt } from "@/lib/use-draft"

type Name = { panelName: string; signInNotice: string }

/** The name of the panel and the notice of the sign-in page, which anyone who opens the panel sees. */
export function NameFields({ value, onChange, errorAt }: { value: Name; onChange: (change: Partial<Name>) => void; errorAt?: ErrorsAt }) {
  return (
    <>
      <Field data-invalid={!!errorAt?.("panelName")}>
        <FieldLabel htmlFor="settings-panel-name">{t("Name")}</FieldLabel>
        <Input
          id="settings-panel-name"
          aria-invalid={!!errorAt?.("panelName")}
          // i18next-instrument-ignore-next-line: the name the panel has without one
          placeholder="Noryx"
          maxLength={64}
          value={value.panelName}
          onChange={(e) => onChange({ panelName: e.target.value })}
        />
        <FieldDescription>
          {t(
            "Names the panel in the browser's tab, the sidebar and notifications, e.g. Noryx · Test, so that two panels don't look the same. Authenticator apps show it for two-factor authentication set up from now on; those set up already keep the name they have.",
          )}
        </FieldDescription>
        <FieldError errors={errorAt?.("panelName")} />
      </Field>
      <Field data-invalid={!!errorAt?.("signInNotice")}>
        <FieldLabel htmlFor="settings-sign-in-notice">{t("Notice on the sign-in page")}</FieldLabel>
        <Textarea
          id="settings-sign-in-notice"
          aria-invalid={!!errorAt?.("signInNotice")}
          rows={3}
          maxLength={500}
          value={value.signInNotice}
          onChange={(e) => onChange({ signInNotice: e.target.value })}
        />
        <FieldDescription>
          {t("Plain text, e.g. whom to ask for access. Both are public: the sign-in page shows them to anyone who opens the panel.")}
        </FieldDescription>
        <FieldError errors={errorAt?.("signInNotice")} />
      </Field>
    </>
  )
}
