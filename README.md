# rig

One control plane for a project's services and the infrastructure around them, on your machine,
in docker, or on Kubernetes. Describe services once in `rig.yaml`; run, watch and tune them from one
CLI and one terminal UI.

- **Services**: up (in dependency order, with start delays), down, start, stop, restart, scale (also `+1`), deploy by tag, build (also from a git ref), logs, exec
- **Infrastructure**: started once and left alone; one shared instance can serve local processes, docker and kind (`rig infra`)
- **Observe**: Grafana-style dashboards (rows, variables, stat/gauge/bar/table panels, clickable legends), traces filtered by service, operation and duration, profiles, debuggers
- **Tests**: go test suites from `rig.yaml` with live results, filters, output, reruns of failures, race, coverage, benchmarks against the last run, JUnit
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
rig test unit --race          # a test suite; --failed reruns the failures, -o file saves the report
rig test ./pkg/x/...          # any packages, no suite needed
rig report load --since 20m   # metrics of a load test (reports: in rig.yaml) as a markdown table
rig ns --create me            # Kubernetes: switch the environment to a new namespace (rig ns lists them)
rig metrics targets -o f.json # services as Prometheus file_sd targets, for a local Prometheus
rig resume last               # the TUI as a saved session (S) left it
rig ide                       # GoLand / VS Code "rig: <service>" remote-debug configs; D in the TUI, then run one
rig mcp                       # MCP server for AI agents
```

`-e <env>` (or `$RIG_ENV`) picks the environment. A `protected: true` environment refuses changes
without `--yes`; pushing over an existing image tag needs it too. In the TUI the confirmation is the `--yes`.

## Terminal UI

Screens load nothing until opened, and only the one showing refreshes. Everything is a sortable grid
(`<` `>` pick the column, `I` inverts, or click a header) and works with the mouse. On a Kubernetes
cluster, dangerous changes (stop, deploy, delete, edits) ask first and `enter` confirms; local, docker
and kind never ask. `?` shows the keys of the current screen.

| screen | |
|---|---|
| Services | your services by section (`sections:`; `[` `]` or a click, `i` infrastructure); mark with `space`, a whole section with `a` or a double-click on it, then start (in dependency order)/stop/restart/scale/deploy them together; `h` edits a Kubernetes autoscaler (or creates one), and scaling past one asks whether to move it; `R` changes requests and limits; `F` lists the service's manifests to edit, sync from what runs, or apply; replica changes (an autoscaler's too) show for a while; `D` attaches a debugger (debug build, a stable port per service); `p` takes a profile (the picker says what each kind shows) as a sortable table, `W` saves it as a report; `enter` opens one: instances and its live log, which scrolls like the Logs screen, `l` moves it there |
| Logs | the services you pick, merged, or one instance; `←` `→` scroll sideways, dragging over lines copies them |
| Metrics | dashboards as tabs, `$variables` and time range in the header (click them), foldable rows; click a legend entry for only that series, ctrl-click to hide it; `v` opens a panel with a sortable legend table and a cursor |
| Traces | filter by service, operation, minimum duration, time window, text, errors |
| Queries | saved queries (with parameters), ad hoc ones, schedules with a trend of the first number; `H` every run of the session |
| KV | browse, edit and delete keys right in the store; `o` picks the editor (nano, vim, VS Code, the desktop's); `R` restarts the services that read the edited key; `F` loads the config files into it (`kv-*` tasks) |
| Data | databases (objects, rows, definitions, running queries), caches (keys, values), queues; `e` edits a cell, `space` marks rows, `D` deletes them (table rows by primary key, Redis keys and entries); `Q` queries where you stand; `/` filters with globs |
| Load | generators: rate and config shared by every instance, instance count, `i` (or a click) one instance's charts or all, `c` their KV config, `v` their env, `W` saves a metrics report |
| Manifests | objects or folders (`t`), relations, a file's issues (`i`), apply (`a`), make a service (`n`); `e` edits an object in your editor and saves it into its file, `s` writes what the cluster runs into the file (only fields someone set, `$VARS` kept), `L` edits it on the cluster; `d` picks among every manifest folder of the project |
| Hosts | nodes as htop-style CPU, memory and disk bars, the selected one's CPU history, and a shell (double-click) |
| Tests | suites as tabs and the `go test` command they run; `r` runs, `f` reruns failures, `.` the selected test, `O` sets flags (race, cover, -run, …); a tree of packages and tests (`i` cycles failed/passed/skipped/running), its output beside it, benchmarks with the change since the last run, saved runs (`h`) |

Screens switch with `1`-`0` and `` ` `` (Tests), or a click on their name; tabs inside a screen
(dashboards, apps/infra, objects/folders, saved/history, suites, filters) are clickable too.
`T` runs a task from rig.yaml, `N` switches or creates a Kubernetes namespace, and the Metrics screen's
`m` points the dashboards at another metrics source. `S` saves the session (screens, query results and history) for `rig resume`,
under the user's config directory (`~/.config/rig/projects/...`); `M` frees the mouse so
the terminal can select text; the header shows alerts (`A`). `q` quits at once unless a test run or an
operation is still going, or load generators are sending: those stop with rig (tests, queries, port
forwards) or keep going without it (generators, services), and it asks.

## Agents

```json
{ "mcpServers": { "rig": { "command": "rig", "args": ["mcp"], "cwd": "/path/to/project" } } }
```

Agents get terse output (`--brief`, `RIG_BRIEF=1`: tab-separated, no colour, long cells cut). Every
TUI screen has a CLI twin: `rig logs -E`, `rig metrics`, `rig profile`, `rig query --every 30s --times 10`,
`rig load`, `rig data`, `rig alerts`.

Tools: `rig_envs`, `rig_status`, `rig_up`, `rig_down`, `rig_service`, `rig_scale`, `rig_build`, `rig_deploy`,
`rig_logs`, `rig_query`, `rig_load`, `rig_kv`, `rig_infra`, `rig_task`, `rig_test`, and `rig` for any other command.
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
