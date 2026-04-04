package cli

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
)

// Run implements the top-level CLI contract for both the canonical and
// compatibility command names.
func Run(name, version string, args []string, cwd string, stdout, stderr io.Writer, getenv func(string) string) int {
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}
	if getenv == nil {
		getenv = os.Getenv
	}
	if cwd == "" {
		cwd = "."
	}
	_ = cwd
	_ = getenv

	if len(args) == 0 {
		_, _ = fmt.Fprint(stderr, topLevelUsage(name))
		return 1
	}

	switch args[0] {
	case "--help", "-h", "help":
		_, _ = fmt.Fprint(stdout, topLevelUsage(name))
		return 0
	case "--version", "-V", "version":
		_, _ = fmt.Fprintln(stdout, name+" "+version)
		return 0
	case "run":
		return runSuite(name, args[1:], stderr)
	case "init":
		return runInit(name, args[1:], stderr)
	default:
		_, _ = fmt.Fprintf(stderr, "Error: unknown command %q\n\n", args[0])
		_, _ = fmt.Fprint(stderr, topLevelUsage(name))
		return 1
	}
}

func topLevelUsage(name string) string {
	return fmt.Sprintf(`%s - evaluation runner for clnkr

Usage:
  %s <command> [flags]

Commands:
  run     Run an evaluation suite
  init    Scaffold an evaluations/ directory

Flags:
  --help      Show this help
  --version   Print version

Run '%s <command> --help' for details on a specific command.
`, name, name, name)
}

func runSuite(name string, args []string, stderr io.Writer) int {
	flags := flag.NewFlagSet(name+" run", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() {
		fmt.Fprintf(stderr, `Usage: %s run [flags]

Run an evaluation suite against the current directory.
`, name)
	}
	if err := flags.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		_, _ = fmt.Fprintf(stderr, "Error: %v\n", err)
		return 1
	}
	if flags.NArg() != 0 {
		_, _ = fmt.Fprintf(stderr, "Error: unexpected arguments: %s\n", strings.Join(flags.Args(), " "))
		return 1
	}
	_, _ = fmt.Fprintf(stderr, "Error: %s run is not implemented yet\n", name)
	return 1
}

func runInit(name string, args []string, stderr io.Writer) int {
	flags := flag.NewFlagSet(name+" init", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() {
		fmt.Fprintf(stderr, `Usage: %s init

Scaffold an evaluations/ directory with a default suite and example task.
The directory must not already exist.
`, name)
	}
	if err := flags.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		_, _ = fmt.Fprintf(stderr, "Error: %v\n", err)
		return 1
	}
	if flags.NArg() != 0 {
		_, _ = fmt.Fprintf(stderr, "Error: unexpected arguments: %s\n", strings.Join(flags.Args(), " "))
		return 1
	}
	_, _ = fmt.Fprintf(stderr, "Error: %s init is not implemented yet\n", name)
	return 1
}
