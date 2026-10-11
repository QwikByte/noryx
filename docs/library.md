# Templates, file sets, plugins and mods

The **Library** of the panel: what new servers start with, and what many servers share.

## Templates

A template preconfigures new servers: software, Minecraft version, memory, the
[settings](servers.md#settings-and-images) of a server, `server.properties`,
[tags](servers.md#lists-tags-and-bulk-actions) and a list of plugins or mods from Modrinth or Hangar. When a server is
created from a template, only node, name, port, storage and world are chosen, and its stop timeout and time zone can
differ from the template's; `server.properties` is written before the first start and each plugin is installed in the
newest release that suits the server, unless the template [keeps a version](#plugin-versions) of it, so templates don't
go stale. Templates are created from scratch or from an existing server ("Save as template"), which takes its settings,
its properties (except those the manager sets), its tags and the plugins that come from Modrinth or Hangar. Worlds and
plugin configurations are not part of templates; [file sets](#file-sets) share the configurations of plugins among
servers. The properties Noryx sets itself (the port, address and RCON) and secret ones can't be part of a template.

### Tags

Servers created from a template get its tags, e.g. `bedwars`, so they are targets of the file sets, backup jobs and
schedules of these tags right away. The file sets still have to be applied to them, so a template puts no files with
secrets on a server by itself.
Giving a new server tags needs the permission to change the settings of the servers on its node, as tags of a server do;
without it, the server is created without them, and the panel says so.

### Plugin versions

A plugin of a template can keep a version known to work instead of the newest: the menu next to it offers the versions
that suit the template's software and Minecraft version, betas and alphas included. The template is checked against them
when it is saved, and new servers get exactly that version, downloaded with its hash checked like any other. If the kept
version doesn't suit a new server, e.g. because it runs another Minecraft version than the template, that plugin is left
out rather than installed in another version, the others are installed, and the panel tells which one is missing. Choose
another version in the template, or install the plugin from the server's **Plugins** tab.

### Export and import

**Export** downloads a template as a JSON file, e.g. to move it to another master or to share it. The file holds what
the template sets up: its settings, properties, tags and plugins with the versions it keeps, but no IDs of the master,
its nodes or servers. **Import** on the **Templates** page reads such a file of up to 1 MiB under a name of choice and
saves it like a template saved in the panel, with the same checks: its plugins and kept versions are looked up again and
must suit its software, and properties that Noryx sets are refused. Files from a newer version of Noryx with a format
this master doesn't know yet are refused with a message to update Noryx first.

## File sets

A file set keeps files that many servers share in one place, e.g. the configuration of LuckPerms, chat or anti-cheat
plugins or a `server-icon.png`, and puts them on the servers of tags and networks: those with a tag, or the game servers
or the proxy of a network. Paths are relative to the server's folder, e.g. `plugins/LuckPerms/config.yml`.

### Files

The page of a set is one workspace: its files as folders next to the editor, with an **Insert** menu for variables,
databases and secrets, and beside them the set's targets, variables, secrets and details. A bar above tells what the set
needs, e.g. servers that are outdated or secrets and variables without a value, and offers to apply it. **New file**
starts an empty file or uploads one of the computer; **Import** browses the folders of a server and takes chosen files
or whole folders at the same paths, as the file manager shows them, skipping those larger than 1 MiB and those with
code; a new file's folder can be chosen on a server too. A set keeps the newest 20 versions of its files with who saved
them; the history shows what each version changed and loads an older one into the editor to save it as the newest. A
save based on an older version than the newest is refused, so that it can't undo what someone else saved. A set has up
to 100 files of up to 1 MiB, 3 MiB in all: text files (UTF-8 without NUL bytes) and binary files, e.g. images, which the
panel never shows as text but previews if they are images, downloads and replaces, and which servers get as they are;
agents of older versions get none, as they would write them empty, and the state of the set tells to update them. A file
can be written only to servers that don't have it, for files that plugins rewrite. Files that Noryx writes itself
(`server.properties`, `eula.txt`, `velocity.toml`, BungeeCord's `config.yml`, `spigot.yml`, `config/paper-global.yml`,
the lists of players, `noryx-*` files), those with secrets of the server, `.jar`, `.zip` and `.class` files, and
archives, Java classes and programs under any name can't be part of a set; plugins come from the **Plugins** page, with
their hashes checked. On a server, a path comes from one set only.

### Placeholders

The master fills in `{{server.name}}`, `{{server.id}}`, `{{server.port}}` and `{{network.server}}`, the server's name in
its network, for each server; these only hold letters, digits and a few other characters, so they can't add lines to a
file. `{{secret:<name>}}` is a secret of the set: a single line of up to 1 KiB, typed in or generated randomly. The API
only tells the names of secrets and when they changed, never their values. Other text in double braces stays as it is,
as some plugins use it themselves; binary files are written as they are, without placeholders filled in.

### Variables

`{{var:<name>}}` is a variable of the set, for what differs between servers, e.g. their role or the name of their
world. The set's **Variables** give it values for single servers, the servers of networks (their proxies included),
those with tags, and all servers. A server gets its own value, else its network's, else that of the first of its tags
in alphabetical order, else the one for all servers. A value is a single line of up to 128 characters without quotes,
backslashes and braces, so it can't add lines to a file, end a quoted text or make up another placeholder. A set has up
to 50 variables with up to 200 values each; they are saved with the set, like its targets, but not in its versions.
Values for servers and networks that are deleted are removed, and a deleted network leaves the targets of sets.

### Databases

`{{datastore:<datastore>.<database>.<field>}}` fills in how a server reaches a database of a
[datastore](databases.md) of its own network, so that one set serves the servers of every node, e.g. for LuckPerms:

```yaml
address: {{datastore:main.luckperms.host}}:{{datastore:main.luckperms.port}}
database: {{datastore:main.luckperms.database}}
username: {{datastore:main.luckperms.user}}
password: {{datastore:main.luckperms.password}}
```

`host` and `port` are those of the datastore's container for the servers on its node, and its address and port in the
private network of the nodes for the others; `database` and `user` share the database's name. `password` is filled in by
the agents only, like a secret, and the file that holds it is hidden on the server. The datastore is the one of that
name in the server's network, so a set for several networks uses the datastore of each. Only those who may manage
datastores may add a password to a set or change a file that holds one, which the log records, and apply a set that puts
passwords on servers; others may change the rest of the set. A new password of a database makes the servers that have it
outdated, until the set is applied again. Agents of older versions get no passwords.

### Problems

A secret or variable without a value for a server, a database the server doesn't reach (it is in no network, its
network has no such datastore or database, or its node and the datastore's aren't both in the private network of the
nodes), and files that grow beyond the limits of a set once filled in show on the **Servers** tab and in the preview;
applying leaves these servers out rather than write a broken file.

### Applying

Saving changes no server; **Save and apply** saves and goes on to apply. **Apply** shows first what changes on each
server, with the diff of each file between the server's copy and the new version, servers with the same changes
together; files with secrets show the version the server has and the new one, with placeholders instead of values, and
whether the server's copy changed, and binary files only whether they change. It names the servers that get the secrets
for the first time. Applying writes the files atomically as the server's user, removes those the set no longer has
unless they changed on the server, at most 8 servers of a node at a time, and leaves moving servers alone. It can then
restart the running servers whose files changed, the game servers of a network a few at a time like a rolling restart,
as most plugins only read their configuration when they start. The results tell how it went on each server, and **Retry
the failed ones** applies the same version again to the servers where it failed. Applying can be cancelled while it
writes the files, but not once it restarts servers.

### State

The page of a set tells for each server whether it has the newest files, an older version or other values of its
variables, secrets or passwords (outdated), files that changed on it since, none yet, whether it is no longer a target
but still has files of the set, or whether its node can't be reached. The file manager marks the files that come from a
set, as applying it again replaces changes made there.

### Leaving

A server that loses its tag or leaves its network, and the servers of a deleted set, lose the files with secrets of the
set right away, or, if their node is offline, once it is back. Applying the set again removes the other files from
servers it is no longer for, unless they changed on them; a deleted set leaves them as they are. Copies of a server lose
the files with secrets; moving a server takes everything along.

## Plugins and mods

Plugins (Paper, Purpur, Folia, Leaf, Velocity, BungeeCord, Waterfall) and mods (Fabric, Quilt, Forge, NeoForge) are
installed from [Modrinth](https://modrinth.com), either on any number of servers at once from the **Plugins** page or
from the **Plugins**/**Mods** tab of a server. The **Plugins** page switches between plugins and mods, so a project made
for both only shows the software and servers of the chosen kind. The search filters by software, Minecraft version,
categories (e.g. economy, management, optimization) and, for mods, those players don't have to install, and sorts by
relevance, downloads, followers, newest or recently updated. The source, the sort and the software of plugins and of
mods each user chose last apply again, in all their browsers. The master picks the newest release for each server's
software and Minecraft version, installs the projects it requires, and replaces an older version of the same project.
Where no release suits a server, it installs the newest beta or alpha and the panel warns about it. Another version that
suits the server, betas and alphas included, can be chosen instead, also to downgrade a project. Installed files are
recognised by their hash, so the tab shows their project, version (marked as beta or alpha) and available updates, also
for files uploaded by hand; it searches, filters (updates, not from Modrinth) and sorts them, by name or size as the
user sorted them last, and updates all at once to the newest release that suits the server, never to a beta or alpha.
Own `.jar` files can be uploaded too. Servers load changes when they restart.

**What changed** next to an update shows the changelogs of the versions after the installed one up to the update, and in
the version menu those of the newest suitable versions, as their authors wrote them on Modrinth or Hangar.
**Keep this version** keeps a project at its version, e.g. because a newer one breaks its configuration: **Update all**
leaves it out, while choosing another version in its menu still works. A plugin or mod can be turned off instead of
removed: its file moves into the folder `.disabled` of the plugin folder, e.g. `plugins/.disabled/`, which no server
loads, and back when it is turned on again. Turned-off files are listed as off, can be removed, are updated in that
folder and are left out of **Update all**. Before a project is turned off or removed, the tab names the turned-on
projects that require it, as Modrinth and Hangar list their dependencies. **Open its settings** opens the folder of a
plugin's settings in the file manager, e.g. `plugins/LuckPerms/`, named by its `plugin.yml`, `paper-plugin.yml`,
`bungee.yml` or `velocity-plugin.json`.

To install from the **Plugins** page, the game servers or the proxy of a network are chosen at once, and servers are
found by name, node, software or `#tag`; those that have the project already are marked with their version. More than
100 servers are installed on in batches of 100, one after the other. Installing on many servers handles at most 8 of a
node at a time and tells what it installed on each; **Retry the failed ones** installs the same again where it failed.

**Installed** on the **Plugins** page gathers the plugins or mods of all servers you may see, recognised by their hash
like on the tabs: each project with its servers, versions and updates, and the servers that keep it at its version or
have it turned off. Nodes that can't be reached or don't answer within 15 seconds are named instead. **Update
everywhere** updates a project on all its servers to the newest release that suits each, never to a beta or alpha,
except where it is kept at its version or turned off; **Remove everywhere** deletes its files, turned-off ones too, and
names the plugins that need it first. Both run as an operation that tells how it went on each server, with **Retry the
failed ones**. Servers whose plugins you may not manage are left out and named.

Installing from the **Plugins** page, **Update everywhere** and **Remove everywhere** can restart the running servers
whose plugins changed afterwards, as applying a file set does: the game servers of a network a few at a time like a
rolling restart, so that it stays open, others at once. Once it restarts servers, it can't be cancelled.

### Hangar

Plugins of Paper and its forks except Folia, Velocity, BungeeCord and Waterfall can come from
[Hangar](https://hangar.papermc.io), PaperMC's plugin repository, too: the search switches between Modrinth and Hangar.
They are installed, updated, chosen in another version and kept in templates like those of Modrinth, with the plugins
they require from Hangar. Installed files are recognised by their hash on both; a file that is on both counts as
Modrinth's, and installing it from Hangar replaces it rather than adding another copy. Floodgate, which lets Bedrock
players join a network, comes from GeyserMC's download server; the panel installs it on the proxy with Geyser.

### Modpacks

A server can be created from a [Modrinth modpack](https://modrinth.com/modpacks) for Fabric, Quilt, Forge or NeoForge:
**Create server** searches the modpacks and offers the versions of the chosen one, the newest release first. The pack
decides the software, the Minecraft version and the version of the mod loader. The master downloads the pack, then the
files servers need (not those for players only) and writes them into the new server, with the files the pack brings
itself (`overrides`, then `server-overrides`), except `server.properties` and `eula.txt`. Afterwards it is a server like
any other: the **Mods** tab recognises the mods and updates them. A server that didn't get all files of its pack is
deleted again. Creating servers from modpacks needs the permission to manage plugins and mods on the node.

The master remembers the pack and version a server was created from, and the files the pack wrote with their SHA-512
hashes. The **Modpack** section of the server's **Settings** tab shows them, marks a newer release, and moves the server
to another version of its pack for the same mod loader, newer or older: **Update modpack** or **Change version**. This
runs as an [operation](panel.md#operations), which can be cancelled while the master downloads and checks the version
and compares it with what the server has, which the agent hashes. Then:

1. The server stops if it runs, and is backed up with its worlds, mods and configuration and the folders of the pack's
   files, as "Before modpack" and the version.
2. What the new version doesn't change stays as it is, whatever happened to it on the server.
3. Mods follow the pack: new ones are added, changed ones replaced and dropped ones removed, also other versions of
   their projects installed by hand on the **Mods** tab. A mod that you removed stays removed.
4. The other files of the pack are written or removed only if the server has them as the pack wrote them, or doesn't
   have them. Files that changed on the server since, or that the pack didn't write, stay as they are and are listed
   afterwards, as are files with secrets of the server.
5. If the new version changes the Minecraft or loader version of the pack, the server gets the new one, which creates
   its container again. Otherwise a version set in the server's settings stays.
6. The server starts again if it ran before.

If the backup fails, nothing changes, and a server that ran starts again. If something fails after it, the server stays
stopped, as it may be updated in part, and the error names the backup to restore. Copies of a server and servers that
move to another node keep their pack. Servers created before the master remembered packs have none, and their mods are
updated on the **Mods** tab. Updating needs the permissions to change the server's settings and to manage its plugins
and mods, and an agent of this version.
