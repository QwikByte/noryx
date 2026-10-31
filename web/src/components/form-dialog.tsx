// guard and useBusy live with the content they guard; editing this file reloads the dialogs instead of refreshing them.
/* oxlint-disable react/only-export-components */
import { type ComponentProps, createContext, useContext, useLayoutEffect, useState } from "react"
import { DialogContent } from "@/components/ui/dialog"

/** Keeps a dialog open while its action runs, so that it isn't closed by mistake. */
export function guard(busy: boolean) {
  return {
    showCloseButton: !busy,
    onInteractOutside: (e: Event) => busy && e.preventDefault(),
    onEscapeKeyDown: (e: KeyboardEvent) => busy && e.preventDefault(),
  }
}

const Busy = createContext<(busy: boolean) => void>(() => {})

/**
 * The content of a dialog whose form is a component inside it, which starts anew each time the dialog opens, as the
 * content goes when it closes. The form tells with useBusy while its action runs, and the dialog stays open meanwhile.
 */
export function FormDialogContent(props: ComponentProps<typeof DialogContent>) {
  const [busy, setBusy] = useState(false)
  return (
    <Busy value={setBusy}>
      <DialogContent {...props} {...guard(busy)} />
    </Busy>
  )
}

/** Keeps the dialog of a form open while busy, e.g. while its action runs. */
export function useBusy(busy: boolean) {
  const setBusy = useContext(Busy)
  useLayoutEffect(() => {
    setBusy(busy)
    // A form that goes while busy, e.g. as its action goes on in the background, leaves the dialog free for the next one.
    return () => setBusy(false)
  }, [busy, setBusy])
}
