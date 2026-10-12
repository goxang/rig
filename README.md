# rig

[![ci](https://github.com/goxang/rig/actions/workflows/ci.yml/badge.svg)](https://github.com/goxang/rig/actions/workflows/ci.yml)
[![release](https://img.shields.io/github/v/release/goxang/rig)](https://github.com/goxang/rig/releases/latest)

**The AI-integrated pilot for every service you run.** Manage every part of your software from the terminal:
services, infrastructure, data, Kubernetes, logs, metrics, tests. Local processes, docker or Kubernetes;
Go, Python, Node, Java, Rust or anything that runs. Describe it once in `rig.yaml`, then run, watch and tune
it all from one CLI and one terminal UI.

- **Plugin-based**: the core knows no technology. Docker, Kubernetes, SQL, Prometheus, GitLab, Jira and
  the rest are plugins, and anyone can write one: YAML, a shell script, or a Go, Python or TypeScript
  program, or any MCP server bridged in.
- **AI built in**: `@` on any screen chats about what it shows and can act on it; query prompts get AI
  autocomplete. Works with opencode (free models, no login needed), Claude Code, OpenAI or any
  OpenAI-compatible endpoint, Anthropic, DeepSeek and 9router.

## How it is built

```
 clients   CLI · TUI · MCP server · AI · HTTP/WS server (rig serve)
              │  all through one api.Engine: policy, jobs and audit apply to every caller
 kernel    catalog · sources (data frames) · actions (jobs) · views (widget trees) · events · policy
 engine    environments, dependency order, up/down, svc:// resolution, run state
 host      install, lock, trust, supervisor, sandbox; talks to plugins over rig/1 (JSON-RPC)
 plugins   rig-std (docker, kubernetes, sql, prometheus, ...) · gitlab · jira · terraform · yours
```

A plugin adds **sources** (data), **actions** (verbs, each with an effect: read, write or danger),
**views** (tabs and panels), **importers** and **contracts** (`metrics/1`, `scm/1`, ...: implement one and
rig's own screens use your plugin). See [design](docs/design.md) and [plugins](docs/plugins.md).

## What it does

- **Run**: up in dependency order, start, stop, restart, scale, build, deploy, exec, debug
- **Watch**: merged logs you can query by field, Grafana-style dashboards, traces, alerts
- **Data**: browse and edit databases, Redis, RabbitMQ, Kafka, key-value config; saved queries on a schedule
- **Kubernetes**: manifests as a graph, edited field by field, synced from the cluster; HPAs, namespaces
- **Test and load**: test suites with live results, load generators with rate and instances
- **AI**: `@` chat on every screen, inline query completions, `@?` plain-language queries; `rig mcp` gives
  agents every tool
- **Any stack**: not just Go; Python, Node, Java, Rust, any image or binary; `rig init` reads compose files,
  manifests and sources

## Install

```bash
curl -fsSL https://raw.githubusercontent.com/goxang/rig/main/install.sh | sh
```

Linux and macOS (amd64, arm64). With Go 1.26+: `go install github.com/goxang/rig/cmd/rig@latest`.

rig itself knows no technology. Runtimes, builders, databases, metrics and the rest come from the
official plugins in [goxang/rig-plugins](https://github.com/goxang/rig-plugins), prebuilt: no Go needed.
The first `rig` in a project installs the ones its `rig.yaml` names (for you, in `~/.config/rig`) and
the ones `rig.lock` pins (after one trust prompt). `rig upgrade` updates rig itself. To install the
everyday set by hand:

```bash
rig plugin add --user pack-std
```

A plugin that needs a token (Jira, Confluence, GitLab) shows it on its tab: press `S` there, or click
the banner, to paste the token; it is kept in `~/.config/rig`, never in the repository.

Joining a project that already has a `rig.yaml`: run `rig doctor` there. It names each thing still
missing on your machine (plugins, workspace trust, a credential such as `rig secret set JIRA_TOKEN`)
with the command that fixes it; `rig` shows the same list when it opens.

## Start

```bash
rig init      # rig.yaml from what the project has: compose, manifests, sources
rig doctor    # what is missing: tools, cluster or daemon, variables, ports, components
rig up        # everything, in dependency order
rig           # the terminal UI; ? lists the keys of every screen
```

No `rig.yaml` yet? `rig up && rig` still work: rig infers the project in memory as `rig init` would,
and writes nothing until you run `rig init`.

On a project that already has a `rig.yaml`: `rig env` lists its environments, `-e <name>` picks one.

A small `rig.yaml`:

```yaml
project: shop
default: docker

services:
  api:
    depends_on: [db]
    build: { go: ./cmd/api }
    ports: { http: 8080, metrics: 9090 }
    health: { port: http, path: /healthz }
  db: { role: infra, image: "postgres:17", ports: { pg: 5432 } }

environments:
  docker: { runtime: { type: docker } }
  kind:   { runtime: { type: kind, cluster: shop, manifests: [deploy] } }

components:
  db: { type: sql, driver: postgres, addr: "svc://db:5432", user: app, password: "${DB_PASSWORD}" }
```

Try one of the [examples](examples): `cd examples/shop && rig up && rig`.

## Plugins

```bash
rig plugin search jira          # the registry index
rig plugin add pack/gitlab      # install and pin in rig.lock
rig plugin ls                   # what the project uses, on or off
```

Official ones, a pack each: `pack-std` (local, docker, kubernetes, kind, go and command builders, git,
compose/godev imports), `pack-data` (sql, redis, rabbitmq, kafka, consul), `pack-observability`
(prometheus, scrape, loki, jaeger, tempo, zipkin, pprof, grafana), `pack-qa` (junit, pytest, jest,
vitest, hurl, k6, vegeta, ghz), `pack-gitlab`, `pack-github`, `pack-atlassian` (jira, confluence),
`pack-cloud-aws` (aws, terraform, helm, registry) and `pack-product` (agents that turn specs into
stories and MRs, every write waiting for approval). Plus `slack`, `mcp-bridge`, `arcade`, `ssh`,
`delve`, `goland` and importers for just, make, npm scripts, Procfile, dotenv, OpenAPI and CI files.
`rig-std list` prints them all.

Write your own:

```bash
rig plugin new sentry --lang yaml   # or bash, python, go, ts
cd sentry && rig plugin test
cd ~/my-project && rig plugin dev ~/sentry
```

A plugin is a folder with a `rig-plugin.yaml`. If you can write `rig.yaml`, you can write one; add a
program only when YAML and shell are not enough. [Plugins](docs/plugins.md) has the whole story, and
`skills/rig-plugin` lets an AI agent write one for you.

## Docs

- [Guide](docs/guide.md): CLI, every screen and its keys, AI, GoLand, agents
- [Config](docs/config.md): every `rig.yaml` key
- [CLI](docs/cli.md): every command
- [Plugins](docs/plugins.md): using and writing plugins; [protocol](docs/protocol.md), [contracts](docs/contracts.md), [views](docs/views.md)
- [Security](docs/security.md): trust, sandbox, secrets, policy, server, audit
- [Design](docs/design.md): how rig is built
- [Manifests](docs/manifests.md): a manifest layout that works well with rig

All of it ships in the binary too: `rig docs guide`.

## Contributing

New technology support goes into a plugin, not the core: [goxang/rig-plugins](https://github.com/goxang/rig-plugins)
or your own repo. Fixes, screens and docs are welcome here. `go vet ./... && go test -race ./...` must
pass. Commits follow Conventional Commits; every green push to `main` is released (`feat:` minor,
anything else patch).

## License

[Apache 2.0](LICENSE)
