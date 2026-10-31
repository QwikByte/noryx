# Development

How to build, run and release Noryx. [AGENTS.md](../AGENTS.md) maps the code and lists the rules for changes, for AI
agents and people alike.

## Running locally

Requirements: Go 1.27, Node.js 22. Nodes need Docker, or Podman 4.9 or newer as root (`serve --runtime podman`).

`scripts/ubuntu-test.sh` sets up a complete test environment on Ubuntu (desktop, server or WSL): it installs Go, Node.js
and Docker when missing, builds everything, starts master and agent and can create a Paper test server.

To run the parts yourself:

```sh
go run ./cmd/noryx-master --data-dir .data/master user add admin   # once, prompts for a password
make dev-master                                                    # API on :8080, enrollment on :9443
make dev-web                                                       # panel on http://localhost:5173
```

In the panel, add a node with the agent address `127.0.0.1:7443` and enroll the agent with its join token, which the
panel shows under "Installed the agent another way?":

```sh
go run ./cmd/noryx-agent --data-dir .data/agent enroll <join-token>
go run ./cmd/noryx-agent --data-dir .data/agent serve --listen 127.0.0.1:7443
```

## Commands

| Command         | Purpose                                                                        |
| --------------- | ------------------------------------------------------------------------------ |
| `make build`    | Builds the panel and both binaries into `bin/`                                 |
| `make test`     | Runs all Go tests, including the end-to-end test, and the panel's unit tests   |
| `make lint`     | golangci-lint, oxlint, the translation checks and the TypeScript type check    |
| `make generate` | Regenerates the gRPC code after changing `api/**/*.proto`                      |
| `make packages` | Builds the packages and archives of a release into `dist/`, without publishing |

Tests that run containers need root and only run with `NORYX_DOCKER_TEST` set: with
`NORYX_DOCKER_TEST=1 go test ./internal/agent/runtime/docker`, datastores run in Docker, and with
`NORYX_DOCKER_TEST=podman` in Podman, along with `TestPodmanLive`, which checks what Podman does differently.

The panel's unit tests use [Vitest](https://vitest.dev). They sit next to the modules they test, as `*.test.ts` or
`*.test.tsx`, and run in Node without a browser: `npm test` in `web` runs them once, `npx vitest` again on every change.

A smoke test in `web/e2e` signs in to the panel in Chromium with [Playwright](https://playwright.dev) and opens the
overview and the console of a server. `TestPanelInBrowser` in `internal/e2e` runs it against a master with the panel
built in and a fake node, only with `NORYX_PANEL_TEST` set. `CHROMIUM_PATH` makes it use another Chromium than
Playwright's own, e.g. that of the system:

```sh
(cd web && npm ci && npm run build && npx playwright install chromium)
NORYX_PANEL_TEST=1 go test -tags ui -run TestPanelInBrowser ./internal/e2e
```

## Windows

Noryx runs on Linux. On Windows, develop in WSL 2, e.g. with `scripts/ubuntu-test.sh`. Natively, only the master builds
and runs, e.g. for `make dev-web`; the agent and `go build ./...` need `GOOS=linux`, which also checks the code with
`GOOS=linux go vet ./...`. The tests need Linux, as some check file permissions and Unix sockets, and `npm run lint` may
fail on `i18next-cli`, whose native SWC binding doesn't load on every Windows; `oxlint` and `tsc -b` run.

## Test machines

`scripts/deploy-dev.sh --master root@vm1 --agent root@vm1 --agent root@vm2` builds the panel and both programs of the
checkout for Linux and installs them over SSH on machines with Noryx installed, restarting their services; servers keep
running. Turn off **Check for updates** in the panel's settings, so that it doesn't offer the latest release as an
update of the test build.

## Translations

The panel's texts are English and serve as the keys of their translations ([i18next](https://www.i18next.com)):
components show them with `t("…")`, or `<Trans>` for texts with markup, and `msg("…")` marks those outside of
components. `npm run i18n` in `web` lists them in `web/src/locales/en.json` and adds new ones to the other languages,
e.g. `de.json`, untranslated: they are empty there and show in English until someone translates them. `make lint` fails
while the files are out of date or a component shows a text without `t`. A new language is a copy of `en.json` with the
texts translated, named after its code with a script or region if it needs one, e.g. `pt-BR.json`. Add the code to
`locales` in `web/i18next.config.ts` and to `Languages` in `web/languages.go`, which the master accepts as users'
languages; the language menu offers it then.

## Releasing

The release workflow tests, builds the panel and both programs with [GoReleaser](https://goreleaser.com) and creates a
draft of a GitHub release with the packages, archives, checksums, attestations and the installer, which installs the
version it belongs to. A second job signs `checksums.txt` with the secret `RELEASE_SIGNING_KEY` and publishes the
release. Start the workflow under **Actions → Release → Run workflow** with a version `vX.Y.Z`, or `vX.Y.Z-rc.N` for a
pre-release: it tags the newest commit of `main`. Pushing such a tag runs it too:

```sh
git tag v1.2.0 && git push origin v1.2.0
```

### Release notes

The notes of a release are the **Release notes** sections of its pull requests, which the [pull request
template](../.github/pull_request_template.md) asks for: one line per change that administrators notice, starting with
`New:`, `Improved:`, `Fixed:` or `Security:`. `scripts/release-notes.sh` collects them from the pull requests merged
since the previous release (for a pre-release, since the previous tag) and groups them by these kinds; lines without a
kind, pull requests without the section and updates of dependencies end up under **Other changes**, and an empty section
adds nothing. Edit a pull request's description before releasing to change its lines. To see the notes of a tag, run
`scripts/release-notes.sh v1.2.0` with `gh` signed in. The panel shows them in **What's new** of its update notice.

### Release key

Installers only accept releases signed with the Ed25519 key in `RELEASE_KEY` of `packaging/install.sh`, and the second
job fails if the secret doesn't belong to it. A new key, e.g. for a fork, is created with
`openssl genpkey -algorithm ed25519 -out release.pem`, and `openssl pkey -in release.pem -pubout -outform DER | base64`
prints its value for `RELEASE_KEY`. Installations only learn of a new key through an update, so the first release that
has it in `RELEASE_KEY` is still signed with the old one.
