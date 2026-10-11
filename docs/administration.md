# Settings, users and permissions

## Settings

The **Settings** page configures the master, gives an overview of the agents and manages who may do what. Each tab only
shows to users with the permission for it.

### General

The **General** tab shows the running master (version, uptime, addresses, CA fingerprint, certificates) and its
settings. The address the panel listens at (it replaces `--http-addr`; empty uses the flag again) and its HTTPS apply
when the master starts again, e.g. with **Restart master** or the next update; the page says so until then. Only
administrators change them, as they can open the panel to other networks. The address is checked when saved, and if the
master can't listen there when it starts, e.g. because another program took the port, the panel falls back to
`--http-addr` and shows why, so a wrong address can't lock you out. For HTTPS, the panel serves a self-signed
certificate for the IP addresses and names of the machine, the domain if one is set, and the host of the enrollment
address; it shows its fingerprint to compare with the browser's warning. Or it gets one from Let's Encrypt for its
domain, which has to point to the machine: Let's Encrypt checks it at port 443 of the panel or at port 80, where the
master then sends browsers to HTTPS. Until Let's Encrypt issued one, and for other names such as an IP address, the
panel serves the self-signed certificate, and the page shows why. With a certificate of the command line (`--tls-cert`),
these settings don't apply. **Restart master**, for administrators, stops the master and lets systemd start it again
(also reading `master.env` again), unless servers are moving to another node; Minecraft servers keep running. The other
settings apply right away: the enrollment address join tokens contain (it replaces `--public-enroll-addr`; empty uses
the flag again), how long join tokens are valid (5 minutes to a day, 1 hour by default), how long sign-ins to the panel
last (1 hour to a week, 12 hours by default), who has to use
[two-factor authentication](#two-factor-authentication), how long log entries and where players played are kept (1 day
to a year, 30 days by default) and how much space the log may take
([100 MiB to 100 GiB, 2 GiB by default](monitoring.md#logs)), the thresholds at which the usage of servers and nodes
warns ([Warnings](monitoring.md#warnings)), the port range and memory reserve that new nodes get, and whether the master
looks for updates. Administrators can also look for an update right
away.

**Name and notice** names the panel, e.g. `Noryx · Test` or a community's name, so that two panels don't look the same:
in the title of the browser's tab, the sidebar and the sign-in page, as the sender and at the start of the subjects of
mails and in the notifications of Slack, and in authenticator apps for
[two-factor authentication](#two-factor-authentication) set up from then on; entries in the apps already keep the name
they have. Empty means Noryx. The notice, e.g. whom to ask for access, shows below the form of the sign-in page. Both
are plain text, a name of up to 64 characters in one line and a notice of up to 500 with line breaks, and public: the
sign-in page reads them without a session, also from `GET /api/panel`.

**Defaults for users** chooses the language, colour theme, accent colour, density and time format of users who haven't
chosen them on their account page, instead of what their browser has; each can stay **From the browser**. They also
apply to users who are signed in, as soon as their panel loads them again, and on the sign-in page of browsers where
nobody chose them yet, e.g. a default accent for a test panel, so they are public like the name. Where the panel offers
the browser's language, it then offers **Default of the panel** with the default language. **Groups of invited users**
are the groups that **Invite user** preselects (see [Invitations](#invitations)).

**New servers** sets what [Create server](servers.md#creating-and-deleting) starts with: a template, which the dialog
chooses for those who may see templates, and else the software, the memory of game servers and of proxies (any amount
from 512 MiB to 64 GiB), the Java version of game servers, the stop timeout and the time zone. The master checks them
like the settings of a server. A template that is deleted later is ignored, and the other settings apply again. Those
who may create servers get these settings without seeing the others.

**Warnings to players** sets how the players of game servers are warned before their servers restart or stop: the
texts of restarts and of stops, with `{minutes}` for the minutes left, e.g. in the language of the players; where the
warnings show, in the chat (`say`), as a title or above the hotbar; the longest lead time of restarts and stops by hand
(1 minute to an hour, 10 minutes by default); and the minutes before at which those warn again (up to 5, 5 and 1 by
default). Restarts and stops by hand, also of many servers, use all of it, and
[schedules and workflows](automation.md) the texts unless they have their own, and where they show. The texts go to the
console of every server they warn, so changing them also needs the permission to send console commands to all
servers; the master checks them like a warning of one's own: one line of up to 200 characters, which fits the console
as the warnings show. Users who may restart or stop servers, or see schedules or workflows, see them as the
placeholders of warnings without seeing the settings.

### Agents

The **Agents** tab lists all nodes with their agent version, certificate and settings, which can be changed there too.
Administrators update an agent that is older than the master there with **Update** (see [Updates](installation.md#updates)).

### Terminal

The **Terminal** runs the commands of `noryx-agent` (`status`, `server …`, `backup …`, `datastore …`) on any node, and
the master's own commands: `status`, `node list`, `node renew <node>` and `logs`. `help` lists them; output streams in
as it happens, e.g. for `server logs <id>`, and Ctrl+C stops a command. Commands that write nothing for a while, e.g.
`server stop <id>` with a long stop timeout, keep the connection alive, so that a reverse proxy in front of the master
doesn't cut them off. A node's page opens its terminal directly.
Besides the permission to use the terminal, every command needs its own, e.g. `server restart <id>` the one to restart
this server.

**View** chooses the size of the text, whether long lines wrap, which they don't unless chosen, so that tables stay
aligned, and whether the terminal is light in the light theme, and **Fill the window** gives it the whole window until
Esc.

Tab completes the commands the user may run, their flags and their arguments: IDs of servers, also from the first
letters of their names, and of nodes, datastores, databases and backups. When several choices are left, a second Tab
lists them, e.g. servers with their names, and a click takes one. Tab in an empty line moves on to the next control.

↑ and ↓ repeat earlier commands, which the browser tab keeps for each node and the master until it is closed or the
user signs out.

### Notifications

The **Notifications** tab sends warnings and errors to Discord, Slack, a webhook or by mail, with rules that choose
them; see [Notifications](monitoring.md#notifications). It shows to administrators and those who may manage
notifications and see the log for all servers.

### Users and groups

The **Users** tab invites users, chooses their groups, disables and deletes them, creates setup links and turns off
two-factor authentication for users who lost their phone; it searches them, filters them by group and state and sorts
them ([Lists](panel.md#lists)). The **Groups** tab defines what their members may do, see
[Users and permissions](#users-and-permissions).

## Users and permissions

Users get their permissions from groups; a user can be in several groups and has the permissions of all of them.

### Fine-grained permissions

There are permissions for every action, by area: nodes (see, change, renew certificates, remove, add, manage the private
network, which applies to all nodes), servers (see, create, start, stop, restart, change settings, delete), console
(read, send commands), players (kick, ban, whitelist and make operators), files and configuration (browse and download,
change files, `server.properties`, plugins and mods), backups (see and download, back up and keep, restore, delete), the
log, notifications, networks, databases of networks, templates, file sets, backup jobs, schedules, the master's
settings, the terminal, users and groups. Previewing and applying a file set also needs the permission to change the
files of every server it touches, and restarting them the one to restart each. Choosing a permission also chooses what
it needs, e.g. seeing the servers one may restart.

### Scopes

The node and server permissions of a group apply to all servers, or only to chosen nodes (including servers created
later) and single servers, e.g. a group that may restart the lobby and use its console. Lists only show the nodes and
servers a user may see, and the log only the entries about them; entries about the master, users or groups need the
permission for all servers. Other permissions, e.g. for networks or schedules, apply everywhere, because they act on any
server.

### Administrators

The built-in Administrators group has every permission, also those that later versions add. Only its members see and
install updates; no permission allows that to other groups. `noryx-master user add` creates administrators, e.g. the
first one or after losing access.

### Invitations

New users get a setup link (valid for three days, usable once) to choose their password; the same link resets a
forgotten password. **Invite user** preselects the groups that **Settings → General** names, of those the inviter may
give; groups deleted since are left out, and the inviter can still change them, within their own permissions as always. The token is in the link's fragment, which browsers don't send to servers, and the panel removes it
from the address bar once it was read. Users change their own password on their account page (in the menu of their name
in the sidebar), which signs them out everywhere else.

### Sessions

The account page lists where the user is signed in: each session with its browser and operating system, as far as the
master recognises them in the browser's User-Agent, the address it was last used from and when, and when it started;
sessions that started before Noryx noted this show it from their next use on. **Sign out** ends one of them, e.g. in a
lost or shared browser, and **Sign out everywhere else** all but the current one. Neither needs the password, as they
only take rights away. Users only see and end their own sessions. Behind a reverse proxy, the addresses are those of the
clients only if `--trusted-proxy` names the proxy (see [Installation](installation.md)). The API lists the sessions with
`GET /api/auth/sessions`, ends one with `DELETE /api/auth/sessions/<id>` and all others with
`DELETE /api/auth/sessions`.

### API tokens

Scripts use the API with personal API tokens instead of a password, which users create and revoke on their account
page, with all of their permissions or fewer; see [REST API and API tokens](api.md). Changing the password, setting one
with a setup link, turning on two-factor authentication and disabling a user revoke the user's tokens.

### Two-factor authentication

Two-factor authentication is off until users set it up on their account page: they scan a QR code with an authenticator
app (TOTP, e.g. Google Authenticator, Aegis or a password manager), which names the entry after the panel's name of the
settings, confirm with a code of it and their password, and get 10 recovery codes that each replace a code once. Then signing in asks for a code after the password, and other
sessions end. A setup link then only sets the password; signing in still needs a code. Turning it off and new recovery
codes need the password. Users who may manage a user turn it off for them, e.g. after they lost their phone and recovery
codes, but not for themselves; without any administrator who can still sign in, `noryx-master user add` creates a new
one.

Under **Settings → General**, those who may change the master's settings require it for all users, also those invited
later, or for the members of chosen groups, e.g. the Administrators. Users it applies to who haven't set it up are sent
to set it up after signing in, or with their next click if they are signed in already, and can do nothing else in the
panel or its terminal until they did; they can still sign out, change their password and end their sessions. The API
answers their other requests with 403 and the code `mfa-setup-required`. Setting it up always works, so the requirement
locks nobody out: users whose two-factor authentication was turned off, e.g. after losing their phone, set it up again
when they sign in next, and so does an administrator created with `noryx-master user add`. Turning it off while it is
required leads straight to setting it up again, e.g. with a new phone. A group that is deleted no longer counts.
