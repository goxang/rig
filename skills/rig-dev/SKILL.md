---
name: rig-dev
description: Change rig itself (github.com/goxang/rig): add or fix an adapter, an importer, a rig.yaml key, a CLI command, an MCP tool, the AI assistant, or anything in the terminal UI (a screen, a key, a picker, the footer), then test it on the examples and ship a release. Use whenever the work is in rig's own source, not in a project's rig.yaml.
---

# Working on rig

Map: `docs/design.md` (layers, primitives, packages, kinds and contracts). Rules: `AGENTS.md`.
Build and test with Go 1.26 and `GOTOOLCHAIN=local`. The UI is bubbletea, lipgloss and bubbles v2
(`charm.land/...`); the full plan to 1.0 is `docs/plan.md`.

## Where a change goes

| change | where | also update |
|---|---|---|
| a rig.yaml key | the type in `spec/spec.go` (decoding) or the adapter's `Options` | a row in `docs/config.md`, then `go generate ./spec` (the schema's descriptions come from those tables; a test fails when it is stale) |
| services from another tool's file | a plugin importer (rig-plugins `cmd/rig-std`: compose, kubernetes, godev) | `imports` row in `docs/config.md`; `rig init` writes the import, never a copy |
| a component type | a plugin, never core: Go adapters go in rig-plugins `adapters/<kind>/<type>` (`plugin.Register` in `init()`, a row in `cmd/rig-std/plugins.go`), anything else is its own plugin (`rig plugin new`) | the components table in `docs/config.md` |
| an extra capability | an optional interface in `core` found by type assertion | nothing outside the adapter and its front end |
| a command | `internal/cli`, thin over `engine` | `docs/guide.md`; shell completion in `complete.go` if it takes names |
| an MCP tool | `mcpTools()` in `internal/cli/mcp.go`; mutating ones go through `guard()` | the tool list in `docs/guide.md` |
| what the assistant knows | `ai/prompt.go` (rules), a screen's `aiContext` in `internal/tui/chat.go` (what it sees) | keep the rules short: every line is read on every turn |
| the UI | `internal/tui` (one `tab` per screen) | `helpLines()` and `screenHelp`, `docs/guide.md` |

## The UI's ground rules

- Never `tea.Batch`: bubbletea runs a batch inside its loop and freezes keys. Use `batch(...)`.
- lipgloss v2 `Width`/`Height` include the border; widths come from `ansi.StringWidth` (Unicode 16, so
  some symbols such as ☰ are 2 cells wide).
- Slow work goes off the loop: a `tea.Cmd` returning a message; to continue on the loop with its
  result, return `thenMsg(func() tea.Cmd {...})`.
- A change the user starts goes through `m.act(label, dangerous, f)` (asks where needed) or `m.do`:
  both record a job, so it shows above the keys and under `!`. Write progress to `jobOut(ctx)`.
- Questions: `m.ask` (input box), `m.pick` / `m.pickMany` (pickers, `picker.groups` for tabs),
  `m.confirm`. They render in their own box above the keys; never draw prompts into the key line.
- Global keys win over a screen's keys while the screen is not typing (`key()` in `tui.go`): check
  `grep -rn '"X"' internal/tui` before taking a letter.
- Mouse: register zones while rendering (`m.zone`, `m.zones`); positions are absolute rows.
- Values that may be secrets go through `maskValue` (names, and passwords inside URLs).
- Content never sizes a box: a list row shows one line (`firstLine` of a description), a box keeps
  one size while the selection moves, and the full text of an item (a task's help and steps) opens
  behind a key in a scrollable view (`picker.detail`, ctrl+o), never under the list.
- Work over a long list (every KV key, every hit) runs once per change, not per frame: cache what
  `view` needs (see `picker.visible`/`widths`), and debounce requests made while the cursor moves.

## Tasks

A task in rig.yaml (the examples, a project's) must be idempotent: a retry, a rerun or an agent runs it
twice. Create if missing, delete tolerantly, reset before rebuilding, verify the outcome. See the tasks
section of `docs/config.md`.

## Portability

- Linux and macOS only. Every host process goes through `internal/sh`: `sh.New(...)` for tools and
  `sh`, `proc_unix.go` for syscalls. Never `exec.Command(...)` on the host.
- Output that redraws (`\r`, spinners) only on a terminal (`term.IsTerminal`); plain lines on a pipe.
- Terminals differ: VTE eats ctrl+shift+↑↓, some eat alt+arrows. Give a key a second binding.
- CI builds four targets (linux, darwin × amd64, arm64) and tests on Linux and macOS.

## Test

```sh
GOTOOLCHAIN=local go vet ./... && GOTOOLCHAIN=local go test -race ./...
test -z "$(gofmt -l .)"
GOTOOLCHAIN=local go run honnef.co/go/tools/cmd/staticcheck@2025.1.1 ./...
GOTOOLCHAIN=local go install ./cmd/rig
```

Then the examples, the one closest to the change: `examples/hello` and `examples/python` (local),
`examples/shop` (docker: `rig up`, queries, load, traces, data), `examples/kind` (kind: `rig up --build`).
Drive the UI in tmux: `tmux new-session -d -s t -x 160 -y 40 'rig'`, `tmux send-keys -t t <key>` with
~0.3s between keys (faster reads as one paste), `tmux capture-pane -p -t t`. Mouse drags cannot be sent
this way: cover them with unit tests.

## Ship

Conventional Commits (`feat:` bumps the minor version, anything else the patch, `!` the major), push
to `main`, wait for the `ci` workflow (about two minutes; its last job tags and publishes the release).
The user runs `rig upgrade`. Never leave a change uncommitted, unpushed or unreleased.
