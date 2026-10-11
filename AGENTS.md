# AGENTS.md — rig

- Go 1.26: build and test with `GOTOOLCHAIN=local`; `gofmt`, `go vet ./...`, staticcheck and `go test -race ./...` must pass (CI runs them on Linux and macOS).
- After changing a rig.yaml type, run `go generate ./spec` (the schema test fails otherwise).
- Every step ends green and with a tmux smoke test: `tmux new-session -d -s t -x 160 -y 40 'rig'` in an example, keys sent 0.3s apart.
- Ship every change yourself: commit (Conventional Commits: `feat:` bumps the minor version, anything else the patch), push to `main`, wait for the `ci` workflow to go green (its last job, `release`, publishes the tag). The user's only step is `rig upgrade`.
- The road to 1.0 is `docs/plan.md`; follow its build order. Core grows no domains: adapters belong in `goxang/rig-plugins`.
- Clients (CLI, TUI, MCP, AI, server) go through `api.Engine` (`engine.Local` in-process): a new operation is an action or a source there, so policy, jobs and audit apply. The kernel (`kernel/`) holds the primitives.
- Working on rig itself: `skills/rig-dev/SKILL.md` (where each kind of change goes, the UI's ground rules, how to test and ship). The user-facing skill is `skills/rig/SKILL.md`; the key reference is `docs/config.md`.
