# Design

```
cmd/rig             the binary: CLI + TUI + every bundled adapter
spec/               rig.yaml: schema, loading, environments, variables, imports, dependency order
core/               the interfaces: one per component kind, plus optional capabilities
plugin/             the adapter registry
engine/             a project opened for one environment: lazy components, svc:// resolution,
                    up/down/build/wait, load limits, run state
manifest/           Kubernetes manifests from any folder: objects, relations, lint, render
adapters/<kind>/<type>
internal/cli        commands, thin over engine
internal/tui        the control plane, same engine
internal/viz        charts, gauges, sparklines, waterfalls, relation graphs
```

## Components and adapters

Everything rig talks to is a **component**: a named instance of an **adapter**, which implements the
interface of one **kind**.

| kind | interface | bundled adapters |
|---|---|---|
| runtime | `Runtime`: discover, start, stop, restart, status, scale, logs, exec, deploy | `local`, `docker`, `kubernetes`, `kind` |
| builder | `Builder` | `go` (no daemon), `docker`, `command` |
| metrics | `Metrics`: instant and range queries | `prometheus` (any Prometheus API), `scrape` (no server) |
| tracing | `Tracing`: services, search, trace | `zipkin`, `jaeger` |
| profiler | `Profiler` | `pprof`, `command` (async-profiler, perf, py-spy, ...) |
| debugger | `Debugger` | `delve` |
| logs | `LogSource` | `runtime` (default), `loki` |
| database | `Database` | `sql` (postgres, mssql, mysql) |
| cache | `Cache` | `redis` |
| messaging | `Messaging` | `rabbitmq` |
| kv | `KV` | `consul` |
| loadgen | `LoadGenerator`: start, stop, set rate, status | `http` (in-process), `kv` (generator pods steered through a key), `command` |
| hosts | `Hosts`: list with usage, shell | `ssh`; runtimes provide their own (nodes, this machine) |
| query | `Querier` | `http`; most adapters also answer queries |

What an adapter can do beyond its interface it offers through **optional interfaces**, found by type
assertion: `Forwarder` (reach a service port), `Querier` (ad hoc queries in its own language),
`Actioner` (named actions: `kind create`, `db seed`, `queue purge-all`, ...), `StateStore`,
`ImageLoader`, `Registrar`, `ProcessLocator`, `Relauncher`, `Pinger`. Front ends discover them, so a
new capability needs no change outside the adapter.

## Writing an adapter

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

`c.Decode` fills your options from the component's YAML. `env` gives the project, the active runtime,
`Resolve` for `svc://` addresses and `Component` for other components (the `kv` load generator uses the
`kv` component it names). Build your own binary by importing `adapters/all` and your package next to
`cmd/rig`'s three lines.

Runtimes read their per-service settings from a section named after them (`k8s:`, `docker:`,
`local:`), so the shared service schema never grows runtime fields.

## Choices worth knowing

- **External tools over client libraries** for kubectl, docker, kind, ssh and dlv: rig uses the user's
  kubeconfig, contexts, ssh config and agents exactly as the terminal does, and the binary stays small.
- **The Kubernetes runtime never uses the current context.** An environment names its context; a
  protected environment refuses changes until confirmed (`--yes`, or the TUI's confirm prompt).
- **Run state lives with the environment**: a ConfigMap on Kubernetes (the whole team sees the tag,
  images and rates), `.rig/<env>/state.json` elsewhere.
- **Builds**: the `go` builder compiles on the host and adds one layer to a base image with
  go-containerregistry: no daemon, no in-container module download, local build cache.
- **No metrics server needed**: the `scrape` adapter reads `/metrics` itself and answers a PromQL
  subset over a short in-memory history.
