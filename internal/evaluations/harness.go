package evaluations

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Keep temp-index add batches comfortably below ARG_MAX so large untracked sets
// never fail on argv size.
const gitAddIntentBatchByteLimit = 64 * 1024

// Harness coordinates evaluation trials and resolves the clnku binary only when
// a clnku trial actually needs it.
type Harness struct {
	tempRoot      string
	trialsDir     string
	repoRoot      string
	evalsDir      string
	buildDir      string
	binaryPath    string
	adapter       AgentAdapter // clnku adapter (default)
	claudeAdapter AgentAdapter // claude adapter, lazily validated
}

// HarnessOption configures optional Harness behavior.
type HarnessOption func(*harnessOptions)

type harnessOptions struct {
	binaryPath string
	evalsDir   string
}

// WithBinary skips building clnku from source and uses the supplied binary
// path instead. If path is empty, the harness resolves "clnku" via PATH.
func WithBinary(path string) HarnessOption {
	return func(o *harnessOptions) {
		o.binaryPath = path
	}
}

// WithEvalsDir sets a custom evaluations directory instead of
// repoRoot/evaluations.
func WithEvalsDir(path string) HarnessOption {
	return func(o *harnessOptions) {
		o.evalsDir = path
	}
}

// RunArtifacts captures the raw outputs from one trial run.
type RunArtifacts struct {
	SuiteID               string
	TaskID                string
	TaskRoot              string
	TrialID               string
	SuiteTaskIndex        int
	TrialAttempt          int
	Mode                  Mode
	Agent                 Agent
	AgentVersion          string
	AgentCommand          []string
	ProviderModel         string
	ProviderBaseURL       string
	StartedAt             time.Time
	FinishedAt            time.Time
	SystemPrompt          string
	Trajectory            string
	EventLog              string
	TranscriptEvents      []TranscriptEvent
	Commands              []CommandRecord
	RawAgentArtifacts     []RawAgentArtifact
	ProviderRequests      []CapturedRequest
	ProviderResponses     []string
	WorkspaceRoot         string
	HomeDir               string
	ConfigDir             string
	StateDir              string
	TempDir               string
	ExitCode              int
	TrialPassed           bool
	GraderResults         []GraderResult
	FailedRequiredGraders []GraderResult
	GitDiff               string
	GitNameStatus         string
	GitNumstat            string
}

type repoRootOverlayState struct {
	Present            bool
	Content            []byte
	Mode               os.FileMode
	IsSymlink          bool
	SymlinkTarget      string
	SymlinkTargetState *repoRootOverlayState
}

// NewHarness prepares a reusable harness for evaluation trials.
// Pass WithBinary to force a specific clnku binary path.
func NewHarness(ctx context.Context, repoRoot string, opts ...HarnessOption) (*Harness, error) {
	var o harnessOptions
	for _, opt := range opts {
		opt(&o)
	}

	tempRoot, err := os.MkdirTemp("", "clnkr-eval-harness-*")
	if err != nil {
		return nil, fmt.Errorf("create harness temp root: %w", err)
	}
	trialsDir := filepath.Join(tempRoot, "trials")
	if err := os.MkdirAll(trialsDir, 0o755); err != nil {
		_ = os.RemoveAll(tempRoot)
		return nil, fmt.Errorf("create harness trials dir: %w", err)
	}

	h := &Harness{
		tempRoot:  tempRoot,
		trialsDir: trialsDir,
		repoRoot:  repoRoot,
		evalsDir:  o.evalsDir,
	}

	if o.binaryPath != "" {
		h.binaryPath = o.binaryPath
	}

	h.adapter = &clnkuAdapter{}

	return h, nil
}

// Close removes harness-owned temporary files, including the built binary.
func (h *Harness) Close() error {
	if h == nil || h.tempRoot == "" {
		return nil
	}
	tempRoot := h.tempRoot
	h.tempRoot = ""
	h.trialsDir = ""
	h.buildDir = ""
	h.binaryPath = ""
	if err := os.RemoveAll(tempRoot); err != nil {
		return fmt.Errorf("remove harness temp root %q: %w", tempRoot, err)
	}
	return nil
}

// RunTrial materializes one task run and captures its raw artifacts.
func (h *Harness) RunTrial(ctx context.Context, suite Suite, task Task, cfg RunConfig) (artifacts RunArtifacts, err error) {
	trialRoot, err := os.MkdirTemp(h.trialsDir, "trial-*")
	if err != nil {
		return RunArtifacts{}, fmt.Errorf("create trial root: %w", err)
	}
	defer func() {
		_ = os.RemoveAll(trialRoot)
	}()

	startedAt := time.Now().UTC()
	taskRoot := resolveTaskRoot(h.repoRoot, h.evalsDir, suite.ID, task.ID)
	artifacts = RunArtifacts{
		SuiteID:        suite.ID,
		TaskID:         task.ID,
		TaskRoot:       taskRoot,
		TrialID:        filepath.Base(trialRoot),
		SuiteTaskIndex: suiteTaskIndex(suite, task.ID),
		TrialAttempt:   0,
		StartedAt:      startedAt,
	}

	mode := effectiveMode(suite, task, cfg)
	cfg, err = normalizeRunConfig(cfg, mode)
	if err != nil {
		return RunArtifacts{}, err
	}
	artifacts.Mode = mode
	artifacts.Agent = EffectiveAgent(task.Agent, suite.Agent, cfg.Agent)
	workspaceDir := h.repoRoot

	homeDir := filepath.Join(trialRoot, "home")
	configDir := filepath.Join(trialRoot, "config")
	stateDir := filepath.Join(trialRoot, "state")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		return RunArtifacts{}, fmt.Errorf("create state dir: %w", err)
	}

	if err := copyTreeOptional(filepath.Join(taskRoot, "input", "home"), homeDir); err != nil {
		return RunArtifacts{}, fmt.Errorf("copy home input: %w", err)
	}
	if err := copyTreeOptional(filepath.Join(taskRoot, "input", "config"), configDir); err != nil {
		return RunArtifacts{}, fmt.Errorf("copy config input: %w", err)
	}

	if err := h.preflightRepoRoot(ctx); err != nil {
		return RunArtifacts{}, err
	}
	overlayState, err := captureRepoRootOverlayState(workspaceDir, "AGENTS.md", "CLAUDE.md")
	if err != nil {
		return RunArtifacts{}, err
	}
	cleanupCtx := context.Background()
	if ctx != nil {
		cleanupCtx = context.WithoutCancel(ctx)
	}
	defer func() {
		if cleanupErr := h.cleanupRepoRoot(cleanupCtx, workspaceDir, overlayState); cleanupErr != nil {
			err = errors.Join(err, cleanupErr)
		}
	}()

	if artifacts.Agent == AgentClnku {
		if err := h.ensureClnkuBinary(ctx); err != nil {
			return RunArtifacts{}, fmt.Errorf("ensure clnku binary: %w", err)
		}
	}

	var mockProvider *MockProvider
	if mode == ModeMockProvider {
		turns, err := loadTurns(filepath.Join(taskRoot, task.ScriptedTurnsFile))
		if err != nil {
			return RunArtifacts{}, fmt.Errorf("load mock turns: %w", err)
		}
		mockProvider = NewMockProvider(turns)
		defer mockProvider.Close()
		cfg.BaseURL = mockProvider.URL()
	}

	artifacts.ProviderModel = cfg.Model
	artifacts.ProviderBaseURL = cfg.BaseURL

	env := []string{
		"CLNKR_API_KEY=" + cfg.APIKey,
		"CLNKR_BASE_URL=" + cfg.BaseURL,
		"CLNKR_MODEL=" + cfg.Model,
		"HOME=" + homeDir,
		"XDG_CONFIG_HOME=" + configDir,
		"XDG_STATE_HOME=" + stateDir,
		"LC_ALL=C",
		"TZ=UTC",
		"PATH=" + os.Getenv("PATH"),
	}
	env = appendEnvFromHostIfSet(env, "MISE_YES")
	if artifacts.Agent == AgentClaude {
		env = appendEnvFromHostIfSet(env, "ANTHROPIC_API_KEY")
		env = appendEnvFromHostIfSet(env, "ANTHROPIC_BASE_URL")
		env = appendEnvFromHostIfSet(env, "CLAUDE_CODE_DISABLE_EXPERIMENTAL_BETAS")
	}

	adapterReq := AdapterRequest{
		TaskRoot:     taskRoot,
		Task:         task,
		WorkspaceDir: workspaceDir,
		HomeDir:      homeDir,
		ConfigDir:    configDir,
		StateDir:     stateDir,
		TrialRoot:    trialRoot,
		BinaryPath:   h.binaryPath,
		Env:          env,
	}

	adapter := h.adapterForAgent(artifacts.Agent)
	adapterResult, err := adapter.Run(ctx, adapterReq)
	if err != nil {
		return RunArtifacts{}, fmt.Errorf("run adapter: %w", err)
	}

	artifacts.ExitCode = adapterResult.ExitCode
	artifacts.AgentVersion = adapterResult.AgentVersion
	artifacts.AgentCommand = adapterResult.AgentCommand
	artifacts.SystemPrompt = adapterResult.SystemPrompt
	artifacts.Trajectory = adapterResult.Trajectory
	artifacts.EventLog = adapterResult.EventLog
	artifacts.TranscriptEvents = adapterResult.TranscriptEvents
	artifacts.Commands = adapterResult.Commands
	artifacts.RawAgentArtifacts = adapterResult.RawAgentArtifacts
	artifacts.WorkspaceRoot = workspaceDir
	artifacts.HomeDir = homeDir
	artifacts.ConfigDir = configDir
	artifacts.StateDir = stateDir
	artifacts.TempDir = h.tempRoot
	gitIndexPath, err := repoGitIndexPath(ctx, workspaceDir)
	if err != nil {
		return RunArtifacts{}, err
	}
	tempIndexPath, err := createTempFilePath(trialRoot, "git-index-*")
	if err != nil {
		return RunArtifacts{}, fmt.Errorf("create temp git index: %w", err)
	}
	if err := copyFile(gitIndexPath, tempIndexPath); err != nil {
		return RunArtifacts{}, fmt.Errorf("copy git index: %w", err)
	}
	if err := captureGitDiffArtifacts(ctx, workspaceDir, tempIndexPath, &artifacts); err != nil {
		return RunArtifacts{}, err
	}

	if mockProvider != nil {
		artifacts.ProviderRequests = mockProvider.Requests()
		artifacts.ProviderResponses = collectProviderResponses(artifacts.ProviderRequests)
	}
	graderResults, policyResult, err := runTrialGraders(ctx, task, artifacts)
	if err != nil {
		return RunArtifacts{}, fmt.Errorf("run graders: %w", err)
	}
	artifacts.GraderResults = append([]GraderResult(nil), graderResults...)
	artifacts.TrialPassed = policyResult.Passed
	artifacts.FailedRequiredGraders = append([]GraderResult(nil), policyResult.FailedRequiredGraders...)
	artifacts.FinishedAt = time.Now().UTC()
	return artifacts, nil
}

// adapterForAgent returns the appropriate adapter for the given agent.
// Claude tasks use the dedicated claudeAdapter; everything else uses the
// default clnku adapter, keeping existing clnku tests fully stable.
func (h *Harness) adapterForAgent(agent Agent) AgentAdapter {
	if agent == AgentClaude {
		if h.claudeAdapter == nil {
			h.claudeAdapter = &claudeAdapter{}
		}
		return h.claudeAdapter
	}
	return h.adapter
}

func resolveTaskRoot(repoRoot, evalsDir, suiteID, taskID string) string {
	if evalsDir != "" {
		return filepath.Join(evalsDir, "suites", suiteID, "tasks", taskID)
	}
	return filepath.Join(repoRoot, "evaluations", "suites", suiteID, "tasks", taskID)
}

func (h *Harness) ensureClnkuBinary(ctx context.Context) error {
	if h.binaryPath != "" {
		return nil
	}

	if h.repoRoot != "" && repoHasClnkuSourceTree(h.repoRoot) {
		if h.buildDir == "" {
			buildDir := filepath.Join(h.tempRoot, "build")
			if err := os.MkdirAll(buildDir, 0o755); err != nil {
				return fmt.Errorf("create harness build dir: %w", err)
			}
			h.buildDir = buildDir
		}
		h.binaryPath = filepath.Join(h.buildDir, "clnku")
		if err := h.buildBinary(ctx); err != nil {
			h.binaryPath = ""
			return err
		}
		return nil
	}

	resolved, err := exec.LookPath("clnku")
	if err != nil {
		return fmt.Errorf("resolve clnku binary: %w", err)
	}
	h.binaryPath = resolved
	return nil
}

func (h *Harness) buildBinary(ctx context.Context) error {
	cmd := exec.CommandContext(ctx, "go", "build", "-o", h.binaryPath, "./cmd/clnku")
	cmd.Dir = h.repoRoot
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("build clnku: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func effectiveMode(suite Suite, task Task, cfg RunConfig) Mode {
	if cfg.Mode != "" {
		return cfg.Mode
	}
	if task.Mode != "" {
		return task.Mode
	}
	if suite.Mode != "" {
		return suite.Mode
	}
	return ModeMockProvider
}

func normalizeRunConfig(cfg RunConfig, mode Mode) (RunConfig, error) {
	switch mode {
	case ModeMockProvider:
		if cfg.APIKey == "" {
			cfg.APIKey = "dummy-key"
		}
		if cfg.Model == "" {
			cfg.Model = "test-model"
		}
		cfg.Mode = ModeMockProvider
		return cfg, nil
	case ModeLiveProvider:
		if cfg.Model == "" {
			cfg.Model = "gpt-5.4-nano"
		}
		if cfg.APIKey == "" {
			return RunConfig{}, fmt.Errorf("normalize run config: live-provider mode missing API key")
		}
		if cfg.BaseURL == "" {
			return RunConfig{}, fmt.Errorf("normalize run config: live-provider mode missing base URL")
		}
		cfg.Mode = ModeLiveProvider
		return cfg, nil
	default:
		return RunConfig{}, fmt.Errorf("normalize run config: unsupported mode %q", mode)
	}
}

func runCommand(ctx context.Context, cwd string, env []string, binary string, args ...string) (string, string, int, error) {
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Dir = cwd
	cmd.Env = env

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err == nil {
		return stdout.String(), stderr.String(), 0, nil
	}

	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return stdout.String(), stderr.String(), exitErr.ExitCode(), nil
	}
	return stdout.String(), stderr.String(), 0, fmt.Errorf("run %q: %w", strings.Join(append([]string{binary}, args...), " "), err)
}

func repoGitEnv(overrides ...string) []string {
	env := scrubbedGitEnv(os.Environ())
	return append(env, overrides...)
}

func scrubbedGitEnv(env []string) []string {
	scrubbed := make([]string, 0, len(env))
	for _, item := range env {
		if strings.HasPrefix(item, "GIT_") {
			continue
		}
		scrubbed = append(scrubbed, item)
	}
	return scrubbed
}

func (h *Harness) preflightRepoRoot(ctx context.Context) error {
	info, err := os.Stat(h.repoRoot)
	if err != nil {
		return fmt.Errorf("preflight repo root %q: %w", h.repoRoot, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("preflight repo root %q: not a directory", h.repoRoot)
	}

	headOut, headStderr, headExit, headErr := runCommand(ctx, h.repoRoot, repoGitEnv(), "git", "rev-parse", "HEAD")
	if headErr != nil {
		return fmt.Errorf("preflight git rev-parse HEAD: %w", headErr)
	}
	if headExit != 0 {
		return fmt.Errorf("preflight git rev-parse HEAD: exit=%d stderr=%s", headExit, strings.TrimSpace(headStderr))
	}
	if strings.TrimSpace(headOut) == "" {
		return fmt.Errorf("preflight git rev-parse HEAD: empty HEAD")
	}

	topLevelOut, topLevelStderr, topLevelExit, topLevelErr := runCommand(ctx, h.repoRoot, repoGitEnv(), "git", "rev-parse", "--show-toplevel")
	if topLevelErr != nil {
		return fmt.Errorf("preflight git rev-parse --show-toplevel: %w", topLevelErr)
	}
	if topLevelExit != 0 {
		return fmt.Errorf("preflight git rev-parse --show-toplevel: exit=%d stderr=%s", topLevelExit, strings.TrimSpace(topLevelStderr))
	}
	absRepoRoot, err := filepath.Abs(h.repoRoot)
	if err != nil {
		return fmt.Errorf("preflight repo root abs path %q: %w", h.repoRoot, err)
	}
	resolvedRepoRoot, err := resolveExistingPath(absRepoRoot)
	if err != nil {
		return fmt.Errorf("preflight resolve repo root %q: %w", absRepoRoot, err)
	}
	resolvedTopLevel, err := resolveExistingPath(strings.TrimSpace(topLevelOut))
	if err != nil {
		return fmt.Errorf("preflight resolve git worktree top-level %q: %w", strings.TrimSpace(topLevelOut), err)
	}
	if filepath.Clean(resolvedRepoRoot) != filepath.Clean(resolvedTopLevel) {
		return fmt.Errorf("preflight repo root %q is not git worktree top-level %q", resolvedRepoRoot, resolvedTopLevel)
	}

	statusOut, statusStderr, statusExit, statusErr := runCommand(ctx, h.repoRoot, repoGitEnv(), "git", "status", "--porcelain", "--untracked-files=all")
	if statusErr != nil {
		return fmt.Errorf("preflight git status: %w", statusErr)
	}
	if statusExit != 0 {
		return fmt.Errorf("preflight git status: exit=%d stderr=%s", statusExit, strings.TrimSpace(statusStderr))
	}
	if strings.TrimSpace(statusOut) != "" {
		return fmt.Errorf("preflight checkout not clean:\n%s", strings.TrimSpace(statusOut))
	}
	return nil
}

func (h *Harness) cleanupRepoRoot(ctx context.Context, repoRoot string, overlayState map[string]repoRootOverlayState) error {
	if _, stderr, exitCode, err := runCommand(ctx, repoRoot, repoGitEnv(), "git", "reset", "--hard", "HEAD"); err != nil {
		return fmt.Errorf("git reset --hard HEAD: %w", err)
	} else if exitCode != 0 {
		return fmt.Errorf("git reset --hard HEAD: exit=%d stderr=%s", exitCode, strings.TrimSpace(stderr))
	}

	if _, stderr, exitCode, err := runCommand(ctx, repoRoot, repoGitEnv(), "git", "clean", "-ffdx", "-e", "eval-results*"); err != nil {
		return fmt.Errorf("git clean -ffdx: %w", err)
	} else if exitCode != 0 {
		return fmt.Errorf("git clean -ffdx: exit=%d stderr=%s", exitCode, strings.TrimSpace(stderr))
	}

	if err := restoreRepoRootOverlayState(repoRoot, overlayState, "AGENTS.md", "CLAUDE.md"); err != nil {
		return err
	}
	statusOut, statusStderr, statusExit, statusErr := runCommand(ctx, repoRoot, repoGitEnv(), "git", "status", "--porcelain", "--untracked-files=all")
	if statusErr != nil {
		return fmt.Errorf("verify cleaned repo root: %w", statusErr)
	}
	if statusExit != 0 {
		return fmt.Errorf("verify cleaned repo root: exit=%d stderr=%s", statusExit, strings.TrimSpace(statusStderr))
	}
	if strings.TrimSpace(statusOut) != "" {
		return fmt.Errorf("verify cleaned repo root left dirty:\n%s", strings.TrimSpace(statusOut))
	}
	return nil
}

func captureGitDiffArtifacts(ctx context.Context, repoRoot, gitIndexPath string, artifacts *RunArtifacts) error {
	env := repoGitEnv("GIT_INDEX_FILE=" + gitIndexPath)
	if err := intentUntrackedPathsForDiff(ctx, repoRoot, env); err != nil {
		return err
	}
	diffOut, _, diffExit, diffErr := runCommand(ctx, repoRoot, env, "git", "diff", "--binary", "--no-renames", "HEAD")
	if diffErr != nil {
		return fmt.Errorf("capture git diff: %w", diffErr)
	}
	if diffExit != 0 {
		return fmt.Errorf("capture git diff exit code %d", diffExit)
	}
	artifacts.GitDiff = diffOut

	nameStatusOut, _, nameStatusExit, nameStatusErr := runCommand(ctx, repoRoot, env, "git", "diff", "--no-renames", "--name-status", "HEAD")
	if nameStatusErr != nil {
		return fmt.Errorf("capture git name-status: %w", nameStatusErr)
	}
	if nameStatusExit != 0 {
		return fmt.Errorf("capture git name-status exit code %d", nameStatusExit)
	}
	artifacts.GitNameStatus = nameStatusOut

	numstatOut, _, numstatExit, numstatErr := runCommand(ctx, repoRoot, env, "git", "diff", "--no-renames", "--numstat", "HEAD")
	if numstatErr != nil {
		return fmt.Errorf("capture git numstat: %w", numstatErr)
	}
	if numstatExit != 0 {
		return fmt.Errorf("capture git numstat exit code %d", numstatExit)
	}
	artifacts.GitNumstat = numstatOut
	return nil
}

func intentUntrackedPathsForDiff(ctx context.Context, repoRoot string, env []string) error {
	_, err := intentUntrackedPathsForDiffWithLimit(ctx, repoRoot, env, gitAddIntentBatchByteLimit)
	return err
}

func intentUntrackedPathsForDiffWithLimit(ctx context.Context, repoRoot string, env []string, maxBatchBytes int) (int, error) {
	if maxBatchBytes <= 0 {
		maxBatchBytes = gitAddIntentBatchByteLimit
	}

	out, stderr, exitCode, err := runCommand(ctx, repoRoot, env, "git", "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return 0, fmt.Errorf("list untracked paths for diff: %w", err)
	}
	if exitCode != 0 {
		return 0, fmt.Errorf("list untracked paths for diff: exit=%d stderr=%s", exitCode, strings.TrimSpace(stderr))
	}

	rawPaths := bytes.Split([]byte(out), []byte{0})
	addPaths := make([]string, 0, len(rawPaths))
	for _, rawPath := range rawPaths {
		if len(rawPath) == 0 {
			continue
		}
		relPath := string(rawPath)
		if isNestedGitRepoPath(repoRoot, relPath) {
			continue
		}
		addPaths = append(addPaths, relPath)
	}
	if len(addPaths) == 0 {
		return 0, nil
	}

	batches := 0
	batchPaths := make([]string, 0, len(addPaths))
	batchBytes := 0
	flushBatch := func() error {
		if len(batchPaths) == 0 {
			return nil
		}
		addArgs := append([]string{"add", "-N", "--"}, batchPaths...)
		if _, stderr, exitCode, err := runCommand(ctx, repoRoot, env, "git", addArgs...); err != nil {
			return fmt.Errorf("intent add untracked paths for diff: %w", err)
		} else if exitCode != 0 {
			return fmt.Errorf("intent add untracked paths for diff: exit=%d stderr=%s", exitCode, strings.TrimSpace(stderr))
		}
		batches++
		batchPaths = batchPaths[:0]
		batchBytes = 0
		return nil
	}
	for _, relPath := range addPaths {
		pathBytes := len(relPath) + 1
		if len(batchPaths) > 0 && batchBytes+pathBytes > maxBatchBytes {
			if err := flushBatch(); err != nil {
				return batches, err
			}
		}
		batchPaths = append(batchPaths, relPath)
		batchBytes += pathBytes
	}
	if err := flushBatch(); err != nil {
		return batches, err
	}
	return batches, nil
}

func isNestedGitRepoPath(repoRoot, relPath string) bool {
	info, err := os.Stat(filepath.Join(repoRoot, relPath))
	if err != nil || !info.IsDir() {
		return false
	}
	_, err = os.Stat(filepath.Join(repoRoot, relPath, ".git"))
	return err == nil
}

func appendEnvFromHostIfSet(env []string, key string) []string {
	value, ok := os.LookupEnv(key)
	if !ok || value == "" {
		return env
	}
	return append(env, key+"="+value)
}

func repoHasClnkuSourceTree(repoRoot string) bool {
	if strings.TrimSpace(repoRoot) == "" {
		return false
	}
	info, err := os.Stat(filepath.Join(repoRoot, "cmd", "clnku"))
	return err == nil && info.IsDir()
}

func captureRepoRootOverlayState(repoRoot string, relPaths ...string) (map[string]repoRootOverlayState, error) {
	absRepoRoot, err := filepath.Abs(repoRoot)
	if err != nil {
		return nil, fmt.Errorf("abs repo root %q: %w", repoRoot, err)
	}
	overlayState := make(map[string]repoRootOverlayState, len(relPaths))
	for _, relPath := range relPaths {
		state, err := captureRepoRootPathState(absRepoRoot, filepath.Join(absRepoRoot, relPath), map[string]bool{})
		if err != nil {
			return nil, err
		}
		if state.Present {
			overlayState[relPath] = state
		}
	}
	return overlayState, nil
}

func restoreRepoRootOverlayState(repoRoot string, overlayState map[string]repoRootOverlayState, relPaths ...string) error {
	absRepoRoot, err := filepath.Abs(repoRoot)
	if err != nil {
		return fmt.Errorf("abs repo root %q: %w", repoRoot, err)
	}
	for _, relPath := range relPaths {
		state, ok := overlayState[relPath]
		if !ok || !state.Present {
			fullPath := filepath.Join(absRepoRoot, relPath)
			if err := os.Remove(fullPath); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("remove overlay %q: %w", fullPath, err)
			}
			continue
		}
		fullPath := filepath.Join(absRepoRoot, relPath)
		if err := restoreRepoRootPathState(absRepoRoot, fullPath, state); err != nil {
			return err
		}
	}
	return nil
}

func captureRepoRootPathState(absRepoRoot, fullPath string, visited map[string]bool) (repoRootOverlayState, error) {
	absFullPath, err := filepath.Abs(fullPath)
	if err != nil {
		return repoRootOverlayState{}, fmt.Errorf("abs overlay %q: %w", fullPath, err)
	}
	if visited[absFullPath] {
		return repoRootOverlayState{}, nil
	}
	visited[absFullPath] = true

	info, err := os.Lstat(absFullPath)
	if err != nil {
		if os.IsNotExist(err) {
			return repoRootOverlayState{}, nil
		}
		return repoRootOverlayState{}, fmt.Errorf("stat overlay %q: %w", absFullPath, err)
	}

	state := repoRootOverlayState{Present: true}
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(absFullPath)
		if err != nil {
			return repoRootOverlayState{}, fmt.Errorf("read symlink overlay %q: %w", absFullPath, err)
		}
		state.IsSymlink = true
		state.SymlinkTarget = target

		targetPath, ok, err := resolveRepoRootSymlinkTargetPath(absRepoRoot, absFullPath, target)
		if err != nil {
			return repoRootOverlayState{}, err
		}
		if ok {
			targetState, err := captureRepoRootPathState(absRepoRoot, targetPath, visited)
			if err != nil {
				return repoRootOverlayState{}, err
			}
			if targetState.Present {
				state.SymlinkTargetState = &targetState
			}
		}
		return state, nil
	}

	if info.IsDir() {
		return repoRootOverlayState{}, fmt.Errorf("overlay %q is a directory, want file or symlink", absFullPath)
	}
	content, err := os.ReadFile(absFullPath)
	if err != nil {
		return repoRootOverlayState{}, fmt.Errorf("read overlay %q: %w", absFullPath, err)
	}
	state.Content = content
	state.Mode = info.Mode().Perm()
	return state, nil
}

func restoreRepoRootPathState(absRepoRoot, fullPath string, state repoRootOverlayState) error {
	if !state.Present {
		if err := os.Remove(fullPath); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove overlay %q: %w", fullPath, err)
		}
		return nil
	}
	if state.IsSymlink {
		if state.SymlinkTargetState != nil {
			targetPath, ok, err := resolveRepoRootSymlinkTargetPath(absRepoRoot, fullPath, state.SymlinkTarget)
			if err != nil {
				return err
			}
			if ok {
				if err := restoreRepoRootPathState(absRepoRoot, targetPath, *state.SymlinkTargetState); err != nil {
					return err
				}
			}
		}
		if err := ensureOverlayParentDir(fullPath); err != nil {
			return err
		}
		if err := os.Remove(fullPath); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove symlink overlay %q: %w", fullPath, err)
		}
		if err := os.Symlink(state.SymlinkTarget, fullPath); err != nil {
			return fmt.Errorf("restore symlink overlay %q: %w", fullPath, err)
		}
		return nil
	}
	if err := ensureOverlayParentDir(fullPath); err != nil {
		return err
	}
	if err := os.WriteFile(fullPath, state.Content, state.Mode); err != nil {
		return fmt.Errorf("restore overlay %q: %w", fullPath, err)
	}
	if err := os.Chmod(fullPath, state.Mode); err != nil {
		return fmt.Errorf("chmod overlay %q: %w", fullPath, err)
	}
	return nil
}

func resolveRepoRootSymlinkTargetPath(absRepoRoot, symlinkPath, target string) (string, bool, error) {
	targetPath := target
	if !filepath.IsAbs(targetPath) {
		targetPath = filepath.Join(filepath.Dir(symlinkPath), targetPath)
	}
	absTargetPath, err := filepath.Abs(targetPath)
	if err != nil {
		return "", false, fmt.Errorf("abs symlink target %q: %w", targetPath, err)
	}
	rel, err := filepath.Rel(absRepoRoot, absTargetPath)
	if err != nil {
		return "", false, fmt.Errorf("rel symlink target %q from %q: %w", absTargetPath, absRepoRoot, err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", false, nil
	}
	return absTargetPath, true, nil
}

func ensureOverlayParentDir(fullPath string) error {
	dir := filepath.Dir(fullPath)
	if dir == "." {
		return nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create overlay parent dir %q: %w", dir, err)
	}
	return nil
}

func repoGitIndexPath(ctx context.Context, repoRoot string) (string, error) {
	out, stderr, exitCode, err := runCommand(ctx, repoRoot, repoGitEnv(), "git", "rev-parse", "--absolute-git-dir")
	if err != nil {
		return "", fmt.Errorf("resolve git dir: %w", err)
	}
	if exitCode != 0 {
		return "", fmt.Errorf("resolve git dir: exit=%d stderr=%s", exitCode, strings.TrimSpace(stderr))
	}
	gitDir := strings.TrimSpace(out)
	if gitDir == "" {
		return "", fmt.Errorf("resolve git dir: empty output")
	}
	return filepath.Join(gitDir, "index"), nil
}

func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return fmt.Errorf("read %q: %w", src, err)
	}
	if err := os.WriteFile(dst, data, 0o600); err != nil {
		return fmt.Errorf("write %q: %w", dst, err)
	}
	return nil
}

func createTempFilePath(dir, pattern string) (string, error) {
	file, err := os.CreateTemp(dir, pattern)
	if err != nil {
		return "", fmt.Errorf("create temp file %q in %q: %w", pattern, dir, err)
	}
	path := file.Name()
	if err := file.Close(); err != nil {
		return "", fmt.Errorf("close temp file %q: %w", path, err)
	}
	return path, nil
}

func writeSeedMessages(dstRoot, srcPath, tempRoot, workspaceDir, homeDir, configDir, stateDir string) (string, error) {
	data, err := os.ReadFile(srcPath)
	if err != nil {
		return "", fmt.Errorf("read seed transcript %q: %w", srcPath, err)
	}

	replacer := strings.NewReplacer(
		"__TMP__", tempRoot,
		"__WORKDIR__", workspaceDir,
		"__HOME__", homeDir,
		"__CONFIG__", configDir,
		"__STATE__", stateDir,
	)

	seedPath := filepath.Join(dstRoot, "seed-messages.json")
	if err := os.WriteFile(seedPath, []byte(replacer.Replace(string(data))), 0o644); err != nil {
		return "", fmt.Errorf("write seed transcript %q: %w", seedPath, err)
	}
	return seedPath, nil
}

func loadTurns(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read model turns %q: %w", path, err)
	}

	var turns []string
	if err := json.Unmarshal(data, &turns); err != nil {
		return nil, fmt.Errorf("parse model turns %q: %w", path, err)
	}
	return turns, nil
}

func copyTreeOptional(src, dst string) error {
	if _, err := os.Stat(src); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("stat %q: %w", src, err)
	}
	return copyTree(src, dst)
}

func copyTree(src, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		return fmt.Errorf("stat %q: %w", src, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("%q is not a directory", src)
	}

	if err := filepath.WalkDir(src, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	}); err != nil {
		return fmt.Errorf("copy tree %q -> %q: %w", src, dst, err)
	}
	return nil
}

func copyProjectAgents(srcDir, workspaceDir string) error {
	src := filepath.Join(srcDir, "AGENTS.md")
	if _, err := os.Stat(src); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("stat %q: %w", src, err)
	}

	data, err := os.ReadFile(src)
	if err != nil {
		return fmt.Errorf("read %q: %w", src, err)
	}
	if err := os.MkdirAll(workspaceDir, 0o755); err != nil {
		return fmt.Errorf("mkdir %q: %w", workspaceDir, err)
	}
	if err := os.WriteFile(filepath.Join(workspaceDir, "AGENTS.md"), data, 0o644); err != nil {
		return fmt.Errorf("write workspace AGENTS.md: %w", err)
	}
	return nil
}

func collectProviderResponses(requests []CapturedRequest) []string {
	responses := make([]string, 0, len(requests))
	for _, request := range requests {
		if request.RawResponse == "" {
			continue
		}
		responses = append(responses, request.RawResponse)
	}
	return responses
}

func suiteTaskIndex(suite Suite, taskID string) int {
	for i, id := range suite.Tasks {
		if id == taskID {
			return i
		}
	}
	return 0
}

func (artifacts RunArtifacts) normalizationRoots() normalizationRoots {
	return normalizationRoots{
		Workdir: artifacts.WorkspaceRoot,
		Home:    artifacts.HomeDir,
		Config:  artifacts.ConfigDir,
		State:   artifacts.StateDir,
		Temp:    artifacts.TempDir,
	}
}
