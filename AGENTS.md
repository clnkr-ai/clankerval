# AGENTS.md

## Overview

`clankerval` is the standalone evaluation runner extracted from `clnkr`. It loads checked-in suites, stages one trial workspace per task, runs an agent adapter, normalizes transcript and outcome artifacts, and writes per-trial bundles plus run-level reports.

This repo ships one binary: `clankerval`. `clnkeval` remains a compatibility alias.

## Commands

```bash
make build      # Build clankerval and a local clnkeval symlink (default target)
make test       # Run the Go test suite with -race
make evaluations  # Run the checked-in dummy suite with the fixture agent
make man        # Regenerate doc/clankerval.1 from doc/clankerval.1.md
make _check-man # Fail if the generated manpage is out of sync
make clean      # Remove local build artifacts

# Targeted tests
go test ./internal/cli -v
go test ./internal/evaluations -v
go test ./internal/release -v
```

## Rules

- Before committing doc changes, run `make man` then `make _check-man`.
- Before committing behavior changes, run `make test` and `make evaluations`.
- Keep `doc/clankerval.1.md` as the source of truth. `doc/clankerval.1` is generated and committed.
- Do not hand-edit generated troff unless you are repairing `go-md2man` output in the source markdown and regenerating immediately.
- The Debian packaging branch is `debian/main`. `main` intentionally does not carry a checked-in `debian/` directory.
- When you need packaging context from `main`, read it from `origin/debian/main` with `git show origin/debian/main:path/to/file`.
- Claude live smoke stays manual. The gated test is `TestClaudeLiveSmokeSuite` and requires `CLANKERVAL_CLAUDE_LIVE_SMOKE=1`, `claude` on `PATH`, and `ANTHROPIC_API_KEY`.

## Architecture

The code is split by responsibility:

```text
cmd/clankerval/          # main() and version injection
cmd/releasechangelog/    # Debian changelog generator used by release CI
internal/cli/            # top-level CLI parsing for run/init/help/version
internal/evaluations/    # suite loading, harness, adapters, normalization, grading, reporting, bundle writing
internal/protocol/       # shared message schema used by seed transcripts and transcript parsing
internal/release/        # Debian changelog generation logic
internal/transcript/     # transcript helpers for state and command envelopes
internal/testfixture/    # fixture agent used by repo-local eval tests
```

`internal/evaluations` is the center of gravity:
- `load.go` validates `suite.json` and `task.json`
- `run_config.go` resolves run-time mode and provider config
- `harness.go` stages the trial root, workspace, HOME/XDG dirs, mock provider, and grader execution
- `agent_clnku.go` and `agent_claude.go` adapt native agent outputs into generic transcript events and command records
- `normalize.go`, `bundle.go`, and `report.go` write the canonical outputs

## Agent model

Agent choice is explicit and agent-aware throughout the run:
- supported agents: `clnku`, `claude`
- precedence: `task.agent > suite.agent > CLI --agent`
- canonical trial IDs include the resolved agent
- bundles and reports record both agent identity and provider metadata

Project-local prompt files are agent-specific:
- `input/project/AGENTS.md` for `clnku`
- `input/project/CLAUDE.md` for Claude

The task-level `instruction_file` is still the shared canonical prompt for both agents.

## Testing patterns

- `internal/cli/app_test.go` covers the public CLI contract.
- `internal/evaluations/*_test.go` mixes unit tests, fixture-agent integration tests, and gated real-Claude tests.
- `make evaluations` is the repo-local harness smoke test. It builds `internal/testfixture/evalfixture-agent` and runs the checked-in `dummy` suite.
- `testdata/evaluations/suites/claude-live-smoke/` is a checked-in manual smoke fixture. The fixture load test always runs. The real live run is gated.

## Design decisions

- `clankerval` normalizes agent output into generic `TranscriptEvent` and `CommandRecord` data before grading. Graders should not need to parse agent-native transcripts.
- Bundle schema is currently `2`. Raw artifacts live under `raw/agent/` plus `raw/commands.jsonl`.
- `clnku` still uses native `--trajectory` and `--event-log` outputs internally, but the bundle contract is agent-neutral.
- Claude runs use `claude --bare --dangerously-skip-permissions` inside a harnessed workspace and HOME. Project-local `CLAUDE.md` is staged into that workspace before launch.

## CI and release

- CI on `main` runs `make test`, `make _check-man`, `make build`, and `make evaluations`.
- Release tags `vX.Y.Z` build binaries on `main`, merge the tagged release SHA into `debian/main`, generate `debian/changelog`, build `.deb` packages, and publish the GitHub release.
- Debian changelog history stays on `debian/main`. If you want upstream release notes on `main`, that is a separate `CHANGELOG.md` policy decision.
