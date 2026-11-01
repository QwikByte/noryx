# Networks

A network puts game servers behind a proxy, on one node or spread across nodes. The panel doesn't add a network system
of its own: it writes the configuration the proxies and game servers document, and shows it as a picture. The master
stores the network and configures each server through its agent.

## Proxies and forwarding

| Proxy      | Configuration   | Forwarding                                   |
| ---------- | --------------- | -------------------------------------------- |
| Velocity   | `velocity.toml` | modern (recommended) or legacy               |
| BungeeCord | `config.yml`    | legacy (`ip_forward`), BungeeCord's only way |

Waterfall, PaperMC's fork of BungeeCord, reached its [end of life](https://forums.papermc.io/threads/1088/) and can no
longer download its command modules, such as `send`, so Noryx doesn't run it; BungeeCord reads the same `config.yml`.

| Game server                | Accepts the players of the proxy through                                                       |
| -------------------------- | ---------------------------------------------------------------------------------------------- |
| Paper, Purpur, Folia, Leaf | `config/paper-global.yml` (modern) or `spigot.yml` with `bungeecord: true` (legacy)            |
| Fabric, Quilt              | [FabricProxy-Lite](https://modrinth.com/mod/fabricproxy-lite) and the Fabric API (modern only) |
| Forge, NeoForge            | [Proxy-Compatible-Forge](https://modrinth.com/mod/proxy-compatible-forge) (modern or legacy)   |

Vanilla servers can't tell forwarded players apart and can't join. Game servers in a network run with
`online-mode=false`, as the proxy authenticates the players, and turn away anyone who doesn't come through the proxy. A
server that leaves its network, and a copy of one, get `online-mode=true` again, and restoring a backup from before
brings back neither offline mode nor the forwarding secret (see [Backups](automation.md#backups)). The overview warns
about a server outside of networks that runs in offline mode, as anyone who reaches it can join under any name. The
panel installs the forwarding mod of a Fabric, Quilt, Forge or NeoForge server from Modrinth when it joins, and removes
it when it leaves.

New Minecraft servers start with a whitelist: add players on the [Players](players.md) page, for one server or the
network.

## Map and routing

The network's page shows where players connect, the proxy and its servers with their state and players. Players join the
servers of the join order and fall back to the next one when a server is offline, full or kicks them (Velocity's `try`,
BungeeCord's `priorities`); servers are dragged into another order. Host names, such as `survival.example.com`, send the
players who connect through them to their own servers (`forced-hosts`, for BungeeCord one server each). Servers have the
name players use with `/server`; BungeeCord's servers also have a MOTD for their host names and can be restricted to
players with the permission `bungeecord.server.<name>`. Of more than 10 servers, the map shows those players join, fall
back to or reach through a host name and those that crash, and folds away the others until it is expanded; the list of
servers can be searched. **Players online** lists the network's players on the [Players](players.md) page.

## Applying changes

All changes are saved and applied together, and the panel tells beforehand what they do. Servers that join or leave
restart, all of them when the forwarding changes. The proxy reloads its configuration through its console
(`velocity reload`, `greload`), which disconnects nobody; BungeeCord can't reload without a server it had, so removing
or renaming a server restarts it. If a node is offline, the change is saved and **Apply again** configures its servers
later; until then, the network's page and the overview tell that its proxy may be out of date. The proxy reaches servers
on other nodes at the host of their node's address and their port, so changing either configures the network again, as
does restoring a backup of one of its servers.

## Copies of servers

**Duplicate** on a game server of a network offers **Add the copy to the network**, e.g. for a second lobby or another
minigame server. One operation copies the server on its node, adds the copy to the network right after the original,
under the original's name in the network with the next free number (`lobby-2` for `lobby`, `lobby-3` for `lobby-2`), and
applies the network. The copy takes the original's place: it follows it among the servers players join and fall back to,
and those of its host names on Velocity, and gets its BungeeCord settings. It stays stopped until it is started. If the
network can't be applied, e.g. as a node is offline, the copy stays in the network and the operation tells to apply it
again. It needs the permission to manage networks besides those to copy the server.

## Changing the proxy

A network can swap its proxy for a free Velocity or BungeeCord proxy on any node, and keeps its servers, join order,
host names and Bedrock port. The new proxy takes over the old one's configuration if it reads the same file, and the
Maintenance plugin with its state and team; other plugins stay with
the old proxy. Changing to Velocity can switch to modern forwarding, which restarts the servers once, as does a proxy
that comes to or leaves the node of a server. The dialog tells all this beforehand and where players join from then on.
The old proxy leaves the network without its forwarding secret and stops; the new one starts if either ran.

## Deleting and removed nodes

Deleting a network makes its game servers standalone again, in online mode and without the forwarding secret; the
proxy keeps running without forwarding. A network with datastores can't be deleted. If the proxy's node can't be
reached, e.g. as it is lost, the network is deleted anyway: its servers on other nodes become standalone, and a warning
tells that the proxy and the network's servers on its node keep their settings until they are deleted or put into a
network again. While the node answers, a server that can't be configured keeps the network.

Removing a node whose servers are part of networks first takes them out, once confirmed (`DELETE /api/nodes/{id}`
with `?release=true`, which also needs the permission to manage networks). A network whose proxy runs on the node, or
all of whose game servers do, is deleted: its game servers on other nodes become standalone, and a proxy on another
node stops forwarding. The other networks lose the node's servers, which players then neither join nor reach through
host names; their proxies forget them first. As those servers knew the forwarding secret, these networks get a new one,
and their running game servers restart once for it. The node itself isn't contacted, so a lost node can be removed too, but if
a server of another node can't be configured, the node stays. A network that would be deleted can't take datastores on
other nodes along, so delete them first; datastores on the node are forgotten with it, like its servers.

## Proxy configuration

The proxy's tab of the network, and the **Configuration** tab of every proxy, edit the other settings of its file as a
form: MOTD, the shown maximum of players, online mode, ping passthrough, compression, timeouts, rate limits, the HAProxy
protocol, query, BungeeCord's permissions and more. Settings that aren't known appear under Advanced. Those the network
decides, such as the servers, are locked. Saving reloads a running proxy.

## Actions

**Servers** starts all servers of the network before the proxy, so that players find them, and stops the proxy first, so
that all players leave at once. **Message** shows a message to the players of all running game servers: in the chat
(`say`), as a title with an optional subtitle, or above the hotbar, see [Players](players.md#actions).

## Restart server by server

The running game servers restart a few at a time (1, 2, 5 or 10) while the network stays open: the proxy first sends
their players to another running server with `send`, and the next servers restart once these run again. The servers
players join first restart last and one at a time; the proxy keeps running, and a server that doesn't start again stops
the restart. It also stops before servers whose players the proxy can't send, e.g. a BungeeCord proxy without its
module `cmd_send`. A rolling restart may take as long as its servers need: for each batch the longest
[stop timeout](servers.md#settings-and-images) among them and 8 minutes to start and run again, an hour at least.

The menu of a running server in the network's list of servers restarts that one alone the same way, **Restart safely**:
its players move to the server players join first, or another running one, and the proxy keeps running. **Send players
elsewhere** only moves its players, e.g. before working on the server. Restarting server by server, all servers or one,
needs the permission to restart the proxy and each server that restarts; sending players elsewhere needs the permission
to manage the players of the proxy, like [sending one player](players.md#actions). [Schedules](automation.md#schedules)
restart the servers of networks server by server too, if chosen.

## Maintenance

The network's overview turns maintenance on and off with the [Maintenance](https://modrinth.com/plugin/maintenance)
plugin on the proxy: the server list shows the network in maintenance, and only the team may join. The first time, the
panel installs the plugin from Modrinth and restarts the proxy to load it, which disconnects all players once. The team
is edited in the panel (the plugin's `maintenance add` and `remove`), the texts in the plugin's `config.yml`, which the
panel links to. The panel reads the state from the plugin's files, so it also shows maintenance turned on in the game.

Maintenance can start now or in 5, 15 or 60 minutes, and last until it is ended or for 30 minutes up to a day; times go
up to 28 days through the API. A single game server of the network can go into maintenance too: the proxy turns away
players who want to join it, and sends those on it to the plugin's fallback server (`fallback` in its `config.yml`,
`lobby` unless changed) or off the network. While the network is in maintenance, it can end in 5, 15 or 60 minutes
instead of now. The panel uses the plugin's timers (`maintenance starttimer`, `endtimer`, `scheduletimer` and
`aborttimer`, with the name of the server), which tell the players in the chat when maintenance starts or ends; a new
timer replaces the one that runs. The plugin keeps timers to itself and forgets them when the proxy restarts, so the
panel shows when maintenance ends only if the plugin keeps its end timer over restarts
(`continue-endtimer-after-restart` in its `config.yml`), and otherwise shows the change once the timer ends. **Abort a
timer** stops the one of the network or of a server. Planning the maintenance of a single server for later needs version
5 of the plugin, as version 4 swaps its times; update it in the proxy's **Plugins** tab. A server named `global` can't
go into maintenance alone, as version 5 reads that name as the whole network.

## Bedrock players

The network's overview lets players of the Bedrock Edition join, on phones, consoles and Windows, at a UDP port of the
proxy's node: the panel installs [Geyser](https://geysermc.org), from Modrinth, and Floodgate, from GeyserMC's download
server, on the proxy, publishes the port and writes it with `auth-type: floodgate` into Geyser's `config.yml`. Geyser
translates their game, and Floodgate lets them join without a Java account, which needs no plugin on the game servers:
Floodgate's key stays on the proxy. Letting Bedrock players in, a new port and **Apply again** update both to their
newest build; other changes of the network leave them be, so that they don't disconnect players. The proxy restarts when
Bedrock players are let in or no longer, their port changes or a plugin is updated; the game servers restart when they
are let in or no longer, as they stop demanding signed chat messages, which Bedrock players can't send
(`ENFORCE_SECURE_PROFILE=FALSE`, locked in their properties; afterwards `enforce-secure-profile=true` again). Java
players who only show secure chat don't see what Bedrock players write. The panel shows where Bedrock players connect
and warns about what keeps them out: Geyser joins as one Minecraft version (it tells which), so game servers of older
versions need ViaVersion and newer ones ViaVersion and ViaBackwards; Geyser needs about 1 GB of memory on the proxy; and
the UDP port must be open on the proxy's node, e.g. in the provider's firewall. Geyser also needs to reach Mojang's and
Microsoft's servers from the proxy, and Bedrock players can't join servers whose mods players must install. In the panel
and the game, Floodgate starts their names with a dot, e.g. `.Steve`. Turning Bedrock off removes both plugins, but
keeps their settings and Floodgate's key for later.

## Reaching the servers

On its own node, the proxy reaches a server by container name over a network that only the two of them share;
such a server's port isn't published at all. [Extra ports](servers.md#extra-ports) of its plugins are, on all addresses
of the node, but never its port. A server on another node is reached over the private network of the nodes
if both nodes are part of it (see [Private network](#private-network)). Otherwise it is reached at that node's host and
the server's port, which must be open for the proxy's node. Docker's rules bypass firewalls such as ufw, so allow only
the proxy's node in Docker's `DOCKER-USER` chain on the server's node, e.g. for port 25566 and the proxy's node
203.0.113.10:
`iptables -I DOCKER-USER -p tcp -m conntrack --ctorigdstport 25566 --ctdir ORIGINAL ! -s 203.0.113.10 -j DROP` (and save
it, e.g. with `netfilter-persistent save`). The panel shows this command with each such server; with Docker's nftables
firewall there is no `DOCKER-USER` chain, so use the private network instead. Podman's rules bypass such firewalls too,
and it has no such chain either; on its nodes, the panel shows a rule in an nftables table of its own instead, which
comes before Podman's rules:
`nft 'add table inet noryx-firewall; add chain inet noryx-firewall forward { type filter hook forward priority -10; }; add rule inet noryx-firewall forward meta l4proto tcp ct status dnat ct original proto-dst 25566 ip saddr != 203.0.113.10 drop'`
(save it in `/etc/nftables.conf` to keep it after a reboot).

## Private network

Nodes form a private WireGuard network, `10.213.0.0/24` with UDP port 51820 unless its settings on the **Nodes** page
say otherwise. Its range changes only while no node is part of it. A node joins on its page once its administrator ran
`noryx-agent overlay allow` on it, and gets the lowest free address; the others reach it at the host of its agent's
address, or at an endpoint set on its page, which a node on the master's machine needs (`install.sh all` registers it at
`127.0.0.1`). The proxy then reaches a server of another member at that node's address in the network, where the node
publishes the server's port only for the proxy's node: no firewall rule and no confirmation for legacy forwarding are
needed, and the traffic between the nodes is encrypted. Joining restarts nothing: a network shows which of its servers
move to the private network when it is applied again, which restarts them once for their new port. Leaving applies the
networks of the node again, which reach the servers at public ports then, and is refused while a network with legacy
forwarding would reach servers that no firewall protects. Nodes outside and kernels without WireGuard (it is part of
Linux since 5.6; RHEL 9 has it only as an unsupported Technology Preview) work as before. The
master configures the members every 5 minutes, so those that were offline catch up, and right away after a change, e.g.
when a node is removed or its address changes. Each node's page shows its address, endpoint, key and peers with their
latest handshake and traffic, and rotates its key; the overview names members that had no handshake with another one for
5 minutes, e.g. as the UDP port is closed. The ports a node publishes there only let in the keys that the other nodes
had when the network was applied, so that a node that later gets the address of a removed one reaches nothing: rotating
a key therefore applies the networks with servers on the node and on other nodes again. Apply them again yourself if one
of their nodes was offline meanwhile, or if a node's key changed otherwise, e.g. as it lost its data. The kernel keeps
the interface `noryx0` while the agent restarts or updates, and `noryx-overlay.service` restores it at boot, before
Docker or Podman starts the servers.

When a proxy doesn't reach a server on another node, the node pages tell why. Each one shows whether the node's firewall
rules for the network (the nftables table `inet noryx`) are in place, missing or incomplete, e.g. after something
flushed the rules: the master writes them again within 5 minutes, or `noryx-agent overlay up` right away. Its
**Published ports** list each port the node publishes in the network with its server or datastore and the nodes that
reach it; a node whose key changed since, marked _old key_, reaches the port once the network is applied again.
**Test** next to a peer, for those who may manage the private network, connects from this node to every port that the
peer publishes for it, and shows for each port whether a connection came about and how long that took, or why not: e.g.
_connection refused_ if the server is stopped, or no answer within 3 seconds if a firewall drops the packets or the
peer is unreachable. The node connects from itself, not from the proxy's container, sends nothing and closes each
connection at once, so a server may log a connection that ended without data. `noryx-agent overlay status` shows the
firewall and the published ports on the node, too.

## Legacy forwarding

Legacy forwarding doesn't prove that players come through the proxy: anyone who reaches such a server can join as any
player. A network with legacy forwarding and servers on other nodes outside the private network therefore needs the
confirmation that a firewall protects them, and no server of it moves to such a node without.
