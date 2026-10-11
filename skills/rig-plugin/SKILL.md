---
name: rig-plugin
description: Write, test and publish a rig plugin (rig-plugin.yaml): a tab, a data source, an action, an importer, a contract implementation or an MCP bridge, in YAML, any script language, Go, Python or TypeScript. Use whenever the user wants rig to show, fetch or do something it does not yet, or asks for a plugin.
---

# Writing a rig plugin

A plugin is a folder with `rig-plugin.yaml`. Start from a template, never from scratch:

```bash
rig plugin new <name> --lang yaml|bash|python|go|ts   # manifest, source, action, view, skill, release workflow
cd <name> && rig plugin test                           # must pass before you are done
rig plugin dev <path>                                  # from a project: link, rebuild on change, trace RPC
```

Pick the smallest way that works:

| need | way |
|---|---|
| a tab of terminals, a dashboard over existing sources, tasks, a pack | **yaml**: views and keys only |
| fetch a list, run a command | **bash** (any language): `run:` scripts |
| state, streams, plans, a contract rig's screens use, many calls | **server**: Go, Python or TypeScript SDK |
| an MCP server already does it | `mcp: {command: ...}` |

## The manifest

```yaml
name: sentry                     # ^[a-z][a-z0-9-]*$, the folder's name
version: 0.1.0
description: Sentry issues as a tab
detect: {files: [.sentryclirc]}  # where it applies; user-wide plugins show views only there
config:                          # JSON Schema properties of plugins.sentry: in rig.yaml
  org: {type: string}
  token: {type: string, secret: true}
permissions: {net: [sentry.io], env: [SENTRY_TOKEN], exec: [curl, jq]}
sources:                         # script sources; a server declares its own at runtime
  issues: {run: ./issues.sh, help: unresolved issues, cache: 30s, args: {project: {type: string}}}
actions:
  resolve: {run: ./resolve.sh, effect: write, help: resolve an issue, args: {id: {type: string}}}
views:
  Sentry:
    slot: {tab: {key: Y}}
    widget: table
    source: sentry.issues        # always <plugin>.<source>
    refresh: 30s
    actions: [{key: R, name: resolve, action: sentry.resolve, args: {id: '${row.id}'}}]
skills: [skills/sentry]
```

- Paths in `run:`, `server.command` and `build:` are relative to the plugin folder; write them as
  `./x` or `bin/x`. Commands run in the **project**; `$RIG_PLUGIN_DIR` is the plugin folder.
- `build:` runs after install from git or a path, and in `plugin dev` and `plugin test`.
- Unknown keys warn and are ignored. Keep `rig plugin test` free of warnings.
- Declare every secret, host, binary (`exec`), path (`fs`) and env variable you need. Undeclared ones
  are not there: the environment is scrubbed and Linux runs plugins under Landlock.

## Scripts

Arguments arrive as JSON in `$RIG_ARGS` and on stdin, the plugin's config as JSON in `$RIG_CONFIG`;
`$RIG_PLUGIN_DIR/bin` is on `PATH`. Print JSON (array of objects), NDJSON, CSV, TSV, YAML or plain
lines; `format:` forces one. Exit 0 is success; stderr goes to `rig plugin logs <name>`. Scripts see
only the environment variables listed in `permissions.env`; rig's secret store (`permissions.secrets`)
reaches servers only, through `c.Secret`.

## Servers

JSON-RPC 2.0 over stdio (`rig docs protocol`). The SDKs do the protocol:

```go
p := sdk.New("sentry").Version("0.1.0")
p.Source("issues", func(c *sdk.Ctx, a struct{ Project string `json:"project"` }) (*sdk.Frame, error) {
	f := sdk.NewFrame(sdk.Col("id", sdk.TypeString), sdk.Col("title", sdk.TypeString), sdk.Col("count", sdk.TypeInt))
	// fetch with c.Secret("SENTRY_TOKEN") and c.Config(); c is a context.Context
	return f.Append("42", "nil map", 7), nil
}, sdk.Describe("unresolved issues"))
p.Action("resolve", sdk.Write, func(c *sdk.Ctx, a struct{ ID string `json:"id" rig:"required"` }) (*sdk.Result, error) {
	return &sdk.Result{Message: "resolved " + a.ID}, nil
})
p.Serve()
```

Python: `@p.source("issues")` returning a list of dicts, `@p.action("resolve", effect="write")`,
`p.serve()`. TypeScript: `new Plugin(name, version).source(...).action(...).serve()`. The templates
vendor the Python and TypeScript SDKs in `rig/`.

- **Frames** are tables: typed columns (`string int float bool time duration bytes ref json`). A column
  of type `ref` (`sentry://issue/42`) makes rows openable. Return the same columns every call.
- **Effects**: `read` (no change), `write` (policy, audit, the AI waits for approval), `danger`
  (asks first). Add a `Plan` (or `plan: true` for scripts) so a write shows its changes before running.
- **State** (`c.State`/`c.SetState`), progress (`c.Progress`), log lines (`c.Line`) and streams
  (`p.Stream`) are there when you need them.

## Views

Layouts: `grid: [[a, b], [c]]`, `tabs: {Name: widget}`, `split: [a, b]`, `stack`, `box`, `scroll`;
or one widget's keys at the view's top level. Widgets: `table list kv tree stat gauge bar sparkline
chart heatmap text markdown code diff logs waterfall timeline calendar graph board form terminal canvas`.
Bind with `source:`, `args:`, `refresh:`. Slots: `tab`, `{tab: {key: K}}`, `home`. A terminal takes
`command`, `dir` (relative to the plugin: `dir: .`), `env`, `keep`. Unknown widgets render their
`fallback:`. Details: `rig docs views`.

## Contracts

`implements: [tracker/1]` promises the contract's sources and actions with its fields
(`rig docs contracts`). Name them `<contract>.<source>`, such as `tracker.issues`. It lights up the
standard view when the plugin has none of its own, lets components of that kind use the plugin, and
`rig plugin test` checks it live.

## Compatibility rules

Append-only: never remove, rename or retype a source, action, argument or column a release had. Add
optional arguments only. A breaking change is a new major version (and a new contract version).

## Testing

`rig plugin test` checks the manifest, runs the build, calls every source that needs no arguments,
checks contracts and replays `testdata/*.jsonl`. Answer what the environment lacks (a setting, a
service) with `sdk.Unavailable(message, hint)`: the test lists it instead of failing. `rig plugin test --record` writes the transcript;
record only output that does not depend on the clock or the network. Then try it for real:
`rig plugin dev <folder>` from a project, open its tab, run its actions.

## Publishing

Commit, push, tag `v0.1.0`. The template's `release` workflow builds archives and a GitHub release;
its summary holds the entry to propose to `goxang/rig-plugins/registry/index.json`. Users can also
`rig plugin add github.com/you/sentry@v0.1.0` straight from git.

## Example: Sentry issues as a tab

```bash
rig plugin new sentry --lang bash && cd sentry
```

`rig-plugin.yaml` as above, with `issues.sh`:

```sh
#!/bin/sh
org=$(printf '%s' "$RIG_CONFIG" | jq -r '.org // empty')
project=$(printf '%s' "$RIG_ARGS" | jq -r '.project // "default"')
curl -sf -H "Authorization: Bearer $SENTRY_TOKEN" \
  "https://sentry.io/api/0/projects/$org/$project/issues/?query=is:unresolved" |
  jq '[.[] | {id, title, count: (.count|tonumber), last: .lastSeen}]'
```

Then `chmod +x issues.sh`, `rig plugin test`, and `rig plugin dev .` from the project. Replace the
skill stub in `skills/sentry/SKILL.md` with when to use each source and action.
