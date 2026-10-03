# rig

One control plane for a project's services and the infrastructure around them, on your machine,
in docker, or on Kubernetes. Describe services once in `rig.yaml`; run, watch and tune them from one
CLI and one terminal UI.

- **Services**: up (in dependency order), down, start, stop, restart, scale, deploy, build, logs, exec, discover
- **Observe**: Grafana-style charts, traces with waterfalls, profiles (pprof or any profiler CLI), debuggers
- **Data & load**: databases, caches, queues, key-value stores, load generators, a query console for all of them
- **Infrastructure**: manifests scanned from any folder and shown as a relation graph; hosts with live usage and a shell

Every part is an adapter behind a small interface, so a new metrics backend, database or runtime is one package.

## Install

```bash
go install github.com/goxang/rig/cmd/rig@latest   # Go 1.23+, lands in $(go env GOPATH)/bin — keep it on PATH
```

`kubectl`, `docker`, `kind`, `ssh` and `dlv` are used when the matching adapter is.

## Use

```bash
rig init              # starter rig.yaml from this directory (Go mains, .godev.yaml, manifests)
rig                   # terminal UI
rig up --build        # build images and deploy everything, phase by phase
rig status            # services in the current environment
rig -e kind logs -F api worker
rig metrics main      # a dashboard from rig.yaml, as charts
rig load rate fleet 200
rig query db "SELECT count(*) FROM orders"
rig manifests deploy/ --graph api
rig hosts ssh node-1
rig task bootstrap    # a named list of steps from rig.yaml
```

`-e <env>` (or `$RIG_ENV`) picks the environment. A `protected: true` environment refuses changes without `--yes`.

## rig.yaml

```yaml
project: shop
default: kind

services:
  api:
    depends_on: [db]
    build: { go: ./cmd/api }
    ports: { http: 8080, metrics: 9090 }
    health: { port: http, path: /healthz }
  db: { role: infra, image: "postgres:17", ports: { pg: 5432 } }

environments:
  local: { runtime: { type: local } }
  kind:  { runtime: { type: kind, cluster: shop, namespace: shop, registry_port: 5001, manifests: [deploy] } }
  prod:  { protected: true, runtime: { type: kubernetes, context: prod-eu, namespace: shop } }

components:
  prom: { type: prometheus, addr: "svc://prometheus:9090" }
  db:   { type: sql, driver: postgres, addr: "svc://db:5432", user: app, password: "${DB_PASSWORD}" }
  load: { kind: loadgen, type: http, target: "svc://api:8080/", rate: 20, max: 500 }

dashboards:
  main:
    - { title: requests/s, query: 'sum by (service) (rate(http_requests_total[1m]))', unit: /s }
```

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
