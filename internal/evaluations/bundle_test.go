package evaluations

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/clnkr-ai/clankerval/internal/protocol"
	"github.com/clnkr-ai/clankerval/internal/transcript"
)

func TestNormalizePath(t *testing.T) {
	t.Parallel()

	tempRoot := t.TempDir()
	workspaceDir := filepath.Join(tempRoot, "workspace")
	homeDir := filepath.Join(tempRoot, "home")
	configDir := filepath.Join(tempRoot, "config")
	stateDir := filepath.Join(tempRoot, "state")
	configAppDir := filepath.Join(configDir, "clnkr")

	for _, dir := range []string{workspaceDir, homeDir, configAppDir, stateDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("MkdirAll(%q): %v", dir, err)
		}
	}

	configFile := filepath.Join(configAppDir, "prefs.json")
	if err := os.WriteFile(configFile, []byte("{}"), 0o644); err != nil {
		t.Fatalf("WriteFile(%q): %v", configFile, err)
	}

	workspaceFile := filepath.Join(workspaceDir, "note.txt")
	if err := os.WriteFile(workspaceFile, []byte("hello\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(%q): %v", workspaceFile, err)
	}

	roots := normalizationRoots{
		Workdir: workspaceDir,
		Home:    homeDir,
		Config:  configDir,
		State:   stateDir,
		Temp:    tempRoot,
	}

	if got := normalizePath(configFile, roots); got != filepath.ToSlash("<CONFIG>/clnkr/prefs.json") {
		t.Fatalf("normalizePath(config/clnkr) = %q, want %q", got, "<CONFIG>/clnkr/prefs.json")
	}

	symlinkRoot := filepath.Join(t.TempDir(), "tmp-link")
	if err := os.Symlink(tempRoot, symlinkRoot); err != nil {
		t.Fatalf("Symlink(%q -> %q): %v", symlinkRoot, tempRoot, err)
	}
	symlinkedWorkspaceFile := filepath.Join(symlinkRoot, "workspace", "note.txt")
	if got := normalizePath(symlinkedWorkspaceFile, roots); got != filepath.ToSlash("<WORKDIR>/note.txt") {
		t.Fatalf("normalizePath(symlinked workspace) = %q, want %q", got, "<WORKDIR>/note.txt")
	}
}

func TestNormalizeTranscript(t *testing.T) {
	t.Parallel()

	artifacts := sampleRunArtifacts(t)

	records, err := NormalizeTranscript(artifacts)
	if err != nil {
		t.Fatalf("NormalizeTranscript(): %v", err)
	}

	var gotKinds []string
	for _, record := range records {
		gotKinds = append(gotKinds, record.Kind)
	}
	wantKinds := []string{
		"system_prompt",
		"user_instruction",
		"assistant_turn",
		"command_start",
		"command_result",
		"state_update",
		"clarification",
		"completion",
	}
	if !reflect.DeepEqual(gotKinds, wantKinds) {
		t.Fatalf("record kinds = %#v, want %#v", gotKinds, wantKinds)
	}

	if records[2].TurnType != "act" {
		t.Fatalf("assistant turn type = %q, want act", records[2].TurnType)
	}
	if records[3].Command != "printf 'hello\\n' > <WORKDIR>/note.txt" {
		t.Fatalf("command_start command = %q", records[3].Command)
	}
	if records[3].Cwd != "<WORKDIR>" {
		t.Fatalf("command_start cwd = %q, want <WORKDIR>", records[3].Cwd)
	}
	if records[4].ExitCode != 0 {
		t.Fatalf("command_result exit_code = %d, want 0", records[4].ExitCode)
	}
	if records[5].Cwd != "<WORKDIR>" {
		t.Fatalf("state_update cwd = %q, want <WORKDIR>", records[5].Cwd)
	}
	if records[6].TurnType != "clarify" {
		t.Fatalf("clarification turn type = %q, want clarify", records[6].TurnType)
	}
	if records[7].TurnType != "done" {
		t.Fatalf("completion turn type = %q, want done", records[7].TurnType)
	}
}

func TestNormalizeTranscriptFromGenericEvents(t *testing.T) {
	t.Parallel()

	tempRoot := t.TempDir()
	workspaceDir := filepath.Join(tempRoot, "workspace")
	homeDir := filepath.Join(tempRoot, "home")
	configDir := filepath.Join(tempRoot, "config")
	stateDir := filepath.Join(tempRoot, "state")

	for _, dir := range []string{workspaceDir, homeDir, filepath.Join(configDir, "clnkr"), stateDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("MkdirAll(%q): %v", dir, err)
		}
	}

	artifacts := RunArtifacts{
		// Trajectory and EventLog are intentionally empty — this test
		// proves normalization works from TranscriptEvents + Commands
		// without clnku-specific source-format parsing.
		Trajectory: "",
		EventLog:   "",
		TranscriptEvents: []TranscriptEvent{
			{Index: 0, Kind: "system_prompt", Role: "system", Content: "System prompt at " + workspaceDir},
			{Index: 1, Kind: "user_instruction", Role: "user", Content: "Create a file in " + workspaceDir},
			{Index: 2, Kind: "assistant_turn", Role: "assistant", TurnType: "act", Content: "I will create the file in " + workspaceDir},
			{Index: 3, Kind: "command_result", Role: "user", Content: "command completed"},
			{Index: 4, Kind: "state_update", Role: "user", Cwd: workspaceDir},
			{Index: 5, Kind: "clarification", Role: "assistant", TurnType: "clarify", Content: "Need anything else in " + homeDir + "?"},
			{Index: 6, Kind: "completion", Role: "assistant", TurnType: "done", Content: "Created file in " + workspaceDir},
		},
		Commands: []CommandRecord{
			{
				Command:  "printf 'hello\\n' > " + filepath.ToSlash(filepath.Join(workspaceDir, "note.txt")),
				Dir:      workspaceDir,
				Stdout:   "",
				Stderr:   "",
				ExitCode: 0,
			},
		},
		WorkspaceRoot: workspaceDir,
		HomeDir:       homeDir,
		ConfigDir:     configDir,
		StateDir:      stateDir,
		TempDir:       tempRoot,
		ExitCode:      0,
	}

	records, err := NormalizeTranscript(artifacts)
	if err != nil {
		t.Fatalf("NormalizeTranscript(): %v", err)
	}

	var gotKinds []string
	for _, record := range records {
		gotKinds = append(gotKinds, record.Kind)
	}
	wantKinds := []string{
		"system_prompt",
		"user_instruction",
		"assistant_turn",
		"command_start",
		"command_result",
		"state_update",
		"clarification",
		"completion",
	}
	if !reflect.DeepEqual(gotKinds, wantKinds) {
		t.Fatalf("record kinds = %#v, want %#v", gotKinds, wantKinds)
	}

	// command_start should be synthesized from Commands, with path normalization.
	if records[3].Kind != "command_start" {
		t.Fatalf("records[3].Kind = %q, want command_start", records[3].Kind)
	}
	if records[3].Command != "printf 'hello\\n' > <WORKDIR>/note.txt" {
		t.Fatalf("command_start command = %q", records[3].Command)
	}
	if records[3].Cwd != "<WORKDIR>" {
		t.Fatalf("command_start cwd = %q, want <WORKDIR>", records[3].Cwd)
	}

	// command_result should use structured data from Commands.
	if records[4].ExitCode != 0 {
		t.Fatalf("command_result exit_code = %d, want 0", records[4].ExitCode)
	}

	// state_update cwd should be normalized.
	if records[5].Cwd != "<WORKDIR>" {
		t.Fatalf("state_update cwd = %q, want <WORKDIR>", records[5].Cwd)
	}

	// clarification and completion should have normalized content.
	if records[6].TurnType != "clarify" {
		t.Fatalf("clarification turn type = %q, want clarify", records[6].TurnType)
	}
	if !strings.Contains(records[6].Content, "<HOME>") {
		t.Fatalf("clarification content = %q, want <HOME> placeholder", records[6].Content)
	}
	if records[7].TurnType != "done" {
		t.Fatalf("completion turn type = %q, want done", records[7].TurnType)
	}
	if !strings.Contains(records[7].Content, "<WORKDIR>") {
		t.Fatalf("completion content = %q, want <WORKDIR> placeholder", records[7].Content)
	}
}

func TestNormalizeOutcome(t *testing.T) {
	t.Parallel()

	artifacts := sampleRunArtifacts(t)

	outcome, err := NormalizeOutcome(artifacts)
	if err != nil {
		t.Fatalf("NormalizeOutcome(): %v", err)
	}

	if outcome.FinalExitCode != 0 {
		t.Fatalf("FinalExitCode = %d, want 0", outcome.FinalExitCode)
	}
	if outcome.FinalCwd != "<WORKDIR>" {
		t.Fatalf("FinalCwd = %q, want <WORKDIR>", outcome.FinalCwd)
	}
	if !outcome.DiffPresent {
		t.Fatal("DiffPresent = false, want true")
	}
	if !reflect.DeepEqual(outcome.ChangedPaths, []string{"note.txt"}) {
		t.Fatalf("ChangedPaths = %#v, want [note.txt]", outcome.ChangedPaths)
	}
	if outcome.ChangedFileCount != 1 {
		t.Fatalf("ChangedFileCount = %d, want 1", outcome.ChangedFileCount)
	}
}

func TestWriteTrialBundle(t *testing.T) {
	t.Parallel()

	artifacts := sampleRunArtifacts(t)
	root := filepath.Join(t.TempDir(), artifacts.TrialID)

	bundle, err := WriteTrialBundle(root, artifacts, nil)
	if err != nil {
		t.Fatalf("WriteTrialBundle(): %v", err)
	}

	if bundle.Root != root {
		t.Fatalf("bundle root = %q, want %q", bundle.Root, root)
	}
	if bundle.SchemaVersion != "3" {
		t.Fatalf("bundle schema version = %q, want %q", bundle.SchemaVersion, "3")
	}
	if bundle.Agent.ID != string(AgentClnku) {
		t.Fatalf("bundle agent id = %q, want %q", bundle.Agent.ID, AgentClnku)
	}
	if bundle.Agent.Version != "0.1.0-test" {
		t.Fatalf("bundle agent version = %q, want %q", bundle.Agent.Version, "0.1.0-test")
	}
	if bundle.Provider.Model != artifacts.ProviderModel {
		t.Fatalf("bundle provider model = %q, want %q", bundle.Provider.Model, artifacts.ProviderModel)
	}

	// Schema v3: raw/agent/ dir and raw/commands.jsonl replace raw/transcript.json and raw/events.jsonl.
	for _, rel := range []string{
		"bundle.json",
		"raw/agent",
		"raw/commands.jsonl",
		"raw/provider-requests.jsonl",
		"raw/provider-responses.jsonl",
		"normalized/transcript.jsonl",
		"normalized/outcome.json",
		"normalized/graders.jsonl",
		"outcome/diff.patch",
		"outcome/name-status.txt",
		"outcome/numstat.txt",
	} {
		path := filepath.Join(root, filepath.FromSlash(rel))
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("Stat(%q): %v", path, err)
		}
	}
	if got, err := os.ReadFile(filepath.Join(root, "outcome", "diff.patch")); err != nil {
		t.Fatalf("ReadFile(outcome/diff.patch): %v", err)
	} else if string(got) != artifacts.GitDiff {
		t.Fatalf("outcome diff = %q, want %q", string(got), artifacts.GitDiff)
	}
	if got, err := os.ReadFile(filepath.Join(root, "outcome", "name-status.txt")); err != nil {
		t.Fatalf("ReadFile(outcome/name-status.txt): %v", err)
	} else if string(got) != artifacts.GitNameStatus {
		t.Fatalf("outcome name-status = %q, want %q", string(got), artifacts.GitNameStatus)
	}
	if got, err := os.ReadFile(filepath.Join(root, "outcome", "numstat.txt")); err != nil {
		t.Fatalf("ReadFile(outcome/numstat.txt): %v", err)
	} else if string(got) != artifacts.GitNumstat {
		t.Fatalf("outcome numstat = %q, want %q", string(got), artifacts.GitNumstat)
	}
	if bundle.Artifacts.OutcomeDiff != "outcome/diff.patch" {
		t.Fatalf("outcome diff path = %q, want outcome/diff.patch", bundle.Artifacts.OutcomeDiff)
	}
	if bundle.Artifacts.OutcomeNameStatus != "outcome/name-status.txt" {
		t.Fatalf("outcome name-status path = %q, want outcome/name-status.txt", bundle.Artifacts.OutcomeNameStatus)
	}
	if bundle.Artifacts.OutcomeNumstat != "outcome/numstat.txt" {
		t.Fatalf("outcome numstat path = %q, want outcome/numstat.txt", bundle.Artifacts.OutcomeNumstat)
	}

	// Schema v3: old raw artifacts must not exist.
	for _, rel := range []string{"raw/transcript.json", "raw/events.jsonl"} {
		path := filepath.Join(root, filepath.FromSlash(rel))
		if _, err := os.Stat(path); err == nil {
			t.Fatalf("old artifact %q still exists under schema v3", rel)
		}
	}

	graderData, err := os.ReadFile(filepath.Join(root, "normalized", "graders.jsonl"))
	if err != nil {
		t.Fatalf("ReadFile(graders.jsonl): %v", err)
	}
	if len(graderData) != 0 {
		t.Fatalf("graders.jsonl = %q, want empty file", string(graderData))
	}

	for _, rel := range []string{
		"normalized/transcript.jsonl",
		"normalized/outcome.json",
	} {
		if bundle.Checksums[rel] == "" {
			t.Fatalf("checksum for %q is empty", rel)
		}
	}

	rawRequests, err := os.ReadFile(filepath.Join(root, "raw", "provider-requests.jsonl"))
	if err != nil {
		t.Fatalf("ReadFile(provider-requests.jsonl): %v", err)
	}
	if got, want := string(rawRequests), artifacts.ProviderRequests[0].RawRequest+"\n"; got != want {
		t.Fatalf("raw provider requests = %q, want %q", got, want)
	}

	rawResponses, err := os.ReadFile(filepath.Join(root, "raw", "provider-responses.jsonl"))
	if err != nil {
		t.Fatalf("ReadFile(provider-responses.jsonl): %v", err)
	}
	if got, want := string(rawResponses), artifacts.ProviderResponses[0]+"\n"; got != want {
		t.Fatalf("raw provider responses = %q, want %q", got, want)
	}
}

func TestWriteTrialBundlePersistsTrialStatus(t *testing.T) {
	t.Parallel()

	artifacts := sampleRunArtifacts(t)
	artifacts.TrialPassed = true
	artifacts.FailedRequiredGraders = []GraderResult{
		{
			GraderID:   "outcome_diff",
			TargetKind: "outcome",
			Passed:     false,
			Message:    "missing diff",
		},
	}
	root := filepath.Join(t.TempDir(), artifacts.TrialID)

	written, err := WriteTrialBundle(root, artifacts, artifacts.FailedRequiredGraders)
	if err != nil {
		t.Fatalf("WriteTrialBundle(): %v", err)
	}
	if !written.TrialPassed {
		t.Fatal("written trial_passed = false, want true")
	}

	loaded, err := LoadBundle(root)
	if err != nil {
		t.Fatalf("LoadBundle(): %v", err)
	}
	if !loaded.TrialPassed {
		t.Fatal("loaded trial_passed = false, want true")
	}
	if len(loaded.FailedRequiredGraders) != 1 || loaded.FailedRequiredGraders[0].GraderID != "outcome_diff" {
		t.Fatalf("loaded failed required graders = %#v, want outcome failure", loaded.FailedRequiredGraders)
	}
}

func TestWriteTrialBundlePreservesRuntimeStyleProviderResponsesVerbatim(t *testing.T) {
	t.Parallel()

	artifacts := sampleRunArtifacts(t)
	artifacts.ProviderResponses = []string{
		"{\"id\":\"resp-1\"}\n",
		"{\"id\":\"resp-2\"}\n",
	}
	root := filepath.Join(t.TempDir(), artifacts.TrialID)

	if _, err := WriteTrialBundle(root, artifacts, nil); err != nil {
		t.Fatalf("WriteTrialBundle(): %v", err)
	}

	rawResponses, err := os.ReadFile(filepath.Join(root, "raw", "provider-responses.jsonl"))
	if err != nil {
		t.Fatalf("ReadFile(provider-responses.jsonl): %v", err)
	}
	want := "{\"id\":\"resp-1\"}\n{\"id\":\"resp-2\"}\n"
	if string(rawResponses) != want {
		t.Fatalf("raw provider responses = %q, want %q", string(rawResponses), want)
	}
}

func TestWriteTrialBundleCreatesEmptyOutcomeArtifactFiles(t *testing.T) {
	t.Parallel()

	artifacts := sampleRunArtifacts(t)
	artifacts.GitDiff = ""
	artifacts.GitNameStatus = ""
	artifacts.GitNumstat = ""
	root := filepath.Join(t.TempDir(), artifacts.TrialID)

	if _, err := WriteTrialBundle(root, artifacts, nil); err != nil {
		t.Fatalf("WriteTrialBundle(): %v", err)
	}

	for _, rel := range []string{"outcome/diff.patch", "outcome/name-status.txt", "outcome/numstat.txt"} {
		info, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatalf("Stat(%s): %v", rel, err)
		}
		if info.Size() != 0 {
			t.Fatalf("%s size = %d, want 0", rel, info.Size())
		}
	}
}

func TestLoadBundle(t *testing.T) {
	t.Parallel()

	artifacts := sampleRunArtifacts(t)
	root := filepath.Join(t.TempDir(), artifacts.TrialID)

	written, err := WriteTrialBundle(root, artifacts, nil)
	if err != nil {
		t.Fatalf("WriteTrialBundle(): %v", err)
	}

	loaded, err := LoadBundle(root)
	if err != nil {
		t.Fatalf("LoadBundle(): %v", err)
	}

	if loaded.TrialID != written.TrialID {
		t.Fatalf("loaded trial id = %q, want %q", loaded.TrialID, written.TrialID)
	}
	if loaded.Artifacts.RawAgentDir != "raw/agent" {
		t.Fatalf("raw agent dir = %q, want raw/agent", loaded.Artifacts.RawAgentDir)
	}
	if loaded.Artifacts.RawCommands != "raw/commands.jsonl" {
		t.Fatalf("raw commands = %q, want raw/commands.jsonl", loaded.Artifacts.RawCommands)
	}
	if loaded.Agent.ID != string(AgentClnku) {
		t.Fatalf("loaded agent id = %q, want %q", loaded.Agent.ID, AgentClnku)
	}

	normalizedTranscript, err := loaded.ReadNormalizedTranscript()
	if err != nil {
		t.Fatalf("ReadNormalizedTranscript(): %v", err)
	}
	if len(normalizedTranscript) != 8 {
		t.Fatalf("normalized transcript len = %d, want 8", len(normalizedTranscript))
	}

	outcome, err := loaded.ReadNormalizedOutcome()
	if err != nil {
		t.Fatalf("ReadNormalizedOutcome(): %v", err)
	}
	if outcome.FinalCwd != "<WORKDIR>" {
		t.Fatalf("loaded final cwd = %q, want <WORKDIR>", outcome.FinalCwd)
	}
	if !outcome.DiffPresent {
		t.Fatal("loaded diff_present = false, want true")
	}
	if !reflect.DeepEqual(outcome.ChangedPaths, []string{"note.txt"}) {
		t.Fatalf("loaded changed paths = %#v, want [note.txt]", outcome.ChangedPaths)
	}
	if outcome.ChangedFileCount != 1 {
		t.Fatalf("loaded changed file count = %d, want 1", outcome.ChangedFileCount)
	}

	graders, err := loaded.ReadGraders()
	if err != nil {
		t.Fatalf("ReadGraders(): %v", err)
	}
	if len(graders) != 0 {
		t.Fatalf("loaded graders len = %d, want 0", len(graders))
	}
}

func TestLoadBundleRejectsSymlinkEscape(t *testing.T) {
	t.Parallel()

	artifacts := sampleRunArtifacts(t)
	root := filepath.Join(t.TempDir(), artifacts.TrialID)
	if _, err := WriteTrialBundle(root, artifacts, nil); err != nil {
		t.Fatalf("WriteTrialBundle(): %v", err)
	}

	escapeTarget := filepath.Join(t.TempDir(), "escape.json")
	if err := os.WriteFile(escapeTarget, []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(%q): %v", escapeTarget, err)
	}
	artifactPath := filepath.Join(root, "normalized", "outcome.json")
	if err := os.Remove(artifactPath); err != nil {
		t.Fatalf("Remove(%q): %v", artifactPath, err)
	}
	if err := os.Symlink(escapeTarget, artifactPath); err != nil {
		t.Fatalf("Symlink(%q -> %q): %v", artifactPath, escapeTarget, err)
	}

	_, err := LoadBundle(root)
	if err == nil || !strings.Contains(err.Error(), "escapes bundle root") {
		t.Fatalf("LoadBundle() error = %v, want symlink escape rejection", err)
	}
}

func TestLoadBundleRejectsSymlinkEscapeWithMissingDescendants(t *testing.T) {
	t.Parallel()

	artifacts := sampleRunArtifacts(t)
	root := filepath.Join(t.TempDir(), artifacts.TrialID)
	if _, err := WriteTrialBundle(root, artifacts, nil); err != nil {
		t.Fatalf("WriteTrialBundle(): %v", err)
	}

	outsideRoot := filepath.Join(t.TempDir(), "outside")
	if err := os.MkdirAll(outsideRoot, 0o755); err != nil {
		t.Fatalf("MkdirAll(%q): %v", outsideRoot, err)
	}

	linkPath := filepath.Join(root, "normalized-link")
	if err := os.Symlink(outsideRoot, linkPath); err != nil {
		t.Fatalf("Symlink(%q -> %q): %v", linkPath, outsideRoot, err)
	}

	var bundleDoc map[string]any
	bundlePath := filepath.Join(root, "bundle.json")
	data, err := os.ReadFile(bundlePath)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", bundlePath, err)
	}
	if err := json.Unmarshal(data, &bundleDoc); err != nil {
		t.Fatalf("json.Unmarshal(bundle.json): %v", err)
	}
	artifactsDoc, ok := bundleDoc["artifacts"].(map[string]any)
	if !ok {
		t.Fatalf("bundle artifacts = %#v, want object", bundleDoc["artifacts"])
	}
	artifactsDoc["normalized_outcome"] = "normalized-link/missing/outcome.json"
	rewritten, err := json.MarshalIndent(bundleDoc, "", "  ")
	if err != nil {
		t.Fatalf("json.MarshalIndent(bundle.json): %v", err)
	}
	rewritten = append(rewritten, '\n')
	if err := os.WriteFile(bundlePath, rewritten, 0o644); err != nil {
		t.Fatalf("WriteFile(%q): %v", bundlePath, err)
	}

	_, err = LoadBundle(root)
	if err == nil || !strings.Contains(err.Error(), "escapes bundle root") {
		t.Fatalf("LoadBundle() error = %v, want symlink ancestor escape rejection", err)
	}
}

func TestLoadBundleRejectsMissingGitOutcomeArtifact(t *testing.T) {
	t.Parallel()

	artifacts := sampleRunArtifacts(t)
	root := filepath.Join(t.TempDir(), artifacts.TrialID)
	if _, err := WriteTrialBundle(root, artifacts, nil); err != nil {
		t.Fatalf("WriteTrialBundle(): %v", err)
	}

	missing := filepath.Join(root, "outcome", "name-status.txt")
	if err := os.Remove(missing); err != nil {
		t.Fatalf("Remove(%q): %v", missing, err)
	}

	_, err := LoadBundle(root)
	if err == nil || !strings.Contains(err.Error(), "outcome/name-status.txt") {
		t.Fatalf("LoadBundle() error = %v, want missing git outcome artifact rejection", err)
	}
}

func TestPreflightRepoRootAcceptsSymlinkedTopLevel(t *testing.T) {
	t.Parallel()

	repoRoot := t.TempDir()
	runGit := func(args ...string) {
		t.Helper()
		if _, stderr, exitCode, err := runCommand(context.Background(), repoRoot, repoGitEnv(), "git", args...); err != nil || exitCode != 0 {
			t.Fatalf("git %v: exit=%d err=%v stderr=%q", args, exitCode, err, stderr)
		}
	}

	runGit("init", "-q")
	runGit("config", "user.email", "test@example.com")
	runGit("config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(repoRoot, "tracked.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(tracked.txt): %v", err)
	}
	runGit("add", "tracked.txt")
	runGit("commit", "-q", "-m", "base")

	linkRoot := filepath.Join(t.TempDir(), "repo-link")
	if err := os.Symlink(repoRoot, linkRoot); err != nil {
		t.Fatalf("Symlink(%q -> %q): %v", linkRoot, repoRoot, err)
	}

	h := &Harness{repoRoot: linkRoot}
	if err := h.preflightRepoRoot(context.Background()); err != nil {
		t.Fatalf("preflightRepoRoot(): %v", err)
	}
}

func TestNormalizeOutcomeUnquotesGitPaths(t *testing.T) {
	t.Parallel()

	repoRoot := t.TempDir()
	runGit := func(args ...string) {
		t.Helper()
		if _, stderr, exitCode, err := runCommand(context.Background(), repoRoot, repoGitEnv(), "git", args...); err != nil || exitCode != 0 {
			t.Fatalf("git %v: exit=%d err=%v stderr=%q", args, exitCode, err, stderr)
		}
	}

	runGit("init", "-q")
	runGit("config", "user.email", "test@example.com")
	runGit("config", "user.name", "Test")
	runGit("config", "diff.renames", "true")
	if err := os.WriteFile(filepath.Join(repoRoot, "base.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(base.txt): %v", err)
	}
	runGit("add", "base.txt")
	runGit("commit", "-q", "-m", "base")
	tabPath := filepath.Join(repoRoot, "tab\tname.txt")
	if err := os.WriteFile(tabPath, []byte("hello\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(tab path): %v", err)
	}
	runGit("add", "-N", "--", filepath.Base(tabPath))

	indexPath, err := repoGitIndexPath(context.Background(), repoRoot)
	if err != nil {
		t.Fatalf("repoGitIndexPath(): %v", err)
	}

	artifacts := RunArtifacts{
		TranscriptEvents: []TranscriptEvent{{Kind: "state_update", Cwd: repoRoot}},
		ExitCode:         0,
	}
	if err := captureGitDiffArtifacts(context.Background(), repoRoot, indexPath, &artifacts); err != nil {
		t.Fatalf("captureGitDiffArtifacts(): %v", err)
	}

	outcome, err := NormalizeOutcome(artifacts)
	if err != nil {
		t.Fatalf("NormalizeOutcome(): %v", err)
	}
	wantPath := filepath.ToSlash("tab\tname.txt")
	if !reflect.DeepEqual(outcome.ChangedPaths, []string{wantPath}) {
		t.Fatalf("ChangedPaths = %#v, want [%q]", outcome.ChangedPaths, wantPath)
	}
	if outcome.ChangedFileCount != 1 {
		t.Fatalf("ChangedFileCount = %d, want 1", outcome.ChangedFileCount)
	}
}

func TestCaptureGitDiffArtifactsDisablesRenameDetection(t *testing.T) {
	t.Parallel()

	repoRoot := t.TempDir()
	runGit := func(args ...string) {
		t.Helper()
		if _, stderr, exitCode, err := runCommand(context.Background(), repoRoot, repoGitEnv(), "git", args...); err != nil || exitCode != 0 {
			t.Fatalf("git %v: exit=%d err=%v stderr=%q", args, exitCode, err, stderr)
		}
	}

	runGit("init", "-q")
	runGit("config", "user.email", "test@example.com")
	runGit("config", "user.name", "Test")
	runGit("config", "diff.renames", "true")
	if err := os.WriteFile(filepath.Join(repoRoot, "a.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(a.txt): %v", err)
	}
	runGit("add", "a.txt")
	runGit("commit", "-q", "-m", "base")
	runGit("mv", "a.txt", "b.txt")

	indexPath, err := repoGitIndexPath(context.Background(), repoRoot)
	if err != nil {
		t.Fatalf("repoGitIndexPath(): %v", err)
	}

	artifacts := RunArtifacts{
		TranscriptEvents: []TranscriptEvent{{Kind: "state_update", Cwd: repoRoot}},
		ExitCode:         0,
	}
	if err := captureGitDiffArtifacts(context.Background(), repoRoot, indexPath, &artifacts); err != nil {
		t.Fatalf("captureGitDiffArtifacts(): %v", err)
	}

	if strings.Contains(artifacts.GitNameStatus, "R100") || strings.Contains(artifacts.GitNumstat, "=>") {
		t.Fatalf("rename-aware diff leaked into captured artifacts: name-status=%q numstat=%q", artifacts.GitNameStatus, artifacts.GitNumstat)
	}

	outcome, err := NormalizeOutcome(artifacts)
	if err != nil {
		t.Fatalf("NormalizeOutcome(): %v", err)
	}
	if !outcome.DiffPresent {
		t.Fatal("DiffPresent = false, want true")
	}
	if !reflect.DeepEqual(outcome.ChangedPaths, []string{"a.txt", "b.txt"}) {
		t.Fatalf("ChangedPaths = %#v, want [a.txt b.txt]", outcome.ChangedPaths)
	}
	if outcome.ChangedFileCount != 2 {
		t.Fatalf("ChangedFileCount = %d, want 2", outcome.ChangedFileCount)
	}
}

func sampleRunArtifacts(t *testing.T) RunArtifacts {
	t.Helper()

	tempRoot := t.TempDir()
	workspaceDir := filepath.Join(tempRoot, "workspace")
	homeDir := filepath.Join(tempRoot, "home")
	configDir := filepath.Join(tempRoot, "config")
	stateDir := filepath.Join(tempRoot, "state")

	for _, dir := range []string{
		workspaceDir,
		homeDir,
		filepath.Join(configDir, "clnkr"),
		stateDir,
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("MkdirAll(%q): %v", dir, err)
		}
	}

	if err := os.WriteFile(filepath.Join(workspaceDir, "note.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(note.txt): %v", err)
	}

	messages := []protocol.Message{
		{Role: "system", Content: "System prompt rooted at " + workspaceDir},
		{Role: "user", Content: "Create " + filepath.Join(workspaceDir, "note.txt")},
		{Role: "assistant", Content: `{"type":"act","command":"printf 'hello\\n' > ` + filepath.ToSlash(filepath.Join(workspaceDir, "note.txt")) + `"}`},
		{Role: "user", Content: commandResultMessage(filepath.ToSlash(filepath.Join(workspaceDir, "note.txt")))},
		{Role: "user", Content: transcript.FormatStateMessage(workspaceDir)},
		{Role: "assistant", Content: `{"type":"clarify","question":"Need anything else in ` + filepath.ToSlash(homeDir) + `?"}`},
		{Role: "assistant", Content: `{"type":"done","summary":"Created ` + filepath.ToSlash(filepath.Join(workspaceDir, "note.txt")) + `"}`},
	}
	trajectoryBytes, err := json.Marshal(messages)
	if err != nil {
		t.Fatalf("json.Marshal(messages): %v", err)
	}

	command := "printf 'hello\\n' > " + filepath.ToSlash(filepath.Join(workspaceDir, "note.txt"))
	eventLog := jsonLine(t, map[string]any{
		"type": "command_start",
		"payload": map[string]any{
			"command": command,
			"dir":     workspaceDir,
		},
	}) + jsonLine(t, map[string]any{
		"type": "command_done",
		"payload": map[string]any{
			"command":   command,
			"stdout":    "",
			"stderr":    "",
			"exit_code": 0,
		},
	})

	// Build agent-neutral TranscriptEvents and Commands that normalization
	// and grading now consume instead of Trajectory/EventLog.
	transcriptEvents := []TranscriptEvent{
		{Index: 0, Kind: "system_prompt", Role: "system", Content: messages[0].Content},
		{Index: 1, Kind: "user_instruction", Role: "user", Content: messages[1].Content},
		{Index: 2, Kind: "assistant_turn", Role: "assistant", TurnType: "act", Content: messages[2].Content},
		{Index: 3, Kind: "command_result", Role: "user", Command: command, Content: messages[3].Content},
		{Index: 4, Kind: "state_update", Role: "user", Cwd: workspaceDir, Content: messages[4].Content},
		{Index: 5, Kind: "clarification", Role: "assistant", TurnType: "clarify", Content: "Need anything else in " + filepath.ToSlash(homeDir) + "?"},
		{Index: 6, Kind: "completion", Role: "assistant", TurnType: "done", Content: "Created " + filepath.ToSlash(filepath.Join(workspaceDir, "note.txt"))},
	}
	commands := []CommandRecord{
		{
			Command:  command,
			Dir:      workspaceDir,
			Stdout:   "",
			Stderr:   "",
			ExitCode: 0,
		},
	}

	startedAt := time.Date(2026, time.March, 31, 10, 0, 0, 0, time.UTC)
	finishedAt := startedAt.Add(2 * time.Second)

	return RunArtifacts{
		SuiteID:         "default",
		TaskID:          "001-basic-edit",
		TrialID:         "trial-123",
		SuiteTaskIndex:  0,
		TrialAttempt:    0,
		Mode:            ModeMockProvider,
		Agent:           AgentClnku,
		AgentVersion:    "0.1.0-test",
		AgentCommand:    []string{"clnku", "--eval"},
		ProviderModel:   "test-model",
		ProviderBaseURL: "http://127.0.0.1:9999",
		StartedAt:       startedAt,
		FinishedAt:      finishedAt,
		SystemPrompt:     messages[0].Content,
		Trajectory:       string(trajectoryBytes),
		EventLog:         eventLog,
		TranscriptEvents: transcriptEvents,
		Commands:         commands,
		ProviderRequests: []CapturedRequest{
			{
				Model:      "test-model",
				Messages:   append([]protocol.Message(nil), messages[:3]...),
				RawRequest: `{"model":"test-model"}`,
				RawResponse: `{"choices":[{"message":{"role":"assistant","content":"{\"type\":\"act\",\"command\":\"` +
					command +
					`\"}"}}]}`,
			},
		},
		ProviderResponses: []string{
			`{"choices":[{"message":{"role":"assistant","content":"{\"type\":\"act\",\"command\":\"` + command + `\"}"}}]}`,
		},
		GitDiff:       "diff --git a/note.txt b/note.txt\n",
		GitNameStatus: "M\tnote.txt\n",
		GitNumstat:    "1\t0\tnote.txt\n",
		WorkspaceRoot: workspaceDir,
		HomeDir:       homeDir,
		ConfigDir:     configDir,
		StateDir:      stateDir,
		TempDir:       tempRoot,
		ExitCode:      0,
	}
}

func commandResultMessage(workspaceFile string) string {
	return "[command]\nprintf 'hello\\n' > " + workspaceFile + "\n[/command]\n" +
		"[exit_code]\n0\n[/exit_code]\n" +
		"[stdout]\n\n[/stdout]\n" +
		"[stderr]\n\n[/stderr]"
}

func jsonLine(t *testing.T, value any) string {
	t.Helper()

	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("json.Marshal(%#v): %v", value, err)
	}
	return string(data) + "\n"
}
