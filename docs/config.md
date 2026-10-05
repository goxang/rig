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
| `dashboards` | `name: [panel, ...]` or `name: {help, vars, panels}` (see below) |
| `tests` | test suites for `rig test` and the Tests screen (see below) |
| `sections` | `name: {help, services: [names, groups or roles]}`: the Services screen's parts, in this order; services in none show under `other` |
| `manifests` | folders `rig manifests` and the TUI scan by default |
| `tasks` | `name: [shell step, ...]` or `name: {help, steps}`, run in order by `rig task <name>` (see below) |
| `queries` | saved queries (see below) |
| `secrets` | `NAME: {help, default}`: values kept out of the repo (see below) |
| `alerts` | thresholds shown in the TUI header and by `rig alerts` (see below) |
| `reports` | metrics a run is measured by, saved as markdown: `rig report`, a suite's `report:`, the Load screen's `W` (see below) |
| `otel` | OpenTelemetry for every app and load service (see below) |
| `ui` | `{tabs: [services, logs, ...]}`: the screens and their order; left out, every screen the project configures something for |
| `ai` | `{deny: [globs], instructions: text}`: project paths the assistant never reads (on top of `.env`, keys and `secrets/`), and notes it gets with every turn |

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
| `manual` | started only when named or through a group or section, never by `all` or its role, so `rig infra up` skips it too (e.g. frontends, an optional Prometheus) |
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
| `otel` | fields replacing the project's `otel:` here (a local environment's endpoints, say) |
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
| `tempo` | `addr`; queries take TraceQL: `{ status = error && duration > 1s }` |
| `loki` | `addr`, `label` (default `app`), `selector` (`{app="{service}"}`) |
| `pprof` | `port`, `path`, `summary` |
| `command` (profiler) | `kinds: {cpu: {command, where: service|host, fetch, format, ext}}`, `pid` |
| `delve` | `dlv`, `port`, `pid` |
| `sql` | `driver` (postgres, mssql, mysql), `addr`, `user`, `password`, `database`, `params`, `seeds`, `clear` |
| `redis` | `addr`, `password`, `db`, `unsafe` |
| `rabbitmq` | `addr` (management API), `user`, `password`, `vhost` |
| `kafka` | `addr` (brokers, comma separated), `user`, `password`, `sasl` (plain, scram-sha-256, scram-sha-512), `tls`; queries: `topics`, `groups`, `lag <group>`, `tail <topic> [n]` |
| `consul` | `addr`, `token` |
| `http` (loadgen) | `target`, `method`, `body`, `headers`, `rate`, `max_in_flight`, `timeout` |
| `kv` (loadgen) | `store`, `key`, `field` (dotted for nested JSON: `a.b`), `services`, `replicas`, `metrics: {source, sent, failed, latency_p99, per_instance}` (`per_instance`: a sent counter labelled `pod` or `instance`, charted per instance on the Load screen) |
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
  main:                                  # a plain list of panels
    - { title: requests/s, query: 'sum by (service) (rate(http_requests_total[1m]))', unit: /s, legend: "{{service}}" }
  service:                               # or help, $variables and panels
    help: one service in depth
    vars:
      service: { query: up, label: service, default: api }     # label_values(up, service)
      node: { values: [a, b], all: true, multi: true }
    panels:
      - row: Traffic                     # a foldable row header
      - { title: requests/s, kind: stat, unit: /s, width: 6, warn: 100, crit: 500, query: 'sum(rate(http_requests_total{service=~"$service"}[$__rate_interval]))' }
      - title: latency
        unit: s
        queries:                         # several queries in one panel, each with its own legend
          - { legend: p50, query: 'histogram_quantile(0.5, sum by (le) (rate(http_seconds_bucket{service=~"$service"}[5m])))' }
          - { legend: p99, query: 'histogram_quantile(0.99, sum by (le) (rate(http_seconds_bucket{service=~"$service"}[5m])))' }
```

| panel key | |
|---|---|
| `title`, `help` | shown on the panel and in its full view |
| `query` / `queries` | one query, or `[{query, legend}]` drawn together |
| `legend` | series name from labels: `"{{pod}} {{code}}"` |
| `unit` | `/s`, `ms`, `s`, `bytes`, `%`, `ratio`, or any suffix |
| `kind` | `line` (default), `stat` (big number and sparkline), `gauge`, `bar` (one bar per series), `table` |
| `width`, `height` | out of 24 columns (default 12; stat and gauge 6), and lines |
| `min`, `max` | a gauge's range (default 0..100 for `%`) |
| `warn`, `crit` | thresholds that colour stat, gauge, bar and table values |
| `stack` | stack the lines |
| `row` | starts a row; a panel with only `row:` is the header |
| `source` | the metrics component (default: the first) |

Queries use `$name` (or `${name}`) for variables: one value as is, several as `(a|b)`, all as `.*`,
so match with `=~`. `$__range`, `$__interval` and `$__rate_interval` follow the time range. A
variable's `default` is its first choice; `all` adds "All", `multi` lets several be picked.

In the TUI a click on a legend entry shows only that series (again: all), ctrl/alt/shift-click hides
it; `v` (or a double click) opens a panel full screen with a sortable table legend (min, max, mean,
last, value at the cursor), series filter `/`, stacking `s` and a cursor set by clicking the chart.
The Services screen's `m` opens the first dashboard with a `$service` variable on that service.

## tests

```yaml
tests:
  unit:
    packages: [./...]
    race: true
  integration:
    help: against the running environment
    packages: [./tests/integration/...]
    env: { API_ADDR: "svc://api:8080" }   # svc:// resolves in the active environment
    needs: [api]                         # the screen warns while these are not up
    count: 1
    timeout: 30m
  bench:
    packages: [./pkg/...]
    bench: .
    benchmem: true
```

`exclude` drops packages from `packages` (go list patterns), so `packages: [./...]` with the
integration suites excluded is "every unit test", new packages included. `report: <name>` measures a
`reports:` entry over each run and appends it to the run's report.

A suite runs `go test -json` in `dir` (default the project directory) with `run`, `skip`, `tags`,
`race`, `cover`, `short`, `count`, `parallel`, `timeout`, `bench`, `benchtime`, `benchmem`, more
`flags`, and `args` for the test binary. `bench` alone runs only benchmarks (`-run ^$`) unless `run`
is set. Runs are kept (the last 50) for `rig test report`, reruns of failures and benchmark deltas.

`rig test <suite>` takes the same flags (`--race --cover --run X --bench . --count 1 ...`), `--failed`
reruns the last run's failures, `--junit file` writes JUnit XML, `-o file` the full report;
`rig test ./pkg/x/...` runs packages with no suite; `rig test report [run] [-o file]`, `rig test runs`.

## reports

```yaml
reports:
  load:
    help: a load test
    source: prom                 # metrics component (default the first)
    metrics:
      - { title: requests/s, unit: /s, stats: [avg, max, p95], query: 'sum(rate(http_requests_total[1m]))' }
      - { title: p95 by service, unit: ms, legend: "{{service}}", query: '...' }
```

Each metric is a range query over the window, summarised per series by `stats`: `avg`, `min`, `max`,
`last`, `p50`, `p90`, `p95`, `p99` (default `avg, max, last`). `rig report load --since 20m` (or
`--from 14:05 --to 14:35`, `--source other-prom`, `-o file`) prints a markdown table and saves it under
the project's data directory; the Load screen's `W` does the same for the time since you started the
generator.
A rerun cuts each failed test back to its deepest parallel ancestor (an integration case's mode),
else its top-level test, so the steps before it run again too.

## tasks

```yaml
tasks:
  bootstrap:
    - rig up infra
    - rig do db seed schema.sql
    - ./scripts/seed-consul.sh svc://consul:8500
    - rig up --build
  reset-db:
    help: empty the app's tables (keeps the schema)
    steps: [rig do db seed truncate.sql]
```

A task is a list of steps, or `{help, steps}`: `help` is what `rig task` and the TUI's `T` show next to
its name (without it they show the steps).

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

## otel

```yaml
otel:
  endpoint: svc://otel-collector:4318        # every signal to one collector, or one by one:
  traces: svc://jaeger:4318/v1/traces
  metrics: svc://prometheus:9090/api/v1/otlp/v1/metrics   # Prometheus with --web.enable-otlp-receiver
  logs: svc://loki:3100/otlp/v1/logs
  protocol: http/protobuf                    # or grpc, http/json
  sample: 0.1                                # parent-based ratio; left out, every trace
  attributes: { team: payments }
```

rig sets the standard variables every OpenTelemetry SDK reads (Go, Java, Python, Node, .NET, ...) on each
app and load service: `OTEL_SERVICE_NAME`, `OTEL_RESOURCE_ATTRIBUTES` (`service.namespace`,
`deployment.environment`, then `attributes`), `OTEL_EXPORTER_OTLP_*ENDPOINT`, `OTEL_EXPORTER_OTLP_PROTOCOL`,
`OTEL_{TRACES,METRICS,LOGS}_EXPORTER` (`none` for a signal with no address), the sampler and
`OTEL_PROPAGATORS=tracecontext,baggage`. A variable the service sets itself wins. `svc://` addresses are the
service's name inside docker or Kubernetes and a forwarded port for local processes. Read the traces back with
a `jaeger` or `tempo` component, the metrics with `prometheus`, the logs with `loki`.

## run state an environment keeps

`rig vars set K=V` overrides a runtime `vars` entry (`$MAIN_DB` in manifests) and `rig setenv <service> K=V`
adds env to a service; both live in the environment's state (a ConfigMap on Kubernetes, `.rig/<env>/`
elsewhere), so every later deploy keeps them and teammates see them.
Whatever rig keeps in the project lives under `.rig/`; in a git repository rig adds `.rig/` to
`.gitignore` the first time it finds that folder unignored.

On the local runtime, a service's `metrics` port that its process does not listen on stands for the
process's own port serving `/metrics` (or its children's, under dlv): services that read their metrics
port from a config store can all take a free one and still be scraped and profiled.
