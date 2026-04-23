# AGENTS.md

## Overview
`clankerval` is the standalone evaluation runner for `clnkr`. Loads checked-in suites, stages one trial workspace per task, runs an agent adapter, normalizes transcript/outcome artifacts, and writes per-trial bundles plus run-level reports. Ships one binary: `clankerval`.

## Commands
```
make build        # Build clankerval (default target)
make test         # Run Go test suite with -race
make evaluations  # Run checked-in dummy suite with fixture agent
make man          # Regenerate doc/clankerval.1 from doc/clankerval.1.md
make _check-man   # Fail if generated manpage is out of sync
make clean        # Remove local build artifacts
```

## Rules
- Run `make man` then `make _check-man` before committing doc changes.
- Run `make test` and `make evaluations` before committing behavior changes.
- `doc/clankerval.1.md` is source of truth. `doc/clankerval.1` is generated and committed.
- Do not hand-edit generated troff unless repairing `go-md2man` output and regenerating immediately.
- Debian packaging branch is `debian/main`. `main` has no `debian/` directory. Read packaging context via `git show origin/debian/main:path/to/file`.
- Claude live smoke is manual: `TestClaudeLiveSmokeSuite` requires `CLANKERVAL_CLAUDE_LIVE_SMOKE=1`, `claude` on `PATH`, and `ANTHROPIC_API_KEY`.

## Architecture
```
cmd/clankerval/          # main(), version injection
cmd/releasechangelog/    # Debian changelog generator for release CI
internal/cli/            # CLI parsing: run/init/help/version
internal/evaluations/    # suite loading, harness, adapters, normalization, grading, reporting, bundles
internal/protocol/       # shared message schema for seed transcripts and parsing
internal/release/        # Debian changelog generation
internal/transcript/     # transcript helpers for state and command envelopes
internal/testfixture/    # fixture agent for repo-local eval tests
```

`internal/evaluations` is the center of gravity: `load.go` validates suite/task JSON, `harness.go` stages trial workspaces, `agent_clnku.go`/`agent_claude.go` adapt native outputs into generic events, `normalize.go`/`bundle.go`/`report.go` write canonical outputs.

## Agent model
Supported agents: `clnku`, `claude`. Precedence: `task.agent > suite.agent > CLI --agent`. Canonical trial IDs include the resolved agent.

Project-local prompt files are agent-specific: `input/project/AGENTS.md` for `clnku`, `input/project/CLAUDE.md` for Claude. The task-level `instruction_file` is the shared canonical prompt for both.

## Testing
- `internal/cli/app_test.go` covers the public CLI contract.
- `internal/evaluations/*_test.go` mixes unit, fixture-agent integration, and gated real-Claude tests.
- `make evaluations` builds `internal/testfixture/evalfixture-agent` and runs the `dummy` suite.
- `testdata/evaluations/suites/claude-live-smoke/` is a manual smoke fixture. Fixture load test always runs; live run is gated.

## Design decisions
- Agent output is normalized into generic `TranscriptEvent`/`CommandRecord` before grading. Graders never parse agent-native transcripts.
- Bundle schema is `2`. Raw artifacts live under `raw/agent/` plus `raw/commands.jsonl`.
- Claude runs use `claude --bare --dangerously-skip-permissions` inside a harnessed workspace/HOME. `CLAUDE.md` is staged before launch.

## CI and release
CI on `main` runs `make test`, `make _check-man`, `make build`, and `make evaluations`. Release tags `vX.Y.Z` build binaries, merge into `debian/main`, generate `debian/changelog`, build `.deb` packages, and publish the GitHub release.
