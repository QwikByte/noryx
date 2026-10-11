# Servers

Everything a single server offers in the panel. New servers can also start from a [template](library.md#templates) or a
[Modrinth modpack](library.md#modpacks).

## Software

Servers run as containers of Docker or [Podman](installation.md#docker-or-podman) based on
[itzg/minecraft-server](https://github.com/itzg/docker-minecraft-server) (Vanilla, Paper, Purpur, Folia, Leaf, Fabric,
Quilt, Forge, NeoForge) and [itzg/mc-proxy](https://github.com/itzg/docker-mc-proxy) (Velocity, BungeeCord; Waterfall
only for existing proxies, see [Networks](networks.md#proxies-and-forwarding)).
Container labels are the agent's only state, so servers keep running while an agent restarts.

## Creating and deleting

**Create server** asks for the node, name, software, Minecraft version, memory, port and storage location. It starts
with the template that the [settings](administration.md#general) choose for new servers, for those who may see
templates, or else with the software, memory, Java version, stop timeout and time zone they set (Paper, 2 GiB, 512 MiB
for proxies, at least 4 GiB for modpacks, the newest Java, 1 minute and UTC by default), on the node the user created a
server on last in this browser, while it is online, or else on the first online node. Memory is one of the usual
amounts, or any other from 512 MiB to 64 GiB that is entered, here as in the settings of servers and templates. The
field of the Minecraft version suggests the releases that Modrinth lists, as do the settings and templates; empty means
the latest. A game server can get a seed, a game mode, a difficulty and a world type under **World**, which start as its
template has them, or as Minecraft's defaults. They are written to `server.properties` before the first start, and the
agent checks them like other properties. The Java version of a game server, the [stop timeout and the time
zone](#settings-and-images) fold away the same way, as the template or the settings have them. Deleting a server with
all its worlds asks for its name first.

A server that ran elsewhere, e.g. at a host or on another panel, can start from **An archive of a server**: a ZIP or
`.tar.gz` archive of the contents of its folder, with `server.properties` at its top. Software, version, memory and port
are chosen as usual, and the archive becomes its data, streamed through the master like an upload of the file manager
(up to 16 GB, while 1 GB stays free on the node, which also needs room for the archive until it is unpacked). The agent
checks the archive like one of the file manager; one with links, paths outside the folder or too much data creates no
server. The server doesn't take what holds secrets of the server it came from or what Noryx writes again: the files of
the console password (`.rcon-cli.env`, `.rcon-cli.yaml`), a proxy's `forwarding.secret`, Floodgate's key and Geyser's
sign-ins, the manifest of file sets and other files of the agent are left out, which the panel names, and the console
password and other secrets in `server.properties` and the configuration of plugins are emptied. It trusts no proxy: its
forwarding settings are removed and it runs in online mode until it joins a [network](networks.md). Creating a server
from an archive needs the permission to create servers on the node and an up-to-date agent.

The page of a server shows the address players join at, with a button to copy it: the host of its node's address with
the server's port, or its proxy's for a server of a network. Without the permissions to see that node and the networks,
it shows only the port.

## Console

Each server has a live console in the panel: its output streams in as it happens, and commands go to game servers
through their console port (RCON), and to proxies through their own console, whose answer follows in the output. The
agent keeps one console connection per game server for all its commands, so the server doesn't log a new one for each.
Commands typed in quick succession run one after the other, in their order. Proxies created by earlier versions accept
commands once they were created again, e.g. by saving their settings.

The output keeps the colours of the server and its plugins, and warnings and errors stand out. It can be searched,
narrowed down to warnings and errors, which the console remembers, cleared and downloaded as a text file of what is
shown. While scrolled up, a button counts the new lines and leads back to the end. The console opens with the last 300
lines; **Load earlier output** adds the earlier ones of the last 1000 lines the node keeps, and older output is in the
server's log files. It keeps 2,000 lines unless the user chose 5,000 or 10,000.

**View** in its toolbar chooses the size of the text, whether long lines wrap or scroll sideways, whether each line shows
when it was written (in the user's time zone, also in the download), and whether the console is light in the light
theme, like the terminal and the editor; the account page has these too ([Settings of each
user](panel.md#settings-of-each-user)). **Fill the window** gives the console the whole window, and Esc leaves.

The prompt keeps the commands of each server while the browser tab is open, and the arrow keys walk through them. While
typing, it suggests Minecraft's commands (or the proxy's), their arguments, the names of the players online and earlier
commands; **Tab** takes a suggestion. Agents of older versions send the output without colours.

## File manager

The file manager of a server browses its data, uploads files and whole folders by drag and drop or with **Upload** (up
to 16 GB each and 10,000 files at once, streamed through the master, as long as 1 GB stays free on the node, like for
backups), creates files and folders, edits configuration files in the browser and downloads files or whole folders as
ZIP archives. A folder can be filtered by name and sorted by name, the largest or the newest first; the browser keeps
the order for every folder. **Move to…** in the menu of a file or folder moves it into another folder, and **Copy to…**
copies it there, or into the same folder under a new name, e.g. `world copy`, as long as 1 GB stays free on the node.
Selected files and folders are downloaded as one ZIP archive, moved, copied or deleted together; agents of older
versions can't download several of them at once.

**Extract…** in the menu of a ZIP or `.tar.gz` archive extracts it into its folder or into a new folder in it, e.g. a
world, a modpack's overrides or the configuration of plugins that were uploaded as one archive. Existing files are only
replaced if that is chosen. The agent reads the list of the archive first and refuses it before it writes anything if
it holds links or other special files, paths outside the folder, more than 100,000 entries, more than 64 GB of files or
far more than the archive's size (zip bombs), a file that exists already, or a file that holds secrets of the server or
that Noryx writes itself, such as `server.properties`, `ops.json` or `whitelist.json` in the server's folder: such files
can be extracted into another folder, or uploaded and edited on their own.

**Search** finds text in the text files of the shown folder and the folders in it, regardless of case, and opens a file
of a match in the editor. Files that only hold secrets are left out and others show them as `<hidden>`, so a search
never finds a secret. Files larger than 4 MB and binary files are left out, and a search stops after 500 matches or 10
seconds. Extracting, copying and searching need an up-to-date agent on the node.

If something changed or deleted a file while it was open in the editor, e.g. a plugin, a file set or another user,
saving shows the difference to the file on the server and offers to load that version or to overwrite it. Agents tell
the editor the version of a file by when it was modified and its size; with agents of older versions, saving
overwrites the file as before. Saving a JSON or YAML file with a syntax error, which servers and plugins may fail to
read, asks first and names the line. The editor highlights JSON, JSON5, YAML, properties, TOML, INI-like files
(`.conf`, `.cfg`, `.ini`), shell scripts, JavaScript and XML. Each user chooses whether it wraps long lines, whether it
indents with 2 or 4 spaces or tabs (YAML always with spaces), whether it takes Vim's keys, the size of its text and
whether it is light in the light theme; **View** next to **Download** has some of these.

Logs (`.log` files) open read only in a viewer that highlights warnings and errors. Of a large log, it shows the last
2 MB and loads earlier parts on request; with agents of older versions, large logs can only be downloaded. Archived logs
(`.log.gz`) are unpacked in the browser, up to their first 16 MB.

Secrets such as the RCON password and the forwarding secret of a network never reach the panel. The file manager hides
files that only hold secrets (`.rcon-cli.env`, `.rcon-cli.yaml`, `forwarding.secret`, Floodgate's `key.pem`) and shows
`server.properties`, `config/paper-global.yml`, Geyser's `config.yml` and the configuration of the forwarding mods of
networks with their secrets as `<hidden>`, which saving keeps. Downloads of folders and backups leave them out the same
way, as they do with the files that [file sets](library.md#file-sets) filled secrets into. Plugins and mods run with the
server, though, and can read them.

## server.properties

`server.properties` can be edited as a form: grouped settings with switches, choices and validated numbers, a MOTD
editor with colour codes and preview, and a search. Only properties of the server's Minecraft version are shown,
comments in the file are kept, and properties the manager relies on (container port, RCON) are locked. Next to the
MOTD, an image becomes the server's icon in the server list: the browser scales it to 64×64 pixels and writes it as
`server-icon.png`, which the server shows after its next start. The configuration of proxies offers the icon too. It
needs the permission to change files.

## Settings and images

The settings of a server can be changed after it was created: name, Minecraft version, the version of the mod loader of
Fabric, Quilt, Forge and NeoForge servers (the newest unless set), memory, port, Java version (8, 11, 17, 21, 25 or the
newest), when it starts on its own, Aikar's flags, JVM options, a CPU limit, the stop timeout and the time zone. The
agent creates the container again with the same data; the old container is only removed once the new one exists. A
server keeps the image it was created with; **Update image** in its settings pulls the newest one and, if it changed,
creates the container again the same way. The old image is removed once no server uses it. New servers get the newest
image too: creating one pulls it, which downloads its changes if it was updated since. Deleting servers keeps their
images. Servers created from a modpack show it in their settings, where they move to [another version of
it](library.md#modpacks).

The **stop timeout** is how long a server may take to save its worlds when it stops or restarts before it is killed:
from 30 seconds to 10 minutes, 1 minute unless changed, e.g. longer for a large modded world. The **time zone**, one of
the IANA time zones such as `Europe/Berlin` chosen from a searchable list, sets the time of the server's log and of
plugins that work with times; servers run in UTC unless one is chosen. Agents of earlier versions keep 1 minute and UTC,
which saving the settings, creating a server and moving one to their node tell.

Stopping and restarting a server run as [operations](panel.md#operations), as they may take as long as its stop
timeout: the panel follows them in a notification, and a reverse proxy in front of the master, e.g. nginx, which gives
up after 60 seconds by default, doesn't cut them off. Starting a server only takes a moment and answers right away.

**Restart with a warning…** and **Stop with a warning…** in the menu of a running game server warn its players first,
1, 2, 5 or 10 minutes before, and again 5 minutes and 1 minute before, like [schedules](automation.md#schedules) do.
The [settings](administration.md#general) can change the texts, the steps, the longest lead time (up to an hour, which
offers 15, 30 and 60 minutes too) and where the warning shows: in the chat (`say`), as a title or above the hotbar.
Until then, the operation shows the time left and can be cancelled, which leaves the server running. The warning has
the text of the settings; one's own message needs the permission to send console commands, as the warning is a console
command. Proxies get no warning.

**Stop** and **Restart** of a server, on its page, its card, its row of the table and the widget of pinned servers, act
right away, unless a user chooses on their account page that they ask first or open the dialog with the warning on,
which only running game servers have, so that the others ask. They can do so only for servers with players, by the
latest count of their node; a server it didn't count, e.g. as its console didn't answer, asks too. `restart lobby` and
`stop lobby` in the [search](panel.md#search) follow the same choice, and selected servers always ask.

When a node shuts down or reboots, systemd gives Docker 90 seconds to stop (its `DefaultTimeoutStopSec`), which cuts
longer stop timeouts short: servers that haven't saved their worlds by then are killed. To give them their full stop
timeout, raise `TimeoutStopSec` of `docker.service` beyond the longest one, e.g. to 11 minutes:

```sh
sudo mkdir -p /etc/systemd/system/docker.service.d
printf '[Service]\nTimeoutStopSec=11min\n' | sudo tee /etc/systemd/system/docker.service.d/stop-timeout.conf
sudo systemctl daemon-reload
```

On a node that runs [Podman](installation.md#docker-or-podman), `podman-restart.service` stops the servers, so raise
its `TimeoutStopSec` the same way, in `/etc/systemd/system/podman-restart.service.d`.

## Crashes and health

A server that crashed and starts again shows as **crashing**, with how often it crashed and its exit code. After 5
crashes in a row, each within 10 minutes of its start, the agent stops it, as Docker or Podman would start it again
forever. Each crash is a warning in the log, which the bell counts, and the notice on the server's page links to its
crash reports.

The images check the health of their server. A server that runs but fails its health check, e.g. as it hangs, shows as
**unhealthy** on its card and page and under what needs attention on the overview; the log tells when it becomes
unhealthy and healthy again. It still counts as running: its console, restarts, stops and schedules work as usual.

## Copies

A server can be duplicated on its node: the copy gets all files, worlds and settings under a new name and port, and
starts stopped. A running game server first writes its worlds to disk and pauses saving while they are copied, so
players stay connected. The copy keeps the original's modpack, but doesn't take over its place in a network unless **Add
the copy to the network** is chosen for a game server of one, e.g. for a second lobby: the copy then joins the network
next to the original, see [Networks](networks.md#copies-of-servers).

## Moving to another node

A server can move to another node with its ID, files, settings and, if chosen, its backups; otherwise the backups are
deleted with it. Port, storage location, memory and CPU limits are checked on the new node first. The server then stops,
its data is copied through the master, and it starts on the new node if it ran before. Its backup jobs, schedules, the
scopes of groups, its usage history and thresholds follow it, and its network is configured again, which restarts the
proxy. The original is deleted only once the server is complete on the new node; if anything fails before, the copy goes
away and the server runs where it was. While it moves, the panel shows the progress, refuses changes to the server and
continues on the new node once it is done; scheduled tasks leave it out meanwhile. The new node needs free space for the
archive of the data besides the data itself, until it is extracted. If the master stops during a move, the server stays
on its old node, stopped.

## Lists, tags and bulk actions

The **Servers** page and the page of each node list servers as cards or as a compact table, the table from 13 servers on
until one is chosen. They are searched, filtered by state, type, node, network and tag, sorted by name, state, players,
CPU, memory, node, network, type, version, port or ticks per second, either way round, and grouped by network, node, type
or tag, in groups that fold away. A click on a header of the table sorts by its column, and another click turns the
order around. **Columns** chooses what the table shows besides the server and its state: its node, network, type,
version, port, tags, players, ticks per second, CPU and memory; the type, version, port and tags of a server show with
its name unless they have columns of their own. The address keeps the search, filters, sort, grouping and view, so that
a view can be shared or bookmarked; where it doesn't say, the view, sort and grouping a user chose last apply, in all
lists and all their browsers, as do the columns and the folded groups, e.g. a network folded on the **Servers** page
stays folded on the page of a node. **Views** next to the search saves what the **Servers** page shows by name, e.g.
"Lobbies on node 2": its search, filters, sort, grouping and view, which the menu and **Ctrl+K** show again. The master
keeps up to 20 views for each user, which only the user sees. **Export CSV** downloads the servers as listed, with their
type, version, port, state and tags and the other columns of the table, e.g. their node or what running servers use,
for spreadsheets. Selected servers start, restart or stop together, run
a console command such as `save-all`, or get and lose tags; an action applies to the selected servers in a fitting state
on which the user may do it, at most 8 at a time on each node, and the panel tells which failed and offers to try those again.
Restarting and stopping them can warn the players of the game servers first, like a single server.

Servers have **tags** such as `lobby` or `bedwars`: up to 10, each of up to 24 letters, digits, `-` and `_`. The master
keeps them; they follow a server that moves, copies get them, servers created from a [template](library.md#tags) get
its tags, and they go with a deleted server. Changing them needs the permission to change the server's settings, though
it doesn't restart the server. [File sets](library.md#file-sets),
[backup jobs and schedules](automation.md#targets-and-times) can target a tag, so a tag can put a server under them.

Servers have **notes** too, e.g. what a test server is for or whom to ask about it: up to 500 characters of plain text,
which the server's page shows and the search of the lists and **Ctrl+K** find. **Notes…** in the menu of a server
changes them. The master keeps them like tags: they follow a server that moves, copies get them, and changing them needs
the permission to change the server's settings but doesn't restart it.

## Node settings

Each node has settings for its servers: the storage location preselected for new servers, a port range (new servers get
the first free port in it) and a memory limit, so that servers together can't get more memory than the node has minus a
reserve for the system (1 GB unless changed). A server counts with the limit of its container, which gives Java about a
quarter more than the server's memory and 256 MB for what it needs besides the heap, e.g. 1.5 GB for 1 GB. Name and
agent address can be changed too.
