# Usage and logs

What nodes, servers and datastores use, and who did what.

## Usage

The panel shows what nodes, servers and [datastores](databases.md#usage) use, now and during the last week.

### Current values

Every agent measures every 5 seconds: CPU and memory of the node and of each server (without the page cache, like
`docker stats`), the network traffic of each server, the size of its data (every 5 minutes), the players online and the
ticks per second of Paper, Purpur and Leaf servers. The number of players comes from the status request that the server
list in the game sends too, which game servers and Velocity answer; BungeeCord and Waterfall are left out, as they log
every such request. The names of all players and the ticks per second come through the server's console port (`list`,
`tps`) over a connection that stays open, because servers log every new one. Game servers behind a proxy only listen
within the network of their proxy, where the status request can't reach them, so their console tells the number of
players too. A network counts the players of its game servers, like the **Players** page, or those its proxy counts
while the agents can't tell them, e.g. agents of older versions. Server cards show CPU, memory and players; the
**Usage** tab of a server and the node page show the rest. Agents measure the CPU, memory and connections of the
datastores of their node too, and the size of their data every minute.

### History

The master records the latest measurement of every agent each minute and keeps it for a week. Charts show the last 24
hours (averages of 5 minutes) or 7 days (averages of 30 minutes), with the most players and the largest size of the data
of each step, and a table shows the same values; the range and the view each user chose last apply again, in all their
browsers. Gaps are times in which a server didn't run or its node couldn't be reached. The history of a server moves and goes away with it, that of a datastore goes away with it. Thresholds and
warnings are only for nodes and servers.

### Warnings

When the master records a measurement, it checks it against thresholds, each with a value and how many minutes it has
to last (0 warns at once):

| Of      | Measure          | Relative to                                                     | Default             |
| ------- | ---------------- | --------------------------------------------------------------- | ------------------- |
| Servers | CPU              | the server's CPU limit, or all cores of its node if it has none | 90 % for 10 minutes |
| Servers | Memory           | the server's memory limit, which includes Java's overhead       | 95 % for 5 minutes  |
| Servers | Ticks per second | 20 at best, over the last minute; warns below the threshold     | 15 for 5 minutes    |
| Nodes   | CPU              | all cores of the node                                           | 90 % for 10 minutes |
| Nodes   | Memory           | the node's memory, without the page cache                       | 90 % for 5 minutes  |
| Nodes   | Storage          | each storage location: the space in use of its file system      | 90 % for 1 minute   |

The defaults are in the **Usage warnings** of the master's settings. A server has its own on its **Settings** tab and a
node on its page, measure by measure: **Use default** follows the settings, otherwise the threshold is the server's or
node's own, and each can be turned off. Changing them needs the permission to change the server's settings or the node;
whoever sees a server or node sees its thresholds. They move with a server to another node and go away with it; a copy
of a server starts with the defaults.

A measure that has been beyond its threshold in every measurement for its minutes is logged once as a warning (category
**Usage**, with the node and server), and once as an information entry when it is back within its threshold, e.g. "A
server uses too much CPU" and "A server uses less CPU again"; nothing in between. A value the agent can't tell, e.g. the
ticks per second while the console doesn't answer, changes nothing, but a measure that went unmeasured for more than 5
minutes, e.g. while its node was offline, starts counting its minutes again. A server that stops, a threshold that is
turned off and a node that is removed end their warnings without an entry. The master keeps this in memory: after it
restarts, a measure that is still beyond its threshold warns again once its minutes have passed. Agents older than the
master don't tell the CPU limit of a server, so until they are updated, its CPU counts against all cores of its node.

Warnings show up in the bell like every warning of the log, and **Needs attention** on the overview lists the servers
and nodes that are beyond a threshold now, with the latest value and how long it has lasted, to the users who may see
them. [Notifications](#notifications) send them to Discord, Slack, a webhook or by mail.

## Logs

The master keeps a log of what happens on it and on its agents, so that it's clear who did what and what went wrong.
Entries are kept for 30 days unless the settings say otherwise, but at most the newest million and 2 GiB (the
settings allow 100 MiB to 100 GiB), so that agents that log too much can't fill the master's disk. Every minute, the
master deletes the expired entries, then the oldest beyond these limits. When this deletes entries before their time,
it logs a warning, and warns again only after entries were kept for their whole time again. Then raise the limit, or
find out on the **Logs** page what logs so much.

The size of an entry counts its texts and its part of the indexes. SQLite reuses the space of deleted entries, so
`master.db` stops growing once the log reached its limit: the log takes about as much space as its limit with usual
entries, and at most about twice as much with entries of unfavourable sizes, as SQLite stores them in pages of 4 KiB.
The file doesn't shrink by itself, e.g. after lowering the limit; a
[backup of the master](automation.md#backing-up-the-master) holds a compact copy of the database, and restoring it
gives the space back.

### Actions

Every request of the panel that changes something, and every download of a file, folder, backup or export, is logged
with the user, the IP address, the node and server it concerned (with their names at that time), the outcome and how
long it took. Denied requests are logged as warnings, and failed ones as errors if the master or an agent failed.
Others, e.g. with an invalid input, are only information: the panel tells the user why. Sign-ins, failed sign-ins,
password changes, changes of two-factor authentication, sign-outs, enrollments, certificate renewals and what backup
jobs and schedules did on each server are logged too.

### Agents

An agent logs every call it receives with its origin (the master or its local CLI) and keeps its latest entries in
memory. The master collects them over the mutually authenticated connection and continues where it left off, also after
a restart of either. Calls that only read are logged at the debug level, downloads at the info level.

### Logs page

The **Logs** page lists the entries newest first, with new ones streaming in. Filters for the time, level, category,
source, node and a text search are part of the address, so a view can be shared. The time is the last hour, 24 hours,
7 days or 30 days, or **From … until …** two dates and times in the user's time zone, either of which can stay open;
one with an end gets no new entries. An entry opens to show all its details and narrows the list to its user, server or
category. Key figures and a chart show the warnings and errors of the last 24 hours; selecting an hour shows its
entries. **Show this level by default** below the filters makes the chosen level the user's default, which applies in
all their browsers where the address names no level, e.g. warnings and errors. The default shows as a chip, which shows
all levels when removed, and **Show all levels by default** takes it back. The entries that match are exported as CSV
or JSON lines; CSV files are separated by commas or semicolons and start with a byte order mark as the user chose
([Settings of each user](panel.md#settings-of-each-user)).
`GET /api/logs` and `GET /api/logs/export` take the time as `since` and `until`, RFC 3339 times of which `until` is
exclusive, and `GET /api/logs/export?format=csv` takes the separator and the byte order mark as
`separator=comma|semicolon` and `bom=on|off`.

### Everywhere else

A bell in the bar above each page counts the new warnings and errors, and new ones show up as notifications, except those of the
user's own actions. Among them are every crash of a server, servers that become unhealthy, servers and nodes that use
too much for a while ([Warnings](#warnings)), and nodes that go offline: the master checks the connections to the agents
every 30 seconds and logs a node that fails two checks in a row once, and once more when it is back. Servers have an
**Activity** tab and nodes an **Activity** section.

Each user can also turn on **Desktop notifications** on their account page, in each browser: while the panel is open in
a tab in the background, new warnings and errors show as notifications of the operating system, except those of the
user's own actions. Later ones replace the notification and count how many came since the tab was left, so that a burst
doesn't fill the screen; a click opens the log. The browser asks for its permission when they are turned on. Some
browsers, e.g. Chrome on Android, only show notifications of sites with a service worker, which the panel has none of.

**What pops up** on the account page chooses which of them show as notifications and on the desktop: warnings and errors,
or errors only, about everything the user may see, or only about their pinned servers and chosen servers, nodes (with
all their servers) and categories, the latter also for entries about no server, e.g. of sign-ins. **Pause pop-ups** in
the menu of the bell, or **Pause** on the account page, keeps all of them from popping up for an hour, 8 hours or a day,
and **Pop up again** ends it sooner. Deleted servers and removed nodes leave these choices. The bell lists and counts all warnings and errors either way. The master keeps these choices for each
user, so that they apply in all their browsers.

### Command line

`noryx-master logs` and the terminal's `logs` command show the log with the same filters, also as JSON lines and
following new entries with `-f`. `noryx-agent logs [-f]` shows an agent's own log, also while the master is unreachable.

### Console and files

Master and agent log to stderr as text or, with `--log-format json`, as JSON; `--log-level` chooses the least important
level (`info` by default) and `--log-file` also writes JSON lines to a file, which is rotated at 10 MB with 5 older
files kept.

## Notifications

Warnings and errors reach administrators who aren't looking at the panel, e.g. a server that keeps crashing or a node
that went offline. The **Notifications** tab of the settings sets them up, for administrators and those with the
permissions to manage notifications and to see the log for all servers, as rules send entries about every node and
server.

### Channels

A channel is where notifications go:

| Kind    | Sends                                                                                                    |
| ------- | -------------------------------------------------------------------------------------------------------- |
| Discord | a message with an embed per entry to a webhook of a Discord channel (its settings, **Integrations**)      |
| Slack   | a message with a block of plain text per entry to an incoming webhook of a Slack app                     |
| Webhook | the entries as JSON by `POST` to any URL, see [Payload of webhooks](#payload-of-webhooks)                |
| Email   | a plain text mail through a mail server, over TLS from the start (port 465) or after STARTTLS (port 587) |

URLs must start with `https://`, and channels only connect to public addresses, see
[Security](security.md#notifications). The URL of a webhook and the password of a mail server can't be seen again once
saved, and the log never names them: to change them, enter new ones; an empty field keeps them. A mail channel keeps its
password only while its server, port and user stay the same, so that nobody can send it to another server. **Send test**
sends a test message right away and shows why it failed, e.g. that the webhook answered `404 Not Found`; each channel
allows three tests at once, then one every 20 seconds.

Each channel shows when it last sent and why it last failed, since the master started. A message that fails is sent once
more after 30 seconds, then given up and counted in the next message. When a channel starts to fail, the master logs a
warning (category **Notifications**) once, and an information entry when it works again; the channel itself doesn't get
the entries about itself, so that it can't keep itself busy.

Entries can hold IP addresses, e.g. of sign-ins, and the names of players, which then leave the master; the panel says
so where channels are set up. Secrets aren't logged, so they are never sent.

### Rules

A rule sends the new entries of the log of at least a level (errors; warnings and errors; or information, warnings and
errors) to a channel: of the categories it chooses, or of all, and of a node or a server if it names one. Rules can be
turned off. An entry that several rules send to the same channel goes there once. The rules of a server follow it to
another node; deleting the server deletes its rules, and removing a node those of the node and its servers.

The master follows the log as entries are added, also those it collects from the agents, and sends only new ones: after
a restart, it starts with the entries added from then on. Entries that come within 5 seconds go in one message of at
most 10 entries, which counts the others. Each channel sends 5 messages at once, then one a minute at most, which
bundles what came meanwhile. So a server in a crash loop or many agents that warn at once can't flood a channel, and an
entry, e.g. of a node that went offline, reaches it within a minute. Messages name each entry's level, message, server
and node, category, user, error and time. Mails come from the name the settings give the panel and start their subjects
with it, e.g. `Noryx · Test: A server crashed …`, as do the notifications of Slack, so that those of two panels can be
told apart.

### Payload of webhooks

A webhook gets a `POST` with `Content-Type: application/json` and the `User-Agent` `Noryx/<version>`, and should answer
with a status of `2xx`; redirects aren't followed.

```json
{
  "test": false,
  "entries": [
    {
      "id": 4711,
      "time": "2026-10-08T12:00:00Z",
      "level": "warn",
      "source": "agent",
      "category": "servers",
      "message": "A server crashed and starts again; its console and crash reports tell why",
      "nodeId": "…",
      "nodeName": "node-1",
      "serverId": "…",
      "serverName": "Lobby",
      "attrs": { "crashes": "2", "exit_code": "1" }
    }
  ],
  "notSent": 0
}
```

`entries` are the entries in the form `GET /api/logs` returns them, oldest first; `user`, `nodeId`, `nodeName`,
`serverId` and `serverName` are left out when an entry has none. `notSent` counts the entries that matched but weren't
sent, as the message was full or sending failed before. A test has `"test": true` and no entries. The URL is the only
credential, so keep it hard to guess, e.g. with a random token in its path.
