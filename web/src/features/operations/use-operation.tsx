import { t } from "i18next"
import { useRef, useState } from "react"
import { toast } from "sonner"
import { isCancelled, type Operation, useLiveOperation } from "./api"
import { LiveToast } from "./operation-progress"

/** What a notification says about an operation that ended well, with a link to what it made. */
export interface Done {
  message: string
  description?: string
  action?: { label: string; onClick: () => void }
  /** It ended, but not entirely well. */
  warning?: boolean
}

/**
 * Runs an action that may become an operation and tells how it ends in a notification. A
 * dialog shows the live operation meanwhile, until it lets it go on in the background, where
 * a notification follows it; with notify, a notification follows it from the start.
 */
export function useOperation() {
  const [id, setId] = useState<string>()
  const live = useLiveOperation(id)
  const notification = useRef<string | number>(undefined)
  const background = useRef(false)

  function run<T>(
    start: (onStart: (op: Operation) => void) => Promise<T>,
    {
      title,
      done,
      then,
      notify = false,
    }: {
      title: string
      done: (result: T) => Done
      /** What follows unless the operation went on in the background, e.g. closing the dialog. */
      then?: (result: T) => void
      notify?: boolean
    },
  ) {
    setId(undefined)
    background.current = false
    notification.current = notify ? toast.loading(title) : undefined
    void start((op) => {
      // An action can run as several operations, e.g. in batches; once in the background, the dialog no longer shows them.
      if (!background.current) setId(op.id)
      if (notification.current !== undefined) toast.loading(<LiveToast id={op.id} title={title} />, { id: notification.current })
    }).then(
      (result) => {
        const { message, description, action, warning } = done(result)
        ;(warning ? toast.warning : toast.success)(message, { id: notification.current, description, action })
        if (!background.current) then?.(result)
      },
      // A dialog in the foreground shows the error itself.
      (e: Error) => {
        if (notification.current === undefined) return
        if (isCancelled(e)) toast.info(t("Cancelled"), { id: notification.current, description: title })
        else toast.error(e.message, { id: notification.current, description: title })
      },
    )
  }

  return {
    run,
    /** The operation as it goes on, once the master runs the action as one. */
    live,
    /** Lets the operation go on in a notification, e.g. as its dialog closes; op is its ID, as soon as it starts. */
    background: (title: string, op = id) => {
      if (!op) return
      background.current = true
      notification.current = toast.loading(<LiveToast id={op} title={title} />)
    },
    reset: () => setId(undefined),
  }
}
