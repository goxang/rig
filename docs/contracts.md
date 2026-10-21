# Contracts

A contract is a versioned convention for a kind of tool: the sources and actions a plugin provides,
their arguments, and the fields of the frames they answer. A plugin declares the ones it serves:

```yaml
name: gitlab
implements: [scm/1, tracker/1]
sources:
  scm.mrs: { run: bin/mrs }        # answers id, title, author, state, source, target, updated, url
```

What implementing one gets you:

- **a component**: a contract with a kind (`metrics/1`, `runtime/1`, `logs/1`, ...) lets the plugin
  stand in for an official one: `components: {m: {type: myplugin}}`. rig's screens, CLI and AI use
  it like any other metrics store. The component's config reaches the plugin in `configure`.
- **a standard view**: `scm`, `tracker`, `docs`, `iac`, `vcs`, `registry` and `secrets` come with a
  screen, added for every plugin that implements them and has no views of its own.

`rig plugin check <name>` checks a plugin against its contracts: every source and action is there,
streams stream, effects match, optional capabilities are whole, and the sources that need no
arguments answer frames with the contract's fields.

The contracts are `runtime builder registry logs metrics traces profiles debugger database cache
queue kv load query tests hosts vcs scm tracker docs iac notify secrets ai unit`, each at version 1. Their
definitions are in `contracts/<name>/v1.yaml`; a new version lives next to the old one.

`unit/1` is a project as an organisational unit: a team, the management above some teams, a
division. It answers `unit.info`, `unit.kpis`, `unit.people`, `unit.items` (risks, blockers,
decisions), `unit.requests` and `unit.children`, and takes `unit.request`, `unit.answer` and
`unit.item`. A unit reads the units below it over their `rig serve`, one level down, with a token
whose role allows only `<plugin>.unit.*`; a request sent down lands in the child's own state and
raises `unit.request` there, which an agent can act on. `unit.people` is separate so a role can
see delivery figures without people figures, or the reverse.

Optional capabilities are what a built-in screen offers beyond the kind's basics: `browse`, `edit`
and `snapshot` on data, `forward`, `env` and `resources` on a runtime, and so on. A screen shows
those features only for plugins that provide the whole capability.

Wire values follow the protocol: times are Unix seconds, durations are seconds, and `json` fields
hold nested values. A `browse` frame named `leaf` holds rows, not further levels. `exec` and `shell`
answer `outputs.argv` (with `env` and `dir`), which rig runs on the user's terminal.

Go adapters can be served without writing these by hand: `contracts.Serve` exposes an in-process
component as its kind's contract, which is how `rig-std` serves the official adapters.
