package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/clnkr-ai/clankerval/internal/evaluations"
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
		return runSuite(name, args[1:], cwd, stdout, stderr, getenv)
	case "init":
		return runInit(name, args[1:], cwd, stdout, stderr)
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
%s`, name, name, name, compatibilityNote(name))
}

func runSuite(name string, args []string, cwd string, stdout, stderr io.Writer, getenv func(string) string) int {
	flags := flag.NewFlagSet(name+" run", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() {
		fmt.Fprintf(stderr, `Usage: %s run [flags]

Run an evaluation suite against the current directory.
%s
Flags:
`, name, compatibilityNote(name))
		flags.PrintDefaults()
	}
	suiteID := flags.String("suite", "default", "suite id to run")
	binaryPath := flags.String("binary", "", "path to agent binary under test (default: build ./cmd/clnku when present, otherwise resolve clnku from PATH)")
	evalsDir := flags.String("evals-dir", "", "evaluations directory (default: <cwd>/evaluations)")
	outputDir := flags.String("output-dir", "", "output directory for trials and reports (default: evals dir)")
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

	cfg, err := evaluations.LoadRunConfigFromEnv(getenv)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "Error: %v\n", err)
		return 1
	}

	var suiteOpts []evaluations.RunSuiteOption
	if *binaryPath != "" {
		suiteOpts = append(suiteOpts, evaluations.WithSuiteBinary(*binaryPath))
	} else if !hasClnkuSourceTree(cwd) {
		resolved, err := resolveBinaryOnPath("clnku", getenv("PATH"))
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "Error: %v\n", err)
			return 1
		}
		suiteOpts = append(suiteOpts, evaluations.WithSuiteBinary(resolved))
	}
	if *evalsDir != "" {
		abs, err := resolvePathFromCWD(cwd, *evalsDir)
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "Error: resolving --evals-dir: %v\n", err)
			return 1
		}
		suiteOpts = append(suiteOpts, evaluations.WithSuiteEvalsDir(abs))
	}
	if *outputDir != "" {
		abs, err := resolvePathFromCWD(cwd, *outputDir)
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "Error: resolving --output-dir: %v\n", err)
			return 1
		}
		suiteOpts = append(suiteOpts, evaluations.WithSuiteOutputDir(abs))
	}

	suiteOpts = append(suiteOpts, evaluations.WithProgress(func(msg string) {
		_, _ = fmt.Fprintf(stderr, "%s: %s\n", name, msg)
	}))

	report, err := evaluations.RunSuite(context.Background(), cwd, *suiteID, cfg, suiteOpts...)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "Error: %v\n", err)
		return 1
	}

	_, _ = fmt.Fprintf(stdout, "suite=%s tasks=%d trials=%d passed=%d failed=%d\n", report.SuiteID, report.TaskCount, report.TrialCount, report.Passed, report.Failed)
	for _, task := range report.Tasks {
		for _, trial := range task.Trials {
			if trial.Passed {
				continue
			}
			_, _ = fmt.Fprintf(stderr, "task=%s trial=%s %s\n", trial.TaskID, trial.TrialID, evaluations.TrialFailureMessage(trial))
		}
	}
	if report.Failed > 0 {
		return 1
	}
	return 0
}

func runInit(name string, args []string, cwd string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet(name+" init", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() {
		fmt.Fprintf(stderr, `Usage: %s init

Scaffold an evaluations/ directory with a default suite and example task.
The directory must not already exist.
%s`, name, compatibilityNote(name))
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

	evalsDir := filepath.Join(cwd, "evaluations")
	if _, err := os.Stat(evalsDir); err == nil {
		_, _ = fmt.Fprintf(stderr, "Error: evaluations/ directory already exists\n")
		return 1
	} else if !os.IsNotExist(err) {
		_, _ = fmt.Fprintf(stderr, "Error: %v\n", err)
		return 1
	}

	if err := evaluations.Init(evalsDir); err != nil {
		_, _ = fmt.Fprintf(stderr, "Error: %v\n", err)
		return 1
	}

	_, _ = fmt.Fprintf(stdout, "initialized evaluations/ with default suite and example task\n")
	return 0
}

func compatibilityNote(name string) string {
	if name != "clnkeval" {
		return ""
	}
	return "\nNote: clankerval is the canonical command name. clnkeval remains supported for compatibility.\n"
}

func hasClnkuSourceTree(cwd string) bool {
	info, err := os.Stat(filepath.Join(cwd, "cmd", "clnku"))
	return err == nil && info.IsDir()
}

func resolveBinaryOnPath(name, pathEnv string) (string, error) {
	for _, dir := range filepath.SplitList(pathEnv) {
		if dir == "" {
			continue
		}
		candidate := filepath.Join(dir, name)
		info, err := os.Stat(candidate)
		if err != nil || info.IsDir() || info.Mode()&0o111 == 0 {
			continue
		}
		return candidate, nil
	}
	return "", fmt.Errorf("resolve %s binary: executable file not found in PATH", name)
}

func resolvePathFromCWD(cwd, path string) (string, error) {
	base := cwd
	if !filepath.IsAbs(base) {
		absBase, err := filepath.Abs(base)
		if err != nil {
			return "", err
		}
		base = absBase
	}
	if filepath.IsAbs(path) {
		return filepath.Clean(path), nil
	}
	return filepath.Join(base, path), nil
}
