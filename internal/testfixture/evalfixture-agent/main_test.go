package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildSystemPromptLayers(t *testing.T) {
	tempRoot := t.TempDir()
	homeDir := filepath.Join(tempRoot, "home")
	configDir := filepath.Join(tempRoot, "config")
	workspaceDir := filepath.Join(tempRoot, "workspace")
	for path, content := range map[string]string{
		filepath.Join(homeDir, "AGENTS.md"):            "home instructions\n",
		filepath.Join(configDir, "clnkr", "AGENTS.md"): "config instructions\n",
		filepath.Join(workspaceDir, "AGENTS.md"):       "workspace instructions\n",
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("MkdirAll(%q): %v", filepath.Dir(path), err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("WriteFile(%q): %v", path, err)
		}
	}

	t.Setenv("HOME", homeDir)
	t.Setenv("XDG_CONFIG_HOME", configDir)

	prompt := buildSystemPrompt(workspaceDir)
	if !strings.HasPrefix(prompt, fixtureBasePrompt) {
		t.Fatalf("prompt prefix mismatch; got %q", prompt[:min(len(prompt), 80)])
	}

	wantSections := []string{
		"<user-instructions>\nhome instructions\n\n</user-instructions>",
		"<config-instructions>\nconfig instructions\n\n</config-instructions>",
		"<project-instructions>\nworkspace instructions\n\n</project-instructions>",
		fixturePromptAppend,
	}
	lastIndex := -1
	for _, want := range wantSections {
		index := strings.Index(prompt, want)
		if index == -1 {
			t.Fatalf("prompt missing section %q in %q", want, prompt)
		}
		if index <= lastIndex {
			t.Fatalf("section %q appears out of order in %q", want, prompt)
		}
		lastIndex = index
	}
}

func TestBuildSystemPromptFallsBackToHomeDotConfig(t *testing.T) {
	tempRoot := t.TempDir()
	homeDir := filepath.Join(tempRoot, "home")
	workspaceDir := filepath.Join(tempRoot, "workspace")
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

	t.Setenv("HOME", homeDir)
	t.Setenv("XDG_CONFIG_HOME", "")

	prompt := buildSystemPrompt(workspaceDir)
	want := "<config-instructions>\nconfig fallback instructions\n\n</config-instructions>"
	if !strings.Contains(prompt, want) {
		t.Fatalf("prompt = %q, want config fallback section %q", prompt, want)
	}
}

func TestAppendEventLogAppendsJSONL(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "events.jsonl")

	file, err := openEventLog(path)
	if err != nil {
		t.Fatalf("openEventLog(first): %v", err)
	}
	if err := appendEventLog(file, eventEnvelope{
		Type:    "command_start",
		Payload: commandStartPayload{Command: "pwd", Dir: "/tmp/work"},
	}); err != nil {
		t.Fatalf("appendEventLog(first): %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("Close(first): %v", err)
	}

	file, err = openEventLog(path)
	if err != nil {
		t.Fatalf("openEventLog(second): %v", err)
	}
	if err := appendEventLog(file, eventEnvelope{
		Type: "command_done",
		Payload: commandDonePayload{
			Command:  "pwd",
			ExitCode: 0,
		},
	}); err != nil {
		t.Fatalf("appendEventLog(second): %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("Close(second): %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", path, err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 {
		t.Fatalf("event lines = %d, want 2", len(lines))
	}

	var got []eventEnvelope
	for _, line := range lines {
		var event eventEnvelope
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("json.Unmarshal(%q): %v", line, err)
		}
		got = append(got, event)
	}
	if got[0].Type != "command_start" || got[1].Type != "command_done" {
		t.Fatalf("event types = %#v, want command_start then command_done", got)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
