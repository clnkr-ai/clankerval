package evaluations

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestClaudeLiveSmokeFixtureLoads(t *testing.T) {
	fixtureRoot := claudeLiveSmokeFixtureRoot(t)

	suite, err := LoadSuite(filepath.Join(fixtureRoot, "suite.json"))
	if err != nil {
		t.Fatalf("LoadSuite(): %v", err)
	}
	if suite.ID != "claude-live-smoke" {
		t.Fatalf("suite id = %q, want %q", suite.ID, "claude-live-smoke")
	}
	if suite.Mode != ModeLiveProvider {
		t.Fatalf("suite mode = %q, want %q", suite.Mode, ModeLiveProvider)
	}
	if suite.Agent != AgentClaude {
		t.Fatalf("suite agent = %q, want %q", suite.Agent, AgentClaude)
	}

	tasks, err := LoadSuiteTasks(fixtureRoot, suite)
	if err != nil {
		t.Fatalf("LoadSuiteTasks(): %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("task count = %d, want 1", len(tasks))
	}
	if tasks[0].ID != "001-fix-failing-test" {
		t.Fatalf("task id = %q, want %q", tasks[0].ID, "001-fix-failing-test")
	}
	if tasks[0].WorkingDirectory != "." {
		t.Fatalf("working_directory = %q, want %q", tasks[0].WorkingDirectory, ".")
	}
	if !tasks[0].Graders.OutcomeCommandOutput.Enabled || !tasks[0].Graders.OutcomeCommandOutput.Required {
		t.Fatalf("outcome_command_output grader = %#v, want enabled required", tasks[0].Graders.OutcomeCommandOutput)
	}
}

func TestClaudeLiveSmokeSuite(t *testing.T) {
	if os.Getenv("CLANKERVAL_CLAUDE_LIVE_SMOKE") != "1" {
		t.Skip("skipping: set CLANKERVAL_CLAUDE_LIVE_SMOKE=1 to run the manual Claude live smoke suite")
	}
	if _, err := exec.LookPath("claude"); err != nil {
		t.Skip("skipping: claude binary not found on PATH")
	}
	if os.Getenv("ANTHROPIC_API_KEY") == "" {
		t.Skip("skipping: ANTHROPIC_API_KEY is not set")
	}

	lockRealRepoForTest(t)
	repoRoot := moduleRoot(t)
	requireCleanRealRepoForTest(t, repoRoot)
	evalsDir := filepath.Join(repoRoot, "testdata", "evaluations")
	outputDir := t.TempDir()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	report, err := RunSuite(
		ctx,
		repoRoot,
		"claude-live-smoke",
		RunConfig{
			Mode:    ModeLiveProvider,
			Agent:   AgentClaude,
			APIKey:  "dummy-key",
			BaseURL: "https://api.anthropic.com",
			Model:   "claude-code-default",
		},
		WithSuiteEvalsDir(evalsDir),
		WithSuiteOutputDir(outputDir),
	)
	if err != nil {
		t.Fatalf("RunSuite(): %v", err)
	}
	if report.TaskCount != 1 || report.TrialCount != 1 || report.Passed != 1 || report.Failed != 0 {
		t.Fatalf("report counts = %#v, want one passing live Claude smoke trial", report)
	}
	if len(report.Tasks) != 1 || len(report.Tasks[0].Trials) != 1 {
		t.Fatalf("report tasks = %#v, want one task with one trial", report.Tasks)
	}
	trial := report.Tasks[0].Trials[0]
	if trial.Agent.ID != string(AgentClaude) {
		t.Fatalf("trial agent id = %q, want %q", trial.Agent.ID, AgentClaude)
	}
	if !trial.Passed {
		t.Fatalf("trial passed = false, want true: %#v", trial)
	}
}

func claudeLiveSmokeFixtureRoot(t *testing.T) string {
	t.Helper()

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd(): %v", err)
	}
	return filepath.Join(filepath.Clean(filepath.Join(cwd, "..", "..")), "testdata", "evaluations", "suites", "claude-live-smoke")
}
