# rig.yaml

rig looks for `rig.yaml` (or `rig.yml`, `.rig.yaml`) from the current directory up; `-f` or `$RIG_FILE`
names one. `${NAME}` and `${NAME:-default}` expand anywhere, from the process environment, then
`${env}` and `${project}`, then the environment's `vars`, then the project's `vars`. `$$` is a literal `$`.

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

## services.<name>

| key | |
|---|---|
| `role` | `app` (default), `infra`, or `load` (left out of `rig up`) |
| `groups` | names to address several services at once: `rig up core` |
| `depends_on` | started first; `up` deploys in phases and waits for each |
| `image` | image to run, or the repository name builds push to |
| `build` | `go: ./cmd/api`, `dockerfile: path` (+ `context`, `args`), or `command: [...]` |
| `run` | local runtime: `command`, `args`, `dir` (default: the binary built from `build.go`) |
| `ports` | `name: number`; `svc://service:name` resolves names |
| `env` | environment for every runtime |
| `replicas` | default 1; on Kubernetes, set here it also overrides the manifest's count |
| `health` | `{port, path}`: readiness for local and docker, a readiness probe in generated manifests |
| `metrics` | `{port, path}`: scraped by the `scrape` metrics adapter |
| `pprof` | `{port, path}`: where the `pprof` profiler connects (default: the metrics port) |
| `k8s` | `workload`, `container`, `service`, `manifests`, `command`, `args` |
| `docker` | `image`, `command`, `args`, `volumes`, `publish` (port name → host port), `extra_args`, `labels` |

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

### runtime options

| type | options |
|---|---|
| `local` | `env`, `stop_timeout` |
| `docker` | `network`, `registry`, `env`, `publish` (default true) |
| `kubernetes` | `context` (required), `namespace`, `registry`, `manifests`, `vars`, `env`, `create_namespace`, `state_configmap`, `node_shell_image` |
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
| `kv` (loadgen) | `store`, `key`, `field`, `services`, `replicas`, `metrics: {source, sent, failed, latency_p99}` |
| `command` (loadgen) | `start`, `stop`, `rate` (with `{rate}`), `status` |
| `ssh` | `defaults`, `hosts: [{name, addr, user, port, key, jump, roles, labels}]` |
| `go` (builder) | `base` (registry image, `docker://image`, or `scratch`), `platform`, `workdir`, `ldflags`, `tags`, `insecure` |
| `docker` (builder) | `platform`, `args` |
| `http` (query) | `addr` |

Every load generator also takes `max` (rates above it need `--force`) and `step` (one `+`/`-`).

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

Each step runs with `sh -c` from the project directory and the task stops at the first failure.
`svc://service:port` in a step becomes a `host:port` reachable from here for the whole task. A nested
`rig` uses the same project file, environment and `--yes` (through `$RIG_FILE`, `$RIG_ENV`, `$RIG_YES`).
Write `$$` for a shell `$`, since `${...}` is rig's own expansion.
