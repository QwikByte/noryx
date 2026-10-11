# The panel

How the admin panel is organised, and what works the same on all of its pages.

## Overview

The **Overview** is the panel's start page, unless a user chooses another one. Below its title it says whether everything runs or how many things need
attention, and then shows the players online, the servers by state, the nodes with what they use, the networks, the
servers with the most players, and what needs attention: crashing and unhealthy servers, offline nodes,
nodes with more memory assigned than they can give, servers and nodes beyond a threshold of their usage, e.g. almost
full storage ([Warnings](monitoring.md#warnings)), nodes whose certificate expires within two weeks, proxies that are
stopped while their servers run, and backup jobs and schedules whose last run failed. It counts like the **Nodes** page:
servers that run, not those that start or crash, and the memory assigned against what the online nodes can give their
servers, after the reserve. Sizes are in binary units (MiB, GiB).

A node's certificate is renewed a month before it expires while the node is online; one that stays offline until then
has to be connected again with a new join token. The master stores when a node's certificate expires as it issues or
renews it and when the node presents a newer one, so it warns about an offline node also after the master restarted.
Of a node that was offline when the master was updated to store it, it learns it once the node is online again.

The Overview is made of widgets: key figures, the server map, what needs attention, the nodes with their CPU of the last
24 hours, the load of all nodes over the last 24 hours, pinned servers, networks, the servers with the most players, the latest
entries of the log, the next runs of backup jobs and schedules with those whose last run failed first, and quick
actions to create a server or a network or add a node. Each user only gets the widgets
their permissions allow. **Customize** arranges them: widgets are dragged by their handle to the place of another, or
moved a place with the arrow keys on it, span one, two or all three columns, and are hidden and added again; **Reset**
brings back the default layout. The master keeps the layout for each user, like the language, so it applies in all their
browsers. The players online and the CPU load of the key figures show how they went while the panel is open. The
server map shows each node in a row with its servers as blocks in the colour of their state, solid while they run and
hollow while they are stopped; a block names its server when pointed at and opens it.

While customizing, the gear of a widget holds its options, which apply right away: how many servers **Most players**
lists, how many entries **Recent activity** shows and from which level, and how many next runs **Schedules** lists (5,
10 or 20, the latter two also 8 as they do unless chosen), the measure and range of **Node load** (24 hours or 7 days),
and the nodes that the server map, **Nodes** and **Node load** show and the networks that **Networks** shows, all unless
some are chosen. **Node load** also keeps the measure chosen in its header. A widget whose chosen nodes or networks are
all gone shows all of them again. The master keeps the options with the layout and refuses options and values a widget
doesn't have; **Reset** takes them back too.

An item of **Needs attention** is hidden with the eye at its end, for the user only but in all their browsers: for an
hour, for a day, or until it changes, e.g. a node in maintenance. One hidden until it changes shows again once it
differs, e.g. a crashing server with another exit code, or once it is gone and comes back, e.g. a node that is offline
again after it was online; the panel notices that it is gone while the overview is open in one of the user's browsers,
and it shows again after a week in any case. The widget and the line below the title count what isn't hidden; **2
hidden** lists the hidden items with how long they stay hidden, and shows each or all of them again. The master keeps
up to 100 hidden items for each user, as what they are about, e.g. `offline/<node ID>`, until when, and a fingerprint of
how they were rather than their texts, and drops those whose time has passed; `PUT /api/preferences/hidden` sets them.

## Navigation and pinned servers

Servers are pinned with the pin on their card, in the table or on their page. Pinned servers are listed with their state
in the sidebar and in their widget, which also starts and stops them. The master keeps them for each user too, up to 20;
they follow a server that moves and go with a deleted server or a removed node. The sidebar lists the overview, servers,
networks, players and nodes, the **Library** (templates, file sets, plugins and mods) and the **Automation** (backup
jobs, schedules and workflows), each with its parts as tabs, the log and settings at its foot, and the user's name below
them, whose menu holds their account, the colour theme, the language and signing out. The sidebar is dark in both colour
themes and folds to its icons, which each browser remembers. The bar above each page shows where it is, e.g.
**Servers › lobby › Files**, with links to the parts above it, and holds the search, the operations and the bell of
warnings and errors. On small screens, the bar's menu holds the sidebar. Lists, figures, charts, tabs and pages are animated, unless the operating system or the user asks for less motion.

## Settings of each user

Each user chooses on their account page, in the menu of their name, the colour theme, the accent colour, the density and
the language of the panel; the menu itself offers the colour theme and the language too. The accent gives buttons, links
and highlights their colour: emerald, blue, violet or graphite, each readable in both themes (WCAG AA), while the colours
of states, such as running or failed, stay. The compact density tightens all spacing, e.g. for long lists of servers, on
screens used with a mouse or touchpad; touch screens keep the comfortable one.

**Navigation and layout** chooses the start page, one of the pages of the sidebar the user may see, where the panel opens
when it is loaded at its address and after signing in, while the sidebar's **Overview** stays the overview. It chooses
the tab a server opens on from lists, the search and other pages: its console, usage, files or settings, or else the
first tab the user may see; the **Console** tab and `console lobby` in the search still open the console. Pages keep a
width that reads well unless they fill wide screens, and move less if the user asks for it, whatever the operating
system says. Shortcuts of single keys can be turned off, see [Keyboard shortcuts](#keyboard-shortcuts).

**Servers** chooses whether **Stop** and **Restart** of a single server act right away, ask first or warn its players,
also only for servers with players; see [Settings and images](servers.md#settings-and-images).

**Times and dates** chooses whether times have 24 or 12 hours, their time zone (the browser's, or one chosen for all
browsers, e.g. UTC), whether times that tell how long ago something was, e.g. "5 minutes ago", show that or the date and
time, with the other one in their tooltip, and whether weeks start on Monday or Sunday, or as the language has it, e.g.
for the days of backup jobs and schedules. New schedules start in the chosen time zone. Changing these reloads the panel.

**Console, terminal and editor** chooses the size of their text, whether they stay dark or are light in the light theme
(with colours that meet WCAG AA, also those of Minecraft), whether each of them wraps long lines, whether the console
shows when each line was written and how many lines it keeps, and how the editor indents and whether it takes Vim's keys;
see [Console](servers.md#console) and [File manager](servers.md#file-manager).

Users who may see the log choose which warnings and errors pop up and pause them, and turn on [desktop
notifications](monitoring.md#everywhere-else) in each browser. **Exports** chooses whether CSV files of tables and of
the log separate their cells with commas or semicolons, which spreadsheets in languages with a decimal comma, e.g.
German, expect and which the language chooses unless the user does, and whether they start with a byte order mark,
which Excel needs to read umlauts.

The master keeps these choices for each user, as well as what they chose last of lists where the address doesn't say,
e.g. the view, sort and grouping of server lists, the tab of [players](players.md), the sort of plugins and the range of
[usage](monitoring.md#history), and the default level of the [log](monitoring.md#logs-page), so that they apply in all
their browsers. Each browser remembers the look and the formats of times it showed last and uses them until someone
signs in; what a user never chose follows the browser, or the language, colour theme, accent colour, density and time
format that the settings give users as defaults ([Settings](administration.md#general)). `GET /api/preferences` returns a user's
settings with the layout of their overview, their pinned servers and what pops up for them; `PATCH
/api/preferences/settings` changes some of the settings, e.g. `{"theme": "dark"}` or `{"timeZone": "Europe/Berlin"}`, or
takes one back to the browser's with `null`, and `PUT /api/preferences/alerts` sets what pops up. The master refuses
settings and values it doesn't know.

The master also keeps the columns of each user's tables of servers, the groups of server lists they folded away and
the views of the **Servers** page they saved ([Lists, tags and bulk actions](servers.md#lists-tags-and-bulk-actions)).
`PUT /api/preferences/folded` replaces the groups, up to 100, and `PUT /api/preferences/views` the views, up to 20 with
names of up to 48 characters; the master checks every value of their searches as the panel checks the address.

## Search

**Ctrl+K** (⌘K) or `/` searches servers, also by tag and notes, players online, networks, nodes and pages from anywhere
in the panel, and, once something is typed, players seen before, templates, file sets, backup jobs, schedules and users;
each only for those who may see them. Before anything is typed, it offers what was opened last: servers, networks,
nodes, templates, file sets, backup jobs and schedules, wherever they were opened. Each browser remembers them for each user, and only those the user
may still see are offered. A search that starts with an action, e.g. `restart lobby`, starts, restarts or stops a server,
asking first or warning its players if the user chose so, or opens its console. **Create server**, **Create network** and
**Add node** open their dialogs from the search too. The views of the **Servers** page that the user saved show there as
well.

## Lists

Other lists are searched and sorted like those of [servers](servers.md#lists-tags-and-bulk-actions). The **Nodes** page
shows nodes as cards or as a table, the table from 13 nodes on until one is chosen, searches them by name, address, host
name, system and version of the agent, and sorts them by name, state, servers, or the share of their CPU and memory in
use, either way round; **Export CSV** downloads them as listed. **Networks** are searched by their names and those of
their proxies and servers, and sorted by name, players or servers. The **Users** tab searches users by name and group,
filters them by group and state, active, invited, disabled or without two-factor authentication, and sorts them by name
or by when they were added. Templates, file sets, backup jobs, schedules and workflows have a search. The address keeps
the search, filters, sort and view of each list, so that it can be shared or bookmarked; where it doesn't say, the view
and sort a user chose last apply, in all their browsers.

## Keyboard shortcuts

Shortcuts are keys typed one after the other, e.g. `g` and then `s` for the servers. `?` lists those the user may use.
They do nothing while the focus is in a field or the editor, or while a dialog is open; only **Ctrl+K** works there too.
Users who don't want shortcuts of single keys, e.g. as they get in the way of speech input (WCAG 2.1.4), turn them off on
their account page; **Ctrl+K** still searches then, and the list of shortcuts in the search says how to turn them on.

| Keys                              | Opens                                                  |
| --------------------------------- | ------------------------------------------------------ |
| **Ctrl+K** (⌘K), `/`              | the search                                             |
| `?`                               | the list of shortcuts                                  |
| `g o`, `g s`, `g n`, `g p`, `g m` | the overview, servers, networks, players and nodes     |
| `g t`, `g f`, `g e`               | templates, file sets, and plugins and mods             |
| `g b`, `g c`                      | backup jobs and schedules                              |
| `g l`, `g ,`, `g a`               | the log, the settings and the user's account           |
| `c s`, `c n`, `c m`               | **Create server**, **Create network** and **Add node** |

## Operations

Long actions run as **operations**: creating, copying and changing servers, stopping and restarting them, updating their
image or modpack, installing plugins on many servers, backing up and restoring, and the actions on networks and on many
servers at once. The panel shows their steps as they go, e.g. how much of a server image is downloaded or how many
servers of a network are configured. A dialog can't be closed by mistake meanwhile; **Continue in the background** hands the
operation to a notification, which follows it to its end and links to its result. The operations of the last hour, also
those of other users, are in the list behind the button next to the warnings. The master runs them in the background:
an answer comes right away if the action ends within a second, otherwise `202 Accepted` with the operation, which
`GET /api/operations/{id}` follows, so that neither a closed browser nor a proxy in front of the master cuts it off. The
master can't restart while one runs. Agents tell the progress of their part, e.g. the bytes of a download, through
`ProgressService`; agents of older versions only let the panel show the steps.

Operations whose steps can stop safely show **Cancel** in their dialog, notification and list: creating and copying
servers, backing up, installing plugins, applying file sets, updating a modpack until the server stops for it, and the
actions on players and on many servers at once (`POST /api/operations/{id}/cancel`). The operation stops at its next step that can stop, calls to the agents stop with
it, and it ends as **cancelled**; one that was done before it noticed ends as done. A cancelled creation or copy of a
server leaves no server; once a new server has its modpack, it stays, as installing its plugins can't be cancelled. An
action on many servers begins no more servers, but finishes on those it began, so that e.g. no restart is cut short and
leaves a server stopped. Applying a file set can be cancelled while it writes the files, but not once it restarts
servers. Restoring backups, moving servers, stopping and restarting them, changing their settings or image, and the
actions on networks and datastores can't be cancelled, as they must finish once they began; only while a stop or
restart still [warns the players](servers.md#settings-and-images) can it be cancelled, which stops nothing. Whoever started an
operation may cancel it, and so may others who may do the same, e.g. back up the server; for a file set, those who may
change the files of all servers.

Actions on many servers tell how they ended on each: installing plugins, applying a file set, and the actions on players
and on many servers at once. **Retry the failed ones** tries the same again on the servers where it failed.

## Languages

The panel speaks English and German. It follows the browser, or the default language of the settings, until someone
chooses a language in the menu of their name or on their account page, which the panel stores for the signed-in user, so
that it applies in all their browsers; on the sign-in page, the button next to the colour theme chooses it for the
browser. Dates, times and numbers follow the
language too. What the master and the agents send, such as errors, the log and the descriptions of permissions, stays
English.

## Keyboard and screen readers

Each page names itself in the title of the browser's tab, e.g. `Files · lobby · Servers · Noryx`, which ends with the
name the settings give the panel ([Settings](administration.md#general)). The first press of
Tab shows **Skip to content**, which jumps past the navigation. Opening another page moves the focus to its heading, so
that screen readers announce it; switching the tabs of a page or changing its search leaves the focus where it is.
Choices in a row, such as the time range of charts, the view and state filter of servers or the colour theme, are one
stop of Tab at the chosen option, and the arrow keys, Home and End choose another. On touch screens, small icon buttons,
e.g. the pin of a server, grow to 40 px.
