import { FolderOpenIcon } from "@phosphor-icons/react"
import { t } from "i18next"
import { useState } from "react"
import { toast } from "sonner"
import { guard } from "@/components/form-dialog"
import { Button } from "@/components/ui/button"
import { Dialog, DialogClose, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { FieldError } from "@/components/ui/field"
import { join, type ServerFiles, useChangeEach } from "./api"
import { PathPicker } from "./path-picker"

/**
 * Moves files and folders of a folder into another one. Those that fail stay chosen, e.g. to
 * pick another folder.
 */
export function MoveDialog({ files, dir, names, onClose }: { files: ServerFiles; dir: string; names: string[]; onClose: () => void }) {
  const change = useChangeEach(files)
  const [folder, setFolder] = useState(dir)
  const [left, setLeft] = useState(names)
  const [errors, setErrors] = useState<{ message: string }[]>([])
  const intoItself = left.some((name) => `${folder}/`.startsWith(`${join(dir, name)}/`))

  async function move() {
    const failed = await change.mutateAsync(left.map((name) => ({ action: "move", from: join(dir, name), to: join(folder, name) })))
    if (failed.size === 0) {
      toast.success(left.length === 1 ? t("Moved {{name}}", { name: left[0] }) : t("Moved {{count}} items", { count: left.length }))
      onClose()
      return
    }
    setLeft(left.filter((_, i) => failed.has(i)))
    setErrors([...failed].map(([i, e]) => ({ message: `${left[i]}: ${e.message}` })))
  }

  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent className="sm:max-w-xl" {...guard(change.isPending)}>
        <DialogHeader>
          <DialogTitle>
            {left.length === 1 ? t("Move {{name}}", { name: left[0] }) : t("Move {{count}} items", { count: left.length })}
          </DialogTitle>
          <DialogDescription>{t("Choose the folder to move to.")}</DialogDescription>
        </DialogHeader>
        <PathPicker server={files} folder={folder} onFolder={setFolder} />
        {intoItself && <p className="text-sm text-muted-foreground">{t("A folder can't be moved into itself.")}</p>}
        <FieldError errors={errors} />
        <DialogFooter>
          <DialogClose asChild>
            <Button variant="outline" disabled={change.isPending}>
              {t("Cancel")}
            </Button>
          </DialogClose>
          <Button disabled={change.isPending || folder === dir || intoItself} onClick={move}>
            <FolderOpenIcon />
            {change.isPending ? t("Moving…") : t("Move here")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
