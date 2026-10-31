import { PlugsConnectedIcon } from "@phosphor-icons/react"
import { useQuery } from "@tanstack/react-query"
import { Link } from "@tanstack/react-router"
import { t } from "i18next"
import { Trans } from "react-i18next"
import { CopyField } from "@/components/copy-field"
import { Button } from "@/components/ui/button"
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover"
import { meQuery } from "@/features/auth/api"
import { type ServerFiles, sftpQuery } from "./api"
import { sftpCommand } from "./sftp"

/** Tells how to reach the files of the server over SFTP, while the master serves it. */
export function SftpButton({ files }: { files: ServerFiles }) {
  const { data } = useQuery(sftpQuery(files))
  const { data: user } = useQuery(meQuery)
  if (!data?.port || !user) return null
  return (
    <Popover>
      <PopoverTrigger asChild>
        <Button variant="outline" title={t("Connect with SFTP")}>
          <PlugsConnectedIcon />
          <span className="max-sm:sr-only">{t("SFTP")}</span>
        </Button>
      </PopoverTrigger>
      <PopoverContent align="end" collisionPadding={16} className="w-[min(28rem,calc(100vw-2rem))] gap-3 p-4">
        <div className="space-y-1">
          <p className="font-medium">{t("Connect with SFTP")}</p>
          <p className="text-muted-foreground">
            <Trans
              i18nKey="Editors, scripts and clients such as WinSCP reach the files of this server over SFTP. Sign in with your username and an <tokens>API token</tokens> as password, with the permissions of the file manager: to browse files, and to change them."
              components={{
                tokens: <Link to="/account" hash="tokens" className="font-medium text-foreground underline underline-offset-4" />,
              }}
            />
          </p>
        </div>
        <CopyField value={sftpCommand(user.username, location.hostname, data)} label={t("command")} prefix="$" />
        <div>
          <p className="text-xs text-muted-foreground">{t("Host key fingerprint, which your client asks you to compare the first time")}</p>
          <p className="mt-0.5 font-mono text-xs break-all">{data.hostKey}</p>
        </div>
      </PopoverContent>
    </Popover>
  )
}
