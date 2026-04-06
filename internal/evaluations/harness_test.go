package evaluations

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/clnkr-ai/clankerval/internal/protocol"
	"github.com/clnkr-ai/clankerval/internal/transcript"
)

func TestLoadRunConfigFromEnv(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		env     map[string]string
		want    RunConfig
		wantErr string
	}{
		{
			name: "default mock-provider mode",
			env:  map[string]string{},
			want: RunConfig{
				Mode:   ModeMockProvider,
				APIKey: "dummy-key",
				Model:  "test-model",
			},
		},
		{
			name: "live-provider uses only evaluation env",
			env: map[string]string{
				"CLNKR_EVALUATION_MODE":     string(ModeLiveProvider),
				"CLNKR_EVALUATION_API_KEY":  "eval-key",
				"CLNKR_EVALUATION_BASE_URL": "https://eval.example/v1",
				"CLNKR_EVALUATION_MODEL":    "eval-model",
				"OPENAI_API_KEY":            "openai-key",
				"OPENAI_BASE_URL":           "https://openai.example/v1",
				"CLNKR_API_KEY":             "clnkr-key",
				"CLNKR_BASE_URL":            "https://clnkr.example/v1",
				"CLNKR_MODEL":               "ambient-model",
			},
			want: RunConfig{
				Mode:    ModeLiveProvider,
				APIKey:  "eval-key",
				BaseURL: "https://eval.example/v1",
				Model:   "eval-model",
			},
		},
		{
			name: "live-provider ignores openai fallback",
			env: map[string]string{
				"CLNKR_EVALUATION_MODE": string(ModeLiveProvider),
				"OPENAI_API_KEY":        "openai-key",
				"OPENAI_BASE_URL":       "https://openai.example/v1",
			},
			wantErr: "missing API key",
		},
		{
			name: "live-provider ignores ambient clnkr env",
			env: map[string]string{
				"CLNKR_EVALUATION_MODE": string(ModeLiveProvider),
				"CLNKR_API_KEY":         "ambient-key",
				"CLNKR_BASE_URL":        "https://ambient.example/v1",
				"CLNKR_MODEL":           "ambient-model",
			},
			wantErr: "missing API key",
		},
		{
			name: "live-provider defaults model",
			env: map[string]string{
				"CLNKR_EVALUATION_MODE":     string(ModeLiveProvider),
				"CLNKR_EVALUATION_API_KEY":  "eval-key",
				"CLNKR_EVALUATION_BASE_URL": "https://eval.example/v1",
			},
			want: RunConfig{
				Mode:    ModeLiveProvider,
				APIKey:  "eval-key",
				BaseURL: "https://eval.example/v1",
				Model:   "gpt-5.4-nano",
			},
		},
		{
			name: "invalid mode",
			env: map[string]string{
				"CLNKR_EVALUATION_MODE": "bogus",
			},
			wantErr: "unknown CLNKR_EVALUATION_MODE",
		},
		{
			name: "live-provider requires base url",
			env: map[string]string{
				"CLNKR_EVALUATION_MODE":    string(ModeLiveProvider),
				"CLNKR_EVALUATION_API_KEY": "eval-key",
			},
			wantErr: "missing base URL",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := LoadRunConfigFromEnv(func(key string) string {
				return tt.env[key]
			})
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %v, want substring %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("LoadRunConfigFromEnv(): %v", err)
			}
			if got != tt.want {
				t.Fatalf("config = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestMockProvider(t *testing.T) {
	t.Run("serves mock turns in order and captures requests", func(t *testing.T) {
		provider := NewMockProvider([]string{
			`{"type":"act","command":"pwd"}`,
			`{"type":"done","summary":"finished"}`,
		})
		defer provider.Close()

		firstRequestBody := `{"messages":[{"content":"system prompt","role":"system"},{"content":"first task","role":"user"}],"model":"mock-model"}`
		first := postChatCompletionBody(t, provider.URL(), firstRequestBody)
		if got := first.Choices[0].Message.Content; got != `{"type":"act","command":"pwd"}` {
			t.Fatalf("first response = %q, want mock turn", got)
		}

		secondRequestBody := `{"model":"mock-model","messages":[{"role":"system","content":"system prompt"},{"role":"user","content":"second task"}]}`
		secondResponse, secondBody := postChatCompletionRawBody(t, provider.URL(), secondRequestBody)
		if secondResponse.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want %d body=%q", secondResponse.StatusCode, http.StatusOK, secondBody)
		}
		var second chatCompletionResponse
		if err := json.Unmarshal([]byte(secondBody), &second); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if got := second.Choices[0].Message.Content; got != `{"type":"done","summary":"finished"}` {
			t.Fatalf("second response = %q, want mock turn", got)
		}

		requests := provider.Requests()
		if len(requests) != 2 {
			t.Fatalf("request count = %d, want 2", len(requests))
		}
		if requests[0].Model != "mock-model" {
			t.Fatalf("request model = %q, want mock-model", requests[0].Model)
		}
		if len(requests[0].Messages) != 2 {
			t.Fatalf("request messages = %#v, want two messages", requests[0].Messages)
		}
		if requests[0].Messages[1].Content != "first task" {
			t.Fatalf("request message content = %q, want first task", requests[0].Messages[1].Content)
		}
		if requests[0].RawRequest != firstRequestBody {
			t.Fatalf("raw request = %q, want %q", requests[0].RawRequest, firstRequestBody)
		}
		if requests[1].RawRequest != secondRequestBody {
			t.Fatalf("raw request = %q, want %q", requests[1].RawRequest, secondRequestBody)
		}
		if requests[1].RawResponse != secondBody {
			t.Fatalf("raw response = %q, want %q", requests[1].RawResponse, secondBody)
		}
	})

	t.Run("returns stable error when exhausted", func(t *testing.T) {
		provider := NewMockProvider([]string{`{"type":"done","summary":"only"}`})
		defer provider.Close()

		_ = postChatCompletion(t, provider.URL(), map[string]any{
			"model":    "mock-model",
			"messages": []map[string]string{{"role": "user", "content": "one"}},
		})

		resp, body := postChatCompletionRaw(t, provider.URL(), map[string]any{
			"model":    "mock-model",
			"messages": []map[string]string{{"role": "user", "content": "two"}},
		})
		if resp.StatusCode != http.StatusInternalServerError {
			t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusInternalServerError)
		}
		if !strings.Contains(body, "no more mock turns") {
			t.Fatalf("body = %q, want exhaustion error", body)
		}
		requests := provider.Requests()
		if got := requests[len(requests)-1].RawResponse; got != body {
			t.Fatalf("exhausted raw response = %q, want %q", got, body)
		}
	})

	t.Run("Requests returns a caller-safe copy", func(t *testing.T) {
		provider := NewMockProvider([]string{`{"type":"done","summary":"finished"}`})
		defer provider.Close()

		_ = postChatCompletion(t, provider.URL(), map[string]any{
			"model": "mock-model",
			"messages": []map[string]string{
				{"role": "user", "content": "original"},
			},
		})

		requests := provider.Requests()
		requests[0].Model = "mutated"
		requests[0].Messages[0].Content = "mutated"

		again := provider.Requests()
		if len(again) != 1 {
			t.Fatalf("request count after caller mutation = %d, want 1", len(again))
		}
		if again[0].Model != "mock-model" {
			t.Fatalf("stored model = %q, want mock-model", again[0].Model)
		}
		if again[0].Messages[0].Content != "original" {
			t.Fatalf("stored content = %q, want original", again[0].Messages[0].Content)
		}
	})
}

func TestRunTrial(t *testing.T) {
	t.Run("basic-edit", func(t *testing.T) {
		ctx := context.Background()
		repoRoot := newTempRepoRoot(t)
		cleanupGeneratedRunOutput(t, repoRoot)
		t.Cleanup(func() {
			cleanupGeneratedRunOutput(t, repoRoot)
		})
		harness := newHarnessForTests(t, ctx, repoRoot)

		suite, task := loadDefaultBasicEdit(t, repoRoot)
		artifacts, err := harness.RunTrial(ctx, suite, task, RunConfig{Mode: ModeMockProvider})
		if err != nil {
			t.Fatalf("RunTrial(): %v", err)
		}

		if artifacts.Mode != ModeMockProvider {
			t.Fatalf("mode = %q, want %q", artifacts.Mode, ModeMockProvider)
		}
		if artifacts.ExitCode != 0 {
			t.Fatalf("exit code = %d, want 0", artifacts.ExitCode)
		}
		if artifacts.SystemPrompt == "" {
			t.Fatal("system prompt is empty")
		}
		if !strings.Contains(artifacts.SystemPrompt, "Keep changes tight. Work in the current directory.") {
			t.Fatalf("system prompt missing project AGENTS instructions: %q", artifacts.SystemPrompt)
		}
		if artifacts.Trajectory == "" {
			t.Fatal("trajectory is empty")
		}
		if artifacts.EventLog == "" {
			t.Fatal("event log is empty")
		}
		if len(artifacts.ProviderRequests) == 0 {
			t.Fatal("provider requests = 0, want captured mock-provider requests")
		}
		if len(artifacts.ProviderResponses) == 0 {
			t.Fatal("provider responses = 0, want captured mock-provider responses")
		}
		if !artifacts.TrialPassed {
			t.Fatal("trial_passed = false, want true")
		}
		if len(artifacts.FailedRequiredGraders) != 0 {
			t.Fatalf("failed required graders = %#v, want empty", artifacts.FailedRequiredGraders)
		}
		if len(artifacts.GraderResults) != 2 {
			t.Fatalf("grader count = %d, want 2", len(artifacts.GraderResults))
		}
		entries, err := os.ReadDir(filepath.Join(repoRoot, "evaluations", "trials"))
		if err != nil && !os.IsNotExist(err) {
			t.Fatalf("ReadDir(trials): %v", err)
		}
		if err == nil && len(entries) != 0 {
			t.Fatalf("repo trial output entries = %d, want 0 before RunSuite persistence", len(entries))
		}
		assertScriptedParity(t, artifacts)

		wantWorkspace := map[string]string{
			"AGENTS.md": "Keep changes tight. Work in the current directory.\n",
			"note.txt":  "hello\n",
		}
		if !reflect.DeepEqual(artifacts.Workspace, wantWorkspace) {
			t.Fatalf("workspace = %#v, want %#v", artifacts.Workspace, wantWorkspace)
		}

		// clnkuAdapter must populate generic CommandRecord from event log.
		if len(artifacts.Commands) == 0 {
			t.Fatal("Commands is empty, want at least one CommandRecord from clnku event log")
		}
		if artifacts.Commands[0].Command != "printf 'hello\\n' > note.txt" {
			t.Fatalf("Commands[0].Command = %q, want printf command", artifacts.Commands[0].Command)
		}
		if artifacts.Commands[0].ExitCode != 0 {
			t.Fatalf("Commands[0].ExitCode = %d, want 0", artifacts.Commands[0].ExitCode)
		}
		if artifacts.Commands[0].Dir == "" {
			t.Fatal("Commands[0].Dir is empty, want workspace directory from command_start event")
		}

		// clnkuAdapter must populate generic TranscriptEvents from trajectory.
		if len(artifacts.TranscriptEvents) == 0 {
			t.Fatal("TranscriptEvents is empty, want adapted transcript events from clnku trajectory")
		}
		foundUserInstruction := false
		foundCommandEvent := false
		for _, ev := range artifacts.TranscriptEvents {
			if ev.Kind == "user_instruction" {
				foundUserInstruction = true
			}
			if ev.Kind == "command_result" {
				foundCommandEvent = true
			}
		}
		if !foundUserInstruction {
			t.Fatal("TranscriptEvents missing user_instruction event")
		}
		if !foundCommandEvent {
			t.Fatal("TranscriptEvents missing command_result event")
		}

		// clnkuAdapter must carry native raw artifacts for bundle writing.
		if len(artifacts.RawAgentArtifacts) == 0 {
			t.Fatal("RawAgentArtifacts is empty, want trajectory and event log as raw artifacts")
		}
		artifactNames := make(map[string]bool)
		for _, a := range artifacts.RawAgentArtifacts {
			artifactNames[a.Name] = true
		}
		if !artifactNames["trajectory.json"] {
			t.Fatal("RawAgentArtifacts missing trajectory.json")
		}
		if !artifactNames["events.jsonl"] {
			t.Fatal("RawAgentArtifacts missing events.jsonl")
		}
	})

	t.Run("optional transcript grader failure does not fail the trial", func(t *testing.T) {
		ctx := context.Background()
		repoRoot := newTempRepoRoot(t)
		harness := newHarnessForTests(t, ctx, repoRoot)

		suite, task := writeTempSuiteTask(t, repoRoot, "optional-transcript-fail", map[string]string{
			"input/instruction.txt": "Create note.txt in the repo root with the contents hello and then finish.\n",
			"input/model-turns.json": `[
  "{\"type\":\"act\",\"command\":\"printf 'hello\\\\n' > note.txt\"}",
  "{\"type\":\"done\",\"summary\":\"finished\"}"
]`,
			"input/project/AGENTS.md":      "Keep changes tight. Work in the current directory.\n",
			"expected/workspace/AGENTS.md": "Keep changes tight. Work in the current directory.\n",
			"expected/workspace/note.txt":  "hello\n",
			"task.json": `{
  "id": "optional-transcript-fail",
  "instruction_file": "input/instruction.txt",
  "scripted_turns_file": "input/model-turns.json",
  "working_directory": "workspace",
  "full_send": true,
  "step_limit": 10,
  "graders": {
    "outcome_workspace_snapshot": {
      "enabled": true,
      "required": true
    },
    "transcript_command_trace": {
      "enabled": true,
      "required": false,
      "expected_commands": [
        "pwd"
      ],
      "expected_exit_codes": [
        0
      ],
      "max_command_count": 5
    }
  }
}`,
		})

		artifacts, err := harness.RunTrial(ctx, suite, task, RunConfig{Mode: ModeMockProvider})
		if err != nil {
			t.Fatalf("RunTrial(): %v", err)
		}

		if !artifacts.TrialPassed {
			t.Fatal("trial_passed = false, want true")
		}
		graders := artifacts.GraderResults
		if len(graders) != 2 {
			t.Fatalf("grader count = %d, want 2", len(graders))
		}
		foundTranscriptFailure := false
		for _, grader := range graders {
			if grader.GraderID == "transcript_command_trace" {
				foundTranscriptFailure = !grader.Passed
			}
		}
		if !foundTranscriptFailure {
			t.Fatalf("grader records = %#v, want transcript failure", graders)
		}
	})

	t.Run("required transcript grader failure fails the trial", func(t *testing.T) {
		ctx := context.Background()
		repoRoot := newTempRepoRoot(t)
		harness := newHarnessForTests(t, ctx, repoRoot)

		suite, task := writeTempSuiteTask(t, repoRoot, "required-transcript-fail", map[string]string{
			"input/instruction.txt": "Create note.txt in the repo root with the contents hello and then finish.\n",
			"input/model-turns.json": `[
  "{\"type\":\"act\",\"command\":\"pwd\"}",
  "{\"type\":\"act\",\"command\":\"printf 'hello\\\\n' > note.txt\"}",
  "{\"type\":\"done\",\"summary\":\"finished\"}"
]`,
			"input/project/AGENTS.md":      "Keep changes tight. Work in the current directory.\n",
			"expected/workspace/AGENTS.md": "Keep changes tight. Work in the current directory.\n",
			"expected/workspace/note.txt":  "hello\n",
			"task.json": `{
  "id": "required-transcript-fail",
  "instruction_file": "input/instruction.txt",
  "scripted_turns_file": "input/model-turns.json",
  "working_directory": "workspace",
  "full_send": true,
  "step_limit": 10,
  "graders": {
    "outcome_workspace_snapshot": {
      "enabled": true,
      "required": true
    },
    "transcript_command_trace": {
      "enabled": true,
      "required": true,
      "expected_commands": [
        "printf 'hello\\n' > note.txt"
      ],
      "expected_exit_codes": [
        0
      ],
      "max_command_count": 5
    }
  }
}`,
		})

		artifacts, err := harness.RunTrial(ctx, suite, task, RunConfig{Mode: ModeMockProvider})
		if err != nil {
			t.Fatalf("RunTrial(): %v", err)
		}

		if artifacts.TrialPassed {
			t.Fatal("trial_passed = true, want false")
		}
		if len(artifacts.FailedRequiredGraders) != 1 || artifacts.FailedRequiredGraders[0].GraderID != "transcript_command_trace" {
			t.Fatalf("failed required graders = %#v, want transcript failure", artifacts.FailedRequiredGraders)
		}
		graders := artifacts.GraderResults
		foundTranscriptFailure := false
		for _, grader := range graders {
			if grader.GraderID == "transcript_command_trace" {
				foundTranscriptFailure = !grader.Passed && strings.Contains(grader.Message, "command count")
			}
		}
		if !foundTranscriptFailure {
			t.Fatalf("grader records = %#v, want required transcript command-count failure", graders)
		}
	})

	t.Run("required outcome grader failure fails the trial", func(t *testing.T) {
		ctx := context.Background()
		repoRoot := newTempRepoRoot(t)
		harness := newHarnessForTests(t, ctx, repoRoot)

		suite, task := writeTempSuiteTask(t, repoRoot, "required-outcome-fail", map[string]string{
			"input/instruction.txt": "Create note.txt in the repo root with the contents hello and then finish.\n",
			"input/model-turns.json": `[
  "{\"type\":\"act\",\"command\":\"printf 'hello\\\\n' > note.txt\"}",
  "{\"type\":\"done\",\"summary\":\"finished\"}"
]`,
			"input/project/AGENTS.md":      "Keep changes tight. Work in the current directory.\n",
			"expected/workspace/AGENTS.md": "Keep changes tight. Work in the current directory.\n",
			"expected/workspace/note.txt":  "wrong\n",
			"task.json": `{
  "id": "required-outcome-fail",
  "instruction_file": "input/instruction.txt",
  "scripted_turns_file": "input/model-turns.json",
  "working_directory": "workspace",
  "full_send": true,
  "step_limit": 10,
  "graders": {
    "outcome_workspace_snapshot": {
      "enabled": true,
      "required": true
    },
    "transcript_command_trace": {
      "enabled": false,
      "required": false
    }
  }
}`,
		})

		artifacts, err := harness.RunTrial(ctx, suite, task, RunConfig{Mode: ModeMockProvider})
		if err != nil {
			t.Fatalf("RunTrial(): %v", err)
		}

		if artifacts.TrialPassed {
			t.Fatal("trial_passed = true, want false")
		}
		if len(artifacts.FailedRequiredGraders) != 1 || artifacts.FailedRequiredGraders[0].GraderID != "outcome_workspace_snapshot" {
			t.Fatalf("failed required graders = %#v, want outcome failure", artifacts.FailedRequiredGraders)
		}
	})

	t.Run("live-provider uses configured endpoint and model", func(t *testing.T) {
		ctx := context.Background()
		repoRoot := newTempRepoRoot(t)
		harness := newHarnessForTests(t, ctx, repoRoot)

		var requests []CapturedRequest
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/chat/completions" {
				http.NotFound(w, r)
				return
			}
			var req chatCompletionRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}
			requests = append(requests, CapturedRequest{
				Model:    req.Model,
				Messages: append([]protocol.Message(nil), req.Messages...),
			})
			var content string
			switch len(requests) {
			case 1:
				content = `{"type":"act","command":"printf 'hello\\n' > note.txt"}`
			default:
				content = `{"type":"done","summary":"created note.txt"}`
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"choices": []map[string]any{
					{
						"message": map[string]string{
							"role":    "assistant",
							"content": content,
						},
					},
				},
				"usage": map[string]int{
					"prompt_tokens":     1,
					"completion_tokens": 1,
				},
			})
		}))
		defer server.Close()

		suite, task := loadDefaultBasicEdit(t, repoRoot)
		artifacts, err := harness.RunTrial(ctx, suite, task, RunConfig{
			Mode:    ModeLiveProvider,
			APIKey:  "live-key",
			BaseURL: server.URL,
			Model:   "live-model",
		})
		if err != nil {
			t.Fatalf("RunTrial(): %v", err)
		}

		if artifacts.Mode != ModeLiveProvider {
			t.Fatalf("mode = %q, want %q", artifacts.Mode, ModeLiveProvider)
		}
		if artifacts.ProviderModel != "live-model" {
			t.Fatalf("provider model = %q, want live-model", artifacts.ProviderModel)
		}
		if artifacts.ProviderBaseURL != server.URL {
			t.Fatalf("provider base URL = %q, want %q", artifacts.ProviderBaseURL, server.URL)
		}
		if len(requests) != 2 {
			t.Fatalf("request count = %d, want 2", len(requests))
		}
		for i, req := range requests {
			if req.Model != "live-model" {
				t.Fatalf("request %d model = %q, want live-model", i, req.Model)
			}
		}
	})

	t.Run("preserves prompt layering when home and config trees are present", func(t *testing.T) {
		ctx := context.Background()
		repoRoot := newTempRepoRoot(t)
		harness := newHarnessForTests(t, ctx, repoRoot)

		suite, task := writeTempSuiteTask(t, repoRoot, "layered-prompt", map[string]string{
			"input/instruction.txt":        "Create note.txt in the repo root with the contents hello and then finish.\n",
			"input/model-turns.json":       "[\"{\\\"type\\\":\\\"done\\\",\\\"summary\\\":\\\"finished\\\"}\"]\n",
			"input/project/AGENTS.md":      "project instructions\n",
			"input/home/AGENTS.md":         "home instructions\n",
			"input/config/clnkr/AGENTS.md": "config instructions\n",
			"expected/workspace/.gitkeep":  "",
			"input/workspace/.gitkeep":     "",
			"task.json": `{
  "id": "layered-prompt",
  "instruction_file": "input/instruction.txt",
  "scripted_turns_file": "input/model-turns.json",
  "working_directory": "workspace",
  "full_send": true,
  "step_limit": 5,
  "graders": {
    "outcome_workspace_snapshot": {
      "enabled": true,
      "required": true
    },
    "transcript_command_trace": {
      "enabled": false,
      "required": false
    }
  }
}`,
		})

		artifacts, err := harness.RunTrial(ctx, suite, task, RunConfig{Mode: ModeMockProvider})
		if err != nil {
			t.Fatalf("RunTrial(): %v", err)
		}

		for _, want := range []string{"home instructions", "config instructions", "project instructions"} {
			if !strings.Contains(artifacts.SystemPrompt, want) {
				t.Fatalf("system prompt missing %q: %q", want, artifacts.SystemPrompt)
			}
		}
		if artifacts.Workspace["AGENTS.md"] != "project instructions\n" {
			t.Fatalf("workspace AGENTS = %q, want project instructions", artifacts.Workspace["AGENTS.md"])
		}
	})

	t.Run("reuses configured clnku binary across trials", func(t *testing.T) {
		ctx := context.Background()
		repoRoot := newTempRepoRoot(t)
		harness := newHarnessForTests(t, ctx, repoRoot)

		suite, task := loadDefaultBasicEdit(t, repoRoot)
		firstPath := harness.binaryPath
		if firstPath == "" {
			t.Fatal("binary path is empty after NewHarness")
		}

		if _, err := harness.RunTrial(ctx, suite, task, RunConfig{Mode: ModeMockProvider}); err != nil {
			t.Fatalf("first RunTrial(): %v", err)
		}
		if _, err := harness.RunTrial(ctx, suite, task, RunConfig{Mode: ModeMockProvider}); err != nil {
			t.Fatalf("second RunTrial(): %v", err)
		}
		if harness.binaryPath != firstPath {
			t.Fatalf("binary path = %q, want reused path %q", harness.binaryPath, firstPath)
		}
	})

	t.Run("adapter seam is called and results are mapped", func(t *testing.T) {
		ctx := context.Background()
		repoRoot := newTempRepoRoot(t)
		harness := newHarnessForTests(t, ctx, repoRoot)

		fake := &fakeAdapter{
			result: AdapterResult{
				ExitCode:     42,
				SystemPrompt: "fake-system-prompt",
				AgentVersion: "fake-1.0",
				AgentCommand: []string{"fake-agent", "run"},
				Trajectory:   `[]`,
				EventLog:     ``,
			},
		}
		harness.adapter = fake

		suite, task := writeTempSuiteTask(t, repoRoot, "adapter-seam", map[string]string{
			"input/instruction.txt":    "do nothing\n",
			"input/model-turns.json":   `["{\"type\":\"done\",\"summary\":\"ok\"}"]`,
			"input/workspace/.gitkeep": "",
			"task.json": `{
  "id": "adapter-seam",
  "instruction_file": "input/instruction.txt",
  "scripted_turns_file": "input/model-turns.json",
  "working_directory": "workspace",
  "full_send": true,
  "step_limit": 5,
  "graders": {}
}`,
		})

		artifacts, err := harness.RunTrial(ctx, suite, task, RunConfig{Mode: ModeMockProvider})
		if err != nil {
			t.Fatalf("RunTrial(): %v", err)
		}

		if !fake.called {
			t.Fatal("adapter was not called")
		}
		if fake.req.WorkspaceDir == "" {
			t.Fatal("adapter request missing workspace dir")
		}
		if fake.req.TaskRoot == "" {
			t.Fatal("adapter request missing task root")
		}
		if len(fake.req.Env) == 0 {
			t.Fatal("adapter request missing env")
		}
		if artifacts.ExitCode != 42 {
			t.Fatalf("exit code = %d, want 42", artifacts.ExitCode)
		}
		if artifacts.SystemPrompt != "fake-system-prompt" {
			t.Fatalf("system prompt = %q, want fake-system-prompt", artifacts.SystemPrompt)
		}
		if artifacts.AgentVersion != "fake-1.0" {
			t.Fatalf("agent version = %q, want fake-1.0", artifacts.AgentVersion)
		}
		if len(artifacts.AgentCommand) != 2 || artifacts.AgentCommand[0] != "fake-agent" {
			t.Fatalf("agent command = %v, want [fake-agent run]", artifacts.AgentCommand)
		}
		if artifacts.Trajectory != `[]` {
			t.Fatalf("trajectory = %q, want []", artifacts.Trajectory)
		}
	})

	t.Run("claude adapter receives anthropic api key from host env", func(t *testing.T) {
		ctx := context.Background()
		repoRoot := newTempRepoRoot(t)
		harness := newHarnessForTests(t, ctx, repoRoot)
		t.Setenv("ANTHROPIC_API_KEY", "anthropic-test-key")

		fakeClaude := &fakeAdapter{
			result: AdapterResult{
				ExitCode:     0,
				AgentVersion: "claude-test",
				AgentCommand: []string{"claude", "--bare"},
			},
		}
		harness.claudeAdapter = fakeClaude

		suite, task := writeTempSuiteTask(t, repoRoot, "claude-env-pass-through", map[string]string{
			"input/instruction.txt":  "Say hello\n",
			"input/model-turns.json": `["{\"type\":\"done\",\"summary\":\"hello\"}"]`,
			"task.json": `{
  "id": "claude-env-pass-through",
  "instruction_file": "input/instruction.txt",
  "scripted_turns_file": "input/model-turns.json",
  "working_directory": "workspace",
  "step_limit": 5,
  "full_send": true,
  "agent": "claude",
  "graders": {
    "outcome_workspace_snapshot": { "enabled": false, "required": false },
    "transcript_command_trace": { "enabled": false, "required": false }
  }
}`,
		})

		if _, err := harness.RunTrial(ctx, suite, task, RunConfig{Mode: ModeMockProvider}); err != nil {
			t.Fatalf("RunTrial(): %v", err)
		}
		if !fakeClaude.called {
			t.Fatal("claude adapter was not called")
		}
		if !envContains(fakeClaude.req.Env, "ANTHROPIC_API_KEY", "anthropic-test-key") {
			t.Fatalf("adapter env missing ANTHROPIC_API_KEY; env=%v", fakeClaude.req.Env)
		}
	})

	t.Run("claude trials do not require clnku binary setup", func(t *testing.T) {
		ctx := context.Background()
		repoRoot := newTempRepoRoot(t)
		harness, err := NewHarness(ctx, repoRoot, WithEvalsDir(filepath.Join(repoRoot, "evaluations")))
		if err != nil {
			t.Fatalf("NewHarness(): %v", err)
		}
		t.Cleanup(func() {
			if err := harness.Close(); err != nil {
				t.Fatalf("Close(): %v", err)
			}
		})

		fakeClaude := &fakeAdapter{
			result: AdapterResult{
				ExitCode:     0,
				AgentVersion: "claude-test",
				AgentCommand: []string{"claude", "--bare"},
			},
		}
		harness.claudeAdapter = fakeClaude

		suite, task := writeTempSuiteTask(t, repoRoot, "claude-no-clnku", map[string]string{
			"input/instruction.txt":  "Say hello\n",
			"input/model-turns.json": `["{\"type\":\"done\",\"summary\":\"hello\"}"]`,
			"task.json": `{
  "id": "claude-no-clnku",
  "instruction_file": "input/instruction.txt",
  "scripted_turns_file": "input/model-turns.json",
  "working_directory": "workspace",
  "step_limit": 5,
  "full_send": true,
  "agent": "claude",
  "graders": {
    "outcome_workspace_snapshot": { "enabled": false, "required": false },
    "transcript_command_trace": { "enabled": false, "required": false }
  }
}`,
		})

		artifacts, err := harness.RunTrial(ctx, suite, task, RunConfig{Mode: ModeMockProvider})
		if err != nil {
			t.Fatalf("RunTrial(): %v", err)
		}
		if !fakeClaude.called {
			t.Fatal("claude adapter was not called")
		}
		if fakeClaude.req.BinaryPath != "" {
			t.Fatalf("claude adapter BinaryPath = %q, want empty", fakeClaude.req.BinaryPath)
		}
		if artifacts.Agent != AgentClaude {
			t.Fatalf("Agent = %q, want %q", artifacts.Agent, AgentClaude)
		}
	})

	t.Run("cleanup removes trial dirs and harness temp root", func(t *testing.T) {
		ctx := context.Background()
		repoRoot := newTempRepoRoot(t)
		harness := newHarnessForTests(t, ctx, repoRoot)

		suite, task := loadDefaultBasicEdit(t, repoRoot)
		if _, err := harness.RunTrial(ctx, suite, task, RunConfig{Mode: ModeMockProvider}); err != nil {
			t.Fatalf("RunTrial(): %v", err)
		}

		entries, err := os.ReadDir(harness.trialsDir)
		if err != nil {
			t.Fatalf("ReadDir(%q): %v", harness.trialsDir, err)
		}
		if len(entries) != 0 {
			t.Fatalf("trial dirs still present: %v", entries)
		}

		tempRoot := harness.tempRoot
		if err := harness.Close(); err != nil {
			t.Fatalf("Close(): %v", err)
		}
		if _, err := os.Stat(tempRoot); !os.IsNotExist(err) {
			t.Fatalf("temp root stat error = %v, want not exist", err)
		}
	})
}

func TestFixtureAgentSupportsHarnessContract(t *testing.T) {
	fixturePath := mustEvalFixturePath(t)
	tempRoot := t.TempDir()
	workspaceDir := filepath.Join(tempRoot, "workspace")
	homeDir := filepath.Join(tempRoot, "home")
	configDir := filepath.Join(tempRoot, "config")
	if err := os.MkdirAll(filepath.Join(configDir, "clnkr"), 0o755); err != nil {
		t.Fatalf("MkdirAll(config/clnkr): %v", err)
	}
	for path, content := range map[string]string{
		filepath.Join(workspaceDir, "AGENTS.md"):       "workspace instructions\n",
		filepath.Join(homeDir, "AGENTS.md"):            "home instructions\n",
		filepath.Join(configDir, "clnkr", "AGENTS.md"): "config instructions\n",
		filepath.Join(tempRoot, "seed-messages.json"):  "",
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("MkdirAll(%q): %v", filepath.Dir(path), err)
		}
		if content != "" {
			if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
				t.Fatalf("WriteFile(%q): %v", path, err)
			}
		}
	}

	seedMessages := []protocol.Message{
		{Role: "user", Content: "seed instruction"},
	}
	seedPath := filepath.Join(tempRoot, "seed-messages.json")
	seedBytes, err := json.Marshal(seedMessages)
	if err != nil {
		t.Fatalf("json.Marshal(seedMessages): %v", err)
	}
	if err := os.WriteFile(seedPath, seedBytes, 0o644); err != nil {
		t.Fatalf("WriteFile(seed messages): %v", err)
	}

	baseEnv := append(os.Environ(),
		"HOME="+homeDir,
		"XDG_CONFIG_HOME="+configDir,
		"XDG_STATE_HOME="+filepath.Join(tempRoot, "state"),
		"CLNKR_BASE_URL=",
		"CLNKR_API_KEY=",
		"CLNKR_MODEL=",
	)

	dumpCmd := exec.Command(fixturePath, "--dump-system-prompt")
	dumpCmd.Dir = workspaceDir
	dumpCmd.Env = baseEnv
	systemPrompt, err := dumpCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("dump system prompt: %v: %s", err, systemPrompt)
	}
	promptText := string(systemPrompt)
	wantSections := []string{
		"<user-instructions>\nhome instructions\n\n</user-instructions>",
		"<config-instructions>\nconfig instructions\n\n</config-instructions>",
		"<project-instructions>\nworkspace instructions\n\n</project-instructions>",
		"clankerval eval fixture",
	}
	lastIndex := -1
	for _, want := range wantSections {
		index := strings.Index(promptText, want)
		if index == -1 {
			t.Fatalf("system prompt = %q, want section %q", promptText, want)
		}
		if index <= lastIndex {
			t.Fatalf("system prompt = %q, want section order %v", promptText, wantSections)
		}
		lastIndex = index
	}

	trajectoryPath := filepath.Join(tempRoot, "trajectory.json")
	eventLogPath := filepath.Join(tempRoot, "events.jsonl")
	runCmd := exec.Command(
		fixturePath,
		"-p", "Create note.txt in the repo root with the contents hello and then finish.",
		"--event-log", eventLogPath,
		"--trajectory", trajectoryPath,
		"--max-steps", "10",
		"--full-send",
		"--load-messages", seedPath,
	)
	runCmd.Dir = workspaceDir
	runCmd.Env = baseEnv
	output, err := runCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("run fixture: %v: %s", err, output)
	}

	trajectoryBytes, err := os.ReadFile(trajectoryPath)
	if err != nil {
		t.Fatalf("ReadFile(trajectory): %v", err)
	}
	var trajectory []protocol.Message
	if err := json.Unmarshal(trajectoryBytes, &trajectory); err != nil {
		t.Fatalf("parse trajectory: %v", err)
	}
	if len(trajectory) != 6 {
		t.Fatalf("trajectory length = %d, want 6 messages", len(trajectory))
	}
	if trajectory[0] != seedMessages[0] {
		t.Fatalf("seed message = %#v, want %#v", trajectory[0], seedMessages[0])
	}
	if trajectory[1].Role != "user" || !strings.Contains(trajectory[1].Content, "Create note.txt") {
		t.Fatalf("task prompt message = %#v, want user instruction", trajectory[1])
	}
	if trajectory[2].Role != "user" {
		t.Fatalf("state message role = %q, want user", trajectory[2].Role)
	}
	if cwd, ok := transcript.ExtractStateCwd(trajectory[2].Content); !ok || cwd != workspaceDir {
		t.Fatalf("state message = %q, want cwd %q", trajectory[2].Content, workspaceDir)
	}
	actTurn, err := protocol.ParseTurn(trajectory[3].Content)
	if err != nil {
		t.Fatalf("parse act turn: %v", err)
	}
	act, ok := actTurn.(*protocol.ActTurn)
	if !ok {
		t.Fatalf("assistant turn type = %T, want *protocol.ActTurn", actTurn)
	}
	if act.Command != "printf 'hello\n' > note.txt" {
		t.Fatalf("act command = %q, want note writer", act.Command)
	}
	if trajectory[4].Role != "user" || !strings.Contains(trajectory[4].Content, "[command]\nprintf 'hello\n' &gt; note.txt\n[/command]") {
		t.Fatalf("command result message = %q, want command envelope", trajectory[4].Content)
	}
	doneTurn, err := protocol.ParseTurn(trajectory[5].Content)
	if err != nil {
		t.Fatalf("parse done turn: %v", err)
	}
	done, ok := doneTurn.(*protocol.DoneTurn)
	if !ok {
		t.Fatalf("final turn type = %T, want *protocol.DoneTurn", doneTurn)
	}
	if done.Summary != "fixture task completed" {
		t.Fatalf("done summary = %q, want fixture task completed", done.Summary)
	}

	eventLogBytes, err := os.ReadFile(eventLogPath)
	if err != nil {
		t.Fatalf("ReadFile(event log): %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(eventLogBytes)), "\n")
	if len(lines) != 2 {
		t.Fatalf("event log lines = %d, want 2", len(lines))
	}
	var start struct {
		Type    string `json:"type"`
		Payload struct {
			Command string `json:"command"`
			Dir     string `json:"dir"`
		} `json:"payload"`
	}
	if err := json.Unmarshal([]byte(lines[0]), &start); err != nil {
		t.Fatalf("parse command_start event: %v", err)
	}
	if start.Type != "command_start" || start.Payload.Command != "printf 'hello\n' > note.txt" || start.Payload.Dir != workspaceDir {
		t.Fatalf("command_start = %#v, want command_start for %q in %q", start, "printf 'hello\n' > note.txt", workspaceDir)
	}
	var doneEvent struct {
		Type    string `json:"type"`
		Payload struct {
			Command  string `json:"command"`
			Stdout   string `json:"stdout"`
			Stderr   string `json:"stderr"`
			ExitCode int    `json:"exit_code"`
		} `json:"payload"`
	}
	if err := json.Unmarshal([]byte(lines[1]), &doneEvent); err != nil {
		t.Fatalf("parse command_done event: %v", err)
	}
	if doneEvent.Type != "command_done" || doneEvent.Payload.Command != "printf 'hello\n' > note.txt" || doneEvent.Payload.ExitCode != 0 {
		t.Fatalf("command_done = %#v, want successful note writer", doneEvent)
	}
	if got := strings.TrimSpace(doneEvent.Payload.Stdout + doneEvent.Payload.Stderr); got != "" {
		t.Fatalf("command output = %q, want empty stdout/stderr", got)
	}
	if data, err := os.ReadFile(filepath.Join(workspaceDir, "note.txt")); err != nil {
		t.Fatalf("ReadFile(note.txt): %v", err)
	} else if string(data) != "hello\n" {
		t.Fatalf("note.txt = %q, want hello\\n", string(data))
	}
}

func TestFixtureAgentPromptFallsBackToHomeDotConfig(t *testing.T) {
	fixturePath := mustEvalFixturePath(t)
	tempRoot := t.TempDir()
	workspaceDir := filepath.Join(tempRoot, "workspace")
	homeDir := filepath.Join(tempRoot, "home")
	configPath := filepath.Join(homeDir, ".config", "clnkr", "AGENTS.md")

	for path, content := range map[string]string{
		configPath:                               "config fallback instructions\n",
		filepath.Join(workspaceDir, "AGENTS.md"): "workspace instructions\n",
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("MkdirAll(%q): %v", filepath.Dir(path), err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("WriteFile(%q): %v", path, err)
		}
	}

	cmd := exec.Command(fixturePath, "--dump-system-prompt")
	cmd.Dir = workspaceDir
	cmd.Env = append(os.Environ(),
		"HOME="+homeDir,
		"XDG_CONFIG_HOME=",
	)
	systemPrompt, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("dump system prompt: %v: %s", err, systemPrompt)
	}

	promptText := string(systemPrompt)
	wantConfig := "<config-instructions>\nconfig fallback instructions\n\n</config-instructions>"
	if !strings.Contains(promptText, wantConfig) {
		t.Fatalf("system prompt = %q, want fallback section %q", promptText, wantConfig)
	}
	if !strings.Contains(promptText, "<project-instructions>\nworkspace instructions\n\n</project-instructions>") {
		t.Fatalf("system prompt = %q, want project instructions section", promptText)
	}
}

func TestAdapterBoundaryTypes(t *testing.T) {
	t.Run("AdapterRequest can be populated from harness state", func(t *testing.T) {
		req := AdapterRequest{
			TaskRoot:     "/tmp/task-root",
			Task:         Task{ID: "test-task", InstructionFile: "input/instruction.txt", StepLimit: 10},
			WorkspaceDir: "/tmp/workspace",
			HomeDir:      "/tmp/home",
			ConfigDir:    "/tmp/config",
			StateDir:     "/tmp/state",
			TrialRoot:    "/tmp/trial",
			BinaryPath:   "/usr/local/bin/clnku",
			Env:          []string{"HOME=/tmp/home", "PATH=/usr/bin"},
		}
		if req.TaskRoot != "/tmp/task-root" {
			t.Fatalf("TaskRoot = %q, want /tmp/task-root", req.TaskRoot)
		}
		if req.Task.ID != "test-task" {
			t.Fatalf("Task.ID = %q, want test-task", req.Task.ID)
		}
		if len(req.Env) != 2 {
			t.Fatalf("Env length = %d, want 2", len(req.Env))
		}
	})

	t.Run("AdapterResult carries agent-neutral artifacts", func(t *testing.T) {
		result := AdapterResult{
			ExitCode:     0,
			AgentVersion: "1.0.0",
			AgentCommand: []string{"clnku", "-p", "do something"},
			SystemPrompt: "system prompt text",
			Trajectory:   `[{"role":"user","content":"hello"}]`,
			EventLog:     `{"type":"command_start","payload":{}}`,
			TranscriptEvents: []TranscriptEvent{
				{Index: 0, Kind: "user_instruction", Role: "user", Content: "hello"},
			},
			Commands: []CommandRecord{
				{Command: "echo hello", Dir: "/tmp", Stdout: "hello\n", ExitCode: 0},
			},
			RawAgentArtifacts: []RawAgentArtifact{
				{Name: "trajectory.json", Content: []byte(`[]`)},
			},
		}
		if result.ExitCode != 0 {
			t.Fatalf("ExitCode = %d, want 0", result.ExitCode)
		}
		if len(result.TranscriptEvents) != 1 {
			t.Fatalf("TranscriptEvents length = %d, want 1", len(result.TranscriptEvents))
		}
		if result.TranscriptEvents[0].Kind != "user_instruction" {
			t.Fatalf("TranscriptEvents[0].Kind = %q, want user_instruction", result.TranscriptEvents[0].Kind)
		}
		if len(result.Commands) != 1 {
			t.Fatalf("Commands length = %d, want 1", len(result.Commands))
		}
		if result.Commands[0].Command != "echo hello" {
			t.Fatalf("Commands[0].Command = %q, want echo hello", result.Commands[0].Command)
		}
		if len(result.RawAgentArtifacts) != 1 {
			t.Fatalf("RawAgentArtifacts length = %d, want 1", len(result.RawAgentArtifacts))
		}
		if result.RawAgentArtifacts[0].Name != "trajectory.json" {
			t.Fatalf("RawAgentArtifacts[0].Name = %q, want trajectory.json", result.RawAgentArtifacts[0].Name)
		}
	})

	t.Run("AdapterResult populates RunArtifacts agent-neutral fields", func(t *testing.T) {
		result := AdapterResult{
			ExitCode:     1,
			AgentVersion: "2.0.0",
			AgentCommand: []string{"claude", "--bare", "-p", "fix the bug"},
			TranscriptEvents: []TranscriptEvent{
				{Index: 0, Kind: "user_instruction", Role: "user", Content: "fix the bug"},
				{Index: 1, Kind: "command_start", Role: "system", Command: "go test ./..."},
			},
			Commands: []CommandRecord{
				{Command: "go test ./...", Dir: "/workspace", Stdout: "FAIL", ExitCode: 1},
			},
			RawAgentArtifacts: []RawAgentArtifact{
				{Name: "session.jsonl", Content: []byte("session data")},
				{Name: "result.json", Content: []byte(`{"ok":false}`)},
			},
		}

		artifacts := RunArtifacts{
			SuiteID: "test-suite",
			TaskID:  "test-task",
			Agent:   AgentClaude,
		}
		artifacts.ExitCode = result.ExitCode
		artifacts.AgentVersion = result.AgentVersion
		artifacts.AgentCommand = result.AgentCommand
		artifacts.TranscriptEvents = result.TranscriptEvents
		artifacts.Commands = result.Commands
		artifacts.RawAgentArtifacts = result.RawAgentArtifacts

		if artifacts.Agent != AgentClaude {
			t.Fatalf("Agent = %q, want %q", artifacts.Agent, AgentClaude)
		}
		if artifacts.AgentVersion != "2.0.0" {
			t.Fatalf("AgentVersion = %q, want 2.0.0", artifacts.AgentVersion)
		}
		if len(artifacts.AgentCommand) != 4 {
			t.Fatalf("AgentCommand length = %d, want 4", len(artifacts.AgentCommand))
		}
		if len(artifacts.TranscriptEvents) != 2 {
			t.Fatalf("TranscriptEvents length = %d, want 2", len(artifacts.TranscriptEvents))
		}
		if len(artifacts.Commands) != 1 {
			t.Fatalf("Commands length = %d, want 1", len(artifacts.Commands))
		}
		if len(artifacts.RawAgentArtifacts) != 2 {
			t.Fatalf("RawAgentArtifacts length = %d, want 2", len(artifacts.RawAgentArtifacts))
		}
	})

	t.Run("CommandRecord JSON round-trips", func(t *testing.T) {
		record := CommandRecord{
			Command:  "echo hello",
			Dir:      "/workspace",
			Stdout:   "hello\n",
			Stderr:   "",
			ExitCode: 0,
		}
		data, err := json.Marshal(record)
		if err != nil {
			t.Fatalf("Marshal: %v", err)
		}
		var decoded CommandRecord
		if err := json.Unmarshal(data, &decoded); err != nil {
			t.Fatalf("Unmarshal: %v", err)
		}
		if decoded != record {
			t.Fatalf("round-trip mismatch: got %#v, want %#v", decoded, record)
		}
	})

	t.Run("TranscriptEvent JSON round-trips", func(t *testing.T) {
		event := TranscriptEvent{
			Index:   3,
			Kind:    "command_start",
			Role:    "system",
			Content: "",
			Command: "ls -la",
		}
		data, err := json.Marshal(event)
		if err != nil {
			t.Fatalf("Marshal: %v", err)
		}
		var decoded TranscriptEvent
		if err := json.Unmarshal(data, &decoded); err != nil {
			t.Fatalf("Unmarshal: %v", err)
		}
		if decoded != event {
			t.Fatalf("round-trip mismatch: got %#v, want %#v", decoded, event)
		}
	})

	t.Run("RawAgentArtifact carries name and content", func(t *testing.T) {
		artifact := RawAgentArtifact{
			Name:    "events.jsonl",
			Content: []byte(`{"type":"command_start"}`),
		}
		if artifact.Name != "events.jsonl" {
			t.Fatalf("Name = %q, want events.jsonl", artifact.Name)
		}
		if string(artifact.Content) != `{"type":"command_start"}` {
			t.Fatalf("Content = %q, want event JSON", string(artifact.Content))
		}
	})
}

func TestRunTrialPopulatesAgentField(t *testing.T) {
	ctx := context.Background()
	repoRoot := newTempRepoRoot(t)
	harness := newHarnessForTests(t, ctx, repoRoot)

	suite, task := loadDefaultBasicEdit(t, repoRoot)
	cfg := RunConfig{Mode: ModeMockProvider, Agent: AgentClnku}
	artifacts, err := harness.RunTrial(ctx, suite, task, cfg)
	if err != nil {
		t.Fatalf("RunTrial(): %v", err)
	}

	if artifacts.Agent != AgentClnku {
		t.Fatalf("Agent = %q, want %q", artifacts.Agent, AgentClnku)
	}
}

func TestRunTrialUsesAdapterSeam(t *testing.T) {
	t.Run("RunTrial calls adapter.Run and uses its result", func(t *testing.T) {
		ctx := context.Background()
		repoRoot := newTempRepoRoot(t)
		harness := newHarnessForTests(t, ctx, repoRoot)

		// Replace the real adapter with a recording wrapper.
		realAdapter := harness.adapter
		recorder := &recordingAdapter{delegate: realAdapter}
		harness.adapter = recorder

		suite, task := loadDefaultBasicEdit(t, repoRoot)
		artifacts, err := harness.RunTrial(ctx, suite, task, RunConfig{Mode: ModeMockProvider})
		if err != nil {
			t.Fatalf("RunTrial(): %v", err)
		}

		// Prove the adapter was called exactly once.
		if recorder.callCount != 1 {
			t.Fatalf("adapter call count = %d, want 1", recorder.callCount)
		}

		// Prove the request was populated correctly.
		req := recorder.lastRequest
		if req.Task.ID != task.ID {
			t.Fatalf("adapter request Task.ID = %q, want %q", req.Task.ID, task.ID)
		}
		if req.BinaryPath != harness.binaryPath {
			t.Fatalf("adapter request BinaryPath = %q, want %q", req.BinaryPath, harness.binaryPath)
		}
		if req.WorkspaceDir == "" {
			t.Fatal("adapter request WorkspaceDir is empty")
		}
		if req.TrialRoot == "" {
			t.Fatal("adapter request TrialRoot is empty")
		}
		if len(req.Env) == 0 {
			t.Fatal("adapter request Env is empty")
		}

		// Prove the adapter result flows into artifacts.
		if len(artifacts.AgentCommand) == 0 {
			t.Fatal("AgentCommand is empty, want non-empty from adapter result")
		}
		if artifacts.AgentCommand[0] != harness.binaryPath {
			t.Fatalf("AgentCommand[0] = %q, want binary path %q", artifacts.AgentCommand[0], harness.binaryPath)
		}
		if artifacts.SystemPrompt == "" {
			t.Fatal("SystemPrompt is empty, want populated from adapter result")
		}
		if artifacts.Trajectory == "" {
			t.Fatal("Trajectory is empty, want populated from adapter result")
		}
		if artifacts.EventLog == "" {
			t.Fatal("EventLog is empty, want populated from adapter result")
		}
	})

	t.Run("adapter stages project instructions", func(t *testing.T) {
		ctx := context.Background()
		repoRoot := newTempRepoRoot(t)
		harness := newHarnessForTests(t, ctx, repoRoot)

		suite, task := loadDefaultBasicEdit(t, repoRoot)
		artifacts, err := harness.RunTrial(ctx, suite, task, RunConfig{Mode: ModeMockProvider})
		if err != nil {
			t.Fatalf("RunTrial(): %v", err)
		}

		// Project instructions staged by adapter appear in system prompt.
		if !strings.Contains(artifacts.SystemPrompt, "Keep changes tight. Work in the current directory.") {
			t.Fatalf("system prompt missing project AGENTS instructions: %q", artifacts.SystemPrompt)
		}
		// Workspace has AGENTS.md staged by adapter.
		if artifacts.Workspace["AGENTS.md"] != "Keep changes tight. Work in the current directory.\n" {
			t.Fatalf("workspace AGENTS.md = %q, want project instructions", artifacts.Workspace["AGENTS.md"])
		}
	})

	t.Run("second trial reuses same adapter", func(t *testing.T) {
		ctx := context.Background()
		repoRoot := newTempRepoRoot(t)
		harness := newHarnessForTests(t, ctx, repoRoot)

		recorder := &recordingAdapter{delegate: harness.adapter}
		harness.adapter = recorder

		suite, task := loadDefaultBasicEdit(t, repoRoot)
		if _, err := harness.RunTrial(ctx, suite, task, RunConfig{Mode: ModeMockProvider}); err != nil {
			t.Fatalf("first RunTrial(): %v", err)
		}
		if _, err := harness.RunTrial(ctx, suite, task, RunConfig{Mode: ModeMockProvider}); err != nil {
			t.Fatalf("second RunTrial(): %v", err)
		}
		if recorder.callCount != 2 {
			t.Fatalf("adapter call count = %d, want 2", recorder.callCount)
		}
	})
}

// recordingAdapter wraps a real AgentAdapter and records calls.
type recordingAdapter struct {
	delegate    AgentAdapter
	callCount   int
	lastRequest AdapterRequest
}

func (r *recordingAdapter) Run(ctx context.Context, req AdapterRequest) (AdapterResult, error) {
	r.callCount++
	r.lastRequest = req
	return r.delegate.Run(ctx, req)
}

// fakeAdapter is a test double for AgentAdapter that records its invocation.
type fakeAdapter struct {
	called bool
	req    AdapterRequest
	result AdapterResult
	err    error
}

func (f *fakeAdapter) Run(_ context.Context, req AdapterRequest) (AdapterResult, error) {
	f.called = true
	f.req = req
	return f.result, f.err
}

func envContains(env []string, key, value string) bool {
	prefix := key + "="
	for _, item := range env {
		if strings.HasPrefix(item, prefix) && strings.TrimPrefix(item, prefix) == value {
			return true
		}
	}
	return false
}

func loadDefaultBasicEdit(t *testing.T, repoRoot string) (Suite, Task) {
	t.Helper()

	return writeTempSuiteTask(t, repoRoot, "001-basic-edit", map[string]string{
		"input/instruction.txt": "Create note.txt in the repo root with the contents hello and then finish.\n",
		"input/model-turns.json": `[
  "{\"type\":\"act\",\"command\":\"printf 'hello\\\\n' > note.txt\"}",
  "{\"type\":\"done\",\"summary\":\"finished\"}"
]`,
		"input/project/AGENTS.md":      "Keep changes tight. Work in the current directory.\n",
		"expected/workspace/AGENTS.md": "Keep changes tight. Work in the current directory.\n",
		"expected/workspace/note.txt":  "hello\n",
		"task.json": `{
  "id": "001-basic-edit",
  "instruction_file": "input/instruction.txt",
  "scripted_turns_file": "input/model-turns.json",
  "working_directory": "workspace",
  "full_send": true,
  "step_limit": 10,
  "graders": {
    "outcome_workspace_snapshot": {
      "enabled": true,
      "required": true
    },
    "transcript_command_trace": {
      "enabled": true,
      "required": false
    }
  }
}`,
	})
}

func writeTempSuiteTask(t *testing.T, repoRoot, taskID string, files map[string]string) (Suite, Task) {
	t.Helper()

	suitesRoot := filepath.Join(repoRoot, "evaluations", "suites")
	suiteDir, err := os.MkdirTemp(suitesRoot, "task2-*")
	if err != nil {
		t.Fatalf("MkdirTemp(): %v", err)
	}
	t.Cleanup(func() {
		_ = os.RemoveAll(suiteDir)
	})

	taskDir := filepath.Join(suiteDir, "tasks", taskID)
	for rel, content := range files {
		target := filepath.Join(taskDir, rel)
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			t.Fatalf("MkdirAll(%q): %v", target, err)
		}
		if err := os.WriteFile(target, []byte(content), 0o644); err != nil {
			t.Fatalf("WriteFile(%q): %v", target, err)
		}
	}

	suiteID := filepath.Base(suiteDir)
	suiteJSON := `{
  "id": "` + suiteID + `",
  "description": "temp suite",
  "mode": "mock-provider",
  "trials_per_task": 1,
  "failure_policy": {
    "stop_on_first_failure": true,
    "max_failed_tasks": 1
  },
  "tasks": ["` + taskID + `"]
}`
	if err := os.WriteFile(filepath.Join(suiteDir, "suite.json"), []byte(suiteJSON), 0o644); err != nil {
		t.Fatalf("WriteFile(suite.json): %v", err)
	}

	suite, err := LoadSuite(filepath.Join(suiteDir, "suite.json"))
	if err != nil {
		t.Fatalf("LoadSuite(): %v", err)
	}
	tasks, err := LoadSuiteTasks(suiteDir, suite)
	if err != nil {
		t.Fatalf("LoadSuiteTasks(): %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("task count = %d, want 1", len(tasks))
	}
	return suite, tasks[0]
}

type chatCompletionResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
}

func postChatCompletion(t *testing.T, baseURL string, payload map[string]any) chatCompletionResponse {
	t.Helper()

	resp, body := postChatCompletionRaw(t, baseURL, payload)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%q", resp.StatusCode, http.StatusOK, body)
	}

	var decoded chatCompletionResponse
	if err := json.Unmarshal([]byte(body), &decoded); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return decoded
}

func postChatCompletionRaw(t *testing.T, baseURL string, payload map[string]any) (*http.Response, string) {
	t.Helper()

	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	resp, err := http.Post(baseURL+"/chat/completions", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("post chat completions: %v", err)
	}
	t.Cleanup(func() {
		_ = resp.Body.Close()
	})
	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response body: %v", err)
	}
	return resp, string(responseBody)
}

func postChatCompletionBody(t *testing.T, baseURL, body string) chatCompletionResponse {
	t.Helper()

	resp, responseBody := postChatCompletionRawBody(t, baseURL, body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%q", resp.StatusCode, http.StatusOK, responseBody)
	}

	var decoded chatCompletionResponse
	if err := json.Unmarshal([]byte(responseBody), &decoded); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return decoded
}

func postChatCompletionRawBody(t *testing.T, baseURL, body string) (*http.Response, string) {
	t.Helper()

	resp, err := http.Post(baseURL+"/chat/completions", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("post chat completions: %v", err)
	}
	t.Cleanup(func() {
		_ = resp.Body.Close()
	})
	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response body: %v", err)
	}
	return resp, string(responseBody)
}

func repoRoot(t *testing.T) string {
	t.Helper()

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd(): %v", err)
	}
	return filepath.Dir(cwd)
}

func assertScriptedParity(t *testing.T, artifacts RunArtifacts) {
	t.Helper()

	var trajectory []protocol.Message
	if err := json.Unmarshal([]byte(artifacts.Trajectory), &trajectory); err != nil {
		t.Fatalf("parse trajectory: %v", err)
	}

	assistantIndices := make([]int, 0, len(artifacts.ProviderRequests))
	for i, message := range trajectory {
		if message.Role == "assistant" {
			assistantIndices = append(assistantIndices, i)
		}
	}
	if len(assistantIndices) != len(artifacts.ProviderRequests) {
		t.Fatalf("assistant count = %d, want %d", len(assistantIndices), len(artifacts.ProviderRequests))
	}

	for i, request := range artifacts.ProviderRequests {
		if request.Model != artifacts.ProviderModel {
			t.Fatalf("request %d model = %q, want %q", i, request.Model, artifacts.ProviderModel)
		}
		if len(request.Messages) == 0 {
			t.Fatalf("request %d has no messages", i)
		}
		if request.Messages[0].Role != "system" {
			t.Fatalf("request %d first role = %q, want system", i, request.Messages[0].Role)
		}
		if request.Messages[0].Content != artifacts.SystemPrompt {
			t.Fatalf("request %d system prompt mismatch", i)
		}

		wantMessages := trajectory[:assistantIndices[i]]
		if !reflect.DeepEqual(request.Messages[1:], wantMessages) {
			t.Fatalf("request %d transcript mismatch\nactual: %#v\nexpected: %#v", i, request.Messages[1:], wantMessages)
		}
		if request.RawResponse != artifacts.ProviderResponses[i] {
			t.Fatalf("request %d raw response mismatch", i)
		}
	}
}
