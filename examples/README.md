# Examples

Each folder is a whole project: a `rig.yaml` and the code it runs. Start with `hello`.

| example | runs on | what it shows |
|---|---|---|
| [hello](hello) | local processes | the smallest `rig.yaml`: one Go service, logs, metrics without Prometheus, a load generator, a test suite; opens in the simple view |
| [shop](shop) | docker (or local + docker infra) | an api and a worker over Postgres, Redis, RabbitMQ and Zipkin; `compose.yaml` is the same stack, and what `rig init` started from. Every screen has something real: orders in the database, the queue and its failures, one trace from api to worker, saved queries, alerts |
| [python](python) | local processes | a Python service (stdlib only): no build step, JSON logs, `/metrics` read by rig, load, unittest |
| [kind](kind) | kind, docker, local | Kubernetes: manifests in `deploy/`, an HPA, pprof, load from rig |

```bash
cd examples/hello
rig up        # build and start
rig           # the control plane (V switches simple / detailed view, ? lists the keys)
rig down
```

`examples/internal/demo` is what the services share (JSON logs, metrics, spans), so each `main.go`
is only about its own work.
