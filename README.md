# rig

One control plane for a project's services and the infrastructure around them, on your machine,
in docker, or on Kubernetes. Describe services once in `rig.yaml`; run, watch and tune them from one
CLI and one terminal UI.

- **Services**: up (in dependency order, with start delays), down, start, stop, restart, scale (also `+1`), deploy by tag, build (also from a git ref), logs, exec
- **Infrastructure**: started once and left alone; one shared instance can serve local processes, docker and kind (`rig infra`)
- **Observe**: Grafana-style charts, traces filtered by service, operation and duration, profiles, debuggers
- **Data & load**: databases, caches, queues, key-value stores (browse, edit, restart readers), load generators (rate and instances), saved queries that run on a schedule
- **Alerts**: node cpu/memory/disk (or any query) over a threshold, in the TUI header and `rig alerts`
- **Secrets**: `rig secret set`, kept outside the repo; `helm:` charts deploy like manifests
- **Agents**: `rig mcp` serves all of it to AI agents over MCP; `--brief` keeps every answer short

Every part is an adapter behind a small interface, so a new metrics backend, database or runtime is one package.

## Install

```bash
go install github.com/goxang/rig/cmd/rig@latest   # Go 1.23+, lands in $(go env GOPATH)/bin — keep it on PATH
```

`kubectl`, `docker`, `kind`, `ssh` and `dlv` are used when the matching adapter is.

## Use

```bash
rig init                      # starter rig.yaml from this directory (Go mains, .godev.yaml, manifests)
rig                           # terminal UI
rig infra up                  # infrastructure, once
rig up test                   # every service tagged test, and what they depend on
rig up --build                # build images and deploy everything, phase by phase
rig build app -t v1 --ref main && rig deploy app -t v1   # apps only; infrastructure untouched
rig scale core +1             # every service in group core, one more replica
rig status
rig -e kind logs -F api worker
rig query                     # saved queries and queryable components
rig query db-top-cpu n=5
rig load rate fleet 200 && rig load scale fleet +2
rig task bootstrap            # a named list of steps from rig.yaml
rig vars set MAIN_DB=x        # manifest variables per environment; rig setenv api K=V for env
rig data db Switch tables     # walk databases and caches
rig logs -E '(?i)timeout' api # regex over logs
rig alerts                    # what is over its thresholds
rig resume last               # the TUI as a saved session (S) left it
rig mcp                       # MCP server for AI agents
```

`-e <env>` (or `$RIG_ENV`) picks the environment. A `protected: true` environment refuses changes
without `--yes`; pushing over an existing image tag needs it too.

## Terminal UI

Screens load nothing until opened, and only the one showing refreshes. Everything is a sortable grid
(`<` `>` pick the column, `I` inverts, or click a header) and works with the mouse. On a Kubernetes
cluster, dangerous changes (stop, deploy, delete, edits) ask first and `enter` confirms; local, docker
and kind never ask. `?` shows the keys of the current screen.

| screen | |
|---|---|
| Services | your services, `i` switches to infrastructure or both; mark several with `space` (`a` all shown), then start/stop/restart/scale/deploy them together; `enter` opens one: instances and its live log |
| Logs | the services you pick, merged, or one instance |
| Metrics | dashboards from rig.yaml, on demand |
| Traces | filter by service, operation, minimum duration, time window, text, errors |
| Queries | saved queries (with parameters), ad hoc ones, schedules with a trend of the first number; `H` every run of the session |
| KV | browse and edit keys; `R` restarts the services that read the edited key; `I` fills the store (`kv-*` tasks) |
| Data | databases (objects, rows, definitions, running queries), caches (keys, values), queues; `Q` queries where you stand; `/` filters with globs |
| Load | generators: rate, instances, `c` their KV config, `v` their env |
| Manifests | objects or folders (`t`), relations, a file's issues (`i`), apply (`a`), make a service (`n`) |
| Hosts | nodes as htop-style CPU, memory and disk bars, the selected one's CPU history, and a shell |

`T` runs a task from rig.yaml. `S` saves the session (screens, query results and history) for `rig resume`,
under the user's config directory (`~/.config/rig/projects/...`); `M` frees the mouse so
the terminal can select text; the header shows alerts (`A`).

## Agents

```json
{ "mcpServers": { "rig": { "command": "rig", "args": ["mcp"], "cwd": "/path/to/project" } } }
```

Agents get terse output (`--brief`, `RIG_BRIEF=1`: tab-separated, no colour, long cells cut). Every
TUI screen has a CLI twin: `rig logs -E`, `rig metrics`, `rig profile`, `rig query --every 30s --times 10`,
`rig load`, `rig data`, `rig alerts`.

Tools: `rig_envs`, `rig_status`, `rig_up`, `rig_down`, `rig_service`, `rig_scale`, `rig_build`, `rig_deploy`,
`rig_logs`, `rig_query`, `rig_load`, `rig_kv`, `rig_infra`, `rig_task`, and `rig` for any other command.
Each runs the CLI, so protections apply: changes to a protected environment need `"confirm": true`.

## rig.yaml

```yaml
project: shop
default: kind

services:
  api:
    depends_on: [db]
    groups: [core, test]
    build: { go: ./cmd/api }
    ports: { http: 8080, metrics: 9090 }
    health: { port: http, path: /healthz }
  db: { role: infra, shared: true, image: "postgres:17", ports: { pg: 5432 }, docker: { publish: { pg: 5432 }, bind: 0.0.0.0 } }

environments:
  docker: { runtime: { type: docker } }
  local:  { infra: docker, runtime: { type: local } }
  kind:   { infra: docker, runtime: { type: kind, cluster: shop, namespace: shop, registry_port: 5001, manifests: [deploy] } }
  prod:   { protected: true, runtime: { type: kubernetes, context: prod-eu, namespace: shop } }

components:
  prom: { type: prometheus, addr: "svc://prometheus:9090" }
  db:   { type: sql, driver: postgres, addr: "svc://db:5432", user: app, password: "${DB_PASSWORD}" }
  load: { kind: loadgen, type: http, target: "svc://api:8080/", rate: 20, max: 500 }

queries:
  slow-orders: { source: db, query: "SELECT * FROM orders WHERE took_ms > {{ms}}", params: { ms: "500" }, every: 30s }

dashboards:
  main:
    - { title: requests/s, query: 'sum by (service) (rate(http_requests_total[1m]))', unit: /s }
```

`db` is shared: it runs once, in the docker environment, and local processes and the kind cluster
use that one (kind reaches it through a Service pointing at the host).

`svc://service:port` reaches a service in whatever environment is active (port-forward on Kubernetes,
published port on docker, localhost locally). Full reference: [docs/config.md](docs/config.md).

## Try it

[`examples/kind`](examples/kind) is a complete project (api, worker, load generator, Prometheus, Zipkin,
Consul, Postgres, Redis, RabbitMQ) that runs on kind, docker or locally:

```bash
cd examples/kind
rig do runtime create && rig up --build && rig
```

## Docs

- [docs/config.md](docs/config.md): every field of `rig.yaml`
- [docs/design.md](docs/design.md): how rig is built, and how to write an adapter
- [docs/manifests.md](docs/manifests.md): a manifest layout that works well with rig
