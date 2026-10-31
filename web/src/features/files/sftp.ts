import type { SftpAccess } from "./api"

/** Quotes a word for a POSIX shell unless it needs none. */
const quote = (word: string) => (/^[\w@%+=:,./-]+$/.test(word) ? word : `'${word.replaceAll("'", `'\\''`)}'`)

/** The command that opens the folder of a server with OpenSSH's sftp, as user at host. */
export const sftpCommand = (user: string, host: string, { port, path }: SftpAccess) => `sftp -P ${port} ${quote(`${user}@${host}:${path}/`)}`
