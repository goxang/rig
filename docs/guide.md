# rig guide

Everything the [README](../README.md) leaves out: the CLI, every screen of the terminal UI, the AI
assistant, GoLand and agents. Every rig.yaml key is in [config.md](config.md).

## CLI

```bash
rig                           # terminal UI
rig infra up                  # infrastructure, once
rig up test                   # every service tagged test, and what they depend on
rig up --build                # build images and deploy everything, phase by phase
rig build app -t v1 --ref main && rig deploy app -t v1   # apps only; infrastructure untouched
rig scale core +1             # every service in group core, one more replica
rig status
rig -e kind logs -F api worker
rig query                     # saved queries and queryable components
rig query db-top-cpu n=5
rig load rate fleet 200 && rig load scale fleet +2
rig task bootstrap            # a named list of steps from rig.yaml
rig vars set MAIN_DB=x        # manifest variables per environment; rig setenv api K=V for env
rig data db Switch tables     # walk databases and caches
rig logs -E '(?i)timeout' api # regex over logs
rig alerts                    # what is over its thresholds
rig test unit --race          # a test suite; --failed reruns the failures, -o file saves the report
rig test ./pkg/x/...          # any packages, no suite needed
rig report load --since 20m   # metrics of a load test (reports: in rig.yaml) as a markdown table
rig ns --create me            # Kubernetes: switch the environment to a new namespace (rig ns lists them)
rig metrics targets -o f.json # services as Prometheus file_sd targets, for a local Prometheus
rig resume last               # the TUI as a saved session (S) left it
rig ide --open                # every service into GoLand's Services view by section: live logs, stop, debug
rig attach api                # start it if down, follow its logs; Ctrl-C stops it (what GoLand's configs run)
rig mcp                       # MCP server for AI agents
```

`-e <env>` (or `$RIG_ENV`) picks the environment. A `protected: true` environment refuses changes
without `--yes`; pushing over an existing image tag needs it too. In the TUI the confirmation is the `--yes`.

## Terminal UI

Screens load nothing until opened, and only the one showing refreshes. Everything is a sortable grid
(`<` `>` pick the column, `I` inverts, or click a header) and works with the mouse. On a Kubernetes
cluster, dangerous changes (stop, deploy, delete, edits) ask first and `enter` confirms; local, docker
and kind never ask. `?` shows the keys of the current screen.
Drag over any text (results, queries, test output, the chat) to copy it; the selection stays inside the
box it starts in. Copies go to the system clipboard (wl-copy, xclip or xsel) and over OSC 52.

| screen | |
|---|---|
| Services | your services by section (`sections:`; `[` `]` or a click, `i` infrastructure); mark with `space`, a whole section with `a` or a double-click on it, then start (in dependency order)/stop/restart/scale/deploy them together; `h` edits a Kubernetes autoscaler (or creates one), and scaling past one asks whether to move it; `R` changes requests and limits; `F` lists the service's manifests to edit, sync from what runs, or apply; replica changes (an autoscaler's too) show for a while; `D` attaches a debugger (debug build, a stable port per service); `o` puts every service into GoLand (below); `p` takes a profile (the picker says what each kind shows) as a sortable table, `W` saves it as a report; `enter` opens one: instances and its live log, which scrolls like the Logs screen, `l` moves it there |
| Logs | the services you pick, merged, or one instance. JSON, console and logfmt lines read as `LEVEL message key=value`, with JSON, protobuf text and Go `%v` values inside fields shown as compact JSON; `h` hides fields, `s` shows lines raw. `/` filters by text, regex or fields: `output.Transaction.ID=202604 level!=debug msg~timeout` (`=`, `!=`, `~` contains; terms ANDed). `↑` `↓` or a click pick a line, `enter` inspects it as a tree (`f` filters by the selected field, `w` wrap, `J` `K` next line); dragging over text copies it |
| Metrics | dashboards as tabs, `$variables` and time range in the header (click them), foldable rows; click a legend entry for only that series, ctrl-click to hide it; `v` opens a panel with a sortable legend table and a cursor; drag across a chart zooms to that time range, `Z` (or ⊖) zooms out, `,` `.` (or ‹ ›) shift it, ctrl+wheel zooms around the mouse |
| Traces | filter by service, operation, minimum duration, time window, text, errors |
| Queries | saved queries (with parameters), ad hoc ones, schedules with a trend of the first number; `H` every run of the session; `y` copies the query (or, on the result, the row), `Y` the whole result as TSV. Writing a query opens a popup: the whole text wrapped, `ctrl+a` selects it, `ctrl+c` / `ctrl+y` copy it |
| KV | browse, edit and delete keys right in the store; `/` searches every key, field and value as you type and opens the hit with its field selected; `enter` edits a JSON value field by field; `o` picks the editor; `R` restarts the services that read the edited key; `F` loads the config files into it (`kv-*` tasks) |
| Data | databases (objects, rows, definitions, running queries), caches (keys, values), queues; `e` edits a cell, `space` marks rows, `D` deletes them (table rows by primary key, Redis keys and entries); `Q` queries where you stand (on a table row: that row by its primary key; on a Redis key: the read for its type); going back (`esc` or ‹ back) lands on the row you opened; `y`/`Y` copy the row/all as TSV; `/` filters with globs |
| Load | generators: rate and config shared by every instance, instance count, `i` (or a click) one instance's charts or all, `c` their KV config, `v` their env, `W` saves a metrics report; `z` or a click shows a chart full size, with the Metrics panel view's legend, filter, range and drag to zoom |
| Manifests | objects or folders (`t`), relations, a file's issues (`i`), apply (`a`), make a service (`n`); `→` (or a click) walks the object field by field like the KV screen: `enter` edits a value, `a` adds a field, `D` deletes one, each saved into the file at once with its comments kept; `/` searches every field and value and lands on the field; `e` edits an object in your editor and saves it into its file, `s` writes what the cluster runs into the file (only fields someone set, `$VARS` kept), `L` edits it on the cluster; `d` picks among every manifest folder of the project |
| Hosts | nodes as htop-style CPU, memory and disk bars, the selected one's CPU history, a shell (double-click), and `p` the pods on a node, sortable by CPU, memory or age |
| Tests | suites as tabs and the `go test` command they run; `r` runs, `f` reruns failures, `.` the selected test, `O` sets flags (race, cover, -run, …); a tree of packages and tests (`i` cycles failed/passed/skipped/running), its output beside it, benchmarks with the change since the last run, saved runs (`h`) |

Screens switch with `1`-`0` and `` ` `` (the eleventh), or a click on their name; tabs inside a screen
(dashboards, apps/infra, objects/folders, saved/history, suites, filters) are clickable too. A screen
shows only when rig.yaml gives it something (no `kv` component, no KV screen); `ui: { tabs: [services,
logs, data, tests] }` picks the screens and their order, and the number keys follow it.
`T` runs a task from rig.yaml, `ctrl+e` shows the environment (variables with secrets hidden, each
component's address and database), `N` switches or creates a Kubernetes namespace, and the Metrics screen's
`m` points the dashboards at another metrics source. `S` saves the session (screens, query results and history) for `rig resume`,
under the user's config directory (`~/.config/rig/projects/...`); `M` frees the mouse so
the terminal can select text; the header shows alerts (`A`). `q` quits at once unless a test run or an
operation is still going, or load generators are sending: those stop with rig (tests, queries, port
forwards) or keep going without it (generators, services), and it asks.

## AI

Set it up once, in three steps:

1. Install one backend: [opencode](https://opencode.ai) (`curl -fsSL https://opencode.ai/install | bash`) or
   [Claude Code](https://claude.com/claude-code) (`npm i -g @anthropic-ai/claude-code`), and log in to it.
   No login: opencode's free models work as they are.
2. `rig ai check` says what rig found and whether a turn works. To use your own key instead,
   `rig ai config provider=openai url=https://…/v1 api_key=… model=…` (or `deepseek`, `anthropic`, `9router`).
3. Press `@` on any screen, or run `rig ai why is api failing?` in the terminal.

For an agent that edits the project (Claude Code, Codex, Cursor, opencode) instead: `rig skill --install`
teaches it rig.yaml, and the MCP config under [Agents](#agents) gives it rig's tools.

`@` on any screen opens a chat about what the screen shows: the selected service, key, table, the log
lines in view. It runs your own **opencode** or **Claude Code** (whichever is installed) with rig as its
only tools, so it can do anything you can: change a KV key, restart services, run and schedule queries,
open screens, explain logs against the code, suggest `rig profile` / `go tool pprof` commands.
Query prompts (Data `Q`, Queries, Metrics, Logs grep, KV values) get inline completions: `tab` takes them,
`ctrl+t` turns them off or on for good. A suggested query (Data `Q`, a new query) waits behind the empty
input: type your own and the AI completes it, `tab` takes the suggestion to edit, `enter` runs it as is.
`:?` then words asks for the rest in plain language: `:?logs slower than 1s`, or
`SELECT * FROM transactions WHERE :?amount above 100k`; the AI's version shows above the input,
`tab` takes it, `enter` runs it.
Data `Q` on a procedure or function writes its call with every parameter as `NULL /* type */` to fill in,
AI or not.

In the chat, `/model` and `/effort` pick the chat's model (opencode's whole list, or opus/sonnet/haiku) and
reasoning level, `/fast` the completion model, `/autocomplete` switches suggestions. Completions through
the backend start it every time (seconds); `fast_url` + `fast_api_key` (any OpenAI-compatible endpoint, e.g.
`https://api.anthropic.com/v1` or a router) make them one HTTP request on a kept-alive connection.

```
rig ai                                 the UI with the chat open
rig ai why is parser failing?          one turn in the terminal; rig ai -c "…" continues it
rig resume                             pick a conversation or saved UI session: enter continues, d closes
rig ai config                          the setup; rig ai check tests it
rig ai config provider=deepseek api_key=sk-…
rig ai config provider=openai url=https://…/v1 api_key=… model=…
rig ai config proxy=localhost:10808    every AI request goes through it
rig ai config fast_url=https://…/v1 fast_api_key=… fast_model=…   completions as one direct request
```

No setup is needed: an opencode or Claude Code login is used as it is, and without one opencode's free
models are. Providers: `own`, `opencode`, `openai` (any compatible endpoint), `9router`, `deepseek`,
`anthropic` (for Claude Code). The key lives in `~/.config/rig/ai.json` (0600).

Guard rails, enforced by rig's MCP server rather than the prompt: a conversation is bound to the
environment it started on (other `env`s are refused); the project directory is the only workspace, with
credentials, `ai.deny` paths and `.git` out of reach; files change only through `rig_file` (create, edit, move;
deleting asks you), never a shell. On a protected or Kubernetes
environment a dangerous step (stop, scale down, deploy, delete, DROP/DELETE without WHERE, tasks,
infrastructure) runs only when your message asked for it in so many words, else rig asks you first;
on a protected one every change needs that. Secrets and `rig mcp`/`debug` are out of reach.

## GoLand

`o` on the Services screen (or `rig ide --open`) writes each service of the environment into GoLand's
Services view (`alt+8`), one folder per `sections:` entry plus `rig · infra`:

- `<service>` runs `rig attach`: starts it if it is down and shows its live log; stop stops it (infrastructure
  only stops following). Started or stopped in rig, GoLand follows: the config ends when the service does.
- `<service> · debug` (Go services): press Debug on it and GoLand starts the service if it is down, brings its
  debugger up in the background (`rig debug --detach --start`, on the service's stable port) and attaches;
  breakpoints work from there, on local processes, docker, kind and Kubernetes alike.
  `<service> · stop debugger` ends it (or `rig debug --stop <service>`).

They are rig commands on the same environment, so the TUI and GoLand show the same processes. GoLand
rewrites `.idea/workspace.xml` when it closes: if the Services view does not list them, add Shell Script
and Go Remote there (`+` › Run Configuration Type).

## Agents

```json
{ "mcpServers": { "rig": { "command": "rig", "args": ["mcp"], "cwd": "/path/to/project" } } }
```

Agents get terse output (`--brief`, `RIG_BRIEF=1`: tab-separated, no colour, long cells cut). Every
TUI screen has a CLI twin: `rig logs -E`, `rig metrics`, `rig profile`, `rig query --every 30s --times 10`,
`rig load`, `rig data`, `rig alerts`.

Tools: `rig_init` (draft or write rig.yaml for the project, with infrastructure presets), `rig_docs` (every
rig.yaml key), `rig_envs`, `rig_status`, `rig_up`, `rig_down`, `rig_service`, `rig_scale`, `rig_build`, `rig_deploy`,
`rig_logs`, `rig_query`, `rig_load`, `rig_kv`, `rig_infra`, `rig_task`, `rig_test`, `rig_ui` (acts in a rig UI that
started the agent), `rig_file` (list, read, write, edit, move, delete inside the project; credentials and `.git`
refused, delete needs `"confirm": true`), and `rig` for any other command.
Each runs the CLI, so protections apply: changes to a protected environment need `"confirm": true`.

`rig skill --install` puts a skill into `.claude/skills/rig` and `.agents/skills/rig`, so Claude Code, Codex,
opencode and others know how to write and extend rig.yaml (services, infrastructure, `otel:`, `ui.tabs`,
dashboards, tests) and check it with `rig env`; `rig docs config` is the reference they read.
