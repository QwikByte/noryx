import { CopyIcon } from "@phosphor-icons/react"
import { useQuery, useQueryClient } from "@tanstack/react-query"
import { t } from "i18next"
import { useState } from "react"
import { toast } from "sonner"
import { guard } from "@/components/form-dialog"
import { Button } from "@/components/ui/button"
import { Dialog, DialogClose, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { FieldError } from "@/components/ui/field"
import { copyFile, filesQuery, join, type ServerFiles } from "./api"
import { PathPicker } from "./path-picker"

/** The name of a copy among the names of a folder, e.g. "config copy.yml", then "config copy 2.yml". */
function copyName(name: string, names: Set<string>) {
  const dot = /\.tar\.gz$/i.test(name) ? name.length - 7 : name.lastIndexOf(".")
  const [stem, ext] = dot > 0 ? [name.slice(0, dot), name.slice(dot)] : [name, ""]
  for (let number = 1; ; number++) {
    const copy = (number === 1 ? t("{{name}} copy", { name: stem }) : t("{{name}} copy {{number}}", { name: stem, number })) + ext
    if (!names.has(copy)) return copy
  }
}

/**
 * Copies files and folders of a folder into another one, or into the same one under new names. Those that fail stay
 * chosen, e.g. to pick another folder.
 */
export function CopyDialog({ files, dir, names, onClose }: { files: ServerFiles; dir: string; names: string[]; onClose: () => void }) {
  const queryClient = useQueryClient()
  const [folder, setFolder] = useState(dir)
  const [left, setLeft] = useState(names)
  const [errors, setErrors] = useState<{ message: string }[]>([])
  const [pending, setPending] = useState(false)
  const { data } = useQuery(filesQuery(files, folder))
  const intoItself = folder !== dir && left.some((name) => `${folder}/`.startsWith(`${join(dir, name)}/`))

  async function copy() {
    setPending(true)
    const taken = new Set(data?.files.map((f) => f.name))
    const failed: [string, Error][] = []
    for (const name of left) {
      const to = folder === dir ? copyName(name, taken) : name
      taken.add(to)
      await copyFile(files, join(dir, name), join(folder, to)).catch((e: Error) => failed.push([name, e]))
    }
    setPending(false)
    void queryClient.invalidateQueries({ queryKey: ["files", files.nodeId, files.serverId] })
    if (failed.length === 0) {
      toast.success(left.length === 1 ? t("Copied {{name}}", { name: left[0] }) : t("Copied {{count}} items", { count: left.length }))
      onClose()
      return
    }
    setLeft(failed.map(([name]) => name))
    setErrors(failed.map(([name, e]) => ({ message: `${name}: ${e.message}` })))
  }

  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent className="sm:max-w-xl" {...guard(pending)}>
        <DialogHeader>
          <DialogTitle>
            {left.length === 1 ? t("Copy {{name}}", { name: left[0] }) : t("Copy {{count}} items", { count: left.length })}
          </DialogTitle>
          <DialogDescription>{t("Choose the folder to copy to. Copies in the same folder get new names.")}</DialogDescription>
        </DialogHeader>
        <PathPicker server={files} folder={folder} onFolder={setFolder} />
        {intoItself && <p className="text-sm text-muted-foreground">{t("A folder can't be copied into itself.")}</p>}
        <FieldError errors={errors} />
        <DialogFooter>
          <DialogClose asChild>
            <Button variant="outline" disabled={pending}>
              {t("Cancel")}
            </Button>
          </DialogClose>
          <Button disabled={pending || !data || intoItself} onClick={copy}>
            <CopyIcon />
            {pending ? t("Copying…") : t("Copy here")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
