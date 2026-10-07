# AGENTS.md — rig

- Go 1.23: build and test with `GOTOOLCHAIN=local`; `gofmt`, `go vet ./...` and `go test -race ./...` must pass (CI runs them plus staticcheck, macOS and Windows builds).
- After changing a rig.yaml type, run `go generate ./spec` (the schema test fails otherwise).
- Ship every change yourself: commit (Conventional Commits: `feat:` bumps the minor version, anything else the patch), push to `main`, wait for the `ci` workflow to go green (its last job, `release`, publishes the tag). The user's only step is `rig upgrade`.
- Working on rig itself: `skills/rig-dev/SKILL.md` (where each kind of change goes, the UI's ground rules, portability, how to test and ship). The user-facing skill is `skills/rig/SKILL.md`; the key reference is `docs/config.md`.
