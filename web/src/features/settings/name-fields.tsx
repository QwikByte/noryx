import { t } from "i18next"
import { Field, FieldDescription, FieldLabel } from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import { Textarea } from "@/components/ui/textarea"

type Name = { panelName: string; signInNotice: string }

/** The name of the panel and the notice of the sign-in page, which anyone who opens the panel sees. */
export function NameFields({ value, onChange }: { value: Name; onChange: (change: Partial<Name>) => void }) {
  return (
    <>
      <Field>
        <FieldLabel htmlFor="settings-panel-name">{t("Name")}</FieldLabel>
        <Input
          id="settings-panel-name"
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
      </Field>
      <Field>
        <FieldLabel htmlFor="settings-sign-in-notice">{t("Notice on the sign-in page")}</FieldLabel>
        <Textarea
          id="settings-sign-in-notice"
          rows={3}
          maxLength={500}
          value={value.signInNotice}
          onChange={(e) => onChange({ signInNotice: e.target.value })}
        />
        <FieldDescription>
          {t("Plain text, e.g. whom to ask for access. Both are public: the sign-in page shows them to anyone who opens the panel.")}
        </FieldDescription>
      </Field>
    </>
  )
}
