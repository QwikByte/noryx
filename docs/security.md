# Security model

How Noryx protects the master, the agents and the servers they run, and what a compromised part can and can't do.

## Own certificate authority

The master creates an Ed25519 certificate authority on first start. All master ↔ agent traffic is TLS 1.3 with mutual
authentication. Identities are names, not IPs (`master.noryx.internal`, `<node-id>.node.noryx.internal`), so nodes can
change their address without re-enrolling.

## Short-lived certificates

Master and node certificates are valid for 90 days and renewed automatically once a third of their lifetime is left. For
a node, the agent creates the new key and only sends a signing request; it installs the signed certificate after
checking it, without a restart. The panel can renew a node on demand. A node that stays offline until its certificate
expires has to be enrolled again with a new join token. So that the panel warns about such a node in time, also after
the master restarted, the master stores when each node's certificate expires: from the certificates it issues and those
a node presents when it connects, which the CA signed for that node, and only when it is later than the stored expiry.
A compromised node can't make its certificate seem to expire sooner or later than the latest one issued.

## Enrollment

Adding a node creates a single-use join token (valid for one hour unless the settings say otherwise, stored only as a
hash). It contains the master address, the node ID, the secret and the CA fingerprint. The agent creates its key pair
locally, sends a CSR and pins the CA fingerprint, so the exchange can't be intercepted. Private keys never leave the
node. The master checks the token before it signs the CSR, and a client gets 5 attempts, then one every 12 seconds.

## Agents only obey the master

The agent requires a client certificate with the master identity. Node certificates are server-only, so a compromised
node can't command other nodes. Locally, the agent is controlled through a Unix socket (mode `0600` inside a `0700` data
directory).

## Panel

Argon2id password hashes, session tokens stored as SHA-256 hashes, `__Host-` cookies (`HttpOnly`, `Secure`,
`SameSite=Strict`), cross-origin request protection, a strict Content Security Policy and self-hosted fonts. Sign-in
attempts are rate limited per client address (IPv6 per /64 network) and per username, changes that need the password per
user. A username has a larger budget than a client, so that a single client can't keep a user out. Client addresses come
from the `X-Forwarded-For` or `X-Real-IP` header only for the reverse proxies named with `--trusted-proxy`, also those
that sessions show. The panel names a session by a random ID of its own, never by its token or the token's hash, and
users only see and end their own sessions, which needs no password, as it only takes rights away. A session notes the
address, browser and time of its last use at most once a minute; the browser and operating system are only names from
fixed lists that the master recognises in the User-Agent, which are shown but never trusted. The CSV
files of servers, nodes and players, which the browser writes, protect spreadsheets from formulas in names, tags and ban
reasons as the export of the log does, and quote cells that hold the separator the user chose, comma or semicolon. Notes of servers are plain text, which the panel shows as text only.
So are the name of the panel and the notice of its sign-in page: of limited length, without control characters but the
line breaks of the notice, and public, as the sign-in page reads them without a session. The settings take no HTML, CSS
or images for them, so the Content Security Policy stays as strict. The defaults of users' language and look are public
too, and only values that users can choose themselves.

## Two-factor authentication

Codes of the app (RFC 6238) work only once, and from the fifth wrong code in a row on, codes aren't checked for a minute
that doubles with every further wrong one, up to a day; parallel guesses count too. Recovery codes have 50 random bits
and are stored as SHA-256 hashes. Those who may change the master's settings can require two-factor authentication for
all users or for groups; the change is logged. Until a user it applies to has set it up, the master refuses every route
but those of the user's own account (setting it up, signing out, the password, sessions, language and preferences) with
403, the terminal, streams and the permissions the panel loads included. Setting it up is never refused, so the
requirement locks nobody out, and `noryx-master user add` stays the way back for administrators who lost their app and
recovery codes. The secret of the app is stored in the master's database, which needs the same
protection as the CA key next to it. The panel must be served over HTTPS (its settings, a reverse proxy or
`--tls-cert`/`--tls-key`), otherwise browsers drop the secure session cookie (`localhost` is exempt). With a certificate
of Let's Encrypt or `--tls-cert`, the master tells browsers to use HTTPS only (HSTS, one year), but not with a
self-signed one, which would lock browsers out once it changes; behind a reverse proxy, set it there. The master may
listen at ports below 1024 (`CAP_NET_BIND_SERVICE`), e.g. 443 and 80, and has no other privileges.

## API tokens

Scripts authenticate with personal API tokens: `noryx_` and 128 random bits, a prefix that lets secret scanners
recognise them. A token only counts in the `Authorization: Bearer` header with this prefix, never in a URL or as a
cookie, so other credentials of a reverse proxy there leave the session alone. The master stores only its SHA-256 hash
and shows a token once, as it is created, which needs the password and, with two-factor authentication, a code; the
panel and the log name it by a random ID of its own and its name, never by the token or its hash. Each request gets the
permissions its user has at that moment, limited to those of the token and the ones they require, so a token never has
more than its user, and one with fewer never what only administrators may do. Tokens of disabled users stop working,
also if they are enabled again, and those of deleted users are deleted. Changing the password, setting one with a setup
link and turning on two-factor authentication revoke the user's tokens, as they end the other sessions: whoever knew the
old password may have created them. A requirement of two-factor authentication applies to the tokens of the users it
covers until they set it up. Tokens only work on the routes that state their permissions to `access.Mux`; the routes of
the user's own account (`/api/auth/` and `/api/preferences`) refuse them, so a token can't sign in to the panel, change
the password or two-factor authentication, end sessions or create and revoke tokens. A wrong token takes an attempt from
the client's budget of sign-ins, and a client without attempts left can't use tokens either. Pages of other sites can't
use a token in a browser: the master allows no cross-origin requests, so browsers don't send the header, and the
cross-origin request protection still refuses their changes; that of the session cookie stays as it is. Operations that
a token started are the token's, so that a token with fewer permissions can't cancel those of its user or of other
tokens without the permission they need. The log names the token besides the user in the entries of what it did, and a
token notes the address and time of its last use at most once a minute.

## Networks

Velocity's modern forwarding signs the forwarded player data with a random secret per network, which is stored in the
master's database and on the network's servers; the API never returns it, and the file manager hides it in every file
that holds it. Legacy forwarding (BungeeCord's) can be spoofed by anyone who reaches a server, so the servers of such a
network are only reachable by the proxy on its node, and those on other nodes need the operator's confirmation that a
firewall protects them. Servers that leave a network, also as it is deleted, run in online mode again without the
secret. Restoring a backup keeps what decides how a server takes part in a network as it is: a proxy's forwarding secret
and the files with secrets of Geyser and Floodgate, the forwarding settings, including the secret Velocity 1 kept in
its configuration, and a game server's online mode. The agent puts them into the extracted backup before it replaces
anything, so an old backup brings back neither the secret of a network the server left nor the trust in its proxy, nor
a former secret of its network; the master then configures the server's network again. A removed node may be
compromised, and its proxy's data holds the secret, so a node with servers of networks is
only removed once they left their networks: game servers of other nodes no longer trust its proxy, and proxies no
longer send players to its servers. The node itself isn't contacted, and if a server of another node can't be
configured, the node stays. Only deleting a network whose proxy's node doesn't answer skips the proxy and the servers
on that node, which then only trust each other, and says so. The forwarding mods come from Modrinth like other mods,
checked against their SHA-512 hashes. Proxies read console commands from their standard input, which only the agent
writes to through the runtime; no RCON plugin is added. Podman closes that input once anyone attached to it leaves, so
there a shell of the proxy's user in its container writes the command to the input of the proxy's process, taking it on
its own input rather than as an argument. The commands of the Maintenance plugin only take what the agent checked
itself: names of players, the name of a server that its configuration of the proxy has, and timers as whole minutes up
to 28 days, so nothing else becomes part of a command.

## Containers

Containers run with `no-new-privileges`, memory and PID limits, and only the capabilities the images need to hand the
data to the server's user: `CHOWN`, `SETUID` and `SETGID`. Proxies run as the user of their image (uid 1000) from the
start, which owns their data, without any capabilities. Servers of a node can't reach each other: they share a network
without communication between containers (`noryx-servers`), and a Velocity proxy shares another one only with its
backends on the node.

A node runs its containers with Docker or with Podman as root, and the same holds for both. The agent sets every option
of a container itself, so that Podman gets the same users, capabilities, limits, health checks and published addresses
as Docker, and as root it keeps the users of the containers, which own the data on the node, as Docker does. Podman
ignores the option of a network that keeps its containers apart, and keeps networks apart only while its API remembers
it: Podman 4.9.3 of Ubuntu 24.04, for one, forgets it once anyone inspects the network through Docker's API, as the agent
does. So on its nodes, the bridges of the agent's networks are named `noryx-…`, the one of `noryx-servers` after the
network, and the agent drops in nftables tables of its own whatever this bridge would forward from one container to
another (`bridge noryx`) and whatever the node would route from one of these bridges to another (`inet
noryx-networks`): when it starts, before each start of a server, and at boot with `noryx-isolate.service`, without
which Podman doesn't start the servers. An agent that can't set up the tables, e.g. as the kernel lacks
`nft_meta_bridge`, doesn't run. A network of the agent without such a bridge, e.g. of an older agent, is created again
once no container uses it, and no server or datastore starts in it until then. The agent checks every answer of the
runtime's socket and refuses one at which the other runtime answers, e.g. Podman behind Docker's socket, or a Podman
older than 4.9 or rootless, as it relies on how each one keeps servers apart; whether Podman runs rootless, which
Docker's API doesn't tell, it asks Podman's own API once for each version. With SELinux, e.g. on RHEL, Podman also
confines each container to the files labelled for it, and the agent has it label the mounts of each container for that
container alone (`Z`).

## Agent input

Every request is validated by the agent. Server files are confined to the data directory (`os.Root`), and game servers
are only created after the operator accepts the Minecraft EULA. JVM options may only contain characters that the image's
start script can't interpret as shell syntax, can't override the memory limit and can't run code: Java agents, class and
module paths, commands on errors, options read from files, class data archives, JVMCI compilers, debugging or JMX ports,
and the system properties of Java, JNDI, logging libraries and JNA, which name classes, libraries or configurations to
load (also from URLs), are refused. A server that got such an option before an update refused it keeps it until it is
removed: the agent logs a warning when it starts, and the panel shows it on the server's page and the overview.
Settings can't set other variables of the images, such as `CUSTOM_SERVER`, `PLUGINS` or `JVM_XX_OPTS`, which download or
run code and would get around these checks: besides the variables of checked settings, a container only gets its time
zone as `TZ`, once the agent found it among the IANA time zones it knows.

## File manager

The agent confines every path to the server's data directory, including through symbolic links, and new files belong to
the server's user. No path leads into the temporary files and folders of the agent (`.noryx-*`), which aren't listed
either, as they can hold secrets, e.g. a backup that is being restored or a file of a file set before it is hidden.
Downloads are sent as attachments with a sandboxing CSP, so an uploaded HTML file can't run scripts in the panel.
Secrets of the server stay on the node: files that only hold them can't be listed, read, written or moved, others show
them as `<hidden>`, no file or folder with secrets can be moved or copied where they would show, and archives leave
them out. Only moving a server to another node copies them. Copies never follow links, and one that a
file set filled with secrets while it was copied is removed again. Moving, copying or deleting several files and folders
at once checks each of them like a single one. The viewer of logs shows them as text and unpacks archived logs in the
browser only up to 16 MB, so that a small archive can't exhaust the browser's memory. A search shows files as the
editor does: without the files that only hold secrets and with secrets as `<hidden>`, also those a file set marks while
it searches, and it doesn't follow links or read the temporary files of the agent, which can hold the secrets of a file
being written.

Archives that are extracted, uploaded as backups or imported as servers are untrusted, unlike the backups the agent made
itself. The agent reads the list of such an archive before it writes anything and refuses it for links, hard links,
devices and other special files, absolute paths, `..` and control characters in names, encrypted entries, a path twice
or both as a file and a folder, more than 100,000 entries, more than 64 GB of files, and files that would be more than
100 times the size of the archive (zip bombs; up to 64 MB are exempt). It reads at most 64 MB of the directory of a ZIP
archive, which it keeps in memory, refuses one whose end announces more entries or a larger directory before it reads
any, as Go's reader of ZIP archives reserves memory for them first, and decompresses a `.tar.gz` archive at most as far
as these limits allow, twice: to check it, then to extract it, when each entry must match the list. The files must fit
with 1 GB to spare. Every entry is written below its folder in the server's data (`os.Root`), as the server's user, and
neither through a link nor in place of something that isn't a file, so a link of the server can't lead it elsewhere.
Of the permissions in the archive, a file only keeps that its owner may run it, which the server's user could allow
itself, and never special ones such as setuid. The file manager extracts nothing into a hidden path, the manifest of
file sets, the temporary files of the agent or the files Noryx writes itself, such as `server.properties`, `ops.json`
and `forwarding.secret`, and refuses the whole archive if it would.
Extracting and copying need the permission to change files, searching the one to read them.

## Plugins and downloads

The master downloads only from Modrinth's CDN, up to 256 MB, and only uses a file whose SHA-512 hash matches the one
Modrinth's API lists; from Hangar, only from its CDN and with the SHA-256 hash its API lists, and versions that only
link elsewhere can't be installed. Floodgate only comes from GeyserMC's download server, with the SHA-256 hash its API
lists. A modpack is checked the same way, and each of its files against the SHA-512 hash in the pack; packs with files
elsewhere than on Modrinth's CDN or with paths that leave the server's folder are refused before a server is created or
moves to another version of its pack, and the agent confines the files like those of the file manager. To tell which
files of a pack changed on a server, the agent hashes them only if they are regular files without secrets, not through
links, so a hash tells nothing about a secret, and an update never replaces or removes such files. A compromised server
can only make its own files look changed or unchanged. Updating a pack needs the permissions to change the server's
settings and to manage its plugins and mods, as it does both. The version of a mod loader ends up in a variable of
the server image, so the agent only accepts letters, digits, `.`, `_`, `+` and `-`. The agent decides the folder from
the server type and only accepts plain `.jar` file names in it. Turned-off plugins stay in the server's data, in the
folder `.disabled` of the plugin folder, which backups of the plugins include; the agent confines it like the plugin
folder, never replaces a file when it moves one, and refuses a link in its place. To link a plugin to the folder of its
settings, the agent reads the name from the plugin's jar, which the server could have written: at most 4 MiB of the jar
and 64 KiB of its `plugin.yml` or the like, and only a name of letters, digits, spaces, `_`, `.` and `-` that doesn't
start with a dot and is a folder of the plugin folder. What servers have installed only lists the servers a user may
see. Installing, updating or removing plugins on many servers needs the permission to manage the plugins of each:
installing refuses servers without it, updating and removing leave them out and tell so, and restarting servers
afterwards needs the permission to restart each. Changelogs are Markdown of the projects' authors, which the panel
renders without HTML and without loading images. Project icons are fetched by the master, so the browser never contacts
Modrinth or Hangar and the Content Security Policy stays unchanged. To whitelist a Bedrock player, the
master sends their gamertag to GeyserMC's global API, and the agent only accepts the IDs Floodgate gives Bedrock
players.

## File sets

Applying a set needs the permission to change the files of every server it touches, besides the one to manage file sets,
as a set can configure plugins that run code, e.g. scripts; whoever may only change the tags of a server can make it a
target, but never applies anything, and the preview names the servers that would get secrets for the first time. Secrets
are stored in the master's database like the forwarding secret, as a key next to them would be in the same backups as
the CA key, which already controls all agents. The master sends their values only to the agents, in a field of their own
that, like the files, is never logged; the agent fills them in and hides the resulting files like the RCON password: the
file manager can't list, read, write or move them, and downloads of folders and backups leave them out. Each server
records in `noryx-filesets.json` which set wrote which file, with its SHA-256 hash, and which files held secrets; these
stay hidden for the life of the server. The manifest is hidden too, can't be deleted or replaced from the file manager,
and is left out of backups and restores, so that restoring an older backup can't bring back a file with secrets without
hiding it; restoring keeps these files as they are, so that it brings back no secret of a set the server left. As a set
marks a file before it writes it, the agent checks the marks once a file is open, also while it archives a folder, and
copies of a server lose every file that the original marked. Master and agent both refuse the files that Noryx writes
itself or that hold secrets of the server, so a set can't change forwarding or RCON settings, and the agent writes no
file through a link or into a file where a folder should be. A compromised server can change its own manifest, which
only changes its own state or the hiding of secrets it can read anyway.

Values of the variables of a set are a single line without quotes, backslashes and braces, so that they can't add lines
to a file, end a quoted text or make up a placeholder, also one of a secret. Binary files are written as they are, and
besides `.jar`, `.zip` and `.class` files, master and agent refuse archives, Java classes and Linux programs by their
first bytes, so that a set can't add code under another name. The passwords of databases reach servers only through
their placeholders, which the master fills in for the databases of a server's own network only. Saving a set that adds a
password or changes a file with one, and applying a set that puts passwords on servers, need the permission to manage
datastores, and the log records which passwords a set uses, so that nobody who may not see a password gets it onto a
server they control and reads it there. The master sends them in the field of secrets, and the agent only accepts
passwords of the form the master generates and hides the files like those with secrets. Agents tell the master that they
write binary files and fill in passwords; older ones, which would write binary files empty, get neither.

## Templates

An imported template file is untrusted: the master reads at most 1 MiB, refuses other formats and unknown versions of
its format as well as unknown fields, and then saves it exactly like a template from the panel. So neither can set the
properties Noryx sets itself (port, address, RCON) or secret ones, plugins and kept versions are looked up again on
Modrinth and Hangar rather than taken from the file, and the agent checks the settings again when a server is created
from it. Exported files hold no IDs of the master, nodes or servers and no secrets, as templates have none. Creating a
server with the tags of a template needs the permission to change the settings of the node's servers, like tags of an
existing server; the tags make it a target of their file sets, which still have to be applied, so a template puts no
secrets on a server by itself. A version a template keeps is installed only if it runs on the new server, from the same
sources and with the same hash checks as any other; otherwise its plugin is left out rather than replaced by another
version.

## Databases

A datastore runs as the image's user without capabilities, with `no-new-privileges`, memory, CPU and PID limits and a
health check. The agent generates the superuser's password, which the image reads from a file that only it may read,
never from the environment or labels, and which never leaves the node; MariaDB's root may only sign in on the container
itself. Every database has its own user with rights on it alone, and on PostgreSQL nobody else may connect to it or to
the superuser's database. Only the network's servers reach a datastore: on its node over an internal network
without internet, which they share with each other as backends in a proxy's network already do; from other nodes only
over the private network of the nodes, for the nodes of its servers, never at a public address. The agent checks every
name against `^[a-z][a-z0-9_]{0,31}$`, refuses those of the engines themselves, and only takes passwords from `a-z2-7`,
so no statement or configuration file needs escaping; statements go to the clients in the container on their standard
input. Dumps are loaded as their database's own user, never as the superuser, so a dump can't gain more rights; as
MariaDB has no way to sign in as a user without its password, the user gets a random one for the time of the load. The
clients' own commands are off while they load a dump, also one uploaded from elsewhere: MariaDB's client runs in binary
mode, which only allows `DELIMITER`, `\C` and the sandbox, in its sandbox and without sending files for `LOAD DATA
LOCAL`, and psql in its restricted mode with a random key, which refuses `\!`, `\connect`, `\copy`, `\i`, `\o` and all
others. The `\restrict` and `\unrestrict` with which pg_dump wraps a dump are left out only at its start and end, as its
author knows their key; any other one fails the load. So a dump can't run programs in the container, read its files,
e.g. the superuser's password, or connect to another database or as another user. An uploaded dump is checked before it
is kept: a single SQL file goes into a database the master knows, and a ZIP archive may only hold a `<database>.sql`
with a valid name for each database besides folders and macOS's `__MACOSX`, up to 1000 entries, 50 databases and SQL
files of 64 GB and 100 times its size together, all read to their ends to check their checksums; the agent keeps only
these SQL files, and uploads have at most 16 GB and stop before they leave less than 1 GB free, also those whose size
the master doesn't know in advance. The agent never stores the passwords of users: upgrades keep the hashes, which it
checks before it uses them in a statement. Dumps are kept like backups and checked before a restore drops anything, so
a damaged one changes nothing. Browsing never reads as the superuser, as reading runs what the database's user may have
written, e.g. casts to text, functions and views: on PostgreSQL it reads as the database's own user, and on MariaDB,
which signs in users only with their passwords, as a user that may only read the database, signs in only on the
container with a random password and exists only while the agent reads. It reads in a session that only reads and stops
each statement after 10 seconds, with statements the agent builds itself from names that need no escaping: tables,
schemas and columns whose names don't match `^[A-Za-z0-9_$-]{1,64}$` aren't shown, the columns to sort and filter by
must be the table's, the value of a filter, at most 1 KiB of UTF-8 without NUL, goes into the statement in hexadecimal,
MariaDB's client runs in its sandbox, and a page has at most 3 MiB. To count the connections of a datastore, the agent
runs a query of its own as the superuser, and the master records the usage only of the datastores it has on the node
that reports it. The log
of a datastore's container, which only those who may manage datastores see, shows the statements that the engines log,
e.g. when one fails, so the agent hides every quoted value after `PASSWORD`, `PASSWORD(`, `IDENTIFIED BY` and `USING`,
the hashes of passwords too, before it sends a line: also with escaped quotes, and to the end of the line where it can't
tell where a value ends. It drops control characters, so that a line can't control the terminal of the local CLI, and
the master relays the log without logging it. The passwords are stored in the master's database like the forwarding
secret, and are never logged. The API returns them
only to those who may manage datastores, one at a time on request, without caching, and logs who asked; they can
download all data in dumps anyway, and only they may put them into [file sets](#file-sets) and on servers.
A compromised master knows them, as it knows the forwarding secret, and could restore
or delete data, but can't learn the superuser's password or place data outside the allowed storage locations.

## Duplicates

Copying never follows symbolic links, so a copy can't pull in files from outside the server's directory. A copied
Velocity proxy loses its forwarding secret, a copied BungeeCord proxy stops forwarding and a copied game server stops
trusting the proxy, so a copy can't impersonate a server of a network. Copied proxies also lose Floodgate's key, with
which Geyser vouches for Bedrock players, and copied game servers demand signed chat again. A copied Fabric or Quilt
server keeps FabricProxy-Lite, which turns players away until it is removed in the **Mods** tab. A copy of a game server
of a network only joins the network, and gets its forwarding secret, if the user who copies it may also manage networks;
it stays on the node of the original, so it is as exposed as the original is.

## Backups

The agent keeps backups outside of the servers' folders, accessible to itself only (mode `0700`), so a compromised
server can't read or tamper with them. The master can only choose among the storage locations the node's administrator
allowed, and backup IDs and paths are validated by the agent, also the paths a backup leaves out and those a restore
chooses, which must be in the backup. Restoring confines every entry to the server's folder, and backups never contain
symbolic links. Downloads are attachments like those of the file manager and hide the secrets the same way; the backups
on the node keep them, so restoring works. Restoring keeps the files that only hold secrets, such as the server's
console password, and the secrets of file sets and networks as they are, see [File sets](#file-sets) and
[Networks](#networks); the manifest of file sets is never backed up or restored. Restoring into another server is a
copy like a duplicate: the master relays the backup between the agents with the original's secrets hidden, as in a
download, so the other server gets none of them, nor the original's files with secrets of file sets; the agent puts the
other server's own secrets wherever the backup says `<hidden>`, and keeps its network's secret and forwarding settings.
It needs the permission to see the backups of the original and to restore backups of the other server, and only goes
between game servers or between proxies. Like moving a server, it brings whatever the backup holds, e.g. plugins, which
a compromised node could have changed like the data of its servers. Marking a backup to keep needs the permission to
back up the server, and letting its job delete it again the one to delete backups. The master refuses restoring chosen
paths, into another server or with a backup first on nodes whose agent would ignore that, e.g. restore all of a backup
instead.

An uploaded backup is untrusted. The agent receives it outside the server's data, keeps it only once it checked it like
an archive of the file manager (see [File manager](#file-manager)), and marks it as untrusted in its details, which only
the agent writes. Restoring an untrusted backup checks it again, with limits for backups (up to 1,000,000 entries and
1 TB), and extracts it confined like an archive of the file manager, without the files that only hold secrets, the
manifest of file sets and the files of the agent, whose versions on the server stay as they are, e.g. the pardons that
wait for the end of temporary bans; it replaces every secret in the other files with the server's own, not only
placeholders, and keeps the server's network settings like any restore. A backup from another node, moved
with its server, copied there or a copy restored into a server, is untrusted too: the agent keeps it once it checked it
like an uploaded one, with the limits for backups, and refuses it once it is larger than the master announced or less
than 1 GB would stay free, also when the data of a moving server arrive. Listing, downloading and restoring an
untrusted backup read the directory of its archive within the limits for backups too. The mark moves with the backup to
another node, and copies for restoring into another server keep it; the master refuses both with agents that would
drop it. Uploading needs the permissions to back up and to restore the server, and to change its files: an uploaded
backup can bring any file, e.g. a plugin, which runs with the server and can read its secrets once it is restored, such
as the forwarding secret of its network, like a plugin uploaded with the file manager.

## Copies of backups

Copies of backups leave the server's node, so the agent of the node makes them as downloads, with the secrets of the
server hidden: files that only hold secrets, such as the console password, the forwarding secret of the network,
Floodgate's key and the files with secrets of file sets, are left out, and secrets in other files, e.g. in
`server.properties` or the forwarding settings, are `<hidden>`. Restoring a copy works like restoring a backup into
another server: the agent puts the server's own secrets wherever the copy says `<hidden>`, and keeps the server's
secrets, those of its network and its forwarding settings, see [Backups](#backups). The rest of the server's data is in
the copies as in its backups: worlds, plugins with their data and configuration, which can hold passwords that weren't
set through file sets.

The master doesn't encrypt copies, so whoever runs an S3-compatible storage can read them. It asks the storage to
encrypt them with its own keys (SSE-S3) unless that is turned off, which only protects them from those who get at its
disks, not from the provider: choose a storage that you trust with the data of the servers. The master only connects to
the configured endpoint, over HTTPS with a certificate the system trusts, follows no redirects, signs every request with
AWS Signature Version 4 including the hash of its body, and accepts the endpoint, region, bucket and folder only in
strict forms. The secret key of a storage is stored in the master's database like the forwarding secret, is part of
`noryx-master backup`, and is never returned by the API or logged; endpoint, bucket and access key show to those who may
see backup jobs. A compromised storage can only hand out archives of its own, which are restored like those of a
compromised node.

Another node keeps copies apart from the backups of its own servers, out of reach of containers and the file manager,
and never restores them itself: the master relays them, as agents never connect to each other. Its administrator can
read them there, like the backups of the servers on their own node. Agents mark every backup that came from elsewhere, a
copy or one of another node, as untrusted, and restore it like an uploaded backup (see [Backups](#backups)): checked
again with the limits for backups, extracted confined like an archive of the file manager, and with every secret
replaced by the server's own; the master refuses to restore a copy with an agent that would trust it. So a compromised
node or storage can change the data of the servers restored from its
copies, like a node can change the backups of its own servers, but nothing beyond them. A copy belongs to its server on
the node it was copied from, and follows the server when it moves: nodes tell the IDs of their servers themselves, so a
compromised node that claims the ID of another node's server only gets copies of its own, and a job neither replaces
nor deletes that server's copies for it.

Copies take the backups of all servers away from their nodes, so adding, changing and deleting storages, and saving a
job that copies, need the permissions to manage backup jobs and to see and download the backups of all servers; each run
checks that the user who saved the job last still has them. Seeing and restoring the copies of a server needs the
permission to see its backups, on the node it is on now; those of servers that are gone, whose scope is unknown, need it
on all servers. Restoring needs the permission to restore backups of the server restored into, which must be of the
same kind, and deleting a copy the permission to delete backups.

## Permissions

Every API route states the permission it needs when it is registered, so none can be added without; the terminal checks
each command the same way and refuses commands without a check. Permissions are loaded for every request, so changes,
disabling and deleting apply right away; disabled users are signed out. Streams that follow output, the console, the
log of a datastore and terminal commands such as `server logs`, end every 5 minutes, so the panel checks the session and
the permissions again; the console and the log connect again on their own and continue. Users can only grant
permissions they have themselves, within their own scope, and only manage users who have no more permissions than they
do, so no one can raise their own permissions. The groups that the settings preselect for invitations are only shown
checked, of those the inviter may give: the invitation checks the groups it gets like any other.
The last enabled administrator can't be disabled, deleted or removed from the Administrators. The master logs every
change with the user who made it, also denied attempts. An operation in progress can only be cancelled by the user who
started it, in the panel or with the same API token, or by one who has the permissions it needed on what it is about,
and only while it is at steps that stop
safely: restoring a backup, moving a server, stopping or restarting one once it no longer warns its players, and
restarting servers one after the other always finish.
A warning before a stop or restart by hand is a console command, `say` or `title`, so a message of one's own needs the
permission to send console commands on each server; others get the text of the settings and can't put text of their own
in the chat. Changing these texts needs the permissions to change the master's settings and to send console commands to
all servers, as they go to the console of every server that is warned, also by schedules and workflows; the master
checks them like a message of one's own, a single line of up to 200 characters without control characters, and makes a
title or the text above the hotbar JSON with an encoder, never by hand.
Restarting servers of a network one after the other, also a single one, needs the permission to restart the proxy, which
sends their players elsewhere, and each server that restarts; sending the players of a server elsewhere needs the
permission to manage the players of the proxy, like sending one player. Schedules, which may restart any server, also
server by server, need the permission to manage schedules everywhere, checked when they are saved; backup jobs likewise
need the permission to manage backup jobs, which applies to all servers. Both run as the master, not as the user who
saved them, and their targets of tags and networks resolve to the servers these have at each run, which needs no rights
beyond those that apply everywhere anyway. There is one side effect: whoever may change the settings of a server, which
include its tags, can put it under a backup job or schedule by giving it a tag that one targets, or take it out of one
by removing the tag; the panel says so where tags are edited. Runs that a user started by hand keep the user's name in
the history of the job or schedule, which those see who may see it.
Schedules that back up servers first, update their images or their plugins and mods need the permissions to back up
servers, change their settings or manage their plugins and mods too, as these would need by hand, and on all servers,
like the permission to manage schedules: their targets of nodes, tags and networks get new servers at any time. The
user who saves such a schedule or runs it right away needs them, checked with the permissions of the request, so an API
token with fewer permissions can't save one. The master records who saved a schedule last, and each run checks that
this user, unless deleted or disabled, still has them, with the user's current permissions; otherwise the run fails
before it acts. This is the same check as on saving, repeated with the permissions of the moment, rather than one per
server, which wouldn't allow more: no permission on some servers can let a schedule back up or update all of them.

## Terminal

The panel's terminal is not a shell. The agent's commands are the same code as its local CLI, but run in the master and
reach the agent through the existing mutually authenticated connection, so the agent offers nothing new to the master.
Commands that only the node's administrator may run (`storage`, `enroll`, `overlay allow`, `deny` and `up`) don't exist
there. Command lines are split like a shell would, but nothing is expanded or executed by one, and the master logs every
command with the user who ran it. Completion offers only the commands that the user's permissions allow on some server
or node of the target, and only the servers, nodes, datastores and backups the panel shows the user anyway; each command
still checks its permission with its arguments when it runs.

## Log

An agent can only add entries about its own node and its servers, at a limited rate of entries and of bytes, and entries
are cut to a maximum size. Besides its retention, the log is kept under a size limit (2 GiB by default) that the master
checks every minute, so that agents can only exceed it by what they add in a minute, a few MiB each. SQLite reuses the
space of deleted entries, so the log takes at most about twice its limit on disk, however long the retention. So a
compromised agent can't fill the database, also over weeks, or write entries about other nodes. Request fields that may hold
secrets, such as the forwarding secret of a network or the URL of a webhook, are never logged. Exports protect
spreadsheets from formulas in entries, and log files are only readable by their owner. A live stream ends every 5
minutes and the browser connects again, which checks the session and the permissions again. Behind a reverse proxy, the
logged IP address is that of the proxy, unless `--trusted-proxy` names it.

## Notifications

Managing notifications needs the permission to see the log for all servers besides its own, as rules send entries about
every node and server, and their channels take them out of the master. The URLs of webhooks and the passwords of mail
servers are stored in the master's database like the forwarding secret; the API never returns them, the log never names
them, also not in errors, and an empty field keeps them, so that nobody who may manage notifications learns them. A mail
channel keeps its password only while its server, port and user stay the same, so that it can't be sent to another
server. Messages carry the texts of entries, never secrets, which aren't logged; entries can hold IP addresses and the
names of players, which the panel says where channels are set up. A rule of a server that is deleted, or of a node that
is removed, is deleted with it rather than left to send the entries of its node or of all nodes.

Channels connect to public addresses only, so that the master can't be used to reach its own network: the address of
every connection is checked right before it is made, after DNS answered, so that a name can't resolve to another address
later. Refused are loopback, private (which include the private network of the nodes, as it must be one), link-local,
shared (carrier-grade NAT), multicast, unspecified, reserved, documentation and benchmarking addresses, IPv6 unique
local addresses, 6to4 and Teredo, and IPv4-mapped and NAT64 forms of refused IPv4 addresses. Webhooks need HTTPS with a
certificate that the system trusts, redirects aren't followed and proxies of the environment aren't used, as they would
connect elsewhere; mails go over TLS from the start or after STARTTLS, and the master refuses to sign in or send without
it. Every request and mail has a timeout, and only the status of an answer is shown, not its body. Discord and Slack get
the texts of entries, which agents and servers write, as text that can't mention anyone or be formatted: escaped for
Discord, which also gets no mentions allowed, and as Slack's plain text with `&`, `<` and `>` escaped, like the name of
the panel in its notifications. The subjects of mails, and the name of the panel in them and as their sender, are a
single line without control characters and encoded, so that neither an entry nor the name can add headers. A compromised agent
can only add warnings about its own node and servers, at the limited rate of the log, and each channel sends 5 messages
at once, then one a minute at most, which counts what it leaves out. **Send test** works three times at once, then once
every 20 seconds per channel.

## Workflows

Workflows act like the user who saved them last, never with more: each step needs the permission its action needs by
hand on all servers, an entry of the log as a trigger the permission to see the log, servers and measures the one to see
servers, and notifications and HTTP requests, which take data out of the master, the one to manage notifications. A step
that runs another workflow needs what that one needs, as the other one acts on its behalf. Saving a workflow checks
these with the permissions of the request, so an API token with fewer permissions can't save one, and so does running
one by hand; each run checks again that the user who saved it last, unless deleted or disabled, still has them, as does
each step that runs another workflow. Whoever may see workflows sees their runs with the data of their triggers and what
their steps told, e.g. entries of the log about any server, the names of players and the answers of requests.

A workflow can't keep itself running: entries of the log that it wrote don't start it, triggers start it up to 30 times
at once and then once every 10 seconds, a run takes at most 72 hours and 10,000 steps, loops at most 1,000 items or
times, and workflows run each other up to 5 deep. Templates only read data, never run code; a regular expression of a
condition runs in linear time. Text that a template puts into a console command, a message or a header is a single line
without control characters, so data can't add commands or headers, and the steps for players check the names they get,
so data can't name others there, such as `@a`. In a console command, data can still be any argument that its template
takes, so a workflow should check what a webhook sends before using it there, e.g. with a condition.

When servers, nodes, networks, notification channels or workflows are deleted, workflows only lose what referred to
them: a trigger whose servers are all gone is removed rather than left without targets, which would watch all servers,
and a step without servers or without its network, channel or workflow is turned off, so that a deletion never lets a
workflow act on more than before.

HTTP requests of workflows go through the same connections as [notifications](#notifications): HTTPS to public addresses
only, checked right before each connection, without redirects or proxies, with a timeout, and answers up to 64 KB. The
values of secret headers are stored like the URLs of webhooks: the API never returns them, runs never record them and
they aren't templates. A secret header keeps its value only while its step sends it to the same URL, so that nobody can
send it elsewhere without knowing it.

The URL of a webhook holds a random token of 260 bits, of which the master keeps only the SHA-256, so that its database
and backups don't hold it; the panel shows the token once. Calls need no session, only the token, so the protection of
the panel against cross-site requests doesn't apply to them; they start nothing else. A call starts only an active
workflow with a webhook trigger, with up to 64 KB, which the run treats as data. Clients that call with unknown tokens
are slowed down: after 10 such calls, to one a minute. Reverse proxies may log the paths of requests, and so the token;
a new URL makes the old one useless.

## Usage

Agents report what their node and its servers use, so a compromised agent can make up or hide the usage and the warnings
of its own node and servers, but of no others. The master records at most 500 servers of a node, with valid IDs only,
and checks at most 32 storage locations with valid names, so that an agent can't fill its database or memory. Changing
the thresholds of a server needs the permission to change its settings, of a node the permission to change the node,
and warnings only show to those who may see the server or node.

## Players

The master notes where players played from the names of the players online that the agents measure: the name, the
server, the day and the minutes, never IP addresses or anything else about them, kept as long as the log and deleted
with their server or node. A compromised agent can make up players of its own game servers, but the master records only
names of players (16 letters, digits and `_`, or Floodgate's dot before), at most 1000 of a server per measurement and
10,000 players and servers of a node per day, so that it can't fill the database. Users only see where players played
on the servers they may see.

The faces of players send their names to Mojang, and those of Bedrock players to GeyserMC; the master asks for them,
only over HTTPS and from fixed hosts, and fetches skins only from Minecraft's textures server by the ID of a texture,
not by an address that a profile names. As names come from users and from agents, the master keeps up to 5000 faces,
looks up at most 30 at once and then one every 2 seconds, and decodes only skins of up to 256 KiB that are images of 64
by 64 or 64 by 32 pixels; it serves the face it drew itself as an image with a sandboxing CSP, so the browser never
contacts them. Messages to players need the permission to send console commands on each server, like the network's
message; their text becomes JSON with an encoder, never by hand, so it can't add components or commands. A temporary ban
ends with a pardon that waits in `noryx-pending-players.json` in the server's data like the changes for stopped servers:
a compromised server can change its own waiting pardons, which only affects itself, as it could pardon anyone anyway.
The names of the players who joined come from the server's cache of players (`usercache.json`), which the server writes,
so the agent reads at most 8 MiB and 10,000 entries of it, like the lists, and only names of players.

## Moving servers

Agents never connect to each other: the master relays the server's archive and backups between them over its mutually
authenticated connections. The new node checks the settings like those of a new server and extracts the archive confined
to the server's data directory, without symbolic links. Moving needs the permissions to delete the server and read its
files, and to create servers on the new node.

A server created from an archive of a server from elsewhere gets the archive checked like one of the file manager,
before anything of it is used: links, paths outside the data and too much data delete the new server again. It leaves
out the files that only hold secrets, the manifest of file sets and the files of the agent, empties the secrets in the
other files, and removes the forwarding settings, so that the server trusts no proxy and runs in online mode, until a
network configures it. It needs the permission to create servers on the node, which is enough to bring any plugin with
the archive: such a plugin runs with the server and can read the secrets it gets later, e.g. the forwarding secret of a
network it joins, or a file set's.

## Console

To run commands on a game server, e.g. to ask it for its ticks per second, the agent reads the console password from the
server's `server.properties` and connects to the server's console port inside the runtime's network; the password never
leaves the node. A server that doesn't answer its console, e.g. a compromised one, holds up only its own commands: the
agent applies the waiting changes of players to each server on its own, and gives each change 15 seconds before it waits
for the next try.

Servers and their plugins write the output, so the panel shows it only as text: the agent turns its colours into
Minecraft's colour codes, which the panel only maps to class names of a fixed set, never to markup. The plain output,
which the CLI and the terminal show, has no control characters but line breaks and tabs, so that a server can't control
the terminal it is shown in.

## Private network

Only nodes whose administrator ran `noryx-agent overlay allow` join, so the master can't open a UDP port and an
interface on a host that didn't agree. Each node creates its WireGuard key itself
(`/var/lib/noryx-agent/overlay/private.key`, mode `0600`); only the public key reaches the master, over the node's
mutually authenticated connection, and the master's backup holds public keys only. The agent checks what the master
configures: the range must be a private IPv4 range (RFC 1918) that overlaps none of the node's addresses and routes,
e.g. a provider's private network or a network of Docker or Podman, and each peer gets exactly one address in it, so a
master can't pull other traffic of the node into the tunnel. An nftables table of its own (`inet noryx`), which works
next to the iptables and nftables rules of Docker and Podman, drops packets for the node's address that don't arrive
through the tunnel, new connections from the tunnel to the node itself, e.g. to SSH or the agent, and forwarded
connections from the tunnel except those of a proxy's node to the ports of its servers. WireGuard accepts from a peer
only its own address and doesn't answer unauthenticated packets. The agent binds each address it lets reach a port to
the public key the peer at that address had then, which the master sends along, and drops the address once a peer has it
with another key: a node that gets the address of a removed one, even while a member was offline and never saw the
removal, reaches none of the ports published for the removed one. A compromised node reaches only the ports of its own
servers on other nodes; removing it removes it everywhere. A rotated key is let in again as the master applies the
node's networks again; a key that changes otherwise, e.g. as a node lost its data, needs its networks applied again in
the panel. A compromised master could add a peer of its own, but it can already reconfigure every server. If the
interface is missing, e.g. after a failed boot, the ports of these servers are reachable nowhere.

A test of the connections to a peer can't be turned into a scanner of other hosts. The agent connects only to the
address that its own configuration gives the peer with the key the master names, which lies in the private range
checked above, and only if WireGuard sends that address to that peer; the socket is bound to `noryx0`, so no connection
leaves the node another way, e.g. into its local network, to the internet or to its own services. It connects only to
the ports that the master's last configuration says the peer publishes for this node, which the master takes from that
peer's own report, so a compromised node can only make others test its own address. A test connects to at most 32
ports, each within 3 seconds, and the agent opens 64 test connections at once and then 4 a second; it sends nothing and
closes each connection at once. What remains: a compromised master can claim any ports for a peer and so probe the TCP
ports of the peers' addresses in the network at that rate, but each peer's firewall lets this node reach only the ports
published for it, and such a master can already run code in the servers, which reach these addresses anyway. Only those
who may manage the private network test, and only towards nodes they may see. The agent finds its firewall rules by the
comments it writes with them, as the nftables package can't read rules with these conntrack matches; only root on the
node could change rules and keep their comments. A node's page names published ports with only the servers, datastores
and nodes that the user may see, and `overlay status` in the panel's terminal needs the permissions to see the node, all
its servers and the datastores.

## Storage locations

Only the node's administrator decides where server data may be stored (`noryx-agent storage add`). The panel can only
choose among these locations, so a compromised master can't mount other host directories into containers.

## Installation

The installer only runs once it is downloaded completely, downloads only over HTTPS from the releases and installs a
package only if its SHA-256 checksum matches the release's `checksums.txt`, and only if the release key that is part of
the installer verifies its signature. The private key is a secret of the repository that only the job signing the
release gets, so someone who can merely change releases, e.g. with a stolen token, can't make installations accept other
files. Release files also carry build provenance attestations, signed through GitHub, that tie them to the release
workflow and the tagged commit. The master runs as its own system user with a hardened systemd unit, and refuses a data
directory of another user, so a command run as root can't leave files there that lock the master out.

## Updates

The master can't install anything itself. To update, it only creates a file that makes systemd start
`noryx-master-update`, which runs as root but takes no input from the master: it installs the latest release with the
installer of the installed package, checking the signature and the checksums. Agents only install releases newer than
themselves, so even a compromised master can't downgrade a node to a vulnerable version, and the version must have the
form of a release. `systemctl mask noryx-master-update.path` forbids updates from the panel.
