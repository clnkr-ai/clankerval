package evaluations

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"

	"github.com/clnkr-ai/clankerval/internal/testsupport/clnkusim"
)

func moduleRoot(t *testing.T) string {
	t.Helper()

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd(): %v", err)
	}
	return filepath.Clean(filepath.Join(cwd, "..", ".."))
}

func newTempRepoRoot(t *testing.T) string {
	t.Helper()

	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "evaluations", "suites"), 0o755); err != nil {
		t.Fatalf("MkdirAll(evaluations/suites): %v", err)
	}
	trackedNotePath := filepath.Join(root, trackedDummyNotePath())
	if err := os.MkdirAll(filepath.Dir(trackedNotePath), 0o755); err != nil {
		t.Fatalf("MkdirAll(%q): %v", filepath.Dir(trackedNotePath), err)
	}
	gitSteps := [][]string{
		{"init"},
		{"-C", root, "config", "user.name", "Codex"},
		{"-C", root, "config", "user.email", "codex@example.com"},
	}
	for _, args := range gitSteps {
		cmd := exec.Command("git", args...)
		if len(args) == 1 && args[0] == "init" {
			cmd.Dir = root
		}
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "note.txt"), []byte(""), 0o644); err != nil {
		t.Fatalf("WriteFile(note.txt): %v", err)
	}
	if err := os.WriteFile(trackedNotePath, []byte("seed note\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(%q): %v", trackedNotePath, err)
	}
	add := exec.Command("git", "-C", root, "add", "note.txt", filepath.ToSlash(trackedDummyNotePath()))
	if out, err := add.CombinedOutput(); err != nil {
		t.Fatalf("git add note fixtures: %v: %s", err, out)
	}
	excludePath := filepath.Join(root, ".git", "info", "exclude")
	if err := os.WriteFile(excludePath, []byte("evaluations/\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(%q): %v", excludePath, err)
	}
	commit := exec.Command("git", "-C", root, "commit", "--allow-empty", "-m", "init")
	if out, err := commit.CombinedOutput(); err != nil {
		t.Fatalf("git commit init: %v: %s", err, out)
	}
	return root
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
	return fmt.Sprintf("printf 'hello\\n' > %s", trackedDummyNotePath())
}

func trackedDummyNoteCommandLiteral() string {
	return strings.ReplaceAll(trackedDummyNoteCommand(), "\\", "\\\\")
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

	statusOut, statusErr, exitCode, err := runCommand(
		context.Background(),
		repoRoot,
		repoGitEnv(),
		"git",
		"status",
		"--porcelain",
		"--untracked-files=all",
	)
	if err != nil {
		t.Fatalf("git status: %v", err)
	}
	if exitCode != 0 {
		t.Fatalf("git status exit=%d stderr=%s", exitCode, strings.TrimSpace(statusErr))
	}
	if trimmed := strings.TrimSpace(statusOut); trimmed != "" {
		t.Skipf("skipping real-checkout test in dirty repo:\n%s", trimmed)
	}
}

func mustClnkuPath(t *testing.T) string {
	t.Helper()

	stageEvalClnkuOnce.Do(func() {
		tempDir, err := os.MkdirTemp("", "clankerval-eval-clnku-*")
		if err != nil {
			stageEvalClnkuErr = fmt.Errorf("create temp dir for staged clnku: %w", err)
			return
		}
		stageEvalClnkuPath = filepath.Join(tempDir, "clnku")

		if err := clnkusim.BuildBinary(stageEvalClnkuPath); err != nil {
			stageEvalClnkuErr = fmt.Errorf("build staged clnku: %w", err)
		}
	})
	if stageEvalClnkuErr != nil {
		t.Fatal(stageEvalClnkuErr)
	}
	return stageEvalClnkuPath
}

func newHarnessForTests(t *testing.T, ctx context.Context, repoRoot string, evalsDir ...string) *Harness {
	t.Helper()

	resolvedEvalsDir := filepath.Join(repoRoot, "evaluations")
	if len(evalsDir) > 0 && evalsDir[0] != "" {
		resolvedEvalsDir = evalsDir[0]
	}
	harness, err := NewHarness(
		ctx,
		repoRoot,
		WithBinary(mustClnkuPath(t)),
		WithEvalsDir(resolvedEvalsDir),
	)
	if err != nil {
		t.Fatalf("NewHarness(): %v", err)
	}
	t.Cleanup(func() {
		if err := harness.Close(); err != nil {
			t.Fatalf("Close(): %v", err)
		}
	})
	return harness
}

var (
	stageEvalClnkuOnce sync.Once
	stageEvalClnkuPath string
	stageEvalClnkuErr  error

	stageEvalFixtureOnce sync.Once
	stageEvalFixturePath string
	stageEvalFixtureErr  error
)

func mustEvalFixturePath(t *testing.T) string {
	t.Helper()

	stageEvalFixtureOnce.Do(func() {
		tempDir, err := os.MkdirTemp("", "clankerval-evalfixture-*")
		if err != nil {
			stageEvalFixtureErr = fmt.Errorf("create temp dir for staged eval fixture: %w", err)
			return
		}
		stageEvalFixturePath = filepath.Join(tempDir, "evalfixture-agent")

		cwd, err := os.Getwd()
		if err != nil {
			stageEvalFixtureErr = fmt.Errorf("getwd for eval fixture build: %w", err)
			return
		}
		moduleRoot := filepath.Clean(filepath.Join(cwd, "..", ".."))

		cmd := exec.Command("go", "build", "-o", stageEvalFixturePath, "./internal/testfixture/evalfixture-agent")
		cmd.Dir = moduleRoot
		output, err := cmd.CombinedOutput()
		if err != nil {
			stageEvalFixtureErr = fmt.Errorf("build staged eval fixture: %w: %s", err, output)
		}
	})
	if stageEvalFixtureErr != nil {
		t.Fatal(stageEvalFixtureErr)
	}
	return stageEvalFixturePath
}
