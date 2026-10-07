---
name: rig
description: Set up and run this project with rig (rig.yaml): write or extend rig.yaml for any stack (compose, Kubernetes, Go, Python, Node, Java, Rust), add infrastructure (Postgres, Redis, Kafka, RabbitMQ, OpenTelemetry), choose the UI's screens, add dashboards, queries and test suites, then start, watch and test the services. Use whenever the user asks to set up rig, add a service or component to rig.yaml, change rig's screens or panels, or run, debug or test services through rig.
---

# rig

rig runs a project's services and infrastructure (local processes, docker, kind, Kubernetes) from one
`rig.yaml`. The full key reference is `rig docs config` (MCP: `rig_docs`); read the section you
touch before editing.

## Start a project

1. `rig init --dry-run` (MCP: `rig_init`) prints the rig.yaml rig would write from what the directory
   has: compose files, Kubernetes manifests, Go modules, Django/FastAPI/Flask, Node, Spring, Cargo.
2. Add infrastructure the code needs: `--with postgres,redis,kafka,rabbitmq,consul,prometheus,jaeger,loki,otel`.
   Look for it in the code first (connection strings, client libraries, env var names).
3. Read the draft against the code: ports each service really listens on, `depends_on`, env vars it
   reads (connection strings must point at service names: `db:5432`, not `localhost`).
4. Write it (`rig init`, or `rig_init` with `write: true`), then `rig env` must load without errors.
5. `rig up` (or `rig up --build` for kind) and `rig status`.

## One source of truth

Services already defined elsewhere are imported, never copied: `imports: [{compose: compose.yml}]`,
`[{kubernetes: deploy/k8s}]` (a service per Deployment/StatefulSet/DaemonSet; the Kubernetes runtime
deploys from those folders). rig reads them on every load, so a new workload or compose service is a
rig service at once. Under `services.<name>` write only what those files lack (a Go main to `build`,
`health`, `groups`, `run` for local processes): rig.yaml's keys merge over the imported service. An
environment `{type: docker, compose: {project: <name>, files: [...]}}` drives compose's own
containers; a remote server is `host: ssh://user@server` (or `context:`). Do not copy images,
commands, env or manifests into rig.yaml. `rig init --deep` on an existing project adds what is new
(imports included) without touching the rest.

## Edit rig.yaml

Edit the file directly (MCP: `rig_file`), then run `rig env` to check it still loads.

| want | write |
|---|---|
| a service | `services.<name>`: `build` (go: ./cmd/x, or dockerfile), `run.command` for local processes, `ports`, `env`, `env_file`, `depends_on`, `health: {port, path}` |
| infrastructure | a service with `role: infra` and `image`, plus the component that reads it: `components.db: {type: sql, driver: postgres, addr: "svc://db:5432", ...}` |
| OpenTelemetry | `otel: {endpoint: svc://otel-collector:4318}` or per signal `traces:`/`metrics:`/`logs:`; rig sets OTEL_* on every app service |
| the UI's screens | `ui: {tabs: [services, logs, metrics, traces, queries, kv, data, load, manifests, hosts, tests]}`: listed ones only, in that order; left out, every screen the project configures |
| a dashboard | `dashboards.<name>: {vars, panels: [{title, query, unit, kind, width}]}`, PromQL against a `prometheus` component |
| a saved query | `queries.<name>: {source: <component>, query: ..., every: 30s}` |
| tests | `tests.<name>`: Go `packages: [./...]`, or any runner as `command:` writing JUnit to `$RIG_JUNIT` (`pytest --junitxml=$RIG_JUNIT`) |
| per-environment changes | `environments.<env>.services.<name>: {...}` patches, `environments.<env>.otel`, `only: [...]` |

Addresses take `svc://service:port`, in components and inside env values alike (`DATABASE_URL:
postgres://app@svc://db:pg/app`): rig resolves them inside the runtime's network for containers and
pods, and forwards a port for local processes. A value used in several places (database name, user)
goes in `vars:` once (`environments.<env>.vars` per environment) and is referenced as `${NAME}`.
Secrets go in `${NAME}` with `secrets:`, never in the file.

## Run and check

`rig status`, `rig logs -F <svc>`, `rig query <component> '<query>'`, `rig test <suite>`
(`--failed` reruns failures), `rig traces`, `rig metrics -- '<promql>'`. Mutating commands on a
protected environment need the user's go-ahead.

## When rig itself falls short

Fix rig instead of working around it in rig.yaml, when you can push to github.com/goxang/rig. Ship
every change you make there yourself: commit (Conventional Commits: `feat:` bumps the minor version,
anything else the patch), push to `main`, wait until the `ci` workflow is green; its
last job, `release`, publishes the new tag (`gh run watch`, `gh release view`). The user's only step is
`rig upgrade`; tell them the version to expect. Never leave a rig change uncommitted, unpushed or
unreleased.
