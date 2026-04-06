package evaluations

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadSuiteAgent(t *testing.T) {
	t.Run("loads suite with valid agent", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "suite.json")
		writeTestFile(t, path, `{
  "id": "default",
  "description": "suite with agent",
  "mode": "mock-provider",
  "agent": "claude",
  "trials_per_task": 1,
  "failure_policy": {
    "stop_on_first_failure": true,
    "max_failed_tasks": 1
  },
  "tasks": ["001-basic-edit"]
}`)

		got, err := LoadSuite(path)
		if err != nil {
			t.Fatalf("LoadSuite(): %v", err)
		}
		if got.Agent != AgentClaude {
			t.Fatalf("suite agent = %q, want %q", got.Agent, AgentClaude)
		}
	})

	t.Run("loads suite without agent field (backward compat)", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "suite.json")
		writeTestFile(t, path, `{
  "id": "default",
  "description": "suite without agent",
  "mode": "mock-provider",
  "trials_per_task": 1,
  "failure_policy": {
    "stop_on_first_failure": true,
    "max_failed_tasks": 1
  },
  "tasks": ["001-basic-edit"]
}`)

		got, err := LoadSuite(path)
		if err != nil {
			t.Fatalf("LoadSuite(): %v", err)
		}
		if got.Agent != "" {
			t.Fatalf("suite agent = %q, want empty", got.Agent)
		}
	})

	t.Run("rejects suite with invalid agent", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "suite.json")
		writeTestFile(t, path, `{
  "id": "default",
  "description": "suite with bad agent",
  "mode": "mock-provider",
  "agent": "bogus",
  "trials_per_task": 1,
  "failure_policy": {
    "stop_on_first_failure": true,
    "max_failed_tasks": 1
  },
  "tasks": ["001-basic-edit"]
}`)

		_, err := LoadSuite(path)
		if err == nil {
			t.Fatal("LoadSuite() error = nil, want invalid agent failure")
		}
		if !strings.Contains(err.Error(), "agent") {
			t.Fatalf("error = %v, want agent validation error", err)
		}
	})
}

func TestLoadTaskAgent(t *testing.T) {
	t.Run("loads task with valid agent", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "task.json")
		writeTestFile(t, path, `{
  "id": "001-basic-edit",
  "instruction_file": "input/instruction.txt",
  "scripted_turns_file": "input/model-turns.json",
  "working_directory": "workspace",
  "agent": "clnku",
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
}`)

		got, err := LoadTask(path)
		if err != nil {
			t.Fatalf("LoadTask(): %v", err)
		}
		if got.Agent != AgentClnku {
			t.Fatalf("task agent = %q, want %q", got.Agent, AgentClnku)
		}
	})

	t.Run("loads task without agent field (backward compat)", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "task.json")
		writeTestFile(t, path, `{
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
}`)

		got, err := LoadTask(path)
		if err != nil {
			t.Fatalf("LoadTask(): %v", err)
		}
		if got.Agent != "" {
			t.Fatalf("task agent = %q, want empty", got.Agent)
		}
	})

	t.Run("rejects task with invalid agent", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "task.json")
		writeTestFile(t, path, `{
  "id": "001-basic-edit",
  "instruction_file": "input/instruction.txt",
  "scripted_turns_file": "input/model-turns.json",
  "working_directory": "workspace",
  "agent": "bogus",
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
}`)

		_, err := LoadTask(path)
		if err == nil {
			t.Fatal("LoadTask() error = nil, want invalid agent failure")
		}
		if !strings.Contains(err.Error(), "agent") {
			t.Fatalf("error = %v, want agent validation error", err)
		}
	})
}

func TestEffectiveAgent(t *testing.T) {
	tests := []struct {
		name       string
		task       Agent
		suite      Agent
		runDefault Agent
		want       Agent
	}{
		{"task wins over suite and default", AgentClaude, AgentClnku, AgentClnku, AgentClaude},
		{"suite wins over default", "", AgentClaude, AgentClnku, AgentClaude},
		{"default used when task and suite empty", "", "", AgentClnku, AgentClnku},
		{"task wins over default when suite empty", AgentClaude, "", AgentClnku, AgentClaude},
		{"all empty defaults to clnku", "", "", "", AgentClnku},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := EffectiveAgent(tt.task, tt.suite, tt.runDefault)
			if got != tt.want {
				t.Fatalf("EffectiveAgent(%q, %q, %q) = %q, want %q", tt.task, tt.suite, tt.runDefault, got, tt.want)
			}
		})
	}
}

func TestLoadSuite(t *testing.T) {
	t.Run("malformed json", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "suite.json")
		writeTestFile(t, path, "{")

		if _, err := LoadSuite(path); err == nil {
			t.Fatal("LoadSuite() error = nil, want parse failure")
		}
	})

	t.Run("missing required fields", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "suite.json")
		writeTestFile(t, path, `{"id":"default"}`)

		if _, err := LoadSuite(path); err == nil {
			t.Fatal("LoadSuite() error = nil, want validation failure")
		}
	})

	t.Run("invalid mode value", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "suite.json")
		writeTestFile(t, path, `{
  "id": "default",
  "description": "suite",
  "mode": "bogus",
  "trials_per_task": 1,
  "failure_policy": {
    "stop_on_first_failure": true,
    "max_failed_tasks": 1
  },
  "tasks": ["001-basic-edit"]
}`)

		if _, err := LoadSuite(path); err == nil {
			t.Fatal("LoadSuite() error = nil, want invalid mode failure")
		}
	})

	t.Run("loads canonical fixture", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "suite.json")
		writeTestFile(t, path, `{
  "id": "default",
  "description": "Baseline evaluation suite for clnku",
  "mode": "mock-provider",
  "trials_per_task": 1,
  "failure_policy": {
    "stop_on_first_failure": true,
    "max_failed_tasks": 1
  },
  "tasks": ["001-basic-edit"]
}`)

		got, err := LoadSuite(path)
		if err != nil {
			t.Fatalf("LoadSuite(): %v", err)
		}
		if got.ID != "default" {
			t.Fatalf("suite id = %q, want %q", got.ID, "default")
		}
		if got.Description != "Baseline evaluation suite for clnku" {
			t.Fatalf("suite description = %q, want %q", got.Description, "Baseline evaluation suite for clnku")
		}
		if got.Mode != ModeMockProvider {
			t.Fatalf("suite mode = %q, want %q", got.Mode, ModeMockProvider)
		}
		if got.TrialsPerTask != 1 {
			t.Fatalf("trials_per_task = %d, want 1", got.TrialsPerTask)
		}
		if len(got.Tasks) != 1 || got.Tasks[0] != "001-basic-edit" {
			t.Fatalf("suite tasks = %#v, want [001-basic-edit]", got.Tasks)
		}
		if !got.FailurePolicy.StopOnFirstFailure {
			t.Fatal("failure policy stop_on_first_failure = false, want true")
		}
		if got.FailurePolicy.MaxFailedTasks != 1 {
			t.Fatalf("failure policy max_failed_tasks = %d, want 1", got.FailurePolicy.MaxFailedTasks)
		}
	})
}

func TestLoadTask(t *testing.T) {
	t.Run("malformed json", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "task.json")
		writeTestFile(t, path, "{")

		if _, err := LoadTask(path); err == nil {
			t.Fatal("LoadTask() error = nil, want parse failure")
		}
	})

	t.Run("missing required fields", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "task.json")
		writeTestFile(t, path, `{"id":"001-basic-edit","graders":{"outcome_workspace_snapshot":{"enabled":true,"required":true},"transcript_command_trace":{"enabled":true,"required":false}}}`)

		if _, err := LoadTask(path); err == nil {
			t.Fatal("LoadTask() error = nil, want validation failure")
		}
	})

	t.Run("missing required grader fields", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "task.json")
		writeTestFile(t, path, `{
  "id": "001-basic-edit",
  "instruction_file": "input/instruction.txt",
  "scripted_turns_file": "input/model-turns.json",
  "working_directory": "workspace",
  "full_send": true,
  "step_limit": 10,
  "graders": {
    "outcome_workspace_snapshot": {
      "required": true
    },
    "transcript_command_trace": {
      "enabled": true,
      "required": false
    }
  }
}`)

		if _, err := LoadTask(path); err == nil {
			t.Fatal("LoadTask() error = nil, want grader validation failure")
		}
	})

	t.Run("invalid task mode value", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "task.json")
		writeTestFile(t, path, `{
  "id": "001-basic-edit",
  "instruction_file": "input/instruction.txt",
  "scripted_turns_file": "input/model-turns.json",
  "working_directory": "workspace",
  "mode": "bogus",
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
}`)

		if _, err := LoadTask(path); err == nil {
			t.Fatal("LoadTask() error = nil, want invalid mode failure")
		}
	})

	t.Run("loads canonical fixture", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "task.json")
		writeTestFile(t, path, `{
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
}`)

		got, err := LoadTask(path)
		if err != nil {
			t.Fatalf("LoadTask(): %v", err)
		}
		if got.ID != "001-basic-edit" {
			t.Fatalf("task id = %q, want %q", got.ID, "001-basic-edit")
		}
		if got.InstructionFile != "input/instruction.txt" {
			t.Fatalf("instruction_file = %q, want %q", got.InstructionFile, "input/instruction.txt")
		}
		if got.ScriptedTurnsFile != "input/model-turns.json" {
			t.Fatalf("scripted_turns_file = %q, want %q", got.ScriptedTurnsFile, "input/model-turns.json")
		}
		if got.WorkingDirectory != "workspace" {
			t.Fatalf("working_directory = %q, want %q", got.WorkingDirectory, "workspace")
		}
		if !got.FullSend {
			t.Fatal("full_send = false, want true")
		}
		if got.StepLimit != 10 {
			t.Fatalf("step_limit = %d, want 10", got.StepLimit)
		}
		if !got.Graders.OutcomeWorkspaceSnapshot.Enabled || !got.Graders.OutcomeWorkspaceSnapshot.Required {
			t.Fatalf("outcome_workspace_snapshot = %#v, want enabled+required", got.Graders.OutcomeWorkspaceSnapshot)
		}
		if !got.Graders.TranscriptCommandTrace.Enabled || got.Graders.TranscriptCommandTrace.Required {
			t.Fatalf("transcript_command_trace = %#v, want enabled and not required", got.Graders.TranscriptCommandTrace)
		}
	})

	t.Run("loads task with outcome_command_output grader", func(t *testing.T) {
		dir := t.TempDir()
		writeTestFile(t, filepath.Join(dir, "task.json"), `{
			"id": "cmd-test",
			"instruction_file": "input/instruction.txt",
			"working_directory": "workspace",
			"step_limit": 5,
			"full_send": true,
			"graders": {
				"outcome_workspace_snapshot": {"enabled": false, "required": false},
				"transcript_command_trace": {"enabled": false, "required": false},
				"outcome_command_output": {
					"enabled": true,
					"required": true,
					"command": ["go", "vet", "./..."],
					"expected_exit_code": 0,
					"stdout_contains": ["ok"],
					"stderr_must_not_contain": ["error"],
					"timeout_seconds": 60
				}
			}
		}`)
		task, err := LoadTask(filepath.Join(dir, "task.json"))
		if err != nil {
			t.Fatalf("LoadTask(): %v", err)
		}
		cfg := task.Graders.OutcomeCommandOutput
		if !cfg.Enabled || !cfg.Required {
			t.Fatalf("enabled=%v required=%v, want both true", cfg.Enabled, cfg.Required)
		}
		if len(cfg.Command) != 3 || cfg.Command[0] != "go" {
			t.Fatalf("command = %v, want [go vet ./...]", cfg.Command)
		}
		if cfg.TimeoutSeconds != 60 {
			t.Fatalf("timeout = %d, want 60", cfg.TimeoutSeconds)
		}
	})

	t.Run("loads task without outcome_command_output grader (backward compat)", func(t *testing.T) {
		dir := t.TempDir()
		writeTestFile(t, filepath.Join(dir, "task.json"), `{
			"id": "no-cmd",
			"instruction_file": "input/instruction.txt",
			"working_directory": "workspace",
			"step_limit": 5,
			"full_send": true,
			"graders": {
				"outcome_workspace_snapshot": {"enabled": true, "required": true},
				"transcript_command_trace": {"enabled": false, "required": false}
			}
		}`)
		task, err := LoadTask(filepath.Join(dir, "task.json"))
		if err != nil {
			t.Fatalf("LoadTask(): %v", err)
		}
		if task.Graders.OutcomeCommandOutput.Enabled {
			t.Fatal("command output grader should be disabled when absent from JSON")
		}
	})

	t.Run("rejects enabled command output grader with empty command", func(t *testing.T) {
		dir := t.TempDir()
		writeTestFile(t, filepath.Join(dir, "task.json"), `{
			"id": "bad-cmd",
			"instruction_file": "input/instruction.txt",
			"working_directory": "workspace",
			"step_limit": 5,
			"full_send": true,
			"graders": {
				"outcome_workspace_snapshot": {"enabled": false, "required": false},
				"transcript_command_trace": {"enabled": false, "required": false},
				"outcome_command_output": {
					"enabled": true,
					"required": true,
					"command": []
				}
			}
		}`)
		_, err := LoadTask(filepath.Join(dir, "task.json"))
		if err == nil {
			t.Fatal("LoadTask() error = nil, want command validation failure")
		}
	})

	t.Run("rejects task path escapes", func(t *testing.T) {
		tests := []struct {
			name               string
			instructionFile    string
			scriptedTurnsFile  string
			seedTranscriptFile string
			workingDirectory   string
			wantErrPart        string
		}{
			{
				name:              "instruction file escapes task root",
				instructionFile:   "../input/instruction.txt",
				scriptedTurnsFile: "input/model-turns.json",
				workingDirectory:  "workspace",
				wantErrPart:       `"instruction_file"`,
			},
			{
				name:              "scripted turns file escapes task root",
				instructionFile:   "input/instruction.txt",
				scriptedTurnsFile: "../input/model-turns.json",
				workingDirectory:  "workspace",
				wantErrPart:       `"scripted_turns_file"`,
			},
			{
				name:               "seed transcript file escapes task root",
				instructionFile:    "input/instruction.txt",
				scriptedTurnsFile:  "input/model-turns.json",
				seedTranscriptFile: "../input/seed.json",
				workingDirectory:   "workspace",
				wantErrPart:        `"seed_transcript_file"`,
			},
			{
				name:              "working directory escapes task root",
				instructionFile:   "input/instruction.txt",
				scriptedTurnsFile: "input/model-turns.json",
				workingDirectory:  "../workspace",
				wantErrPart:       `"working_directory"`,
			},
			{
				name:              "absolute instruction file rejected",
				instructionFile:   "/tmp/instruction.txt",
				scriptedTurnsFile: "input/model-turns.json",
				workingDirectory:  "workspace",
				wantErrPart:       `"instruction_file"`,
			},
		}

		for _, tt := range tests {
			tt := tt
			t.Run(tt.name, func(t *testing.T) {
				dir := t.TempDir()
				taskJSON := `{
  "id": "001-basic-edit",
  "instruction_file": "` + tt.instructionFile + `",
  "scripted_turns_file": "` + tt.scriptedTurnsFile + `",
  "working_directory": "` + tt.workingDirectory + `",
  "seed_transcript_file": "` + tt.seedTranscriptFile + `",
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
}`
				if tt.seedTranscriptFile == "" {
					taskJSON = strings.Replace(taskJSON, `  "seed_transcript_file": "",
`, "", 1)
				}
				writeTestFile(t, filepath.Join(dir, "task.json"), taskJSON)

				_, err := LoadTask(filepath.Join(dir, "task.json"))
				if err == nil {
					t.Fatal("LoadTask() error = nil, want validation failure")
				}
				if !strings.Contains(err.Error(), tt.wantErrPart) || !strings.Contains(err.Error(), "must stay within task root") {
					t.Fatalf("error = %v, want task-root validation for %s", err, tt.wantErrPart)
				}
			})
		}
	})

	t.Run("allows working directory dot", func(t *testing.T) {
		dir := t.TempDir()
		writeTestFile(t, filepath.Join(dir, "task.json"), `{
  "id": "001-basic-edit",
  "instruction_file": "input/instruction.txt",
  "scripted_turns_file": "input/model-turns.json",
  "working_directory": ".",
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
}`)

		task, err := LoadTask(filepath.Join(dir, "task.json"))
		if err != nil {
			t.Fatalf("LoadTask(): %v", err)
		}
		if task.WorkingDirectory != "." {
			t.Fatalf("working_directory = %q, want \".\"", task.WorkingDirectory)
		}
	})
}

func TestLoadSuiteTasks(t *testing.T) {
	t.Run("loads tasks in declared order", func(t *testing.T) {
		root := t.TempDir()
		writeTestFile(t, filepath.Join(root, "suite.json"), `{
  "id": "ordered",
  "description": "ordered suite",
  "mode": "mock-provider",
  "trials_per_task": 1,
  "failure_policy": {
    "stop_on_first_failure": true,
    "max_failed_tasks": 1
  },
  "tasks": ["b-task", "a-task"]
}`)
		writeTestFile(t, filepath.Join(root, "tasks", "a-task", "task.json"), `{
  "id": "a-task",
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
}`)
		writeTestFile(t, filepath.Join(root, "tasks", "b-task", "task.json"), `{
  "id": "b-task",
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
}`)

		suite, err := LoadSuite(filepath.Join(root, "suite.json"))
		if err != nil {
			t.Fatalf("LoadSuite(): %v", err)
		}
		got, err := LoadSuiteTasks(root, suite)
		if err != nil {
			t.Fatalf("LoadSuiteTasks(): %v", err)
		}
		if len(got) != 2 {
			t.Fatalf("task count = %d, want 2", len(got))
		}
		if got[0].ID != "b-task" || got[1].ID != "a-task" {
			t.Fatalf("task order = [%q, %q], want [b-task, a-task]", got[0].ID, got[1].ID)
		}
	})

	t.Run("resolves task file under tasks/id", func(t *testing.T) {
		root := t.TempDir()
		writeTestFile(t, filepath.Join(root, "suite.json"), `{
  "id": "single",
  "description": "single task suite",
  "mode": "mock-provider",
  "trials_per_task": 1,
  "failure_policy": {
    "stop_on_first_failure": true,
    "max_failed_tasks": 1
  },
  "tasks": ["001-basic-edit"]
}`)
		writeTestFile(t, filepath.Join(root, "tasks", "001-basic-edit", "task.json"), `{
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
}`)

		suite, err := LoadSuite(filepath.Join(root, "suite.json"))
		if err != nil {
			t.Fatalf("LoadSuite(): %v", err)
		}
		got, err := LoadSuiteTasks(root, suite)
		if err != nil {
			t.Fatalf("LoadSuiteTasks(): %v", err)
		}
		if len(got) != 1 || got[0].ID != "001-basic-edit" {
			t.Fatalf("tasks = %#v, want one task with id 001-basic-edit", got)
		}
	})

	t.Run("rejects task id mismatch", func(t *testing.T) {
		root := t.TempDir()
		writeTestFile(t, filepath.Join(root, "suite.json"), `{
  "id": "mismatch",
  "description": "mismatch suite",
  "mode": "mock-provider",
  "trials_per_task": 1,
  "failure_policy": {
    "stop_on_first_failure": true,
    "max_failed_tasks": 1
  },
  "tasks": ["wrong-id"]
}`)
		writeTestFile(t, filepath.Join(root, "tasks", "wrong-id", "task.json"), `{
  "id": "other-id",
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
}`)

		suite, err := LoadSuite(filepath.Join(root, "suite.json"))
		if err != nil {
			t.Fatalf("LoadSuite(): %v", err)
		}
		if _, err := LoadSuiteTasks(root, suite); err == nil {
			t.Fatal("LoadSuiteTasks() error = nil, want task id mismatch")
		}
	})

	t.Run("rejects duplicate task ids", func(t *testing.T) {
		root := t.TempDir()
		writeTestFile(t, filepath.Join(root, "suite.json"), `{
  "id": "duplicates",
  "description": "duplicate suite",
  "mode": "mock-provider",
  "trials_per_task": 1,
  "failure_policy": {
    "stop_on_first_failure": true,
    "max_failed_tasks": 1
  },
  "tasks": ["dup", "dup"]
}`)
		writeTestFile(t, filepath.Join(root, "tasks", "dup", "task.json"), `{
  "id": "dup",
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
}`)

		suite, err := LoadSuite(filepath.Join(root, "suite.json"))
		if err != nil {
			t.Fatalf("LoadSuite(): %v", err)
		}
		if _, err := LoadSuiteTasks(root, suite); err == nil {
			t.Fatal("LoadSuiteTasks() error = nil, want duplicate task id failure")
		}
	})

	t.Run("rejects path escape task ids", func(t *testing.T) {
		root := t.TempDir()
		writeTestFile(t, filepath.Join(root, "suite.json"), `{
  "id": "escape",
  "description": "escape suite",
  "mode": "mock-provider",
  "trials_per_task": 1,
  "failure_policy": {
    "stop_on_first_failure": true,
    "max_failed_tasks": 1
  },
  "tasks": ["../escaped"]
}`)

		suite, err := LoadSuite(filepath.Join(root, "suite.json"))
		if err != nil {
			t.Fatalf("LoadSuite(): %v", err)
		}
		if _, err := LoadSuiteTasks(root, suite); err == nil {
			t.Fatal("LoadSuiteTasks() error = nil, want path escape failure")
		}
	})

	t.Run("rejects mock-provider task missing scripted_turns_file when effective mode is mock-provider", func(t *testing.T) {
		root := t.TempDir()
		writeTestFile(t, filepath.Join(root, "suite.json"), `{
  "id": "mock-provider",
  "description": "mock-provider suite",
  "mode": "mock-provider",
  "trials_per_task": 1,
  "failure_policy": {
    "stop_on_first_failure": true,
    "max_failed_tasks": 1
  },
  "tasks": ["001-basic-edit"]
}`)
		writeTestFile(t, filepath.Join(root, "tasks", "001-basic-edit", "task.json"), `{
  "id": "001-basic-edit",
  "instruction_file": "input/instruction.txt",
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
}`)

		suite, err := LoadSuite(filepath.Join(root, "suite.json"))
		if err != nil {
			t.Fatalf("LoadSuite(): %v", err)
		}
		if _, err := LoadSuiteTasks(root, suite); err == nil {
			t.Fatal("LoadSuiteTasks() error = nil, want scripted_turns_file failure")
		}
	})
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %q: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %q: %v", path, err)
	}
}
