# rig: plan

**rig** is the successor to rig. It gives engineers and AI one place to run the whole software lifecycle
(plan, build, test, ship, run, observe, fix). Everything that talks to a technology is a plugin.

A rig holds the work and guides every tool to the same spot. It is three letters, sits next to rig in
meaning, and `rig.yaml` reads naturally. `goxang/rig` is free on GitHub, and no `rig` binary exists on
this machine.

This plan is written to be executed in one continuous run (section 19). It is not a set of phases to
release separately. The end state is rig 1.0 and rig-plugins 1.0, with full rig parity except `flow:`,
the switch project migrated, and everything installed on this machine. Windows is not supported.

---

## 1. Decisions at a glance

1. **rig is a fork of rig, restructured in place.** The tree stays working after every step: a strangler
   refactor, not a rewrite from zero. The rig repo is never touched.
2. **The kernel knows nouns and verbs. Plugins know technologies.** Core has no docker, Kubernetes,
   SQL, Prometheus, Jira or GitLab code. All of it lives in `goxang/rig-plugins`.
3. **Six primitives**, and everything else is built from them: **Catalog** (what exists), **Source**
   (data as frames and streams), **Action** (verbs that change things), **View** (widget trees),
   **Event** (what happened) and **Policy** (who may). **Refs** (URIs) link them.
4. **A plugin is a reusable `rig.yaml` plus an optional program.** If you can write `rig.yaml`, you can
   write a plugin. Code is needed only when YAML and shell are not enough.
5. **Four ways to implement a plugin:**
   - declarative (YAML only)
   - script (any executable, JSON or table on stdout)
   - server (JSON-RPC over stdio, any language, SDKs for Go, Python and TypeScript)
   - MCP bridge (any existing MCP server becomes a plugin)
6. **The protocol is the contract, not Go types.** The protocol `rig/1` is append-only. Widgets and
   contracts are versioned separately. Capabilities are negotiated. Golden transcripts of every official
   plugin run in core CI. Old plugins keep working.
7. **Views are data (widget trees), never terminal code.** The TUI is one renderer. A web renderer can
   be added later with no change to core or plugins. Two escape hatches give total freedom: `terminal`
   (a real PTY in a pane) and `canvas` (the plugin draws).
8. **One Engine API seam.** CLI, TUI, MCP, AI, the server and a future web UI are all clients of the
   same API. Locally it runs in-process. Remotely it runs over HTTPS and WebSocket. That is how server
   mode, RBAC and web stay possible without corrupting the design.
9. **Policy is enforced in one place**, for every caller: a human, the AI, an MCP agent or a remote
   user. RBAC, protected environments, re-authentication and four-eyes approval are all policy effects.
10. **One source of truth.** Importers turn other tools' files (compose, Kubernetes, godev, just, make,
    Grafana, Prometheus rules, OpenAPI, GitLab CI, rig.yaml) into `rig.yaml` fragments on every load.
    `rig.yaml` holds only what no other file states, and `rig lint --redundant --fix` enforces it.
11. **Install is automatic and safe.** A project declares its plugins, or rig infers them from the
    component types it uses. The first run installs exactly what `rig.lock` pins, after a workspace
    trust prompt.
12. **Standard, learnable controls.** The CLI grammar is `rig <verb> [targets]` and
    `rig <name> <action>`. The TUI command palette (`:`) uses the same grammar. **Lowercase keys look,
    uppercase keys change.** Right-click on anything lists every action that applies to it.
13. **Rig's performance decisions are kept** (section 17): lazy everything, one batched status call,
    work done once per change and never per frame, external tools over client libraries.
14. **The stack:** Go 1.26 (toolchain directive), bubbletea v2 and lipgloss v2 (`charm.land/...`),
    creack/pty with charmbracelet/x/vt, the official MCP Go SDK, Linux and macOS only.

---

## 2. Repositories

| repo | what | module / artifacts |
|---|---|---|
| `goxang/rig` (fork of `goxang/rig`, history kept) | kernel, engine, Engine API, server, TUI, widgets, AI, CLI, contracts, protocol, SDKs | `github.com/goxang/rig`; the SDK is a separate zero-dependency module `github.com/goxang/rig/sdk` |
| `goxang/rig-plugins` | every official plugin, the packs, and the registry index | one Go module; one multiplexed binary `rig-std` per OS and architecture; `registry/index.json` |
| `<company>/rig-pack` (example: the switch's org) | a company pack: required plugins, policy, skills, shared tasks | a plugin whose manifest mostly lists `requires:` |

The official plugins are versioned in lockstep (`rig-plugins vX.Y.Z` tags every plugin). Third-party
plugins version themselves.

Names and paths:
- Files: `rig.yaml`, `rig.lock`, `.rig/`.
- Config: `~/.config/rig/`. Data: `~/.local/share/rig/`. Cache: `~/.cache/rig/`.
- Environment variables use the prefix `RIG_*`. During migration, `RIG_*` is still read and also set
  for task steps.

---

## 3. Principles (these settle disputes)

1. **Core does not grow domains.** If one plugin could do it, it is not core.
2. **Additive only after 1.0.** Never remove, rename or retype anything in the protocol, a manifest, a
   contract or a widget. Add new and deprecate old.
3. **Data, not code, crosses the boundary.** Frames, widget trees, action specs and refs are all JSON
   with schemas.
4. **Lazy, batched, cached, visible-only.** Nothing runs that nobody looks at.
5. **The same thing has the same name everywhere.** An action called `restart` is the CLI verb, the
   key `R`, the palette entry, the MCP tool and the AI tool.
6. **Least privilege by default.** Plugins get declared secrets, declared paths and a scrubbed
   environment. The AI gets the user's rights at most.
7. **Low learning curve beats power.** YAML plus interpolation plus templates. No general-purpose
   config language (no CUE, Jsonnet or expressions).
8. **External tools over client libraries** (kubectl, docker, kind, ssh, dlv, git, terraform, aws). The
   user's contexts, credentials and configs work exactly as they do in their shell, and binaries stay
   small.

---

## 4. Architecture

### 4.1 Layers

```
 clients     CLI  ·  TUI renderer  ·  MCP server  ·  AI tool router  ·  HTTP/WS server  ·  (web, later)
               └──────────────────────────────┬──────────────────────────────────────────────┘
 Engine API   catalog · query/stream · run/jobs · render · events · search · resolve(ref) · plugins
               │                (Go interface; in-process locally, HTTPS + WebSocket remotely)
 kernel       Catalog │ Sources+Frames │ Actions+Jobs │ Views+Widgets │ Events+Hooks │ Policy+Audit
              config pipeline (load → include → plugin defaults → imports → templates → env overlay
              → interpolate → validate → catalog, with provenance) · refs · cache · identity
 engine       environments, dependency order, up/down/wait, svc:// resolution, shared infra, run state
 host         discovery · install/lock/trust · supervisor (lazy start, idle stop, restart, limits)
              transports: declarative │ script │ JSON-RPC stdio │ MCP bridge · sandbox (Landlock)
 plugins      rig-std (docker, kubernetes, sql, prometheus, ...) · jira · gitlab · terraform · yours
```

### 4.2 The six primitives

| primitive | what it is | example |
|---|---|---|
| **Catalog** | Typed resources `{kind, name, spec, links, origin}` built from config. Kinds come from core (`service`, `component`, `environment`, `task`, `query`, `dashboard`, `suite`, `report`, `agent`, `view`) and from plugins (`terraform.stack`, `gitlab.project`). | `service/parser`, `component/db` |
| **Source** | A named data provider answering `query` (a frame) or `stream` (frames or lines over time), with an optional query language, completion and a cache TTL. | `metrics.range`, `db.query`, `jira.search` |
| **Action** | A named verb with an argument schema and an `effect` (`read`, `write` or `danger`). It may support `plan` (dry run). Running one creates a **job** with progress, output, cancel and audit. | `runtime.restart`, `jira.create`, `tf.apply` |
| **View** | A widget tree with data bindings, placed in a **slot** (a top tab, a sub-tab of another view, a detail page for a ref scheme, a section of the service page, a side panel). | the Metrics screen, a Payments tab |
| **Event** | A typed bus message; hooks and agents subscribe to it. | `deploy.finished`, `alert.firing`, `scm.mr.merged` |
| **Policy** | The decision for (subject, verb, object): `allow`, `deny`, `confirm`, `reauth` or `approve` (four-eyes). Every write is audited. | AI writes go to the approval inbox |

**Refs** are the glue. Every resource and every row can carry a URI whose scheme is owned by a plugin.
- Core owns: `svc://`, `env://`, `view://`, `job://`, `file://path#L12`.
- Plugins own, for example: `k8s://ns/deployment/api`, `db://db/dbo.transactions?id=7`, `trace://abc`,
  `jira://PAY-12`, `gitlab://grp/proj/-/merge_requests/12`, `confluence://SPACE/Page`, `tf://infra/aws_db.main`.
- `enter` on a ref opens it. `rig open <ref>` opens it from the CLI. The AI follows refs with
  `open(ref)`. Links between refs (an MR to an issue to a service to a deploy to an alert) are how the
  AI and the user cross tools.

**Context** travels with every call:
`{project, env, user, roles, scope: {service, selection, range}, trace_id, deadline, dry_run}`. A plugin
view therefore reacts to what was picked elsewhere (rig's `$service`, generalised).

### 4.3 The Engine API seam

`api.Engine` is the only thing clients use:

```go
type Engine interface {
	Catalog(ctx) (*Catalog, error)
	Query(ctx, SourceRef, Args) (*Frame, error)
	Stream(ctx, SourceRef, Args) (Stream, error)
	Run(ctx, ActionRef, Args, RunOpts) (*Job, error)   // policy, confirm and approval happen here
	Jobs(ctx, JobFilter) ([]*Job, error)
	Render(ctx, ViewRef, Args) (*Tree, error)
	Events(ctx, Filter) (<-chan Event, error)
	Search(ctx, string) ([]Hit, error)
	Resolve(ctx, Ref) (*Resolved, error)
	Complete(ctx, Language, string, int) ([]Completion, error)
	Plugins(ctx) PluginManager
	Session(ctx) *Identity
}
```

- **Local:** `engine.Local` runs in the rig process. This is the default: one binary, no daemon.
- **Remote:** `api.Client` speaks HTTPS and WebSocket to `rig serve`. The JSON types are the same ones
  the plugin protocol uses.
- **Test:** `api.Fake` drives TUI and CLI tests.

### 4.4 What core contains, and what it does not

**Core contains:**
- the six primitives and refs
- the config pipeline and its schema composition
- the engine (environments, dependency order, lifecycle, `svc://`, shared infra, run state)
- the plugin host, installer, lockfile and trust
- the widget library and the TUI renderer, the keymap, the palette, themes and search
- standard views for the core contracts (data files, not code)
- AI: chat, backends, tool router, redaction, agents, inbox, incident mode
- MCP server, HTTP server, auth, RBAC and audit
- alerts (rules over any source), reports (templates over any sources), sessions, secrets store, docs

**Core does not contain:** any runtime, builder, database, broker, metrics, logs or traces backend, VCS
host, tracker, docs system, cloud or IaC tool. All of these are plugins.

---

## 5. Configuration: one source of truth

### 5.1 `rig.yaml`

```yaml
project: switch
default: local
plugins:                                  # optional: pins and settings; types used below are inferred
  gitlab: { url: https://gitlab.example.local }
  jira:   { url: https://jira.example.local, project: PAY }
packs: [chm/rig-pack@^1]
imports:
  - compose: docker-compose.yml           # 54 services stay defined only there
  - godev: .godev.yaml
  - just: justfile                        # recipes become tasks
  - prometheus-rules: .docker/kubernetes/heavyload/monitoring/prometheus-rules.yml   # alerts
  - grafana: pkg/monitoring/node_exporter/grafana.json                               # a dashboard
  - kubernetes: .docker/kubernetes/service/test
include: [rig.d/*.yaml]                   # split a big file without new concepts
templates:
  app: { groups: [all], health: { port: http, path: /healthz }, build: { go: "./services/${name}/cmd" } }
services:
  parser: { extends: app, groups: [minimum, core] }   # only what compose cannot say
environments:
  local:     { runtime: { type: local } }
  kind:      { runtime: { type: kind, cluster: switch } }
  loadtest2: { runtime: { type: kubernetes, context: loadtest2 }, protected: true }
components:
  db: { type: sql, driver: mssql, addr: "svc://mssql:1433", user: sa, password: "${SA_PASSWORD}" }
views:  { ... }      # section 7
agents: { ... }      # section 10
on:     { ... }      # hooks, section 4.2 Event
policy: { ... }      # section 11
```

The rig keys keep their meaning: `services`, `environments`, `components`, `queries`, `dashboards`,
`tests`, `reports`, `tasks`, `alerts`, `secrets`, `vars`, `sections`, `manifests`, `otel`, `ui`, `ai`,
`help`. The new keys are `plugins`, `packs`, `include`, `templates`, `views`, `agents`, `on`, `policy`
and `links`. `flow:` is dropped.

### 5.2 The pipeline

Load, then `include`, then plugin and pack defaults, then `imports`, then `templates`/`extends`, then
the environment overlay, then `.rig/local.yaml` (personal and gitignored: disabled plugins, keymap,
editor), then interpolation (`${VAR}`, `${VAR:-default}`, `${component.outputs.x}`, `${env.name}`,
`${name}` inside templates), then validation against the **composed schema** (core plus every active
plugin's `config` and `types` schemas), then the catalog.

- **Provenance:** every value remembers `file:line` and the importer it came from.
  - `rig config explain services.parser.image` prints its origin and every layer that touched it.
  - Errors point at the real source file, not at `rig.yaml`.
- **Speed:**
  - Importer output is cached in `.rig/cache/import/` by the sha256 of the files it watches plus the
    importer's version. Warm loads parse no foreign files.
  - The composed schema is compiled once per plugin set.
- **Reload:** file changes trigger an incremental re-run of only the affected importers. `ctrl+r` in the
  TUI and automatic reload with a status-line note are kept from rig.
- **No `rig.yaml` yet:** rig infers the project in memory, exactly like rig, and writes nothing until
  `rig init`.

### 5.3 Boilerplate killers

1. **Importers return whole fragments** (services, tasks, queries, dashboards, alerts, tests,
   components), not only services. Merge rule as in rig: `rig.yaml` keys win, and `key: null` drops an
   imported key.
2. **`templates` and `extends`** for many similar services. **`defaults`** per section.
3. **Inference.** `rig init` and the in-memory default detect stacks, ports, health paths and
   dependencies (rig's deep scan, kept). Plugins can add detectors (`detect:` globs).
4. **Types imply plugins.** `type: sql` resolves to the plugin that provides `sql` through the registry
   index, so `plugins:` is needed only for pins, settings or third-party sources.
5. **`rig lint --redundant [--fix]`** lists and removes values equal to what an import already provides.
   A config that passes is minimal by construction.
6. **Packs** carry shared company configuration (registry mirrors, policy, AI instructions, shared
   tasks) so each repo does not repeat it.

---

## 6. Plugins

### 6.1 Anatomy

```
rig-gitlab/
  rig-plugin.yaml          # the manifest: a rig.yaml fragment plus plugin keys
  bin/                     # optional executables (or built by the release)
  views/*.yaml             # widget trees
  skills/gitlab/SKILL.md   # what the AI and external agents learn
  schemas/*.json           # config and type schemas (optional; inline is fine)
  testdata/*.jsonl         # golden protocol transcripts
  README.md
```

```yaml
# rig-plugin.yaml
name: gitlab
version: 1.3.0                     # set by the release
rig: ">=1.0"                       # host versions it supports
description: GitLab merge requests, pipelines, issues and CI logs
publisher: goxang
requires: { git: ^1 }              # packs are plugins that are mostly `requires`
implements: [scm/1, tracker/1]     # contracts: standard views and AI recipes light up
detect: { git_remote: "gitlab", files: [".gitlab-ci.yml"] }      # rig init suggests it
server: { command: bin/gitlab }    # JSON-RPC process; omit for a declarative plugin
permissions:
  net: ["${config.url}"]
  secrets: [GITLAB_TOKEN]
  exec: [git]
  uses: ["vcs.*"]                  # other plugins' sources and actions it may call through the host
config:                            # JSON Schema of `plugins.gitlab:` in rig.yaml
  url:   { type: string, default: https://gitlab.com }
  token: { type: string, secret: true, default: "${GITLAB_TOKEN}" }
types:     { gitlab-project: { schema: schemas/project.json } }
schemes:   [gitlab]
sources:   { mrs: { args: { state: { enum: [opened, merged, all] } }, cache: 30s } }
actions:   { mr.approve: { effect: write, args: { ref: { type: ref } }, keys: [A], on: [gitlab] } }
views:     { MRs: { slot: tab, file: views/mrs.yaml }, ServiceMRs: { slot: service.section, file: views/svc.yaml } }
importers: { gitlab-ci: { files: [".gitlab-ci.yml"], run: bin/gitlab import-ci } }
on:        { scm.mr.merged: { action: tracker.sync-links } }
tasks:     { mr-cleanup: { steps: [ "rig gitlab mr ls --merged -o json | ..." ] } }
skills:    [skills/gitlab]
```

A declarative-only plugin (your games tab, nothing else needed):

```yaml
name: arcade
version: 0.1.0
views:
  Games:
    slot: { tab: { key: G } }
    tabs:
      Snake:  { widget: terminal, command: bash snake.sh }
      2048:   { widget: terminal, command: ./2048.sh, keep: true }   # keeps running while hidden
      Files:  { widget: terminal, command: yazi }
```

### 6.2 Four ways to implement

| way | when | contract |
|---|---|---|
| **Declarative** | tabs, terminal panes, dashboards, tasks, queries, themes, keymaps, skills, packs | YAML only |
| **Script** | quick sources, importers and actions in any language | `run:`; args in `$RIG_ARGS` (JSON) and on stdin; stdout is JSON, NDJSON, CSV, TSV, YAML or plain lines (auto-detected, `format:` overrides); the exit code is the result; stderr goes to the plugin log |
| **Server** | providers (runtime, metrics, scm, ...), streams, dynamic views, custom widgets, state | JSON-RPC 2.0 over stdio (section 6.5); SDKs in Go, Python and TypeScript |
| **MCP bridge** | any MCP server (Atlassian, GitHub, GitLab, AWS, Terraform, ...) | `mcp: { command: ... }` turns tools into actions (effect from `readOnlyHint`/`destructiveHint`, else `write`) and resources into sources; AI and the palette get them at once, and a rig plugin can add views on top |

Go SDK:

```go
func main() {
	p := sdk.New("cloudwatch")
	p.Source("metrics.range", func(c *sdk.Ctx, a sdk.RangeArgs) (*sdk.Frame, error) { /* ... */ })
	p.Action("alarm.mute", sdk.Write, func(c *sdk.Ctx, a MuteArgs) (*sdk.Result, error) { /* ... */ })
	p.Serve()
}
```

Python SDK:

```python
from rig import Plugin
p = Plugin("hello")

@p.source("greetings")
def greetings(ctx, args):
    return [{"name": "world", "count": 1}]

p.serve()
```

### 6.3 Contribution points

`types` (component types) · `implements` (contracts) · `contracts` (new contracts defined by a plugin) ·
`sources` · `actions` · `views` (with `slot`) · `widgets` (custom widgets) · `importers` · `detect` ·
`schemes` (refs it resolves) · `search` (global search provider) · `complete` (query languages) · `on`
(event hooks) · `events` (events it emits) · `tasks` · `queries` · `dashboards` · `alerts` · `reports` ·
`tests` · `agents` · `skills` · `ai` (instructions per view) · `keys` · `themes` · `doctor` (checks) ·
`config` (schema) · `requires` · `permissions` · `docs`.

**Slots** decide where a view appears without editing anyone else's view:
- `tab`: a top-level screen.
- `subtab: <view>`: a sub-tab of another view, such as a "Kafka lag" sub-tab inside Metrics.
- `service.section`: a section on a service's detail page, such as "MRs touching this service".
- `detail: <scheme>`: what opens for a ref of that scheme.
- `panel`: a side panel.
- `home`: a tile on the Home view.

Actions carry `on:` (scheme, kind or source) so they show up in the right context menus.

### 6.4 Contracts

A contract is a versioned **convention**: named sources and actions with argument and frame schemas, a
set of optional capabilities, and a conformance suite. Core ships contract specs (YAML and JSON Schema
in `contracts/`) and **standard views** for them. Any plugin that implements `metrics/1` gets the
Metrics screen.

The core-shipped v1 contracts are:
- runtime: `runtime/1` (batched status, start, stop, restart, scale, deploy, exec over a PTY, logs, discover, forward; optional capabilities: autoscale, resources, env, debug-relaunch, namespaces, image-load, state-store, manifests)
- build and images: `builder/1`, `registry/1` (tags, prune)
- observability: `logs/1`, `metrics/1`, `traces/1`, `profiles/1`, `debugger/1`
- data: `database/1` (object tree, paged rows, edit by primary key, query, running queries, completion), `cache/1`, `queue/1`, `kv/1`
- quality: `load/1`, `tests/1`
- machines: `hosts/1`
- source control: `vcs/1` (local git), `scm/1` (hosted: MRs/PRs, pipelines, jobs, releases, comments)
- product and infra: `tracker/1` (issues, transitions, comments, links, boards, sprints), `docs/1` (search, read, write, tree), `iac/1` (stacks, plan, apply, outputs, drift, state)
- platform: `notify/1`, `secrets/1`, `ai/1` (model providers beyond the built-in ones)

A plugin may define a new contract (`contracts:`, for example `flags/1` for feature flags) together
with its views. Ecosystems can then grow with no core release.

### 6.5 Protocol `rig/1`

- **Framing:** newline-delimited JSON-RPC 2.0 on stdin and stdout. stderr goes to
  `~/.local/state/rig/logs/<plugin>.log` (`rig plugin logs <name>`).
- **Plugin methods** (all optional except `initialize`):
  - `initialize`: the host sends its version, protocol versions and **host capabilities** (widget kinds
    and versions, features). The plugin answers with what it provides.
  - `configure`: the plugin receives only its own resolved config and component specs, plus declared
    secrets.
  - `query`, `stream`, `run` (with `$/progress`), `plan` (dry run)
  - `import`, `complete`, `render` and `view.event` (dynamic views), `widget.render` (custom widgets:
    frame, size and theme tokens in, cells out)
  - `resolve` (refs), `search`, `doctor`, `event` (subscriptions), `shutdown`
- **Host methods** (plugin to host, filtered by permissions):
  - `host/query` and `host/run`: cross-plugin data and actions. A Grafana-like panel plugin can chart a
    SQL source this way.
  - `host/ask`: input, confirm or pick, shown in the TUI or CLI.
  - `host/ai`: completion through the user's configured backend. Plugins need no AI keys of their own.
  - `host/state.get|set`: per-plugin state, scoped to the project and environment.
  - `host/secret`: declared secrets only.
  - `host/emit` and `host/log`.
- **Notifications:**
  - `$/cancel` (LSP-style) and `$/progress`.
  - `$/stream` (batched data every 50 ms or 500 rows) and `$/ack` (a back-pressure window of 8 batches).
- **Errors:** JSON-RPC codes, plus `-32001` denied by policy, `-32002` not found, `-32003` unavailable
  (a tool is missing; carries a `hint`), `-32004` conflict and `-32005` needs input.
- **Frame:** columnar, typed and renderer-neutral:

```json
{"fields":[{"name":"time","type":"time"},{"name":"p95","type":"number","unit":"ms"},
           {"name":"service","type":"string","ref":"svc://{service}"}],
 "columns":[[1760090000,1760090015],[182.4,190.1],["parser","parser"]],
 "meta":{"total":2,"next":null,"notice":null}}
```

Field types: `string`, `number`, `int`, `bool`, `time`, `duration`, `bytes`, `json`, `ref`. Large
results page with `next` cursors.

### 6.6 Lifecycle and resources

- **Lazy start:** a plugin process starts on its first call, never at startup.
- **Idle stop:** after 10 minutes with no calls, no open streams and no subscriptions. A runtime plugin
  stays up while a TUI is attached, because it serves the status poll.
- **One binary, many processes:** `rig-std` holds every official Go plugin, and `rig-std <name>` serves
  one. Pages of the binary are shared by the kernel, so each extra plugin costs only its heap (about
  5–10 MB). A crash affects one plugin, and the supervisor restarts it with backoff, at most 5 times per
  minute.
- **Limits:** per-call deadlines from context, a memory watermark warning in `rig plugin ls`, and
  `RLIMIT_AS` for script plugins.
- **No startup cost for declarative plugins:** their YAML is merged into the catalog.

### 6.7 The compatibility contract (no breaking plugins)

1. `rig/1` is **append-only**. Methods, fields and enum values are added, never removed, renamed or
   retyped. Unknown fields are ignored on both sides. A request to a plugin never gains a new required
   field.
2. **Negotiate, do not compare versions.** Behaviour depends on the declared `capabilities`, never on
   version numbers.
3. **Widgets are versioned on their own** (`table@1`, `chart@1`):
   - New properties are optional and have defaults.
   - Any node can carry a `fallback:` subtree.
   - A widget unknown to an older host renders its fallback, or a one-line "needs rig ≥ x" notice.
     The view never fails.
4. **Contracts are versioned on their own** (`metrics/1`, later `metrics/2`). The host keeps shims for
   the previous contract version for at least 12 months.
5. **Manifests:** unknown keys produce warnings, not errors. `rig:` sets the minimum host version.
6. **The SDK is generated** from `protocol/rig1.schema.json`, the single source of truth. It has no
   dependencies outside the standard library. A plugin built with an old SDK keeps working on a new
   host because the protocol, not the SDK, is the contract.
7. **Golden transcripts:** every official plugin records `testdata/*.jsonl` sessions. Core CI replays
   all of them against the host, and a core change that breaks one cannot merge.
8. **Compat corpus:** a nightly job installs every plugin in the registry index and runs
   `rig plugin test` against core `main`.
9. **Before 1.0, breaking changes are allowed** but listed in `docs/protocol.md#changes`. At 1.0 the
   protocol freezes under these rules. A future `rig/2` would run side by side with `rig/1` for at
   least 12 months.

### 6.8 Adding a feature without breaking anyone

| change | how |
|---|---|
| a new widget | add `widgets/<name>` in core with `@1`; advertise it in host capabilities; plugins use it with a `fallback`; older hosts render the fallback |
| a widget proven in a plugin | a plugin ships it through `widget.render` first; core adopts the same name and the plugin keeps it as a fallback |
| a new kernel feature | a new optional method or field plus a capability flag |
| a new domain | a plugin-defined contract, then promotion to `contracts/` once two implementations exist |
| a contract change | `x/2` next to `x/1`, a shim, and a deprecation notice in `rig plugin doctor` |

---

## 7. Views, widgets, UI

### 7.1 Widget trees and bindings

```yaml
views:
  Payments:
    slot: tab
    layout:
      grid:
        - - { widget: stat,  title: TPS, source: metrics, query: 'sum(rate(parsersvc_request_latency_millseconds_count[1m]))', unit: /s }
          - { widget: stat,  title: failed 5m, source: db, query: "SELECT count(*) FROM ...", thresholds: { warn: 10, err: 100 } }
        - - { widget: chart, title: p95 by service, source: metrics, query: 'histogram_quantile(0.95, ...)', span: 2 }
        - - widget: table
            title: last failures
            source: db
            query: "SELECT TOP 50 * FROM dbo.transactions WHERE status = 'FAILED' ORDER BY id DESC"
            refresh: 10s
            columns: { status: { color: { FAILED: err, OK: ok } }, id: { ref: "db://db/dbo.transactions?id={id}" } }
            actions: [ { key: R, name: retry, action: switch.retry, args: { id: "${row.id}" } } ]
```

The host owns fetching:
- One request per distinct (source, args, range), shared by every widget bound to it (singleflight).
- TTL per source, refresh only while visible, cancel on hide, error backoff.
- **Transforms** on frames: `filter`, `sort`, `limit`, `select`, `rename`, `group`, `join`. That is
  enough for "metrics per service next to runtime status" with no plugin code.

### 7.2 The widget library (v1)

| group | widgets |
|---|---|
| Layout | `grid`, `split`, `tabs`, `stack`, `box` (border and title), `scroll` |
| Data | `table` (sort, filter, mark, inline edit, column rules, row actions, paging), `tree` (JSON, YAML and KV, field-by-field edit, as on rig's KV and Manifests screens), `list`, `kv`, `form` (generated from a JSON Schema and also used for action arguments) |
| Charts | `stat`, `gauge`, `bar`, `chart` (line, area or bar; zoom by drag, legend table, cursor, the `,` `.` time shift), `heatmap`, `sparkline`, `bars` (htop-style hosts) |
| Text | `text`, `markdown`, `code` (highlighted), `diff`, `logs` (streams; parses JSON, logfmt and console; field filter; tree inspector, as on rig's Logs screen) |
| Flow and structure | `waterfall` (traces), `graph` (relations and manifests), `timeline` (deploys, incidents, events), `board` (Kanban: Jira boards and MR states), `progress` |
| Escape hatches | `terminal` (a PTY), `canvas` (the plugin renders cells) |
| AI | `chat` (core only; one per view through `@`) |

### 7.3 Standard interactions (every widget, every view)

- `/` filter, with rig's grammar kept: words match any cell fuzzily, `*glob*` matches whole cells,
  `col=v`, `col!=v`, `col~text`, a unique part of a column name is enough, and `@?` turns plain words
  into a filter.
- Sorting: click a header, or `<` `>` and `alt+↑↓`.
- Copying: `y` a cell or row, `Y` everything as TSV. A mouse drag copies, through OSC 52 and the system
  clipboard.
- Marking: `space`, and `ctrl+a` for all.
- `enter` opens the ref under the cursor. `esc` or `⌫` walks back through the jump history (kept from rig).
- **Right-click or `.`** opens a **context menu** listing every action that applies to the item, from
  every plugin, with its key. This is the main way users discover what they can do.
- `?` shows help generated from the bound actions and keys, and filters as you type.

### 7.4 The terminal widget

- Implementation: `creack/pty` plus `charmbracelet/x/vt`, pinned to a commit, with
  `hinshun/vt10x` as a fallback if a spike fails.
- Starts lazily the first time the view shows. `keep: true` keeps the process running while hidden.
  `R` restarts it after it exits.
- With focus, every key goes to the process, and so does mouse input (SGR, translated to pane-local
  coordinates). `ctrl+]` returns focus to rig, and the border shows that hint. Resizing sends
  `pty.Setsize` (SIGWINCH).
- Scrollback is 10k lines by default. Selection and copy work as everywhere else. A `rig.terminal@1`
  frame advertises truecolour and mouse support.
- The same widget also hosts `exec` shells into services, ssh to hosts, `kubectl debug`, and any TUI
  program (lazygit, k9s, htop, vim, yazi, a game).

### 7.5 Theme

- Tokens only: `fg`, `bg`, `muted`, `accent`, `ok`, `warn`, `err`, `info`, `border`, `focus`,
  `selection` and `series1..12`.
- Plugins never use raw colours; column rules map values to tokens.
- Rig's contrast correction (lighten or darken until legible) and its `light:` variants are kept. Themes
  can come from plugins. `ctrl+p` picks one with a live preview, as before.

### 7.6 Rendering rules (kept from rig, adapted to bubbletea v2)

- Slow work is never done on the update loop: commands return messages.
- Expensive layout is cached per data change, never computed per frame. Requests made while the cursor
  moves are debounced.
- Content never sizes a box. A list row is one line, and full text opens behind a key.
- Global keys win while a view is not typing. Plugin key conflicts are resolved at load: the global
  key wins, the plugin's binding moves to the palette, and `rig doctor` reports it.
- The `batch()` helper stays until it is verified that `tea.Batch` no longer freezes keys in
  bubbletea v2.

### 7.7 Web readiness (not built now)

- Trees, frames, actions, refs and events are JSON over the Engine API.
- Rules that keep web possible:
  - No widget property may assume a terminal: sizes are abstract rows and columns, and colours are
    tokens.
  - `terminal` maps to xterm.js over WebSocket.
  - `canvas` cells map to HTML.
- A future `rig serve --web` adds only a renderer. No core or plugin changes.

---

## 8. CLI and keys: standard and memorable

### 8.1 Grammar

```
rig <verb> [targets] [flags]        daily verbs: rig up minimum · rig logs parser -f · rig restart session-*
rig <name> <action> [args]          talk to a thing by its name: rig db query "SELECT 1" · rig jira create "Title"
                                     · rig kind create · rig tf plan · rig redis GET key   (name = component, plugin or contract)
rig <noun> <verb>                   management: rig plugin add gitlab · rig env ls · rig secret set X
rig open <ref>                      rig open jira://PAY-12 · rig open svc://parser
```

- Name resolution order: built-in verb, then component, then plugin, then contract. Ambiguity is an
  error that lists the choices.
- Targets are the same everywhere: names, groups, globs (`session-*`), refs, `--all`.

### 8.2 Verbs

| area | verbs |
|---|---|
| project | `init`, `doctor`, `env`, `config` (`explain`, `lint`, `schema`), `migrate`, `docs`, `help` |
| run | `up`, `down`, `start`, `stop`, `restart`, `scale`, `build`, `push`, `deploy`, `exec`, `watch`, `status`, `logs`, `attach`, `debug`, `discover` |
| observe | `metrics`, `traces`, `profile`, `alerts`, `why`, `report` |
| data | `query`, `data`, `open` |
| quality | `test`, `load` |
| automate | `run` (tasks; `task` is an alias), `inbox`, `agents` |
| AI | `ai`, `mcp`, `skill` |
| plugins | `plugin` (`ls`, `search`, `add`, `rm`, `on`, `off`, `up`, `info`, `logs`, `trace`, `new`, `dev`, `test`, `gc`, `vendor`); `add` and `plugins` also work as top-level aliases |
| server | `serve`, `login`, `logout`, `admin` (`users`, `tokens`, `roles`, `audit`) |
| self | `upgrade`, `version`, `completion`, `theme`, `secret`, `vars`, `resume`, `uninstall` |

### 8.3 Flags (every command)

- `-e/--env` and `-r/--remote`
- `-o/--output table|json|yaml|tsv|md`, and `--brief` for agents
- `-y/--yes`, `-n/--dry-run`
- `-f/--follow`, `-w/--watch`
- `-q/--quiet`, `-v/--verbose`

Exit codes: `0` ok, `1` error, `2` usage, `3` a check failed (tests, alerts, load assertions),
`4` denied by policy.

Help:
- Every command shows two or three examples.
- Unknown commands get a "did you mean" suggestion.
- Completion for bash, zsh and fish completes names, groups, refs, actions and plugin names dynamically.
- `rig docs <words>` prints only the sections that hold those words (kept from rig).

### 8.4 TUI keys

**The rule:** lowercase keys look and never change anything on their own. Uppercase keys change
state, and ask when policy says so. Symbols are global modes.

| key | meaning |
|---|---|
| `?` `:` `/` `@` `!` | help · command palette (**same grammar as the CLI**: `:restart parser`) · filter · ask the AI about what is shown · jobs and activity |
| `ctrl+k` | search everything: resources, refs, actions, views, docs, plugins |
| `tab`/`shift+tab` | screens · next and previous screen |
| `[` `]` | sub-tabs |
| `g` then a letter | go to, with the selection: `g l` logs, `g m` metrics, `g t` traces, `g d` data, `g s` services, `g g` top, `G` bottom |
| `enter` `esc` `⌫` | open · back (jump history) |
| `space` `ctrl+a` `y` `Y` `.` or right-click | mark · mark all · copy · copy all · context menu |
| `S` `X` `R` `D` `N` `E` `B` `P` `T` `+` `-` | start · stop · restart · delete · new · edit · build · deploy (push) · run task · scale up and down |
| `ctrl+r` `ctrl+p` `ctrl+e` `ctrl+]` | reload config · theme · environment · leave a terminal pane |
| `q q` or `ctrl+c ctrl+c` | quit (asks when work is still running, as rig does) |

- **Which-key:** pressing a prefix (`g`, or a view's prefix) shows its options in the box above the keys.
- **Footer:** shows the keys of the current context (kept).
- **Mouse:** everything is clickable, a double-click opens, right-click opens the context menu, the
  wheel scrolls, borders drag to resize, and dragging copies.
- **`keys: rig` preset:** restores rig's bindings for anyone migrating. It is set in
  `~/.config/rig/config.yaml` or `.rig/local.yaml`.

---

## 9. Data plane

- **Frames and streams** (section 6.5) are the only data types. Logs are a stream of line batches.
  Parsing JSON, logfmt and console formats happens once in core, so every logs plugin gets it for free.
- **Cross-plugin data:**
  - A widget binds to any source by its component or plugin name (`source: db`, `source: metrics`).
  - A plugin reads other sources with `host/query` when its `uses:` permission allows it.
- **Cache:**
  - Per-source TTLs and singleflight.
  - The Services screen and every badge share one status snapshot per runtime every 3 s (rig's
    `StatusLister` rule).
- **Search index:**
  - Built **once per catalog change** (resources, actions, views, docs, skills), in memory, with fuzzy
    and trigram matching. No embeddings and no vector DB.
  - Plugin `search` providers are asked lazily, with a debounce.
  - Large stores (every KV key, every manifest field) are indexed once per change, as rig does.

---

## 10. AI

### 10.1 Backends
Kept from rig:
- opencode and Claude Code bridges
- OpenAI-compatible endpoints, Anthropic, DeepSeek and 9router
- Ollama and LM Studio
- the fast completion endpoint and the proxy setting

Other providers come from plugins through `ai/1`.

### 10.2 Tool router (no tool explosion)
Forty plugins with ten actions each would mean 400 tools, which ruins the context. The AI instead gets a
small fixed set of **meta-tools**:

| tool | does |
|---|---|
| `find(query)` | ranks actions, sources, views, skills, refs and installable plugins (BM25 over names, descriptions and keywords); returns their schemas |
| `call(action, args)` | runs through policy, then confirm or approval, then audit |
| `query(source, args, as)` | a frame, shaped for tokens: `table` (truncated), `csv` or `stats` |
| `open(ref)` | the resource, its links and the actions available on it |
| `ui(steps)` | drives the TUI that started it (rig's `rig_ui`, kept: open a view at a row, filter, press keys) |
| `skill(name)` | loads a plugin's SKILL.md body (progressive disclosure: only names and descriptions are always in the prompt) |
| `file(op, ...)` | project files, with deny lists (rig's `rig_file`, kept) |
| `docs(words)` | rig and plugin docs |

- The current view's actions and sources are pinned directly, so the common tools need no `find` round
  trip.
- `rig mcp` exposes the same meta-tools to external agents. `--flat` lists every tool for clients that
  want that.

### 10.3 Managing plugins
Plugin management is ordinary actions (`plugin.search`, `add`, `rm`, `on`, `off`, `up`), so "is there a
Sentry plugin? install it" works, with an approval step because these are writes.

### 10.4 Skills
- Every plugin's `skills/` are available to the built-in AI.
- `rig skill install` exports the core skill plus every active plugin skill into `.claude/skills/` and
  `.agents/skills/` for external agents.

### 10.5 Agents and triggers

```yaml
agents:
  triage:
    on: alert.firing
    instructions: Find the root cause with logs, traces and metrics; comment on the linked issue.
    tools: ["runtime.*:read", "logs.*", "metrics.*", "traces.*", "tracker.comment"]
    approve: [tracker.comment]          # waits in the inbox; read tools run freely
  release-notes:
    on: { schedule: "0 9 * * MON" }
    instructions: Summarise last week's merged MRs per service and publish to Confluence.
```

- Agents run wherever the engine runs: in a TUI session, or always-on under `rig serve`.
- **The approval inbox** (`rig inbox`, the Inbox view and later the web) lists pending writes with
  their `plan` diff. Each one can be approved, edited or rejected, and is audited with who approved it.

### 10.6 Guard rails (kept from rig and generalised)
- A conversation is bound to one environment.
- Redaction: the `goxang/scrub` packs, secret values, password-like fields, and DSN and URL passwords.
- `ai.deny` paths. Writes need confirmation. Read-only environments refuse writes.
- Every action is appended to the audit log.
- **New:** everything a plugin source returns (Jira text, logs, Confluence pages, MR descriptions) is
  marked untrusted in the prompt. An instruction found inside data can never approve a write on its own.

### 10.7 Incident mode
`rig why` is kept and works on contracts, not adapters: runtime, logs, traces, metrics, alerts and
events. Deploys and MRs from `scm/1` are added to the evidence timeline.

### 10.8 Product-team work (through plugins, with human sign-off)
- **Spec to backlog:** a Confluence page or markdown spec becomes draft epics and stories with
  acceptance criteria, shown as a table diff. After approval they are created in Jira and linked back to
  the page.
- **MR ↔ issue sync:**
  - The issue key in the branch or title links the MR to the issue.
  - Opening the MR moves the issue to "In review". Merging moves it to "Ready for QA".
  - Deploying comments the environment and the tag on the issue.
- **Release and sprint reports:** MRs, issues, test and load results and incidents go into a
  Confluence page and a Jira comment.
- **Analyst:** questions such as "failed purchases yesterday by bank" are answered over SQL and metrics
  sources (reads, no approval needed).
- **Grooming:** duplicate detection, missing acceptance criteria, estimates proposed (not set).

These ship as `pack/product`: jira, confluence, gitlab or github, markdown-docs, and these agents with
their skills.

---

## 11. Security

1. **Workspace trust:**
   - The first `rig` in a project lists the plugins it would install, with their permissions and
     sources, and asks once (as VS Code workspace trust does).
   - CI and agents use `--yes` or `RIG_TRUST=project`. Without trust, non-interactive runs fail with the
     exact command to run.
2. **Supply chain:**
   - `rig.lock` pins version, source, sha256 per OS and architecture, and a hash of the granted
     permissions.
   - Official artifacts are signed (minisign; the public key is built into rig). Third-party publishers
     are trusted on first use, by key.
   - An update that adds permissions needs re-approval. Auto-install never goes past the lock.
3. **Process isolation:**
   - Plugins get a scrubbed environment (PATH, HOME, LANG and declared variables only), their declared
     secrets only through `host/secret`, and only their own config.
   - On Linux, plugins are launched through `rig __sandbox` (applies Landlock via `go-landlock`, then
     execs the plugin). The filesystem is limited to the project, the plugin's directory, temp
     directories and declared paths. On kernels with Landlock ABI v4 or later, TCP connect is limited to
     declared ports when `net` lists them.
   - On macOS there is no sandbox. Permissions are advisory and audited, and `rig plugin info` says so.
4. **Secrets:**
   - The 0600 store under `~/.config/rig` is kept. `secrets/1` plugins can back it: Vault, 1Password,
     SOPS, AWS Secrets Manager.
   - Values are masked in every view (`maskValue`, kept) and redacted for the AI.
5. **Policy and RBAC** (enforced in `kernel/policy`, for every Engine API call from every client):

```yaml
policy:
  roles:
    viewer:   { allow: ["*:read"] }
    dev:      { extends: viewer, allow: ["*:write@env/local", "*:write@env/docker", "*:write@env/kind"] }
    operator: { extends: dev, confirm: ["*:write@env/loadtest2"], reauth: ["*:danger"] }
    ai:       { allow: ["*:read"], approve: ["*:write"] }          # the AI's writes go to the inbox
  bind: { "group:backend": dev, "user:alice": operator }
environments: { loadtest2: { protected: true }, prod: { readonly: true } }
```

   - Effects: `allow`, `deny`, `confirm` (the same user), `reauth` (password or TOTP again within a
     window: the "password protection"), `approve` (another person, four-eyes).
   - Local default: the local user gets everything except `readonly` and `protected` environments,
     which keep rig's behaviour.
6. **Server mode (`rig serve`):**
   - One or more projects, cloned and kept in sync by the server, served over TLS.
   - Accounts: local users with argon2id passwords, optional TOTP, OIDC (Keycloak, GitLab, Google,
     Okta), and scoped, expiring API tokens.
   - Remote TUI and CLI: `rig login https://rig.example.local`, then `rig -r work ...`.
   - Secrets never leave the server. Clients see masked values.
   - **Environment leases** (`rig lock env loadtest2`) prevent two people deploying over each other.
   - A central audit log, and rate limits.
7. **Audit:**
   - Append-only JSONL per environment: `.rig/<env>/audit.jsonl` locally, the server's store
     remotely.
   - Fields: time, subject (user, AI, agent or token), action, arguments with secrets redacted, effect,
     decision, approver and result.
   - `rig admin audit` filters it.

---

## 12. Distribution and management

- **Registry index:** `goxang/rig-plugins/registry/index.json`. Each entry has a name, description,
  publisher, versions, artifacts with sha256 and signature, `provides` (types, contracts, importers,
  schemes), `detect`, and the packs it belongs to. Community plugins are added by PR to the index; their
  code stays in their own repos.
- **Sources:**
  - an index name: `gitlab`, `pack/product`
  - a git repo: `github.com/o/r[/sub]@v1.2.0`, `gitlab.example.local/grp/r@v1`, or `git+ssh://...`
    (uses the user's git credentials)
  - a tarball: `https://.../x.tar.gz#sha256=...`
  - a local path: `./tools/rig-foo`
- **Mirrors and air-gapped installs:**
  - `registry: https://nexus.example.local/repository/rig/` in the user config or a company pack
    rewrites every download.
  - `rig plugin vendor` copies the locked artifacts into `.rig/vendor/` for offline machines.
- **Scopes:**
  - Project-local `.rig/plugins/*` load automatically under workspace trust.
  - User plugins live in `~/.config/rig/plugins/*`.
  - Installed artifacts go to a content-addressed store, `~/.local/share/rig/plugins/<name>/<version>/`,
    shared across projects.
- **Packs (bundles):** a pack is a plugin with `requires:` plus optional policy, skills, tasks, keymap
  and AI instructions. Versions are resolved with minimal version selection, which is deterministic and
  needs no SAT solver.
  - Official packs: `std` (local, docker, kubernetes, kind, go, git), `observability`, `data`, `qa`,
    `gitlab`, `github`, `atlassian`, `product`, `cloud-aws`.
  - Companies publish their own, for example `chm/rig-pack`.
- **Auto-install:** any rig command in a project whose locked plugins are missing downloads them in
  parallel (after trust), verifies them and continues. A missing type with no pin is resolved through
  the index and written to `rig.lock`.
- **Suggestions:** `rig init` and `rig doctor` read `detect` from the index (".gitlab-ci.yml found: add
  gitlab?"). The Plugins view shows installed plugins (on/off, permissions, logs, memory) and the
  registry (search, details, install).
- **Commands:**
  - `rig plugin ls|search|info|logs|trace`
  - `rig plugin add|rm|up [--major]`
  - `rig plugin on|off [--project]`: personal by default, written to `.rig/local.yaml`; `--project`
    writes to `rig.yaml`
  - `rig plugin gc|vendor`
- **Authoring:**
  - `rig plugin new <name> --lang go|python|ts|bash|yaml` scaffolds the manifest, a sample source,
    action and view, a skill stub, tests, transcripts and a release workflow.
  - `rig plugin dev` links the folder, rebuilds on change, hot-reloads views and shows the RPC trace in
    a pane.
  - `rig plugin test` validates the manifest, runs the conformance suites of the contracts it claims and
    replays its transcripts.
  - Publishing is a git tag: the template's workflow builds, signs and opens the index PR.

---

## 13. Testing, load testing, reports

- **`tests/1`:**
  - Discover suites and tests; stream results as tree events; rerun failures; run one test; flags.
  - Benchmarks with the change since the last run. Saved runs (all kept from rig).
  - Plugins:
    - `gotest`: `go test -json`
    - `junit`: any command that writes JUnit XML
    - `pytest`, `jest`/`vitest`
    - `hurl`: HTTP tests
    - `k6` checks
- **`load/1`:**
  - Generators: `http` (in-process), `kvload` (generator pods steered through a KV key, which the switch
    uses), `command`, `k6`, `vegeta`, `ghz` (gRPC).
  - Rate, instances, live charts and the KV config (all kept).
  - **New:**
    - **profiles** (`ramp`, `steady`, `spike`, `soak`)
    - **assertions** (`p95 < 200ms`, `errors < 0.5%`, `throughput >= 1500/s`) that pass or fail
    - **baselines** (`--vs last` or `--vs <run>`)
- **Reports:**
  - `reports:` keeps rig's form (metrics, traces and queries over a window, saved as md, json or xml)
    and adds:
    - `template:` markdown with bindings to any source
    - test and load results as inputs
    - `compare:` against a baseline
    - an optional `ai:` summary section
  - Outputs: `md`, `html`, `json`, `junit`, `csv`.
  - `publish:` sends it through actions (a Confluence page, an MR comment, a Jira comment, Slack).
  - `every:` schedules it (in a session, or always-on under `rig serve`).
- **CI mode:** `rig test --ci -o junit` and `rig load run smoke --assert` exit with `3` on failure.
  `rig report <name> --vs last` is designed for MR pipelines.

---

## 14. SCM, git, product and infrastructure plugins

- **`git` (`vcs/1`):**
  - status, branches, a log graph, diff, blame, stash, worktrees
  - actions: commit, checkout, pull, push (confirmed)
  - the Changes view; `source` export of a ref (rig's `source`, kept)
- **`gitlab` and `github` (`scm/1`, `tracker/1`):**
  - MRs/PRs (list, diff, approve, merge, comment), pipelines and job logs (streamed into `logs`),
    releases and issues
  - **links:** changed paths map to catalog services, so the plugin knows "this MR touches parser and
    bill" and the UI can offer to build, deploy and test exactly those
  - a CI importer (`gitlab-ci`, `github-actions`) turns jobs into tasks where they are runnable locally
- **`jira` (`tracker/1`):** JQL source with completion, boards on the `board` widget, sprints, actions
  (create, transition, assign, comment, link) and an issue detail view.
- **`confluence` and `markdown-docs` (`docs/1`):** search, read, write and the page tree. Markdown docs
  turn a repo's `docs/` and ADRs into the same view.
- **`terraform` (also covers OpenTofu) (`iac/1`):**
  - A stack component: `plan` (diff widget), `apply` (`danger`, confirm), `outputs` in the catalog
    (`${infra.outputs.db_url}` feeds services), a drift check in `doctor`, and a state browser.
  - Environments map to workspaces, so `rig up -e staging` applies infra first, in dependency order.
- **`aws`:**
  - Uses the `aws` CLI and the user's profiles and SSO.
  - Resource browsers (EC2, ECS, EKS, RDS, S3, Lambda).
  - Implements `metrics/1` and `logs/1` with CloudWatch.
- **`helm`:** releases, values diff, upgrade and rollback.
- **`slack` (`notify/1`).**

---

## 15. Official plugins (`goxang/rig-plugins`)

| plugin | contracts / role | origin |
|---|---|---|
| local, docker, kubernetes, kind | `runtime/1` (+ `manifests` views for kubernetes: the graph, field edit, sync, apply) | rig `adapters/runtime/*`, `manifest/` |
| go, dockerbuild, command-build | `builder/1` | rig `adapters/builder/*` |
| registry | `registry/1` (tags, prune; Nexus and Docker registries) | new (replaces the switch's `nexus-prune` script) |
| prometheus, scrape | `metrics/1` | rig `adapters/metrics/*` |
| loki | `logs/1` (runtime logs come with `runtime/1`) | rig `adapters/logs/loki` |
| zipkin, jaeger, tempo | `traces/1` | rig `adapters/tracing/*` |
| pprof, command-profile | `profiles/1` | rig `adapters/profiler/*` |
| delve | `debugger/1` | rig `adapters/debugger/delve` |
| sql (postgres, mssql, mysql) | `database/1` | rig `adapters/database/sql` |
| redis | `cache/1` | rig `adapters/cache/redis` |
| rabbitmq, kafka | `queue/1` | rig `adapters/messaging/*` |
| consul | `kv/1` | rig `adapters/kv/consul` |
| http-load, kvload, command-load, k6, vegeta, ghz | `load/1` | rig `adapters/loadgen/*` plus new |
| ssh | `hosts/1` | rig `adapters/hosts/ssh` |
| http | queries (ad hoc HTTP) | rig `adapters/query/http` |
| gotest, junit, pytest, jest, hurl | `tests/1` | rig `engine/tests*` plus new |
| otel | injects OTEL variables, provides collector presets | rig `otel:` |
| goland | IDE run configs (`attach`, `debug`) | rig `internal/ide` |
| importers: compose, kubernetes, godev, rig, just, make, npm-scripts, procfile, dotenv, grafana, prometheus-rules, openapi, gitlab-ci, github-actions | `importer` | rig `internal/scaffold` plus new |
| git, gitlab, github | `vcs/1`, `scm/1`, `tracker/1` | new |
| jira, confluence, markdown-docs | `tracker/1`, `docs/1` | new |
| terraform, aws, helm | `iac/1`, `metrics/1`, `logs/1`, views | new |
| slack | `notify/1` | new |
| mcp-bridge | wraps any MCP server | new |
| arcade (example) | declarative terminal tabs | new (doubles as a tutorial) |
| packs: std, observability, data, qa, gitlab, github, atlassian, product, cloud-aws | bundles | new |

---

## 16. Repository layouts

```
goxang/rig
  cmd/rig/                 main (CLI, TUI, server, sandbox helper)
  kernel/
    catalog/ config/ refs/ frames/ views/ actions/ jobs/ events/ policy/ audit/ cache/ search/ identity/
  engine/                  environments, ordering, lifecycle, svc:// resolution, shared infra, run state
  host/                    discovery, manifest, install, lock, trust, registry, supervisor, sandbox
    transport/             declarative, script, rpc (rig/1), mcpbridge
  api/                     Engine interface, Local, Client, HTTP and WebSocket handlers
  server/                  rig serve: auth (passwords, TOTP, OIDC, tokens), sessions, leases
  ui/tui/                  renderer, keymap, palette, which-key, context menu, themes, sessions
  ui/widgets/              the widget library (renderer-neutral model plus TUI drawing)
  ai/                      backends, chat, router, redaction, agents, inbox, why
  cli/                     commands, thin over api.Engine
  contracts/<name>/v1/     spec.yaml, schemas, conformance tests, standard views
  protocol/                rig1.schema.json (source of truth), generator
  sdk/                     separate module: Go SDK (generated types, Serve, testing helpers)
  sdk/python/ sdk/ts/      generated SDKs, published to PyPI and npm
  skills/rig/ skills/rig-plugin/ skills/rig-dev/
  docs/                    config, cli, keys, plugins, protocol, contracts, widgets, security, migration, plan
  examples/                hello, python, shop, kind (kept), plus plugins/ (one of each kind)

goxang/rig-plugins
  cmd/rig-std/             the multiplexed binary
  plugins/<name>/          rig-plugin.yaml, Go package or scripts, views, skills, testdata
  packs/<name>/            rig-plugin.yaml (requires plus extras)
  registry/index.json      generated by CI, plus community entries
  .github/workflows/       ci (lint, test, conformance), release (build, sign, index)
```

---

## 17. Performance

**Decisions kept from rig:**
- external tools over client libraries
- lazy components
- screens load on open, and only the showing one refreshes
- one batched status call per runtime every 3 s
- work done once per change and never per frame
- debounced cursor requests
- run state with the environment (a ConfigMap on Kubernetes, `.rig/<env>/state.json` elsewhere)
- the daemonless Go builder (go-containerregistry) and the `scrape` adapter
- long tasks survive quitting the TUI
- Kubernetes never uses the current context

**New mechanisms:**
- lazy plugin start and idle stop
- shared pages in `rig-std`
- importer cache and a compiled schema
- singleflight source cache
- NDJSON batching with back-pressure
- ring buffers for logs

**Budgets** (measured on the switch project, enforced in CI by a bench job on Linux):

| measure | budget |
|---|---|
| `rig version` | < 20 ms |
| config load, warm or cold | < 50 ms or < 300 ms |
| `rig status` warm (local env) | ≤ rig's time + 10 % |
| TUI first paint | < 150 ms |
| key to frame | p99 < 16 ms |
| idle CPU, TUI open | < 1 % |
| RSS: rig, each plugin, the switch session total | ≤ 45 MB, ≤ 15 MB, ≤ 110 MB |
| plugin round trip (local) | < 1 ms |
| log stream | 10k lines/s with no key lag |
| core binary | ≤ 30 MB (no technology code) |

---

## 18. Migration from rig

- **`rig migrate`** (reads `rig.yaml`, writes `rig.yaml`, never deletes rig files):
  - Keys map one to one where the meaning is unchanged.
  - Component `type`s map to plugins and are pinned in `rig.lock`.
  - `flow:` is dropped with a notice.
  - `imports` are kept. New ones are suggested from detected files: compose, godev, just, make,
    Prometheus rules, Grafana JSON.
  - `rig lint --redundant --fix` runs afterwards.
- **Commands inside tasks** are rewritten from a tested mapping table, for example:

  | rig | rig |
  |---|---|
  | `rig do <c> <a>` | `rig <c> <a>` |
  | `rig task x` | `rig run x` |
  | `rig infra up` | `rig up infra` |
  | `rig db query` | `rig db query` |
  | `rig query` | `rig query` |
  | `rig up --build` | `rig up --build` |

  `$RIG_ENV` and the other `RIG_*` variables are still exported to task steps until the project sets
  `compat: { rig_env: false }`.
- **State:**
  - `.rig/<env>/` is copied to `.rig/<env>/` (state, AI audit, flow settings dropped).
  - On Kubernetes, the state ConfigMap `rig-state` is read as a fallback and then copied to `rig-state`.
  - `~/.config/rig` (themes, secrets, `ai.json`, sessions) is copied to `~/.config/rig`.
- **The switch project** (on branch `rig` in `~/go/src/switch_v2_microservice`; `rig.yaml` stays until
  you sign off):
  - `rig.yaml`: imports of `docker-compose.yml`, `.godev.yaml`, `justfile`, the Kubernetes folders,
    `prometheus-rules.yml` and the node-exporter Grafana JSON; templates for the app services; the
    environments local, docker, kind, k8s and loadtest2 (loadtest2 `protected`).
  - `.rig/plugins/switch/`: the switch-specific tasks rewritten for rig (bootstrap, ship, set-db,
    fresh-db, consul and kv-*, shaparak-link, nexus-prune through the `registry` plugin, loadtest-prep
    and loadtest-hints), the overview and service dashboards, queries, reports, AI instructions and
    `ai.deny`, the load-test skill, and a **Payments** view.
  - `rig.lock`.
  - **Measured outcome:** report the line counts (rig.yaml at 1942 lines, against rig.yaml plus the
    switch plugin) and require zero `lint --redundant` hits. The target is total owned config at most
    60 % of rig.yaml. The actual number is reported, not assumed.
  - **Parity run:**
    - `rig env`
    - `rig doctor` for local, docker and kind
    - `rig -e local up minimum`
    - status, logs and the dashboards rendering
    - the KV screen on Consul; the Data screen on MSSQL and Redis
    - `rig run test` on kind; load generators through kvload on kind; a report
    - read-only browsing of `loadtest2`: **no write action against loadtest2 or k8s during
      acceptance**, because they are shared

---

## 19. Build order (one continuous run)

Every step ends green: gofmt, `go vet`, `go test -race`, staticcheck, the protocol and conformance
suites, and an example smoke test driven in tmux (`tmux new-session -d -s t -x 160 -y 40 'rig'`,
`send-keys` with 0.3 s between keys). Commits are Conventional Commits pushed to `main`. CI publishes
`v0.x` releases automatically during the run. **v1.0.0 is tagged only when section 20 passes.**

1. **Fork and baseline:**
   - Clone `goxang/rig` into `~/go/src/open-pkg/rig` with history.
   - Rename the module, binary, paths, environment prefix and docs to rig.
   - Delete Windows code (`proc_windows.go`, the Windows parts of `internal/sh`, Windows CI targets) and
     the Flow screen and `flow:`.
   - Go 1.26 toolchain directive. Upgrade to bubbletea v2 and lipgloss v2.
   - CI on Linux and macOS. A new `AGENTS.md` and `skills/rig-dev`.
   - Copy this plan to `docs/plan.md`.
   - **Gate:** rig behaves exactly like rig on every example.
2. **Protocol and SDK:**
   - `protocol/rig1.schema.json`, the generator, the Go SDK module, and the Python and TypeScript SDKs.
   - The conformance harness, transcript record and replay, `docs/protocol.md`.
3. **Kernel:**
   - catalog, refs, frames, actions and jobs (port rig's job and activity model), events and hooks,
     policy (local default), audit, identity
   - config pipeline with provenance, include, templates, defaults, interpolation, schema composition,
     importer cache
   - importers return fragments (port `spec/imports.go`)
4. **Engine API seam:** `api.Engine`, `engine.Local`, `api.Fake`. Refactor the CLI and TUI to depend
   only on it, with no behaviour change.
5. **Plugin host:**
   - manifests, discovery, the four transports, the supervisor (lazy start, idle stop, restart,
     limits), the scrubbed environment, the Landlock sandbox helper, permissions
   - lockfile, registry client, mirrors, vendor, workspace trust, auto-install, packs (minimal version
     selection)
   - `rig plugin *`
6. **Widgets and views:**
   - The renderer-neutral widget model and TUI drawing; port rig's grid, jsontree, chartview, logview,
     picker, markdown and viz into widgets.
   - Add `form`, `board`, `timeline`, `diff`, `code`, `canvas` and **`terminal`** (spike x/vt in the
     first hour of this step, with vt10x as the fallback).
   - View specs, slots, bindings (singleflight, TTL, visible-only), transforms.
   - The keymap grammar, which-key, the context menu, the `:` palette on the CLI grammar, the `rig` key
     preset, theme tokens and the Home and Plugins views.
7. **Contracts and standard views:** `contracts/*/v1` specs and conformance tests. Rebuild Services,
   Logs, Metrics, Traces, Data, Queries, KV, Load, Hosts, Tests, Profile and Activity as view specs over
   the contracts.
8. **rig-plugins:**
   - Create the repo and `rig-std`.
   - Port every rig adapter into an SDK plugin: runtimes (Manifests views go with kubernetes), builders,
     metrics, logs, traces, profilers, debugger, sql, redis, rabbitmq, kafka, consul, loadgens, ssh,
     http, gotest, otel, goland, and the importers compose, kubernetes, godev and rig.
   - Record golden transcripts.
   - **Then delete `adapters/` from core.**
   - **Gate:** the core binary has no technology code, and every example passes through plugins.
9. **AI:** port the chat, backends, redaction and guard rails onto policy, actions and audit. Build the
   meta-tool router, plugin skills, agents and triggers, the approval inbox, `why` over contracts, and
   `rig mcp` with meta-tools and `--flat`.
10. **Quality:** `tests/1` runners (gotest, junit, pytest, jest, hurl); `load/1` profiles, assertions and
    baselines plus k6, vegeta and ghz; reports with templates, compare, publish, schedule and CI exit
    codes.
11. **Server:**
    - HTTP and WebSocket transports for the Engine API
    - accounts (argon2id, TOTP, OIDC, tokens), RBAC policy server-side, re-auth, approvals, leases,
      central audit
    - `rig login` and `--remote`
    - a TLS-only default
12. **Ecosystem plugins:**
    - git, gitlab, github, jira, confluence, markdown-docs, terraform, aws, helm, slack, registry,
      mcp-bridge
    - the importers just, make, npm-scripts, procfile, dotenv, grafana, prometheus-rules, openapi,
      gitlab-ci and github-actions
    - arcade
    - all packs, including `pack/product` with its agents
    - the registry index generator and signing
13. **Developer experience:**
    - `rig plugin new` templates for Go, Python, TypeScript, bash and YAML; `rig plugin dev` and `test`
    - `skills/rig-plugin/SKILL.md`: the manifest, the four ways to implement, frames, actions, views and
      slots, contracts, the compatibility rules, testing, publishing, and an end-to-end example
    - `skills/rig/SKILL.md`
    - every doc in `docs/`; `rig docs` from the binary
14. **Migration:**
    - `rig migrate` with the command mapping table (tested on all 120 `rig ` uses in the switch file)
    - state and config copies
    - on the examples, then the switch on branch `rig` (section 18)
15. **Release and install:**
    - Release workflows for rig and rig-plugins (binaries for Linux and macOS on amd64 and arm64,
      checksums, signatures, `install.sh`, `rig upgrade`).
    - Install `rig` into `~/.local/bin` on this machine.
    - The switch's plugins auto-install on its first `rig` run.
    - `rig doctor` passes in the switch.
16. **Acceptance:** section 20, then tag **rig v1.0.0** and **rig-plugins v1.0.0**, which freezes
    `rig/1`.

---

## 20. Acceptance (definition of done)

1. **Parity:**
   - Every rig command except flow has a rig equivalent. A table in `docs/migration.md` lists each one
     with a test.
   - Every screen and feature in rig's guide works: Services, Logs, Metrics, Traces, Queries, KV, Data,
     Load, Manifests, Hosts, Tests, sessions, themes, the AI chat, `@?`, `why`, MCP, GoLand.
2. **Switch:** the parity run in section 18 passes, the line-count report is written, and the
   `loadtest2` checks were read-only.
3. **Plugins:**
   - Every official plugin passes `rig plugin test`.
   - Golden transcripts replay in core CI.
   - A third-party-style plugin in each language (Go, Python, TypeScript, bash, YAML) built from
     `rig plugin new` works end to end.
   - The arcade tab runs a game in a terminal pane, with keys and mouse.
4. **Compatibility drill:**
   - Add a new widget (`timeline@1` already exists, so add a `calendar@1` behind a capability). A plugin
     built against the previous SDK still loads unchanged.
   - A plugin using `calendar@1` on a host without it renders its fallback.
5. **Install and manage:**
   - A fresh clone of the switch on a clean user account auto-installs after the trust prompt.
   - `off` and `rm` work, and a pack installs its whole set.
   - A mirror URL and an offline vendor install both work.
6. **AI:**
   - The built-in AI finds and uses plugin actions and sources through `find`.
   - It installs a plugin through the inbox.
   - The product flow runs against recorded Jira and Confluence fixtures (spec to stories to MR link to
     status to report), and every write waits for approval.
7. **Security:**
   - The policy matrix tests pass (each role against each effect against each environment).
   - The sandbox denies an undeclared path on Linux.
   - Secrets never appear in a plugin's environment, an AI prompt or audit arguments (tests grep for
     planted canaries).
   - `rig serve` works with a password plus TOTP, OIDC (a mock IdP) and a token, and `reauth` and
     `approve` are enforced remotely.
8. **Performance:** every budget in section 17 is met by the CI bench.
9. **Docs and skills:** an agent given only `skills/rig-plugin/SKILL.md` and "write a plugin that shows
   Sentry issues as a tab" produces one that passes `rig plugin test`. Run this as a scripted check with
   the built-in AI.

---

## 21. Risks and mitigations

| risk | impact | mitigation |
|---|---|---|
| **Scope**: everything in one run | the run stalls half-ported | the build order keeps the product working at every step; parity (steps 1–10) comes before ecosystem (12); the strangler refactor means no big-bang switch-over |
| Declarative views cannot express rig's richest screens | parity gaps | the rich behaviour lives in Go widgets (chart zoom, log inspector, editable trees); dynamic views, `canvas` and `terminal` are escape hatches; Metrics and Data are ported first because they are hardest |
| Freezing the protocol too early or wrong | breaking changes later | breaking changes are free until 1.0; the protocol is append-only with negotiation; golden transcripts and the compat corpus; `rig/2` side by side if ever needed |
| The process boundary costs speed | a slower UI than rig | batching, singleflight, visible-only refresh, one status call, shared binary pages; CI budgets block regressions |
| Malicious or compromised plugins | code execution on developer machines and servers | workspace trust, lock hashes, signatures, a permission diff on update, a scrubbed environment, Landlock, audit; no auto-update past the lock |
| Auto-install from a cloned repo | the repo author's code runs | trust prompt on first run; never silent unless trusted |
| Prompt injection through Jira, Confluence, logs or MRs | the AI performs unwanted writes | data marked untrusted, policy plus the approval inbox for writes, the AI's rights at most the user's, audit with the approver |
| AI doing product work produces confident wrong output | bad backlog or reports | drafts and diffs, human approval for external writes, each output links its sources, reports cite evidence IDs (as `why` does) |
| Multi-user server conflicts | two people deploy over each other | environment leases, protected environments, four-eyes approval, central audit |
| Third-party API churn (Jira, GitLab, GitHub, MCP) | broken plugins | isolated inside plugins; core never depends on them; recorded fixtures in plugin CI |
| bubbletea v2 or x/vt regressions | UI bugs | pinned versions; `batch()` kept until verified; x/vt pinned to a commit, vt10x fallback; terminal tests with vim, htop, lazygit and a game |
| macOS has no sandbox | weaker isolation there | said plainly in `plugin info`; permissions still enforced at the host API level (secrets, `uses`, policy) |
| rig and rig diverge while both live | double maintenance | rig goes to fixes only once rig passes acceptance; `rig migrate` keeps the move one command; the switch keeps `rig.yaml` until you sign off |
| The ecosystem does not come | few third-party plugins | strong official plugins, AI-writable plugins (the skill plus `plugin new`), the MCP bridge (instant breadth), company packs |
| Name collision with other "rig" tools | confusion when installing | the goxang namespace; the binary is checked at install; `RIG_BIN` override |

---

## 22. Not doing (on purpose)

- the `flow:` section; Windows
- Go's `plugin` package
- WASM plugins
- gRPC or go-plugin (JSON-RPC is simpler for scripts and every language)
- a general config language (CUE, Jsonnet, expressions)
- embeddings or a vector DB for search
- a web UI now (the API is ready: section 7.7)
- a marketplace website (the index plus `rig plugin search` and the Plugins view is enough)
- third-party in-process Go plugins

---

## 23. What you need to provide

1. **Create the GitHub repositories `goxang/rig` and `goxang/rig-plugins`.** The stored PAT is
   read-only and SSH cannot create repos. Alternatively, provide a token that can create them.
2. **Approve writes to the switch repo** on a new branch `rig`. Nothing is merged into its main
   branch.
3. **A minisign key pair for official plugin signing.** The private key goes into a GitHub secret.
4. **Optional:** Jira, Confluence and GitLab test credentials, for live checks beyond the recorded
   fixtures. Also optional: a Nexus raw repository URL to act as the company plugin mirror.
