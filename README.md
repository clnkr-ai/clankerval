# clankerval

`clankerval` is the extracted evaluation runner for `clnkr`.

Rename note: `clankerval` is the canonical project, repo, and CLI name. `clnkeval` remains a compatibility alias in `PATH`, and both commands accept the same flags.

## Build

```bash
make build
```

That produces `./clankerval` and a local `./clnkeval` symlink for compatibility.

## Usage

Run a suite from the current project:

```bash
clankerval run --suite default
```

Scaffold a new evaluations tree:

```bash
clankerval init
```

The same commands work through the compatibility alias:

```bash
clnkeval run --suite default
clnkeval init
```

## Examples

```bash
# Run the checked-in self-test suite with the fixture agent
make evaluations

# Run a project-owned suite against an external agent binary
clankerval run --suite default --binary /path/to/clnku

# Scaffold the default live-provider example suite
clankerval init
```

## Development

```bash
make test
make evaluations
make man
```

## License

Apache-2.0
