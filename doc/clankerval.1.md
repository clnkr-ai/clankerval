clankerval 1 "clankerval" "User Commands"
=========================================

# NAME

clankerval - evaluation runner for clnkr-compatible agents

# SYNOPSIS

**clankerval** [**--help**] [**--version**] *command* [*flags*]

# DESCRIPTION

**clankerval** runs evaluation suites owned by a consuming project. It currently supports two commands: **run** to execute a suite and **init** to scaffold a default evaluations tree.

Rename note: **clankerval** is the canonical command name. **clnkeval** remains supported as a compatibility alias, and both commands accept the same flags.

# COMMANDS

**run**
: Run an evaluation suite against the current directory.

**init**
: Scaffold an **evaluations/** directory with the default live-provider example suite.

# RUN OPTIONS

**--suite** *id*
: Suite identifier to run. Defaults to **default**.

**--binary** *path*
: Path to the agent binary under test. When omitted, **clankerval** builds **./cmd/clnku** from the current source tree when present; otherwise it resolves **clnku** from **PATH**.

**--evals-dir** *path*
: Evaluations directory. Defaults to **./evaluations** relative to the current working directory.

**--output-dir** *path*
: Output directory for trial bundles and reports. Defaults to the evaluations directory.

# EXAMPLES

Run this repo's checked-in dummy self-test suite:

```bash
make evaluations
```

Run a consuming project's own suite:

```bash
clankerval run --suite default --binary /path/to/clnku
```

Scaffold the default live-provider example suite:

```bash
clankerval init
```

Compatibility alias:

```bash
clnkeval run --suite default --binary /path/to/clnku
clnkeval init
```

# EXIT STATUS

**0**
: Success.

**1**
: Error.

# AUTHOR

Brian Cosgrove <cosgroveb@gmail.com>
