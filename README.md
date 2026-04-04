# clankerval

`clankerval` is the extracted evaluation runner for `clnkr`.

Rename note: `clankerval` is the canonical project, repo, and CLI name. `clnkeval` remains a compatibility alias in `PATH`, and both commands accept the same flags.

## Build

```bash
make build
```

That produces `./clankerval` and a local `./clnkeval` symlink for compatibility.

## Usage

Run the checked-in dummy self-test suite from this repo:

```bash
make evaluations
```

Scaffold a new evaluations tree:

```bash
clankerval init
```

In a consuming project with its own `evaluations/` tree, the compatibility alias accepts the same flags:

```bash
clnkeval run --suite default --binary /path/to/clnku
clnkeval init
```

## Examples

```bash
# Run this repo's checked-in dummy suite with the fixture agent
make evaluations

# Run a consuming project's own suite against an external agent binary
clankerval run --suite default --binary /path/to/clnku

# Same consumer workflow through the compatibility alias
clnkeval run --suite default --binary /path/to/clnku

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
