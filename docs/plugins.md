# Plugins

A plugin is a folder with a `rig-plugin.yaml`. It adds sources (data), actions (things to do), views,
importers, contracts and anything else rig.yaml can hold.

## Using plugins

```bash
rig plugin search gitlab            # the registry index
rig plugin add pack/gitlab          # install and pin in rig.lock: a name, pack/<name>, git URL, archive or path
rig plugin add -u arcade            # only for you, in every project: ~/.config/rig/rig.lock
rig plugin ls                       # what the project has, where from, on or off
rig plugin off jira                 # for you (.rig/local.yaml); --project writes rig.yaml
rig plugin up                       # upgrade within each major version
rig plugin info gitlab              # what it provides and its permissions
rig plugin logs gitlab -F           # its stderr; trace shows the JSON-RPC exchange (RIG_TRACE=gitlab)
```

rig finds plugins in what `rig.lock` pins, then your own `~/.config/rig/rig.lock`, then `~/.config/rig/plugins`, then `.rig/plugins`, then
`plugins.<name>.path`; a later one of the same name wins. `plugins.<name>:` in rig.yaml configures one
against its `config:` schema. A plugin in `~/.config/rig/plugins` shows its views only in projects
it applies to (see [views](views.md)). A pack is a plugin that is mostly `requires:`.

`rig plugin vendor` copies the locked plugins into `.rig/vendor` for offline installs. A mirror (`RIG_REGISTRY` or
`registry:` in `~/.config/rig/config.yaml`) holds `index.json`, `<name>/<version>/<archive>` and the
shared files (one `rig-std` per platform) in `files/`.

## Official plugins

[goxang/rig-plugins](https://github.com/goxang/rig-plugins) holds them, grouped in packs: `pack/std`
(runtimes, builders, git, imports), `pack/data`, `pack/observability`, `pack/qa`, `pack/gitlab`,
`pack/github`, `pack/atlassian`, `pack/cloud-aws` and `pack/product`; plus `slack`, `mcp-bridge`,
`arcade`, `plugin-tree` (`:plugintree`: dependencies, how each talks to rig) and `rig-usage` (`:usage`: CPU and memory of rig and its plugins). Most are one Go binary, `rig-std`; `rig-std list` prints what each one serves.

## Writing one

```bash
rig plugin new sentry --lang go     # or python, ts, bash, yaml
cd sentry && rig plugin test        # manifest, build, sources, contracts, transcripts
cd ~/my-project && rig plugin dev ~/sentry
```

`new` writes the manifest, a source, an action, a view, a skill and a release workflow. `dev` links
the folder into the project, rebuilds it on every change (a running rig reloads) and prints the RPC
trace. `test --record` writes `testdata/<name>.jsonl`, replayed on every later `test`. Run `test` from a
project to use its `plugins.<name>` config and the first component of `type: <name>`.

## Four ways to implement

| way | when | how |
|---|---|---|
| declarative | tabs, terminal panes, dashboards, tasks, queries, packs | YAML only |
| script | quick sources and actions in any language | `run:`; args in `$RIG_ARGS` and on stdin; stdout is JSON, NDJSON, CSV, TSV, YAML or lines (`format:` overrides); the exit code is the result |
| server | providers, streams, plans, dynamic views, state | `server: {command: ...}`, JSON-RPC over stdio ([protocol](protocol.md)); SDKs in Go, Python and TypeScript |
| MCP bridge | any MCP server | `mcp: {command: ...}`: tools become actions, resources sources |

## Manifest

```yaml
name: sentry                       # lowercase, digits, dashes
version: 0.1.0                     # set by the release
rig: ">=1.0"                       # host versions it supports
description: Sentry issues as a tab
publisher: you
requires: {git: ^1}
needs: {jq: "apt install jq / brew install jq"}   # programs on the machine its scripts run: missing ones are named at install, in doctor and when a script is run
implements: [tracker/1]            # contracts: standard views and checks (see contracts)
detect: {files: [.sentryclirc], git_remote: sentry}
build: [go, build, -o, bin/sentry, .]   # runs after a git or path install, and in dev and test
server: {command: [bin/sentry]}    # paths are relative to the plugin's folder
permissions: {net: [sentry.io], secrets: [SENTRY_TOKEN], exec: [git], fs: [], env: []}
config: {org: {type: string}, token: {type: string, secret: true}}
sources: {issues: {run: ./issues.sh, cache: 30s}}        # script ones; a server declares its own
actions: {resolve: {run: ./resolve.sh, effect: write}}
views:
  Sentry:
    slot: {tab: {key: Y}}
    widget: table
    source: sentry.issues          # plugin sources are named <plugin>.<source>
    actions: [{key: R, name: resolve, action: sentry.resolve, args: {id: '${row.id}'}}]
skills: [skills/sentry]
```

Unknown keys warn, never fail. The rest of rig.yaml's keys (tasks, queries, dashboards, agents) may sit
in a manifest too, below the project's own.

## Actions and effects

`effect` is `read`, `write` or `danger`. Writes go through policy and the audit log; danger asks
first; an action with `plan: true` (or a server `Plan`) shows its changes before it runs. Keys in
`keys:` bind it where its `on:` schemes are shown.

## Compatibility

- The protocol and manifest are append-only: nothing is removed, renamed or retyped.
- Behaviour follows declared capabilities, not version numbers.
- Widgets (`table@1`) and contracts (`tracker/1`) version on their own; a host without a widget
  renders its fallback.

## Publishing

Push a tag such as `v0.1.0`: the template's workflow builds the archives and makes a release, and its
summary has the index entry to propose to `goxang/rig-plugins/registry/index.json`.
