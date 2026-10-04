# rig.yaml

rig looks for `rig.yaml` (or `rig.yml`, `.rig.yaml`) from the current directory up; `-f` or `$RIG_FILE`
names one. `${NAME}` and `${NAME:-default}` expand anywhere, from the process environment, then
`rig secret set` values, then `secrets:` defaults, then `${env}` and `${project}`, then the environment's
`vars`, then the project's `vars`. `$$` is a literal `$`.

## Top level

| key | |
|---|---|
| `project` | name; also the docker network and container prefix |
| `default` | environment used when neither `-e` nor `$RIG_ENV` is given |
| `vars` | variables for `${...}` |
| `imports` | services from other tools' files: `- godev: .godev.yaml` |
| `services` | below |
| `environments` | below |
| `components` | below; shared by every environment |
| `dashboards` | `name: [panel, ...]` |
| `manifests` | folders `rig manifests` and the TUI scan by default |
| `tasks` | `name: [shell step, ...]`, run in order by `rig task <name>` (see below) |
| `queries` | saved queries (see below) |
| `secrets` | `NAME: {help, default}`: values kept out of the repo (see below) |
| `alerts` | thresholds shown in the TUI header and by `rig alerts` (see below) |

## services.<name>

| key | |
|---|---|
| `role` | `app` (default), `infra`, or `load` (left out of `rig up`) |
| `groups` | names to address several services at once: `rig up core` |
| `depends_on` | started first; `up` deploys in phases and waits for each, leaving dependencies that already run untouched |
| `image` | image to run, or the repository name builds push to |
| `build` | `go: ./cmd/api`, `dockerfile: path` (+ `context`, `args`), or `command: [...]` |
| `run` | local runtime: `command`, `args`, `dir` (default: the binary built from `build.go`) |
| `ports` | `name: number`; `svc://service:name` resolves names |
| `env` | environment for every runtime |
| `replicas` | left out: keep the running count (or the manifest's); set (0 included) it wins everywhere |
| `shared` | infrastructure one environment runs for others: see `environments.<name>.infra` |
| `delay` | wait this long (`10s`) after the dependencies are ready before starting the service |
| `health` | `{port, path}`: readiness for local and docker, a readiness probe in generated manifests |
| `metrics` | `{port, path}`: scraped by the `scrape` metrics adapter |
| `pprof` | `{port, path}`: where the `pprof` profiler connects (default: the metrics port) |
| `k8s` | `workload`, `container`, `service`, `manifests`, `command`, `args` |
| `helm` | deploy with a chart instead of manifests: `chart`, `version`, `release`, `values` (files), `set` (`$TAG`, `$NAMESPACE` and manifest vars filled in), `image_key`/`tag_key`/`replicas_key` (where builds and scaling go); `k8s.workload` names what the chart creates |
| `docker` | `image`, `command`, `args`, `volumes`, `publish` (port name → host port), `bind` (host address, default 127.0.0.1), `container` (adopt an existing container by name, e.g. a compose one; never removed), `extra_args`, `labels` |

## environments.<name>

| key | |
|---|---|
| `runtime` | a component: `type` plus the runtime's options (below) |
| `protected` | changes need `--yes` or the TUI's confirmation |
| `description` | shown by `rig env` and the TUI |
| `vars` | variables for this environment |
| `only` | services and groups that exist here; dependencies on the rest are dropped |
| `services` | patches merged into services here: `api: { replicas: 3 }` |
| `components` | components added or replaced here; one without `type` patches the shared one |
| `tasks` | tasks replacing the project's tasks of the same name |
| `queries` | saved queries replacing the project's of the same name |
| `infra` | the environment that runs this one's `shared` services; they are started, stopped and reached there, and a Kubernetes runtime gets a Service pointing at the host for each |

### runtime options

| type | options |
|---|---|
| `local` | `env`, `stop_timeout` |
| `docker` | `network`, `registry`, `env`, `publish` (default true) |
| `kubernetes` | `context` (required), `namespace`, `registry`, `pull_registry` (the registry as nodes name it, when it differs from where builds push), `manifests`, `vars`, `env`, `create_namespace`, `state_configmap`, `node_shell_image` |
| `kind` | the kubernetes options plus `cluster`, `node_image`, `workers`, `registry_port`, `preload` |

## components.<name>

`type` picks the adapter; `kind` is needed only when a type exists for several kinds (`http`,
`command`). Everything else is the adapter's options. Addresses take `svc://service:port[/path]`, a URL,
or `host:port`; HTTP adapters also take `user`, `password`, `token` and `headers`.

| type | options |
|---|---|
| `prometheus` | `addr` |
| `scrape` | `targets` (services or URLs; default every service with `metrics`), `interval`, `retention` |
| `zipkin`, `jaeger` | `addr` |
| `loki` | `addr`, `label` (default `app`), `selector` (`{app="{service}"}`) |
| `pprof` | `port`, `path`, `summary` |
| `command` (profiler) | `kinds: {cpu: {command, where: service|host, fetch, format, ext}}`, `pid` |
| `delve` | `dlv`, `port`, `pid` |
| `sql` | `driver` (postgres, mssql, mysql), `addr`, `user`, `password`, `database`, `params`, `seeds`, `clear` |
| `redis` | `addr`, `password`, `db`, `unsafe` |
| `rabbitmq` | `addr` (management API), `user`, `password`, `vhost` |
| `consul` | `addr`, `token` |
| `http` (loadgen) | `target`, `method`, `body`, `headers`, `rate`, `max_in_flight`, `timeout` |
| `kv` (loadgen) | `store`, `key`, `field` (dotted for nested JSON: `a.b`), `services`, `replicas`, `metrics: {source, sent, failed, latency_p99}` |
| `command` (loadgen) | `start`, `stop`, `rate` (with `{rate}`), `status` |
| `ssh` | `defaults`, `hosts: [{name, addr, user, port, key, jump, roles, labels}]` |
| `go` (builder) | `base` (registry image, `docker://image`, or `scratch`), `platform`, `workdir`, `ldflags`, `tags`, `insecure` |
| `docker` (builder) | `platform`, `args` |
| `http` (query) | `addr` |

Every load generator also takes `max` (rates above it need `--force`) and `step` (one `+`/`-`).

## queries

```yaml
queries:
  db-top-cpu:
    source: db                     # any component that answers queries, or runtime
    group: database                # how the Queries screen groups them
    help: statements by CPU
    params: { n: "15" }            # defaults for {{n}}
    query: SELECT TOP {{n}} ...
    every: 30s                     # schedule while the UI is open...
    active: true                   # ...from the start (else switch it on with `a`)
```

`rig query <name> n=5` runs one; `--every 5s` repeats it. Placeholders without a default must be given.

## dashboards

```yaml
dashboards:
  main:
    - { title: requests/s, query: 'sum by (service) (rate(http_requests_total[1m]))', unit: /s, legend: "{{service}}" }
    - { title: uptime, query: 'max(process_uptime_seconds)', unit: s, kind: stat, source: prom }
```

`unit` formats values (`/s`, `ms`, `s`, `bytes`, `%`, `ratio`); `kind: stat` shows a big number with a
sparkline; `source` picks the metrics component.

## tasks

```yaml
tasks:
  bootstrap:
    - rig up infra
    - rig do db seed schema.sql
    - ./scripts/seed-consul.sh svc://consul:8500
    - rig up --build
```

`rig up` with no targets never redeploys infrastructure (`role: infra`) that already runs, and `rig down`
with no targets leaves it running: `rig infra up|down|restart|status` changes it.

Each step runs with `sh -c` from the project directory and the task stops at the first failure.
`svc://service:port` in a step becomes a `host:port` reachable from here for the whole task. A nested
`rig` uses the same project file, environment and `--yes` (through `$RIG_FILE`, `$RIG_ENV`, `$RIG_YES`).
Write `$$` for a shell `$`, since `${...}` is rig's own expansion. `rig task ship parser load` passes
the words after the name as `$1...` and `$RIG_ARGS`.

In the TUI, `T` runs any task; the KV screen's `F` lists the tasks named `kv-*` (filling the store from
the project's config files, say).

## secrets

```yaml
secrets:
  DB_PASSWORD: { default: devpass, help: the app login }   # a public dev default
  API_TOKEN: { help: staging API token }                   # no default: set it
```

`rig secret` lists them and where each value comes from (environment, stored, default, unset), never
the value. `rig secret set API_TOKEN` asks without echo (or reads stdin) and keeps it in
`~/.config/rig/secrets/<project>.json`, mode 0600. Use them like any variable: `password: "${DB_PASSWORD}"`.

## alerts

```yaml
alerts:
  - { name: memory, source: hosts, metric: memory, warn: 90, crit: 98, unit: "%" }   # every node
  - { name: backlog, source: prom, query: "sum by (queue) (rabbitmq_queue_messages_ready)", warn: 1000, crit: 10000 }
  - { name: consumers, source: queue, query: "queues name consumers", warn: 1, crit: 1, below: true }
```

`source: hosts` checks every node's `cpu`, `memory` or `disk` (percent). Any other source is a component
whose query returns rows: each row's first number is checked, its other cells name it. `below: true`
fires under the thresholds. With no `alerts:`, nodes are watched for cpu and memory at 90% and 98%, disk at 97% and 98%.
The TUI checks every 15s and shows the worst in its header (`A` lists all); `rig alerts` checks once and
exits 2 when one is critical. Environments add their own `alerts:`.

## run state an environment keeps

`rig vars set K=V` overrides a runtime `vars` entry (`$MAIN_DB` in manifests) and `rig setenv <service> K=V`
adds env to a service; both live in the environment's state (a ConfigMap on Kubernetes, `.rig/<env>/`
elsewhere), so every later deploy keeps them and teammates see them.
