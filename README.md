# clankerval

[![CI](https://github.com/clnkr-ai/clankerval/actions/workflows/ci.yml/badge.svg)](https://github.com/clnkr-ai/clankerval/actions/workflows/ci.yml)

`clankerval` runs checked-in evaluation suites against agent CLIs. Today the runner supports `clnku` and Claude Code.

Rename note: `clankerval` is the canonical project, repo, and CLI name. `clnkeval` remains a compatibility alias, and both commands accept the same flags.

## Build

```bash
make build
```

That produces `./clankerval` and a local `./clnkeval` symlink for compatibility.

## Usage

Scaffold a new evaluations tree:

```bash
clankerval init
```

Run this repo's checked-in dummy self-test suite:

```bash
make evaluations
```

Run a consuming project's suite against `clnku`:

```bash
export CLNKR_EVALUATION_MODE=live-provider
export CLNKR_EVALUATION_API_KEY=your-api-key
export CLNKR_EVALUATION_BASE_URL=https://api.openai.com/v1
export CLNKR_EVALUATION_MODEL=gpt-5.4-nano

clankerval run --suite default --binary /path/to/clnku
```

Run a consuming project's suite against Claude Code:

```bash
export CLNKR_EVALUATION_MODE=live-provider
export CLNKR_EVALUATION_API_KEY=placeholder
export CLNKR_EVALUATION_BASE_URL=https://api.anthropic.com
export CLNKR_EVALUATION_MODEL=claude-code-default
export ANTHROPIC_API_KEY=your-anthropic-key

clankerval run --suite default --agent claude
```

Phase-1 Claude support keeps the shared live-provider config gate. `CLNKR_EVALUATION_API_KEY`, `CLNKR_EVALUATION_BASE_URL`, and `CLNKR_EVALUATION_MODEL` still need values. The Claude adapter also requires `claude` on `PATH` and forwards `ANTHROPIC_API_KEY` into the harnessed run.

## Suite layout

`clankerval init` creates the default shape:

```text
evaluations/
  suites/
    default/
      suite.json
      tasks/
        001-example/
          task.json
          input/
            instruction.txt
```

At the suite level, `suite.json` selects:
- `id`, `description`
- `mode`: `mock-provider` or `live-provider`
- optional `agent`: `clnku` or `claude`
- `trials_per_task`, `failure_policy`, and ordered `tasks`

At the task level, `task.json` selects:
- `instruction_file`, `working_directory`, `full_send`, `step_limit`
- optional `seed_transcript_file`
- optional `mode` and `agent`
- `scripted_turns_file` for `mock-provider` tasks
- grader configuration

Agent precedence is `task.agent > suite.agent > CLI --agent`, with `clnku` as the default when no level sets it.

Project-local prompt files are agent-specific:
- `input/project/AGENTS.md` for `clnku` tasks
- `input/project/CLAUDE.md` for Claude tasks

The shared `instruction_file` remains the canonical task prompt in both cases.

## Output bundles

Each trial writes a bundle under the run output directory. The important top-level artifacts are:
- `bundle.json` for metadata, including resolved agent and provider identity
- `raw/agent/` for native agent artifacts
- `raw/commands.jsonl` for the normalized command trace
- `normalized/transcript.jsonl`
- `normalized/outcome.json`
- `normalized/graders.jsonl`

## Manual Claude smoke

This repo includes a narrow checked-in live Claude smoke suite. The fixture always loads in CI. The real live run is manual and gated:

```bash
CLANKERVAL_CLAUDE_LIVE_SMOKE=1 \
go test ./internal/evaluations -run TestClaudeLiveSmokeSuite -count=1
```

That test also requires `claude` on `PATH` and `ANTHROPIC_API_KEY` in the environment.

## Development

```bash
make build
make test
make evaluations
make man
make _check-man
```

`doc/clankerval.1.md` is the source of truth for the manpage. `doc/clankerval.1` is generated with `go-md2man` and committed.

## License

Apache-2.0
