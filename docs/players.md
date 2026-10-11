# Players

The **Players** page lists the players online on all game servers, with their server and network, to search and act on;
[`Ctrl+K`](panel.md#search) finds them too. **Seen** lists the players who were online, those seen last first, with
when, how long and where they played. Its other tabs join the ban list, whitelist and operators of all servers or
of a network, with how many servers have each player, and the changes that wait for stopped servers. Temporary bans,
which Paper's ban list and plugins such as EssentialsX write, show when they end, and those that ended are marked. The
**Players** tab of a game server shows the same for that server alone, with the same actions.
**Players online** on a network's page opens the page for that network.
The headers of the tables sort them, by player, server and network, by when they were seen and how long they played, or
by player and number of servers; the address keeps the tab and the order. Where it doesn't say, the tab and the order
each user chose last apply, in all their browsers, and the order also on the servers' **Players** tabs; **Players
online**, e.g. on the overview, opens the players online. **Export CSV** downloads a tab as shown, searched and
sorted, for spreadsheets: the players online with their server, node and network, the players seen with when and how
long they played and their servers, or the players of a list with their servers, and for bans the reason, when it
started and ends and who banned them.

## A player's page

Each name opens the player's page at `/players/<name>`, which `Ctrl+K` finds too, also of players who are offline: when
the player was first and last seen, how many minutes they played on each day and on each server, their entries in the
ban list, whitelist and operators of the servers, and the actions below.

The master notes every minute which players are online on each game server, as its agent measures them anyway, and
keeps where they played for as long as the [log](monitoring.md#logs) (30 days unless the settings say otherwise). It
counts a minute for each measurement that names the player, so the time of a server whose console doesn't answer, which
then only names some players, is missing. Proxies aren't counted, as their players are those of the game servers.
Sightings move with a server to another node and go with a deleted server. Users only see the sightings on the
servers they may see.

## Actions

Kick, ban (with a reason), pardon, add to and remove from the whitelist, make operator and take it away, and turn the
whitelist on or off. A change goes to the player's server, the network or all servers, as chosen; bans go to the network
first. The notification tells on which servers a change failed and offers to try it again there. Kicks leave the
server: Velocity sends kicked players to another server of the network, BungeeCord disconnects
them. **Send to another server** moves a player within the network through the proxy (`send`). The agent reads the
proxy's answer, so the panel tells if the player isn't online or the proxy has no `send`. Velocity only answers if it
can't send, so the agent waits a second for that answer; rolling restarts don't wait. Players named `all` or `current`,
and on BungeeCord like a server of the network, can't be sent, as `send` would read their name as other players too.
**Send players elsewhere** in the [network's list of servers](networks.md#restart-server-by-server) moves all players of
a server at once, like a rolling restart does before a server restarts.
A ban suggests the reasons already in the ban list.

**Several players at once:** the tables of the Players page and of a server's **Players** tab select players with
their checkboxes, up to 50 at a time, and the bar at the bottom acts on them: message, kick, ban or whitelist the
players online, ban or whitelist those seen, and take those of a list off it. Their actions go where the players are
(online, seen or listed), to the network or to all servers, as chosen. Adding players to a list asks for their names,
several at once, and suggests those the servers know: online, seen, and those who joined a server, which the servers
keep in their cache of players (`usercache.json`), so that players who are offline can be whitelisted or banned without
typing their name.

**Temporary bans:** a ban lasts for good, 1 hour, 1 day, 1 week, 30 days or until a chosen time. The servers ban the
player at once and pardon them at the end, with a pardon that waits like the changes of stopped servers below; the lists
show when the ban ends. A server that doesn't run at the end pardons the player once it runs again, and an agent that
restarted still knows the time. Another ban, temporary or for good, or a pardon in the panel replaces the waiting
pardon; a ban in the game itself doesn't, so the pardon still lifts it at the end.

**Messages** go to players online, on the server they're on: privately in the chat, as the server's whisper like with
`msg` (`minecraft:tellraw`), as a title in the middle of the screen with an optional subtitle, or above the hotbar
(`minecraft:title`). The network's **Message** offers the same for all its players.

**Faces:** players show with their face, cut from their skin: of Java players from Mojang, of Bedrock players from
GeyserMC, which uploads their skins to Minecraft's textures server. The master fetches the faces and serves them to the
panel, so the browser never contacts Mojang or GeyserMC; players without a face of their own show the first letter of
their name. The master keeps up to 5000 faces for a day, players without one for an hour, and looks up at most 30 at
once and then one every 2 seconds, so a long list fills in over a few minutes.

## Stopped servers and waiting changes

Stopped servers get a change once they run again, so that a ban also reaches the servers of a network that are stopped.
The agent keeps the waiting changes in `noryx-pending-players.json` in the server's data, also the pardons at the end of
temporary bans, with their time. It tries them every 5 seconds once they are due; a server that doesn't answer a change
within 15 seconds keeps it for the next try, and holds up neither the other servers of its node nor their lists. Agents
of older versions change one player per call and know no temporary bans: a temporary ban on their servers fails with a
note to update the agent, rather than ban for good.

## How it works

The agents run Minecraft's own commands (`minecraft:ban` and so on) through the server's console port, so the lists stay
in the server's files, also where plugins replace the commands, and read the lists from those files. Names are checked
before they become part of a command: 16 letters, digits and underscores, or Floodgate's dot before.

## Bedrock players

Bedrock players have Floodgate's dot before their name. The servers behind the proxy can't look them up, so the
whitelist gets them with the ID Floodgate gives them, which the master asks GeyserMC's global API for: GeyserMC knows
the players who joined a server with Geyser before. The agent writes them into `whitelist.json` and makes the server
reload it (`whitelist reload`); stopped servers get the change once they run. Other actions work by name once a player
joined the server.

## Permissions

Acting on players needs the permission to manage players on each server; making operators also needs the permission to
send console commands, as operators may run any command in the game. Messages need the permission to send console
commands on each server, like the network's **Message**. Sending players to another server of a network needs the
permission to manage the players of its proxy.
