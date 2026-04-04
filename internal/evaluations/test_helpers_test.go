package evaluations

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/clnkr-ai/clankerval/internal/testsupport/clnkusim"
)

func newTempRepoRoot(t *testing.T) string {
	t.Helper()

	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "evaluations", "suites"), 0o755); err != nil {
		t.Fatalf("MkdirAll(evaluations/suites): %v", err)
	}
	return root
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

func newHarnessForTests(t *testing.T, ctx context.Context, repoRoot string) *Harness {
	t.Helper()

	harness, err := NewHarness(
		ctx,
		repoRoot,
		WithBinary(mustClnkuPath(t)),
		WithEvalsDir(filepath.Join(repoRoot, "evaluations")),
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
)
