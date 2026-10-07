---
name: rig-dev
description: Change rig itself (github.com/goxang/rig): add or fix an adapter, an importer, a rig.yaml key, a CLI command, an MCP tool, the AI assistant, or anything in the terminal UI (a screen, a key, a picker, the footer), then test it on the examples and ship a release. Use whenever the work is in rig's own source, not in a project's rig.yaml.
---

# Working on rig

Map: `docs/design.md` (packages, component kinds, optional interfaces). Rules: `AGENTS.md`.
Build and test with `GOTOOLCHAIN=local` (Go 1.23, deps pinned to the last 1.23 releases).

## Where a change goes

| change | where | also update |
|---|---|---|
| a rig.yaml key | the type in `spec/spec.go` (decoding) or the adapter's `Options` | a row in `docs/config.md`, then `go generate ./spec` (the schema's descriptions come from those tables; a test fails when it is stale) |
| services from another tool's file | an `Importer` registered with `spec.RegisterImporter` (`internal/scaffold/import.go`: compose, kubernetes) | `imports` row in `docs/config.md`; `rig init` writes the import, never a copy |
| a component type | `adapters/<kind>/<type>`, `plugin.Register` in `init()`, imported in `adapters/all` | the components table in `docs/config.md` |
| an extra capability | an optional interface in `core` found by type assertion | nothing outside the adapter and its front end |
| a command | `internal/cli`, thin over `engine` | `docs/guide.md`; shell completion in `complete.go` if it takes names |
| an MCP tool | `mcpTools()` in `internal/cli/mcp.go`; mutating ones go through `guard()` | the tool list in `docs/guide.md` |
| what the assistant knows | `ai/prompt.go` (rules), a screen's `aiContext` in `internal/tui/chat.go` (what it sees) | keep the rules short: every line is read on every turn |
| the UI | `internal/tui` (one `tab` per screen) | `helpLines()` and `screenHelp`, `docs/guide.md` |

## The UI's ground rules

- Never `tea.Batch`: bubbletea 1.3.7 runs a batch inside its loop and freezes keys. Use `batch(...)`.
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

## Portability

- Every host process goes through `internal/sh`: `sh.New(...)` for tools, `sh.Shell()` for a POSIX
  shell (Git for Windows' sh on Windows), `proc_unix.go` / `proc_windows.go` for syscalls. Never
  `exec.Command("sh", ...)` on the host.
- Paths written into rig.yaml use `/` (`filepath.ToSlash`); paths read from it go through `filepath`.
- Output that redraws (`\r`, spinners) only on a terminal (`term.IsTerminal`); plain lines on a pipe.
- Terminals differ: VTE eats ctrl+shift+↑↓, some eat alt+arrows. Give a key a second binding.
- CI builds five targets and tests on Linux, macOS and Windows; a Windows failure blocks the release.

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
