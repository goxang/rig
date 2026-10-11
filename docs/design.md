# Design

rig's core knows nouns and verbs; plugins know technologies. The rig binary holds no docker,
Kubernetes, SQL, Prometheus, GitLab or Jira code: all of it is a plugin, most of it in
[goxang/rig-plugins](https://github.com/goxang/rig-plugins).

```
 clients   CLI · TUI · MCP server · AI tool router · HTTP/WS server
              │
 api       api.Engine: catalog, query/stream, run/jobs, render, events, search, resolve, plugins
              │  (engine.Local in-process; api.Client over HTTPS + WebSocket to rig serve)
 kernel    Catalog │ Sources+Frames │ Actions+Jobs │ Views+Widgets │ Events │ Policy+Audit
           config pipeline (load, include, plugin defaults, imports, templates, env overlay,
           interpolate, validate) with provenance · refs · cache · identity
 engine    environments, dependency order, up/down/wait, svc:// resolution, shared infra, run state
 host      discovery · install, lock, trust · supervisor (lazy start, idle stop, restart) · sandbox
           transports: declarative │ script │ JSON-RPC over stdio (rig/1) │ MCP bridge
 plugins   rig-std (docker, kubernetes, sql, prometheus, ...) · gitlab · jira · terraform · yours
```

## The six primitives

| primitive | what it is | example |
|---|---|---|
| **Catalog** | typed resources built from config: services, components, environments, tasks, queries, dashboards, views, plus kinds plugins add | `service/api`, `component/db` |
| **Source** | named data answering `query` (a frame) or `stream` (frames or lines over time), with an optional query language and completion | `metrics.range`, `db.query`, `jira.search` |
| **Action** | a named verb with an argument schema and an `effect` (`read`, `write`, `danger`); running one makes a **job** with output, cancel and audit | `runtime.restart`, `tf.apply` |
| **View** | a widget tree with data bindings, placed in a slot (a tab, a sub-tab, a detail page, a panel) | the Metrics screen, a Sentry tab |
| **Event** | a bus message hooks and agents subscribe to | `deploy.finished`, `alert.firing` |
| **Policy** | the decision for (who, verb, object): allow, deny, confirm, reauth or approve | AI writes wait in the inbox |

**Refs** link them: `svc://api`, `k8s://ns/deployment/api`, `jira://PAY-12`. `enter` on one opens it,
`rig open <ref>` too, and the AI follows them.

## Packages

```
cmd/rig       the binary
api/          the Engine seam every client uses; api.Fake for tests
engine/       engine.Local: a project opened for one environment, behind api.Engine
kernel/       the primitives: catalog, frames, actions, jobs, events, policy, audit, refs, view, inbox, cron
spec/         rig.yaml: schema, loading, environments, variables, imports, lint, migrate
host/         plugins: manifests, discovery, install, lock, registry, signatures, trust, sandbox, MCP bridge
protocol/     rig/1: the JSON schema, generated types, transcripts, conformance
contracts/    metrics/1, scm/1, tracker/1, ...: what a plugin serves to stand in for a kind of tool
core/         the Go interface of each component kind, for Go adapters
plugin/       the in-process registry Go adapters register with (inside a plugin binary, not rig)
sdk/          plugin SDKs: Go, Python, TypeScript
ai/           chat, backends, tool router, redaction, guard rails, agents
server/       rig serve: accounts, TOTP, OIDC, tokens, TLS
manifest/     Kubernetes manifests from any folder: objects, relations, lint, render
scaffold/     rig init and rig plugin new
internal/cli  commands, thin over api.Engine
internal/tui  the terminal renderer, same api.Engine
```

A new operation is an action or a source on the Engine, never a client-only shortcut: that is how
policy, jobs and audit apply to the CLI, the TUI, MCP, the AI and remote users alike.

## Components, kinds and contracts

Everything rig talks to is a **component**: a named instance of a plugin that implements one **kind**.

| kind | contract | official plugins |
|---|---|---|
| runtime | `runtime/1` | `local`, `docker`, `kubernetes`, `kind` |
| builder | `builder/1` | `go` (no daemon), `docker`, `command` |
| metrics | `metrics/1` | `prometheus`, `scrape` (no server), `aws` |
| tracing | `traces/1` | `zipkin`, `jaeger`, `tempo` |
| logs | `logs/1` | `runtime` (default), `loki`, `aws` |
| profiler | `profiles/1` | `pprof`, `command` |
| debugger | `debugger/1` | `delve` |
| database | `database/1` | `sql` (postgres, mssql, mysql) |
| cache | `cache/1` | `redis` |
| messaging | `queue/1` | `rabbitmq`, `kafka` |
| kv | `kv/1` | `consul` |
| loadgen | `load/1` | `http`, `kv`, `command`, `k6`, `vegeta`, `ghz` |
| hosts | `hosts/1` | `ssh` |
| tests | `tests/1` | `junit`, `pytest`, `jest`, `vitest`, `hurl`, `k6` |
| vcs, scm, tracker, docs, iac, registry, notify | `vcs/1`, `scm/1`, `tracker/1`, `docs/1`, `iac/1`, `registry/1`, `notify/1` | `git`, `gitlab`, `github`, `jira`, `confluence`, `markdown-docs`, `terraform`, `registry`, `slack` |

A plugin that implements a contract feeds rig's own screens: write `metrics/1` and the Metrics screen,
alerts and the AI use your backend. `rig-std list` shows what each official plugin serves;
[contracts](contracts.md) lists every contract.

## How an official plugin runs

`rig-std` is one Go binary holding every Go adapter. A component `{type: prometheus}` makes the host
start `rig-std prometheus` on first use (and stop it when idle); rig and the adapter talk rig/1 over
stdio. Inside, the adapter implements the `core` interface of its kind, and `contracts.Serve` turns it
into the contract's sources and actions. What an adapter can do beyond its interface it offers through
optional interfaces (`Forwarder`, `Querier`, `Actioner`, `StatusLister`, ...), found by type assertion,
so a new capability needs no change in rig.

```go
package victoria

func init() {
	plugin.Register(core.KindMetrics, "victoria", "VictoriaMetrics with its extra API", New)
}

type Options struct {
	Addr string `yaml:"addr"`
}

func New(env core.Env, c *spec.Component) (any, error) {
	var o Options
	if err := c.Decode(&o); err != nil {
		return nil, err
	}
	return &Metrics{env: env, opt: o}, nil
}
```

Add it to the table in rig-plugins' `cmd/rig-std/plugins.go`; `go test ./cmd/rig-std` checks it serves
its contract. Outside rig-plugins, a plugin needs none of this: [plugins](plugins.md) shows the YAML,
script, SDK and MCP ways.

## Choices worth knowing

- **External tools over client libraries** for kubectl, docker, kind, ssh and dlv: rig uses the user's
  kubeconfig, contexts, ssh config and agents exactly as the terminal does.
- **The Kubernetes runtime never uses the current context.** An environment names its context; a
  protected environment refuses changes until confirmed.
- **Shared infrastructure**: an environment with `infra: <env>` hands its `shared` services to that
  environment's runtime, so status, logs, start and stop go there and `svc://` resolves there.
- **Lazy everything**: plugins start on first use; the TUI asks the runtime for every service's status
  in one call every 3s, and a screen fetches only while it is showing.
- **Run state lives with the environment**: a ConfigMap on Kubernetes (the whole team sees the tag,
  images and rates), `.rig/<env>/state.json` elsewhere.
- **The protocol is the contract, not Go types**: rig/1, the manifest, widgets and contracts are
  append-only and versioned on their own, so old plugins keep working.
