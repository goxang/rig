# Protocol rig/1

How rig talks to a plugin program. The schema, `protocol/rig1.schema.json`, is the contract: the Go,
Python and TypeScript SDKs are generated from it (`go generate ./protocol`), and a plugin built with
an old SDK keeps working on a new host.

## Framing

Newline-delimited JSON-RPC 2.0 on the plugin's stdin and stdout, one message per line. stderr is the
plugin's log (`~/.local/state/rig/logs/<plugin>.log`). Both sides may have many requests in flight.

## Methods

Host to plugin. Only `initialize` is required; the rest answer `-32601` until implemented.

| method | params | result |
|---|---|---|
| `initialize` | host name and version, protocol versions, host capabilities (widget kinds and versions, features, contracts) | name, version, chosen protocol, `provides`, `capabilities` |
| `configure` | the plugin's own resolved config, its components' specs, its declared secrets | `{}` |
| `query` | `source`, `component`, `args`, `cursor`, `context` | a frame |
| `stream` | `id`, `source`, `args`, `context` | `{}`, then `$/stream` batches |
| `run`, `plan` | `action`, `component`, `args`, `job`, `context` | `message`, `frame`, `refs`, `outputs`, `changes` |
| `import` | `importer`, `files`, `dir`, `config` | a rig.yaml `fragment`, `watch`, `warnings` |
| `complete` | `language`, `text`, `pos` | `items` |
| `render`, `view.event` | `view` (and `node`, `event`, `value`) | a widget tree |
| `widget.render` | `widget`, `props`, `frame`, `size`, `theme` | `lines` |
| `resolve` | `ref` | `kind`, `title`, `view`, `url`, `fields`, `actions`, `links` |
| `search` | `query`, `limit` | `hits` |
| `doctor` | | `checks` |
| `event` | `topic`, `time`, `source`, `refs`, `data` | `{}` |
| `shutdown` | | `{}`, then the plugin exits |

Plugin to host, allowed as far as the manifest's `permissions` say:

| method | what |
|---|---|
| `host/query`, `host/run` | another plugin's sources and actions; policy applies as for any caller |
| `host/ask` | `input`, `confirm`, `pick` or `secret`, shown in the TUI or CLI |
| `host/ai` | a completion through the user's configured AI backend |
| `host/state.get`, `host/state.set` | per-plugin state, scoped to the project and environment |
| `host/secret` | a declared secret |
| `host/emit` | an event on the bus |
| `host/log` (notification) | a log line at `debug`, `info`, `warn` or `error` |

Notifications: `$/cancel {id}` (a request id or a stream id, either way), `$/progress` (a run's
`done`/`total`, `message` or one output `line`, against its `job`), `$/stream` and `$/ack`.

## Streams

The plugin sends `$/stream {id, seq, frame?, lines?, done?, error?}` batches, at most every 50 ms or
500 rows, `seq` counting from 1. The host acknowledges with `$/ack {id, seq}`; the plugin holds back
once 8 batches are unacknowledged. Every stream ends with one batch with `done: true`, also after
`$/cancel`.

## Frames

Columnar, typed and renderer-neutral: `columns[i]` holds every value of `fields[i]`.

```json
{"fields":[{"name":"time","type":"time"},{"name":"p95","type":"number","unit":"ms"},
           {"name":"service","type":"string","ref":"svc://{service}"}],
 "columns":[[1760090000,1760090015],[182.4,190.1],["parser","parser"]],
 "meta":{"total":2,"next":""}}
```

Field types: `string`, `number`, `int`, `bool`, `time` (Unix seconds), `duration` (seconds), `bytes`,
`json`, `ref`. A long result pages: `meta.next` is the cursor the next `query` passes.

## Context

Every call can carry `context: {project, env, user, roles, scope: {service, selection, range}, trace_id,
deadline, dry_run}`, so a plugin reacts to what was picked elsewhere. `user` and `roles` are the
caller: the `rig serve` account (`user/token` for an API token) and its roles for a remote call,
the person who asked for an approved inbox item, else the local user with no roles.

## Errors

JSON-RPC's codes, plus:

| code | meaning |
|---|---|
| `-32001` | denied by policy |
| `-32002` | not found |
| `-32003` | unavailable: a tool or service is missing; `data.hint` says what to do |
| `-32004` | conflict |
| `-32005` | needs input |

## Compatibility

- Append-only: methods, fields and enum values are added, never removed, renamed or retyped. Both
  sides ignore unknown fields. A request never gains a new required field.
- Behaviour follows the capabilities each side declares in `initialize`, never version numbers.
- Widgets (`table@1`) and contracts (`metrics/1`) are versioned on their own.
- Every official plugin records golden transcripts (`testdata/*.jsonl`) that core CI replays.

## SDKs

| language | where | example |
|---|---|---|
| Go | `github.com/goxang/rig/sdk` (standard library only) | `sdk/examples/hello` |
| Python 3.10+ | `sdk/python/rig` (standard library only) | `sdk/python/examples/hello.py` |
| TypeScript, Node 18+ | `sdk/typescript` (no dependencies, typings included) | `sdk/typescript/examples/hello.js` |

```go
p := sdk.New("hello")
p.Source("greetings", func(c *sdk.Ctx, a struct{ Name string `json:"name"` }) (*sdk.Frame, error) {
	return sdk.NewFrame(sdk.Col("name", sdk.TypeString)).Append(a.Name), nil
})
p.Action("greet", sdk.Write, func(c *sdk.Ctx, a sdk.Args) (*sdk.Result, error) {
	c.Progress(1, 1, "greeting")
	return &sdk.Result{Message: "hello"}, nil
})
p.Serve()
```

The three `hello` examples behave identically: `protocol/testdata/hello.jsonl`, recorded from the Go
one, replays against all three.

## Testing a plugin

`rig plugin test [folder]` is what a plugin author runs: the manifest, the build, every source that
needs no arguments, the contracts it implements and its `testdata/*.jsonl` transcripts (`--record`
writes one). A source that answers `-32003` or `-32005` (no project, a missing tool or setting) is
listed as unable to run there, not failed; the Go SDK sends `-32003` for a tool that is missing or fails. Underneath, `protocol/conformance` runs what every plugin must pass: initialize with
unknown fields, configure, unknown methods and sources, invalid params, a stray `$/cancel`, every
source and stream that takes no required arguments (a frame, or `-32003`/`-32005`), `doctor`,
`search` and shutdown.
`protocol/transcript` records a session through `rpc.Options.Tap` and replays it; record sessions
whose output does not depend on the clock.

## Changes

Before 1.0 breaking changes are allowed and listed here. At 1.0 the protocol freezes.

- 0.x: first version.
