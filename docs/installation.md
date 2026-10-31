# Installation and operation

Installing, updating and removing Noryx, and where it keeps its files.

## Supported systems

Releases contain packages for Debian, Ubuntu and their derivatives (`.deb`), Fedora, RHEL, Rocky Linux, AlmaLinux and
openSUSE (`.rpm`) and Arch Linux, each for x86_64 and arm64. Only current Linux systems with systemd and OpenSSL 3 are
supported, e.g. Debian 12, Ubuntu 22.04, RHEL, Rocky Linux and AlmaLinux 9, openSUSE Leap 16 and newer. The installer
picks the package, checks the signature of the release and the package's checksum and sets everything up. Running it
again updates, and `--version vX.Y.Z` installs a certain release. While another installation of packages runs, e.g. the
automatic updates of a new server, it waits up to 10 minutes and says so. It asks questions only when started from a
file as below; piped to `sudo bash`, it uses the defaults instead, because `sudo-rs`, the `sudo` of newer Ubuntu
releases, doesn't pass on what is typed then.

## Master

The master serves the panel:

```sh
curl -fsSLO https://github.com/QwikByte/noryx/releases/latest/download/install.sh && sudo bash install.sh master
```

It asks for the host name or IP address under which the nodes reach this machine (`--public-host`), for the IP address
and port the panel listens at (`--panel-addr`, `127.0.0.1:8080` by default, `0.0.0.0:<port>` for all interfaces, ports
from 1024 on), and for the password of the first administrator, `admin` unless `--admin` names another (piped, it
generates one and writes it to `/etc/noryx/admin-password`, which only root can read). The password needs at least 12
characters and isn't shown while you type it. These options only apply to a new installation; later, both addresses can
be changed in the panel's settings.

Open port 9443 for the nodes, and the port of [SFTP](servers.md#sftp) for its users if you turn it on in the settings.
For plugins and mods, the master needs HTTPS access to `api.modrinth.com` and `cdn.modrinth.com`.

### HTTPS

Browsers only sign in over HTTPS or at `localhost`. Until the panel serves HTTPS, open it through an SSH tunnel, e.g.
`ssh -L 8080:127.0.0.1:8080 <user>@<master>` and `http://localhost:8080`. Then either turn on HTTPS under [**Settings →
General**](administration.md#general): a certificate of Let's Encrypt for a domain, or a self-signed one, which also
works for IP addresses but makes browsers warn. Or serve the panel with a reverse proxy, e.g. with
[Caddy](https://caddyserver.com) and this `Caddyfile`, which also gets the certificate:

```
panel.example.com {
	reverse_proxy 127.0.0.1:8080
}
```

Then add `--trusted-proxy 127.0.0.1` to `NORYX_MASTER_OPTS` in `/etc/noryx/master.env`, so that the master takes the
address of each client from the proxy's `X-Forwarded-For` header. Otherwise all clients share the proxy's address, and
with it the budget of the sign-in rate limit, and the log and the sessions of users show only the proxy's address.
A master that serves the panel over plain HTTP at an address other than loopback, with neither HTTPS nor a trusted
proxy, warns about it in its log at every start.

## Nodes

Add a node in the panel under **Nodes**. It shows a command that installs the agent in the master's version, offers to
install Docker if it's missing (`--install-docker` doesn't ask) or installs [Podman](#docker-or-podman) with
`--runtime podman`, connects the agent with a join token and starts it:

```sh
curl -fsSLO https://github.com/QwikByte/noryx/releases/download/<version>/install.sh && sudo bash install.sh agent --join <join-token>
```

Allow port 7443 only from the master's IP address. For the [private network](networks.md#private-network) of the nodes,
open UDP port 51820 (or the one in its settings) between the nodes.

**Remove node…** in a node's menu stops managing it, also while it is offline or lost. Its servers and datastores keep
running until they are stopped on the node or the agent is uninstalled. Servers of networks
[leave their networks](networks.md#deleting-and-removed-nodes) first, once confirmed.

### Docker or Podman

Each node runs all its servers and datastores with one runtime: Docker, or [Podman](https://podman.io) as root, which
the steps to add a node offer too. With `--runtime podman`, the installer installs Podman from the system's packages if
it's missing, starts its socket (`podman.socket`), lets `podman-restart.service` start the servers at boot and sets
the agent to Podman. Podman needs version 4.9 or newer, e.g. of Debian 13, Ubuntu 24.04 or RHEL 9, and a kernel with
the bridge support and the lookups of routes of nftables (`nft_meta_bridge`, `nft_fib_inet`), which current
distributions have.

Podman runs the same containers as Docker, from the same images, with the same users, capabilities, limits and networks
(see [Containers](security.md#containers)), and it needs no daemon of its own. The agent talks to its
Docker-compatible API and makes up for where Podman behaves differently, e.g. how it tells crashes and health. Plain
processes without containers aren't offered: the agent would have to download and check Java and the servers itself,
supervise and restart them, give each one a user of its own and build the isolation of containers again. Only Podman as
root is supported, as rootless Podman would lose what the agent relies on:

- It forwards published ports through a process of its own on the node, which the firewall of the private network sees
  as a connection to the node itself, which it refuses, so backends that only their proxy's node reaches would be cut
  off. Its default forwarder of bridge networks also hides the addresses of players, which bans and logs need.
- Its containers have no addresses on the node, at which the agent reaches their consoles, and the tables that keep
  servers apart would have to live in its own network namespace.
- It maps the users of containers to others on the node, so the agent couldn't read or hand over the data of servers
  without entering their user namespace, and the limits of memory, CPU and processes only apply if systemd delegates
  them to its user.
- The private network still needs root for WireGuard and its firewall.

To set up Podman by hand on a node whose agent is installed:

```sh
sudo systemctl enable --now podman.socket
sudo systemctl enable podman-restart.service
sudo systemctl enable --now noryx-isolate.service   # keeps the servers apart before Podman starts them at boot
```

Then add `--runtime podman` to `NORYX_AGENT_OPTS` in `/etc/noryx/agent.env` and run
`sudo systemctl restart noryx-agent`; `--runtime-socket <path>` names another socket than `/run/podman/podman.sock`.
The agent refuses a socket at which the other runtime answers, e.g. Podman behind Docker's socket through the package
`podman-docker`, a Podman before 4.9 and rootless Podman: calls then fail and say why.

The runtime is chosen per node, not per server, as each runtime keeps its servers apart with networks and firewall
rules of its own. A node doesn't take its servers along to another runtime: move them to another node first, then
change the runtime and move them back.

Where Podman behaves differently:

- Podman before 5.8 starts only containers whose restart policy is **Always** at boot, so the agent gives them that
  policy there: a server or datastore stopped in the panel starts again when the node boots. From 5.8 on, stopped ones
  stay stopped, like with Docker.
- At shutdown, `podman-restart.service` stops the servers. Raise its `TimeoutStopSec` like Docker's (see
  [Settings and images](servers.md#settings-and-images)).
- Podman before 5.8 tells the time of console lines in whole seconds, so a console that reconnects may show a line
  again.
- Podman keeps the output of a container in a single file, which starts empty once it reaches the limit of the
  [node's settings](servers.md#node-settings), so **Load earlier output** finds fewer lines right after.

The images of servers and datastores come from Docker Hub. To pull them through a mirror, e.g. a cache in your own
network, the agent needs no setting: set `"registry-mirrors": ["https://mirror.example.com"]` in Docker's
`/etc/docker/daemon.json`, or a mirror of `docker.io` in Podman's `/etc/containers/registries.conf`:

```toml
[[registry]]
location = "docker.io"

[[registry.mirror]]
location = "mirror.example.com"
```

How much output the runtime keeps of each container is a [setting of the node](servers.md#node-settings), which takes
the place of what `daemon.json` or `containers.conf` say about it, unless it leaves it to them.

## Everything on one machine

`sudo bash install.sh all` installs master and agent, registers the machine as node and connects its agent, which then
only accepts connections from the machine itself.

## Updates

The master looks for a new release every 6 hours, or as often as its settings say (every 1 to 24 hours). Administrators
then see a notice in the panel with the release notes and install it with **Install now**: the master installs the
release, restarts on it, and then updates every agent to its version, also the one on its own machine, unless
**Update the agents after the master** is off in the settings; then they keep their version until they are updated
node by node. Running services restart; Minecraft servers keep running. The panel of every signed-in user reloads once
the master runs the new version, unless that would lose something, such as unsaved changes in an editor or running
uploads; then it waits until they are done. Agents that are older than the master, e.g. as they were offline or their
update failed, are listed in the notice and in the **Agents** tab of the settings: **Update** next to one updates it
alone, **Update all** in the notice all of them. The master updates all agents by itself only after **Install now**, so
after updating it on its host, a release can be tried on one node first. The check can be turned off in the settings,
e.g. for a master without internet access. While GitHub's API limits the requests of the master's IP address (60 an
hour, shared with everything behind the same address), the master reads only the version from the release page, and the
panel links to the release notes on GitHub. On the command line, `… | sudo bash -s -- update` updates what is
installed; update the master first, then the nodes.

### Pre-releases

Pre-releases, e.g. `v1.3.0-rc.1`, try out the next release, e.g. on a test panel. A machine only gets them once its
administrator adds `--prereleases` to the options in `/etc/noryx/master.env` (`NORYX_MASTER_OPTS`) or
`/etc/noryx/agent.env` (`NORYX_AGENT_OPTS`) and restarts the service; the panel can't change them and only shows that
the master offers pre-releases. The master then offers the highest version among the 10 newest releases on GitHub,
pre-releases included, and **Install now** installs it, as the installer reads the option from `master.env` too and
checks the signature and checksums as for every release. Agents only install a pre-release with the option: without it,
their update fails and says so, and they keep their version until the next release. While GitHub's API limits the
requests of the master's IP address, the master only finds the latest release that isn't a pre-release, and installing
a pre-release fails until the API answers again. `install.sh … --version latest` also takes pre-releases where the
options of every program it installs name `--prereleases`.

## Files and logs

| Where                                           | What                                                                                           |
| ----------------------------------------------- | ---------------------------------------------------------------------------------------------- |
| `/etc/noryx/master.env`, `/etc/noryx/agent.env` | Options of the services, e.g. listen addresses or a log file; `systemctl restart` applies them |
| `/var/lib/noryx-master`, `/var/lib/noryx-agent` | Database and CA of the master; credentials, server data and backups of the agent               |
| `journalctl -u noryx-master`, `-u noryx-agent`  | What the services log; `--log-format json` in the options suits log collectors                 |

## Commands

### On the master's host

Commands run as the master's user `noryx`: `sudo -u noryx noryx-master logs` shows the log, `… user add <name>` creates
an administrator, e.g. after losing access, `… node add <name> <agent-address> --public-enroll-addr <host:port>` adds a
node and prints its join token for scripts, `… backup <file>` saves the master's database and CA, and `… restore <file>`
restores them from an archive of it or of the backup job of the master while the master is stopped (see [Backing up the
master](automation.md#backing-up-the-master)).

### On a node

`sudo noryx-agent status` checks the node, `server logs <id>` follows a console, `datastore logs <id>` the log of a
datastore and `logs -f` the agent's own log.
`backup list <id>`, `backup create <id>` and `backup restore <id> <backup-id>` work while the master is unreachable too.
`storage add ssd /mnt/ssd/noryx` allows another directory for server data, e.g. on a faster disk; new servers can then
be created there from the panel, and backup jobs can keep their backups there. `overlay allow` lets the panel add the
node to the private network of the nodes, `overlay deny` takes that back, and `overlay status` shows its address,
peers, firewall rules and the ports it publishes there.

## Removing

`apt remove`, `dnf remove` or `pacman -R` with `noryx-master` or `noryx-agent` stops and removes a program but keeps its
data. Delete `/var/lib/noryx-master`, `/var/lib/noryx-agent` and `/etc/noryx` to remove that too. The Minecraft servers
of a node keep running in Docker or Podman; delete them in the panel before.

## Installing by hand and verifying releases

Each release also has `.tar.gz` archives with the static binary, its systemd unit and its options, for other
distributions: the unit expects the binary in `/usr/bin`, the options in `/etc/noryx` and, for the master, a system user
`noryx`. `checksums.txt` lists the SHA-256 checksums of all files, and
`gh attestation verify <file> --repo QwikByte/noryx` proves that a file was built by the release workflow.
`checksums.txt.sig` is the signature of the checksums, which the installer checks with the release key. To check files
by hand, e.g. `install.sh` before the first installation, download `checksums.txt` and `checksums.txt.sig` too:

```sh
printf -- '-----BEGIN PUBLIC KEY-----\nMCowBQYDK2VwAyEA18ilyBW0qkWpfEqFR+rW5eQeGC3Sif4OiD8RKpEry5s=\n-----END PUBLIC KEY-----\n' > noryx-release.pem
openssl pkeyutl -verify -pubin -inkey noryx-release.pem -rawin -in checksums.txt -sigfile checksums.txt.sig
sha256sum -c --ignore-missing checksums.txt
```
