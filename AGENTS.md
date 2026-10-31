# Guide for AI agents

Read this before you change Noryx. What the programs do is described for users in [docs/](docs/), and
[docs/security.md](docs/security.md) describes the guarantees every change has to keep.

## Project

Noryx manages Minecraft servers and whole networks from one admin panel. It consists of two programs:

- `noryx-master` serves the panel and its REST API, keeps the SQLite database and its own certificate authority, and
  controls the agents over gRPC with mutual TLS.
- `noryx-agent` runs on every dedicated server and runs its Minecraft servers in Docker. It only obeys the master and
  its local CLI on a Unix socket; never add another way to control it.

## Working rules

- Organise code by feature, not by layer: the master, the agent and the panel each keep a feature in one package or
  folder. Clean up what doesn't fit.
- Plan before you write code. When something is unclear, ask instead of guessing.
- Keep code as short and clear as it can be: rewrite 100 lines that could be 40.
- Think about security before you implement anything: where input comes from, who may send it and what a compromised
  master, agent or server could do with it.
- Use current versions of languages, libraries and APIs.

## Layout

```
api/noryx/v1/          gRPC contract between master and agent, its generated code and the rules both sides check
cmd/                   main packages of noryx-master and noryx-agent
internal/              shared by both programs:
  pki/                 CA, certificate issuing, mTLS configurations
  enrollment/          join token format
  agentcli/            commands that control a running agent, for its local CLI and the panel's terminal
  logging/             log setup: console, rotating file, attributes with a meaning, notes of requests
  buildinfo/           version and repository of the build
  e2e/                 end-to-end tests over real mTLS, with a fake runtime and a fake Modrinth
internal/master/
  app/                 wiring, HTTP and gRPC listeners, CLI
  auth/                accounts, passwords, setup links, two-factor authentication, sessions, sign-in
  access/              permissions, groups with scopes, user management, the permission every API route needs
  ratelimit/           throttles attempts per key, e.g. sign-ins per client
  https/               HTTPS of the panel: Let's Encrypt or a self-signed certificate
  preference/          each user's preferences of the panel: the layout of the overview and pinned servers
  settings/            settings of the master that the panel changes, and a description of the running master
  terminal/            runs the commands of the master and of the agents for the panel
  node/                node registry, enrollment, agent connections
  overlay/             private WireGuard network of the nodes: members, addresses, peers, keeping them configured
  update/              looks for new releases, updates the master through systemd and the agents after it
  server/              server API, forwarded to the node's agent
  tag/                 tags of servers, which the panel finds and groups them by
  operation/           long actions run in the background, with their steps and progress, and the API that follows them
  network/             networks of servers behind a proxy, applied through the agents; actions on their servers,
                       rolling restarts, maintenance, Bedrock players and the settings of proxies
  player/              kicks, bans, whitelists and operators on many servers at once, their joined lists, sending
                       players to another server of a network
  files/               file manager, streamed between the browser and the agent
  properties/          server.properties editor
  plugin/              installs, lists and removes plugins and mods of servers, from Modrinth and Hangar
  modrinth/            client for the Modrinth API and CDN
  hangar/              client for the Hangar API and CDN, with projects and versions shaped like Modrinth's
  geysermc/            client for GeyserMC's download server (Floodgate) and global API (IDs of Bedrock players)
  s3/                  client for S3-compatible storage, signed with AWS Signature Version 4; s3test/ fakes a bucket
  modpack/             creates servers from Modrinth modpacks: checks a pack and writes its files into the server
  template/            templates for new servers
  fileset/             file sets: versions, targets, secrets, variables, the state of servers, previews and applying
  datastore/           datastores of networks: databases and their passwords, ports in the private network, the
                       addresses servers reach them at, dumps, upgrades, rotating passwords and browsing tables
  schedule/            tasks that run on servers or nodes at set times: storage, scheduler, REST API
  backup/              backups of servers, backup jobs as scheduled tasks, and the copies of their backups in
                       S3-compatible storage or on other nodes, with the storages for them; backups of the master: its
                       archive, encrypted with a passphrase, restoring it, and its backup job
  policy/              the panel's schedules (policies in the API): restarts with warnings, stops, starts, commands
  workflow/            workflows: triggers, steps with conditions, loops and variables, templates of data, runs and
                       webhooks
  database/            SQLite, embedded migrations, transactions and the helpers of rows that features share
  logs/                log in the database, logging of API requests, collecting the agents' logs, REST API, CLI
  notify/              notifications of new log entries by rules to Discord, Slack, webhooks and mail, with a dialer
                       that only reaches public addresses
  usage/               history of what nodes, servers and datastores use, from the agents' measurements, REST API
  httpapi/             JSON helpers
internal/agent/
  app/                 wiring, listeners, local CLI
  enroll/              enrollment client
  node/                machine info, certificate renewal, updates of the node
  server/              server lifecycle and input validation
  storage/             storage locations allowed for server data
  overlay/             the node's part of the private network: WireGuard interface, keys, checks, nftables table
  datadir/             confined access to a server's data, owned by the server's user
  archive/             extracts untrusted ZIP and .tar.gz archives: limits, links, protected paths
  secrets/             keeps the secrets in the data of servers from the panel: hidden files and <hidden> values
  rcon/                console connections to game servers (RCON), one per server
  network/             configuration of proxies and game servers for networks, the settings of proxies, Geyser's
                       configuration, and the Maintenance plugin
  player/              kicks, bans, whitelists and operators with Minecraft's commands; changes for stopped servers
  files/               file access for the file manager
  fileset/             files of file sets on servers: secrets filled in, the manifest that hides them, their state
  properties/          reads and updates server.properties, keeping comments
  plugin/              plugin and mod files of servers
  backup/              backups of servers: selection, archives, restoring, and the store that keeps dumps,
                       copies of backups of other nodes and archives of the master too
  datastore/           datastores of networks: databases and users, dumps loaded as their users, tables shown read
                       only, the local CLI's part
  logs/                latest log entries in memory, log service, logging of every call
  grpcerr/             turns errors into the gRPC codes and texts that all services answer with
  progress/            tells the master the progress of calls, e.g. downloading an image, through ProgressService
  stats/               measures what the node and its servers use: CPU, memory, network, data, players, TPS
  runtime/             runtime interface, also for datastores; docker/ implements it, runtimetest/ keeps datastores
                       in memory for tests
web/                   admin panel (React, Vite, Tailwind CSS, shadcn/ui), embedded into the master with -tags ui
  src/features/        one folder per feature with its api.ts, pages and dialogs
  src/components/      shared components; ui/ holds the shadcn/ui components
  src/lib/             helpers: API client, formatting, i18n, theme
  src/locales/         translations of the panel's texts
  e2e/                 smoke test of the panel in a browser (Playwright), which TestPanelInBrowser in internal/e2e runs
docs/                  user documentation
packaging/             installer, systemd units, options and package scripts; .goreleaser.yaml builds releases
scripts/               test environments, deploying test builds, release notes
```

## Checks

Run what CI runs before you push:

| Command                                                                      | Checks                                                             |
| ---------------------------------------------------------------------------- | ------------------------------------------------------------------ |
| `go test -race ./...`                                                        | all Go tests, including the end-to-end tests                       |
| `golangci-lint run ./...`                                                    | Go code (CI uses golangci-lint v2.14.0)                            |
| `cd web && npm run lint && npm run build && npm test`                        | oxlint, the translations, TypeScript, the build and the unit tests |
| `NORYX_PANEL_TEST=1 go test -tags ui -run TestPanelInBrowser ./internal/e2e` | the smoke test of the built panel in Chromium                      |
| `make generate`                                                              | the generated gRPC code; CI fails while it is out of date          |
| `shellcheck packaging/*.sh packaging/*/*.sh scripts/*.sh`                    | the shell scripts                                                  |

The tests need Linux. [docs/development.md](docs/development.md) explains how to run master, agent and panel locally.

## Conventions

- **Master and agent** talk only through `api/noryx/v1`: change the `.proto` files, run `make generate`, and keep rules
  that both sides check next to the generated code. The agent validates every request itself.
- **Database:** add a migration as the next `internal/master/database/migrations/NNNN_name.sql`. Never change a released
  one: the master runs each migration only once.
- **REST API:** register routes with `access.Mux.Handle` and the permission they need. Terminal commands check their
  permission the same way.
- **Secrets** such as the RCON password, forwarding secrets, secrets of file sets and database passwords never reach the
  panel and are never logged.
- **Panel texts** are English and serve as translation keys: `t("…")`, `<Trans>` for texts with markup, `msg("…")`
  outside of components. Run `npm run i18n` in `web` and translate new texts in `web/src/locales/de.json`.
- **Panel tests:** test what a module of the panel decides with Vitest, in a `*.test.ts` or `*.test.tsx` next to it,
  which runs in Node without a browser.
- **Commits** start with one sentence in the imperative that says what changes, e.g. "Let a network change its proxy",
  with details in the body.

## Documentation

- The [README](README.md) stays short: what Noryx is, how to install it and where to read on. Don't describe features
  there.
- Describe a feature in its page under [docs/](docs/), and keep [docs/security.md](docs/security.md) true. A new page
  also goes into the table of the README.

## Pull requests and release notes

Fill in the [pull request template](.github/pull_request_template.md). Its **Release notes** section becomes part of the
notes of the next release, which administrators also read in the panel:

- One line per change they notice, starting with `New:`, `Improved:`, `Fixed:` or `Security:`. Write what they can do
  now or what no longer goes wrong, not how the code does it.
- Leave the section empty for refactoring, tests, CI, documentation and fixes of something not released yet.
- The panel renders the notes with a small Markdown renderer: use **bold**, `code` and links, but no nested lists,
  tables or images.

`scripts/release-notes.sh <tag>` prints the notes a release gets; it needs `gh` signed in.
