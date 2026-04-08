package cli

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"

	"github.com/clnkr-ai/clankerval/internal/evaluations"
	"github.com/clnkr-ai/clankerval/internal/testsupport/clnkusim"
)

func TestRun(t *testing.T) {
	lockRealRepoForTest(t)
	stagedClnku := mustStageClnku(t)

	t.Run("run suite prints summary", func(t *testing.T) {
		repoRoot := moduleRoot(t)
		requireCleanRealRepoForTest(t, repoRoot)
		evalsDir := newTempEvalsDir(t)
		suiteID := writeTempSuite(t, evalsDir, suiteSpec{
			trialsPerTask: 1,
			stopOnFirst:   true,
			maxFailed:     1,
			tasks:         []suiteTaskSpec{{id: "task-pass"}},
		})

		stdout := &bytes.Buffer{}
		stderr := &bytes.Buffer{}
		exitCode := Run("clankerval", "dev", []string{"run", "--suite", suiteID, "--binary", stagedClnku, "--evals-dir", evalsDir}, repoRoot, stdout, stderr, func(string) string { return "" })
		if exitCode != 0 {
			t.Fatalf("exit code = %d, want 0; stderr=%q", exitCode, stderr.String())
		}
		if got, want := stdout.String(), "suite="+suiteID+" tasks=1 trials=1 passed=1 failed=0\n"; got != want {
			t.Fatalf("stdout = %q, want %q", got, want)
		}
		if !strings.Contains(stderr.String(), "clankerval:") {
			t.Fatalf("stderr = %q, want progress output", stderr.String())
		}
	})

	t.Run("failed trial prints stderr context and exits non-zero", func(t *testing.T) {
		repoRoot := moduleRoot(t)
		requireCleanRealRepoForTest(t, repoRoot)
		evalsDir := newTempEvalsDir(t)
		suiteID := writeTempSuite(t, evalsDir, suiteSpec{
			trialsPerTask: 1,
			stopOnFirst:   true,
			maxFailed:     1,
			tasks:         []suiteTaskSpec{{id: "task-fail", noChange: true}},
		})
		stdout := &bytes.Buffer{}
		stderr := &bytes.Buffer{}
		exitCode := Run("clankerval", "dev", []string{"run", "--suite", suiteID, "--binary", stagedClnku, "--evals-dir", evalsDir}, repoRoot, stdout, stderr, func(string) string { return "" })
		if exitCode == 0 {
			t.Fatalf("exit code = 0, want non-zero")
		}
		if !strings.Contains(stdout.String(), "suite="+suiteID) {
			t.Fatalf("stdout = %q, want suite summary", stdout.String())
		}
		if !strings.Contains(stderr.String(), "task=task-fail") || !strings.Contains(stderr.String(), "trial=") || !strings.Contains(stderr.String(), "required graders failed") {
			t.Fatalf("stderr = %q, want task/trial failure context", stderr.String())
		}
	})

	t.Run("run without --binary still builds ./cmd/clnku when source tree exists", func(t *testing.T) {
		sourceRepoRoot := t.TempDir()
		if err := clnkusim.WriteSourceTree(sourceRepoRoot); err != nil {
			t.Fatalf("WriteSourceTree(): %v", err)
		}
		mustWrite(t, filepath.Join(sourceRepoRoot, trackedDummyNotePath()), "seed note\n")
		for _, args := range [][]string{
			{"init"},
			{"-C", sourceRepoRoot, "config", "user.name", "Codex"},
			{"-C", sourceRepoRoot, "config", "user.email", "codex@example.com"},
			{"-C", sourceRepoRoot, "add", "."},
			{"-C", sourceRepoRoot, "commit", "-m", "init"},
		} {
			cmd := exec.Command("git", args...)
			if len(args) == 1 && args[0] == "init" {
				cmd.Dir = sourceRepoRoot
			}
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("git %v: %v: %s", args, err, out)
			}
		}

		evalsDir := newTempEvalsDir(t)
		suiteID := writeTempSuite(t, evalsDir, suiteSpec{
			trialsPerTask: 1,
			stopOnFirst:   true,
			maxFailed:     1,
			tasks:         []suiteTaskSpec{{id: "task-pass"}},
		})
		outputDir := t.TempDir()
		stdout := &bytes.Buffer{}
		stderr := &bytes.Buffer{}
		exitCode := Run(
			"clankerval",
			"dev",
			[]string{"run", "--suite", suiteID, "--evals-dir", evalsDir, "--output-dir", outputDir},
			sourceRepoRoot,
			stdout,
			stderr,
			func(string) string { return "" },
		)
		if exitCode != 0 {
			t.Fatalf("exit code = %d, want 0; stderr=%q", exitCode, stderr.String())
		}
		if !strings.Contains(stderr.String(), "clankerval: building clnku from source...") {
			t.Fatalf("stderr = %q, want source-build progress", stderr.String())
		}
		if !strings.Contains(stderr.String(), "clankerval: harness ready") {
			t.Fatalf("stderr = %q, want harness-ready progress", stderr.String())
		}
		if _, err := os.Stat(filepath.Join(outputDir, "reports", "junit.xml")); err != nil {
			t.Fatalf("expected report in temp output dir: %v", err)
		}
	})

	t.Run("run without --binary falls back to clnku on PATH when ./cmd/clnku does not exist", func(t *testing.T) {
		repoRoot := moduleRoot(t)
		requireCleanRealRepoForTest(t, repoRoot)
		evalsDir := newTempEvalsDir(t)
		suiteID := writeTempSuite(t, evalsDir, suiteSpec{
			trialsPerTask: 1,
			stopOnFirst:   true,
			maxFailed:     1,
			tasks:         []suiteTaskSpec{{id: "task-pass"}},
		})
		stdout := &bytes.Buffer{}
		stderr := &bytes.Buffer{}
		pathEnv := filepath.Dir(stagedClnku) + string(os.PathListSeparator) + os.Getenv("PATH")
		t.Setenv("PATH", pathEnv)
		exitCode := Run("clankerval", "dev", []string{"run", "--suite", suiteID, "--evals-dir", evalsDir}, repoRoot, stdout, stderr, func(key string) string {
			if key == "PATH" {
				return pathEnv
			}
			return ""
		})
		if exitCode != 0 {
			t.Fatalf("exit code = %d, want 0; stderr=%q", exitCode, stderr.String())
		}
		if !strings.Contains(stdout.String(), "suite="+suiteID) {
			t.Fatalf("stdout = %q, want suite summary", stdout.String())
		}
		if strings.Contains(stderr.String(), "building clnku from source...") {
			t.Fatalf("stderr = %q, want PATH fallback without source build", stderr.String())
		}
		if !strings.Contains(stderr.String(), "clankerval: harness ready") {
			t.Fatalf("stderr = %q, want harness-ready progress", stderr.String())
		}
	})

	t.Run("claude agent does not preflight clnku before suite loading", func(t *testing.T) {
		repoRoot := moduleRoot(t)
		stdout := &bytes.Buffer{}
		stderr := &bytes.Buffer{}
		exitCode := Run("clankerval", "dev", []string{"run", "--suite", "missing", "--agent", "claude"}, repoRoot, stdout, stderr, func(key string) string {
			if key == "PATH" {
				return ""
			}
			return ""
		})
		if exitCode == 0 {
			t.Fatal("exit code = 0, want non-zero for missing suite")
		}
		if strings.Contains(stderr.String(), "resolve clnku binary") {
			t.Fatalf("stderr = %q, want suite load failure before any clnku preflight", stderr.String())
		}
		if !strings.Contains(stderr.String(), "run suite load suite") {
			t.Fatalf("stderr = %q, want suite load failure", stderr.String())
		}
	})

	t.Run("run resolves relative evals and output dirs against explicit cwd", func(t *testing.T) {
		repoRoot := moduleRoot(t)
		requireCleanRealRepoForTest(t, repoRoot)
		evalsDir := newTempEvalsDir(t)
		suiteID := writeTempSuite(t, evalsDir, suiteSpec{
			trialsPerTask: 1,
			stopOnFirst:   true,
			maxFailed:     1,
			tasks:         []suiteTaskSpec{{id: "task-pass"}},
		})
		outputDir := t.TempDir()
		relEvalsDir, err := filepath.Rel(repoRoot, evalsDir)
		if err != nil {
			t.Fatalf("filepath.Rel(evalsDir): %v", err)
		}
		relOutputDir, err := filepath.Rel(repoRoot, outputDir)
		if err != nil {
			t.Fatalf("filepath.Rel(outputDir): %v", err)
		}

		stdout := &bytes.Buffer{}
		stderr := &bytes.Buffer{}
		exitCode := Run(
			"clankerval",
			"dev",
			[]string{"run", "--suite", suiteID, "--binary", stagedClnku, "--evals-dir", relEvalsDir, "--output-dir", relOutputDir},
			repoRoot,
			stdout,
			stderr,
			func(string) string { return "" },
		)
		if exitCode != 0 {
			t.Fatalf("exit code = %d, want 0; stderr=%q", exitCode, stderr.String())
		}
		if _, err := os.Stat(filepath.Join(outputDir, "trials")); err != nil {
			t.Fatalf("trials output missing under explicit cwd: %v", err)
		}
		if _, err := os.Stat(filepath.Join(outputDir, "reports", "junit.xml")); err != nil {
			t.Fatalf("report output missing under explicit cwd: %v", err)
		}
	})

	t.Run("run repo-local dummy suite with compiled fixture binary", func(t *testing.T) {
		moduleRoot := moduleRoot(t)
		requireCleanRealRepoForTest(t, moduleRoot)
		outputDir := t.TempDir()
		stdout := &bytes.Buffer{}
		stderr := &bytes.Buffer{}
		exitCode := Run(
			"clankerval",
			"dev",
			[]string{
				"run",
				"--suite", "dummy",
				"--evals-dir", filepath.Join(moduleRoot, "testdata", "evaluations"),
				"--binary", stagedClnku,
				"--output-dir", outputDir,
			},
			moduleRoot,
			stdout,
			stderr,
			func(string) string { return "" },
		)
		if exitCode != 0 {
			t.Fatalf("exit code = %d, want 0; stderr=%q", exitCode, stderr.String())
		}
		if got, want := stdout.String(), "suite=dummy tasks=1 trials=1 passed=1 failed=0\n"; got != want {
			t.Fatalf("stdout = %q, want %q", got, want)
		}
		for _, want := range []string{
			"clankerval: harness ready",
			`clankerval: task 1/1 "001-basic" [clnku] trial 1/1 ...`,
		} {
			if !strings.Contains(stderr.String(), want) {
				t.Fatalf("stderr = %q, want substring %q", stderr.String(), want)
			}
		}

		for _, rel := range []string{
			filepath.Join(outputDir, "reports", "junit.xml"),
			filepath.Join(outputDir, "reports", "open-test-report.xml"),
			filepath.Join(outputDir, "trials", "trial-dummy-clnku-000-00-001-basic", "bundle.json"),
			filepath.Join(outputDir, "trials", "trial-dummy-clnku-000-00-001-basic", "outcome", "diff.patch"),
		} {
			if _, err := os.Stat(rel); err != nil {
				t.Fatalf("Stat(%q): %v", rel, err)
			}
		}
	})

	t.Run("run accepts --agent flag", func(t *testing.T) {
		repoRoot := moduleRoot(t)
		requireCleanRealRepoForTest(t, repoRoot)
		evalsDir := newTempEvalsDir(t)
		suiteID := writeTempSuite(t, evalsDir, suiteSpec{
			trialsPerTask: 1,
			stopOnFirst:   true,
			maxFailed:     1,
			tasks:         []suiteTaskSpec{{id: "task-pass"}},
		})

		stdout := &bytes.Buffer{}
		stderr := &bytes.Buffer{}
		exitCode := Run("clankerval", "dev", []string{"run", "--suite", suiteID, "--binary", stagedClnku, "--agent", "clnku", "--evals-dir", evalsDir}, repoRoot, stdout, stderr, func(string) string { return "" })
		if exitCode != 0 {
			t.Fatalf("exit code = %d, want 0; stderr=%q", exitCode, stderr.String())
		}
		if !strings.Contains(stdout.String(), "suite="+suiteID) {
			t.Fatalf("stdout = %q, want suite summary", stdout.String())
		}
	})

	t.Run("run rejects invalid --agent value", func(t *testing.T) {
		stdout := &bytes.Buffer{}
		stderr := &bytes.Buffer{}
		exitCode := Run("clankerval", "dev", []string{"run", "--suite", "default", "--agent", "bogus"}, ".", stdout, stderr, func(string) string { return "" })
		if exitCode == 0 {
			t.Fatal("exit code = 0, want non-zero for invalid agent")
		}
		if !strings.Contains(stderr.String(), "agent") {
			t.Fatalf("stderr = %q, want agent validation error", stderr.String())
		}
	})

	t.Run("unknown subcommand fails with usage", func(t *testing.T) {
		for _, subcommand := range []string{"list-suites", "list-tasks", "validate", "bogus"} {
			stdout := &bytes.Buffer{}
			stderr := &bytes.Buffer{}
			exitCode := Run("clankerval", "dev", []string{subcommand}, ".", stdout, stderr, func(string) string { return "" })
			if exitCode == 0 {
				t.Fatalf("%s exit code = 0, want non-zero", subcommand)
			}
			if !strings.Contains(stderr.String(), "unknown command") {
				t.Fatalf("%s stderr = %q, want unknown command error", subcommand, stderr.String())
			}
		}
	})

	t.Run("invalid evaluation mode surfaces config error", func(t *testing.T) {
		stdout := &bytes.Buffer{}
		stderr := &bytes.Buffer{}
		exitCode := Run("clankerval", "dev", []string{"run", "--suite", "default"}, ".", stdout, stderr, func(key string) string {
			if key == "CLNKR_EVALUATION_MODE" {
				return "bogus"
			}
			return ""
		})
		if exitCode == 0 {
			t.Fatal("exit code = 0, want non-zero")
		}
		if !strings.Contains(stderr.String(), "unknown CLNKR_EVALUATION_MODE") {
			t.Fatalf("stderr = %q, want invalid mode error", stderr.String())
		}
	})

	t.Run("missing live-provider configuration surfaces config error", func(t *testing.T) {
		stdout := &bytes.Buffer{}
		stderr := &bytes.Buffer{}
		exitCode := Run("clankerval", "dev", []string{"run", "--suite", "default"}, ".", stdout, stderr, func(key string) string {
			if key == "CLNKR_EVALUATION_MODE" {
				return "live-provider"
			}
			return ""
		})
		if exitCode == 0 {
			t.Fatal("exit code = 0, want non-zero")
		}
		if !strings.Contains(stderr.String(), "missing API key") {
			t.Fatalf("stderr = %q, want live-provider config error", stderr.String())
		}
	})

	t.Run("init scaffolds default live-provider example suite", func(t *testing.T) {
		repoRoot := t.TempDir()
		stdout := &bytes.Buffer{}
		stderr := &bytes.Buffer{}
		exitCode := Run("clankerval", "dev", []string{"init"}, repoRoot, stdout, stderr, func(string) string { return "" })
		if exitCode != 0 {
			t.Fatalf("exit code = %d, want 0; stderr=%q", exitCode, stderr.String())
		}
		if got := stdout.String(); got != "initialized evaluations/ with default suite and example task\n" {
			t.Fatalf("stdout = %q, want init success message", got)
		}
		if stderr.Len() != 0 {
			t.Fatalf("stderr = %q, want empty stderr", stderr.String())
		}

		suitePath := filepath.Join(repoRoot, "evaluations", "suites", "default", "suite.json")
		taskPath := filepath.Join(repoRoot, "evaluations", "suites", "default", "tasks", "001-example", "task.json")
		if _, err := os.Stat(suitePath); err != nil {
			t.Fatalf("suite.json missing: %v", err)
		}
		if _, err := os.Stat(taskPath); err != nil {
			t.Fatalf("task.json missing: %v", err)
		}

		suite, err := evaluations.LoadSuite(suitePath)
		if err != nil {
			t.Fatalf("LoadSuite(): %v", err)
		}
		if suite.Mode != evaluations.ModeLiveProvider {
			t.Fatalf("suite mode = %q, want %q", suite.Mode, evaluations.ModeLiveProvider)
		}
		task, err := evaluations.LoadTask(taskPath)
		if err != nil {
			t.Fatalf("LoadTask(): %v", err)
		}
		if task.Mode != evaluations.ModeLiveProvider {
			t.Fatalf("task mode = %q, want %q", task.Mode, evaluations.ModeLiveProvider)
		}
	})
}

func TestTopLevelContract(t *testing.T) {
	cases := []struct {
		name       string
		args       []string
		wantExit   int
		wantStdout string
		wantStderr string
	}{
		{"help long", []string{"--help"}, 0, "clankerval <command> [flags]", ""},
		{"help short", []string{"-h"}, 0, "clankerval <command> [flags]", ""},
		{"help word", []string{"help"}, 0, "clankerval <command> [flags]", ""},
		{"version long", []string{"--version"}, 0, "clankerval ", ""},
		{"version short", []string{"-V"}, 0, "clankerval ", ""},
		{"version word", []string{"version"}, 0, "clankerval ", ""},
		{"no args", nil, 1, "", "clankerval <command> [flags]"},
		{"unknown", []string{"bogus"}, 1, "", "unknown command"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stdout := &bytes.Buffer{}
			stderr := &bytes.Buffer{}
			exit := Run("clankerval", "dev", tc.args, ".", stdout, stderr, func(string) string { return "" })
			if exit != tc.wantExit {
				t.Fatalf("exit = %d, want %d", exit, tc.wantExit)
			}
			if tc.wantStdout != "" && !strings.Contains(stdout.String(), tc.wantStdout) {
				t.Fatalf("stdout = %q, want substring %q", stdout.String(), tc.wantStdout)
			}
			if tc.wantStdout == "" && stdout.Len() != 0 {
				t.Fatalf("stdout = %q, want empty stdout", stdout.String())
			}
			if tc.wantStderr != "" && !strings.Contains(stderr.String(), tc.wantStderr) {
				t.Fatalf("stderr = %q, want substring %q", stderr.String(), tc.wantStderr)
			}
			if tc.wantStderr == "" && stderr.Len() != 0 {
				t.Fatalf("stderr = %q, want empty stderr", stderr.String())
			}
		})
	}
}

func TestSubcommandHelpStreamsToStderr(t *testing.T) {
	for _, args := range [][]string{{"run", "--help"}, {"run", "-h"}, {"init", "--help"}, {"init", "-h"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			stdout := &bytes.Buffer{}
			stderr := &bytes.Buffer{}
			exit := Run("clankerval", "dev", args, ".", stdout, stderr, func(string) string { return "" })
			if exit != 0 {
				t.Fatalf("exit = %d, want 0", exit)
			}
			if stdout.Len() != 0 {
				t.Fatalf("stdout = %q, want empty stdout", stdout.String())
			}
			if !strings.Contains(stderr.String(), "Usage: clankerval "+args[0]) {
				t.Fatalf("stderr = %q, want subcommand usage", stderr.String())
			}
		})
	}
}

var (
	stageClnkuOnce sync.Once
	stageClnkuPath string
	stageClnkuErr  error
)

func mustStageClnku(t *testing.T) string {
	t.Helper()

	stageClnkuOnce.Do(func() {
		tempDir, err := os.MkdirTemp("", "clankerval-clnku-*")
		if err != nil {
			stageClnkuErr = fmt.Errorf("create temp dir for staged clnku: %w", err)
			return
		}
		stageClnkuPath = filepath.Join(tempDir, "clnku")

		if err := clnkusim.BuildBinary(stageClnkuPath); err != nil {
			stageClnkuErr = fmt.Errorf("build staged clnku: %w", err)
		}
	})
	if stageClnkuErr != nil {
		t.Fatal(stageClnkuErr)
	}
	return stageClnkuPath
}

func moduleRoot(t *testing.T) string {
	t.Helper()

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd(): %v", err)
	}
	return filepath.Clean(filepath.Join(cwd, "..", ".."))
}

func lockRealRepoForTest(t *testing.T) {
	t.Helper()

	lockPath := filepath.Join(os.TempDir(), "clankerval-real-repo-tests.lock")
	file, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatalf("OpenFile(%q): %v", lockPath, err)
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX); err != nil {
		_ = file.Close()
		t.Fatalf("Flock(%q): %v", lockPath, err)
	}
	t.Cleanup(func() {
		_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
		_ = file.Close()
	})
}

func requireCleanRealRepoForTest(t *testing.T, repoRoot string) {
	t.Helper()

	cmd := exec.Command("git", "-C", repoRoot, "status", "--porcelain", "--untracked-files=all")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git status: %v: %s", err, out)
	}
	if trimmed := strings.TrimSpace(string(out)); trimmed != "" {
		t.Skipf("skipping real-checkout test in dirty repo:\n%s", trimmed)
	}
}

type suiteSpec struct {
	trialsPerTask int
	stopOnFirst   bool
	maxFailed     int
	tasks         []suiteTaskSpec
}

type suiteTaskSpec struct {
	id       string
	noChange bool
}

func writeTempSuite(t *testing.T, evalsDir string, spec suiteSpec) string {
	t.Helper()

	suitesRoot := filepath.Join(evalsDir, "suites")
	suiteDir, err := os.MkdirTemp(suitesRoot, "clankerval-*")
	if err != nil {
		t.Fatalf("MkdirTemp(): %v", err)
	}
	t.Cleanup(func() {
		_ = os.RemoveAll(suiteDir)
	})

	tasks := make([]string, 0, len(spec.tasks))
	for _, task := range spec.tasks {
		tasks = append(tasks, task.id)
		taskDir := filepath.Join(suiteDir, "tasks", task.id)
		mustWrite(t, filepath.Join(taskDir, "input", "instruction.txt"), "Rewrite `"+trackedDummyNotePath()+"` so it contains `hello`, then finish.\n")
		modelTurns := "[\"{\\\"type\\\":\\\"act\\\",\\\"command\\\":\\\"" + trackedDummyNoteCommandLiteral() + "\\\"}\",\"{\\\"type\\\":\\\"done\\\",\\\"summary\\\":\\\"finished\\\"}\"]\n"
		if task.noChange {
			modelTurns = "[\"{\\\"type\\\":\\\"done\\\",\\\"summary\\\":\\\"finished\\\"}\"]\n"
		}
		mustWrite(t, filepath.Join(taskDir, "input", "model-turns.json"), modelTurns)
		mustWrite(t, filepath.Join(taskDir, "input", "project", "AGENTS.md"), "Keep changes tight. Work in the current directory.\n")
		taskJSON := `{
  "id": "` + task.id + `",
  "instruction_file": "input/instruction.txt",
  "scripted_turns_file": "input/model-turns.json",
  "working_directory": ".",
  "full_send": true,
  "step_limit": 10,
  "graders": {
    "outcome_diff": {
      "enabled": true,
      "required": true
    },
    "transcript_command_trace": {
      "enabled": false,
      "required": false
    }
  }
}`
		mustWrite(t, filepath.Join(taskDir, "task.json"), taskJSON)
	}

	suiteID := filepath.Base(suiteDir)
	suiteJSON := `{
  "id": "` + suiteID + `",
  "description": "clankerval temp suite",
  "mode": "mock-provider",
  "trials_per_task": ` + strconv.Itoa(spec.trialsPerTask) + `,
  "failure_policy": {
    "stop_on_first_failure": ` + strconv.FormatBool(spec.stopOnFirst) + `,
    "max_failed_tasks": ` + strconv.Itoa(spec.maxFailed) + `
  },
  "tasks": ["` + strings.Join(tasks, `","`) + `"]
}`
	mustWrite(t, filepath.Join(suiteDir, "suite.json"), suiteJSON)
	return suiteID
}

func newTempEvalsDir(t *testing.T) string {
	t.Helper()

	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "suites"), 0o755); err != nil {
		t.Fatalf("MkdirAll(%q): %v", filepath.Join(root, "suites"), err)
	}
	return root
}

func trackedDummyNotePath() string {
	return filepath.Join("testdata", "evaluations", "fixtures", "dummy", "001-basic", "note.txt")
}

func trackedDummyNoteCommand() string {
	return "printf 'hello\\n' > " + trackedDummyNotePath()
}

func trackedDummyNoteCommandLiteral() string {
	return strings.ReplaceAll(trackedDummyNoteCommand(), "\\", "\\\\")
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll(%q): %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile(%q): %v", path, err)
	}
}
