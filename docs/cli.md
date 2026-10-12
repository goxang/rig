# CLI

Every command; `rig <command> --help` has its flags. Generated from the code.

| command | what it does |
|---|---|
| `admin audit` | who changed what, when, decided how (on the server with -r) |
| `admin roles` | the roles policy knows on a server: the project's and the built-in ones |
| `admin tokens [create <name> \| rm <id>]` | your API tokens on a server (-r): for CI and scripts; they cannot reauth or approve |
| `admin users [add\|rm\|passwd\|totp <name>]` | accounts of the rig server on this machine |
| `agent ls` | list the agents with their triggers and tools |
| `agent run <name>` | run an agent once now |
| `agent watch` | run each agent whenever its event arrives or its schedule comes due, until stopped |
| `ai [question]` | the assistant: your opencode or Claude Code over rig's tools, bound to this environment; without a question, the UI with its chat open |
| `ai check` | ask the configured model for a one-word completion, to see the setup works |
| `ai config [key=value ...]` | show or change the AI setup (~/.config/rig/ai.json): backend, provider, model, effort, fast_model, fast_url, url, api_key, proxy, autocomplete, disabled |
| `ai connect [opencode\|claude]` | find the installed opencode / Claude Code and use it as you have it set up (its login, config and model) |
| `ai log` | what the assistant ran on this environment: time, command, result, who confirmed |
| `ai sessions` | conversations of this project, newest first |
| `alerts` | check the alerts once (rig.yaml alerts:, default: node cpu/memory/disk at 90%/98%); exit 2 when one is critical |
| `attach <service>` | start a service if it is down and follow its logs; Ctrl-C stops it (--keep leaves it running). GoLand's rig configs run this |
| `build <service\|group...>` | build images and push them to the environment's registry (an existing tag is overwritten only with --yes) |
| `cache <command...>` | run a cache command: rig cache GET key \| INFO |
| `call [action\|source] [name=value...]` | run an action or read a source by name, through policy (on the server with -r) |
| `data [component] [path...]` | walk databases and caches: data db Switch tables dbo.transactions \| data cache db0 <key> |
| `data restore <component> <name>` | replace a database's data with a snapshot (asks on a protected environment) |
| `data seed <component>` | run a database's seed: SQL files or commands (rig up runs it on an empty database) |
| `data snapshot <component> [name]` | save a database's data with its own dump tool (name: a timestamp) |
| `data snapshots <component>` | a component's snapshots, newest first |
| `db create <name>` | create a database |
| `db drop <name>` | drop a database (asks for --yes) |
| `db list` | list databases |
| `db query <sql>` | run SQL; prefix @db to pick a database |
| `debug <service>` | attach a debugger server to a service (--start starts it first) and keep it until Ctrl-C, or in the background (--detach, --stop) |
| `deploy <service\|group...>` | roll out services: --tag deploys that image tag from the registry, --build builds a fresh one first |
| `discover` | everything running in the environment, managed by rig or not |
| `do [component] [action] [args...]` | adapter-specific actions: kind create, db seed, queue purge-all, runtime render, ... |
| `docs [config\|guide\|cli\|plugins\|protocol\|contracts\|views\|security\|design\|manifests] [words...]` | rig's reference, from the binary: every rig.yaml key (config), CLI, screens and AI (guide), architecture (design), manifests; words print only the sections holding them |
| `doctor` | check this environment: rig.yaml, tools, cluster or daemon, ${VARS}, ports, components, AI |
| `down [service\|group...]` | stop services in reverse dependency order (every app when none given; infra only when named) |
| `env` | environments of the project and what each one runs on |
| `exec <service> [-- command...]` | run a command (default: a shell) in a service |
| `hosts [ssh <host> [command...]]` | servers and nodes: usage, and a shell on any of them |
| `ide [service\|group...]` | put the services into GoLand's Services view, grouped by section: logs, stop and debug from the IDE (o on the Services screen) |
| `inbox [approve\|reject <id>]` | writes waiting for your approval (the AI's and agents', by default): what each would do, then approve or reject |
| `infra down [service...]` | stop infrastructure (data stays in its volumes) |
| `infra restart <service...>` | restart infrastructure services |
| `infra status` | infrastructure and where it runs |
| `infra up [service...]` | start infrastructure that is not running |
| `init` | write rig.yaml from what this directory has: compose files, Kubernetes manifests, Go/Python/Node/Java/Rust services |
| `kubeconfig [url \| file \| -]` | fetch the environment's kubeconfig and merge it into yours (~/.kube/config, the old one kept as .rig-backup) |
| `kv get <key>` |  |
| `kv ls [prefix]` |  |
| `kv put <key> <value\|@file>` |  |
| `kv rm <key>` |  |
| `lint` | list rig.yaml values its imports, includes or plugins already give (--fix removes them) |
| `load down <generator>` | lower the rate one step |
| `load ls` | generators with rate and counters |
| `load rate <generator> <rps>` | set the rate (capped at the generator's max unless --force) |
| `load run <load run \| generator>` | run a load: entry (or a generator) with live numbers, then check its assertions; Ctrl-C ends it early |
| `load scale <generator> <replicas\|+n\|-n>` | run more or fewer generator instances (each sends at the rate) |
| `load start <generator>` | start a generator |
| `load stop <generator>` | stop a generator |
| `load up <generator>` | raise the rate one step |
| `lock [env <name>]` | hold an environment so nobody else changes it (a lease; default 2h) |
| `login <url>` | sign in to a rig server; then rig -r <name> ... runs there |
| `logout [name]` | sign out of a rig server and forget it |
| `logs [service\|group...]` | logs of services, merged and coloured per service |
| `manifests [dir...]` | scan Kubernetes manifests (any layout): objects, how they relate, what is broken |
| `mcp` | serve rig to AI agents over MCP (stdio): status, up, deploy, build, logs, queries, load, kv, tasks |
| `metrics [dashboard \| -- query]` | dashboard panels as charts, or one query: rig metrics -- 'sum(rate(x[1m]))' |
| `metrics targets` | the environment's services with metrics as a Prometheus file_sd list (for a local Prometheus container) |
| `migrate` | rewrite rig.yaml to version 1 |
| `ns [namespace]` | Kubernetes namespaces: list them, switch this environment to one (kept until --reset), --create a new one |
| `plugin add <source>...` | install plugins and pin them in rig.lock (--user: only for you): a name, pack/<name>, host/owner/repo[/sub]@ref, a .tar.gz URL or a path |
| `plugin check <name>` | check a plugin against the contracts it implements, live |
| `plugin dev [folder]` | link a plugin folder into this project, rebuild it on change and show its RPC trace |
| `plugin gc` | delete installed plugin versions no project (and not your own) lock pins |
| `plugin info <name>` | what a plugin provides and the permissions it has |
| `plugin logs <name>` | the plugin's stderr and host/log lines |
| `plugin ls` | the project's plugins: version, kind, where from, on or off |
| `plugin new <name> [folder]` | start a plugin: manifest, a source, an action, a view, a skill and a release workflow |
| `plugin off <name>...` | turn plugins off for you (.rig/local.yaml), or for everyone with --project |
| `plugin on <name>...` | turn plugins on for you (.rig/local.yaml), or for everyone with --project |
| `plugin rm <name>...` | unpin plugins (and what a removed pack brought) |
| `plugin search [text]` | plugins in the registry index |
| `plugin test [folder]` | check a plugin: its manifest, build, sources and contracts, and replay testdata/*.jsonl |
| `plugin trace <name>` | the plugin's JSON-RPC exchange, recorded while RIG_TRACE names it (or is *) |
| `plugin up [name...]` | upgrade plugins within their major version (all when none is named) |
| `plugin vendor` | copy the locked plugins into .rig/vendor for offline installs |
| `profile <service>` | capture a profile (cpu, heap, ... per profiler) and print its top |
| `purge` | free space: saved sessions, reports, logs, profiles, builds and backups older than --days, and plugin versions no project uses |
| `push <service\|group...> -t <tag>` | push images rig build --no-push left in the local Docker daemon to the environment's registry |
| `query [saved-query [name=value...] \| component query...]` | run a saved query from rig.yaml, or an ad hoc one in a component's language (SQL, PromQL, redis, kubectl, ...) |
| `queue list` | queues with depth, consumers and rates |
| `queue publish <exchange/key\|queue> <body>` | publish one message |
| `queue purge <queue>` | drop a queue's messages |
| `report [name]` | measure a reports: entry of rig.yaml (metrics, traces, queries) over a time window and save it as markdown, json or xml; without a name, list them |
| `report start <name>` | start a reporter: its window opens now and closes at rig report stop |
| `report stop <name>` | stop a reporter: measure the report from its start until now, print and save it |
| `report watch` | measure and publish each report with a schedule: when it comes due, over the time since its last run |
| `restart <service\|group...>` | restart services (e.g. to reload their configuration) |
| `resume [session\|last]` | continue an AI conversation or a saved UI session (S); without an id, pick one (d closes it) |
| `rollback` | restore the rig version that was installed before the last upgrade (run again to go forward) |
| `scale <service\|group...> <replicas\|+n\|-n>` | set replica counts, or move them up or down: rig scale core +1 |
| `schema` | print rig.yaml's JSON Schema (editors: # yaml-language-server: $schema=https://raw.githubusercontent.com/goxang/rig/main/spec/rig.schema.json) |
| `secret [ls \| set NAME [VALUE] \| rm NAME]` | secrets rig.yaml uses as ${NAME}: kept in your config dir (0600), never in the repo; the environment wins |
| `serve` | serve this project to remote CLIs, MCP clients and the web inbox, over HTTPS, with accounts and server-side policy |
| `setenv <service\|group> [K=V... \| K=]` | env vars of services, kept in the environment's state so every deploy keeps them; running ones redeploy |
| `skill [install\|plugin]` | the agent skill for working with rig: print it (plugin: the one for writing plugins), or install both and every active plugin's skills into .claude/skills and .agents/skills |
| `source <git-ref>` | export a git ref of the project (cached per commit) and print its directory |
| `start <service\|group...>` | start services (deploying what is not there yet) |
| `status [service\|group...]` | state of services in the environment |
| `stop <service\|group...>` | stop services, keeping their definitions |
| `task [name [args...]]` | run a task from rig.yaml (its shell steps, in order), or list them |
| `test [suite \| ./pkg/...]` | run a test suite from rig.yaml, or packages without one (go test -json): --failed reruns the last run's failures; without arguments, list the suites |
| `test report [run\|last]` | a saved run as a report: failures with output, slowest tests, benchmarks, coverage |
| `test runs [suite]` | saved test runs, newest first |
| `theme [name]` | list the UI's colour themes, pick one, or --save a copy to edit (ctrl+p in the UI) |
| `traces [trace-id]` | recent traces, or one trace as a waterfall |
| `trust` | let this project's plugins run and install (workspace trust) |
| `uninstall` | remove the rig binary (add --config to also drop ~/.config/rig) |
| `unlock [env <name>]` | release your lease on an environment (--force breaks someone else's) |
| `up [service\|group...]` | build (with --build) and deploy services and their dependencies, in dependency order |
| `upgrade` | update rig to the latest release (or $RIG_VERSION), using the same installer as the install script |
| `vars [set K=V... \| unset K...]` | manifest variables ($MAIN_DB, ...): rig.yaml's, overridden per environment and kept in its state |
| `version` | print the version |
| `watch [service\|group...]` | rebuild and restart services as their sources change (every app service when none); ctrl+w in the UI |
| `whoami` | who rig acts as, with which roles (on the server with -r) |
| `why <service or symptom>` | incident mode: gather logs, traces, metrics, alerts and changes about a symptom, rank the likely causes, and have the AI find the root cause and propose a fix; answer "fix" to apply it |
