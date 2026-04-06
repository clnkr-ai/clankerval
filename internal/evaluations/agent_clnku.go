package evaluations

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/clnkr-ai/clankerval/internal/protocol"
	"github.com/clnkr-ai/clankerval/internal/transcript"
)

// clnkuAdapter implements AgentAdapter for the clnku agent binary.
type clnkuAdapter struct{}

func (a *clnkuAdapter) Run(ctx context.Context, req AdapterRequest) (AdapterResult, error) {
	// Stage project AGENTS.md into workspace for non-in-place tasks.
	if req.Task.WorkingDirectory != "." {
		if err := copyProjectAgents(filepath.Join(req.TaskRoot, "input", "project"), req.WorkspaceDir); err != nil {
			return AdapterResult{}, fmt.Errorf("copy project AGENTS: %w", err)
		}
	}

	// Dump system prompt.
	systemPrompt, stderrOut, exitCode, err := runCommand(ctx, req.WorkspaceDir, req.Env, req.BinaryPath, "--dump-system-prompt")
	if err != nil {
		return AdapterResult{}, fmt.Errorf("dump system prompt: %w", err)
	}
	if exitCode != 0 {
		return AdapterResult{}, fmt.Errorf("dump system prompt exit code %d: %s", exitCode, strings.TrimSpace(stderrOut))
	}

	// Read instruction file.
	instructionBytes, err := os.ReadFile(filepath.Join(req.TaskRoot, req.Task.InstructionFile))
	if err != nil {
		return AdapterResult{}, fmt.Errorf("read instruction file: %w", err)
	}

	// Create temp paths for event log and trajectory.
	eventLogPath, err := createTempFilePath(req.TrialRoot, "events-*.jsonl")
	if err != nil {
		return AdapterResult{}, fmt.Errorf("create event log path: %w", err)
	}
	trajectoryPath, err := createTempFilePath(req.TrialRoot, "messages-*.json")
	if err != nil {
		return AdapterResult{}, fmt.Errorf("create trajectory path: %w", err)
	}

	// Build agent args.
	args := []string{
		"-p", strings.TrimSpace(string(instructionBytes)),
		"--event-log", eventLogPath,
		"--trajectory", trajectoryPath,
		"--max-steps", fmt.Sprintf("%d", req.Task.StepLimit),
	}
	if req.Task.FullSend {
		args = append(args, "--full-send")
	}

	// Prepare seed transcript if configured.
	if req.Task.SeedTranscriptFile != "" {
		seedPath, err := writeSeedMessages(req.TrialRoot, filepath.Join(req.TaskRoot, req.Task.SeedTranscriptFile), req.TrialRoot, req.WorkspaceDir, req.HomeDir, req.ConfigDir, req.StateDir)
		if err != nil {
			return AdapterResult{}, fmt.Errorf("prepare seed transcript: %w", err)
		}
		args = append(args, "--load-messages", seedPath)
	}

	// Launch the agent.
	_, _, exitCode, err = runCommand(ctx, req.WorkspaceDir, req.Env, req.BinaryPath, args...)
	if err != nil {
		return AdapterResult{}, fmt.Errorf("run task: %w", err)
	}

	// Read raw artifacts.
	trajectoryBytes, err := os.ReadFile(trajectoryPath)
	if err != nil {
		return AdapterResult{}, fmt.Errorf("read trajectory: %w", err)
	}
	eventLogBytes, err := os.ReadFile(eventLogPath)
	if err != nil {
		return AdapterResult{}, fmt.Errorf("read event log: %w", err)
	}

	trajectory := string(trajectoryBytes)
	eventLog := string(eventLogBytes)

	// Translate clnku native outputs into generic fields.
	commands, err := extractClnkuCommands(eventLog)
	if err != nil {
		return AdapterResult{}, fmt.Errorf("extract clnku commands: %w", err)
	}

	transcriptEvents, err := extractClnkuTranscriptEvents(trajectory)
	if err != nil {
		return AdapterResult{}, fmt.Errorf("extract clnku transcript events: %w", err)
	}

	rawArtifacts := []RawAgentArtifact{
		{Name: "trajectory.json", Content: trajectoryBytes},
		{Name: "events.jsonl", Content: eventLogBytes},
	}

	return AdapterResult{
		ExitCode:          exitCode,
		SystemPrompt:      systemPrompt,
		AgentCommand:      append([]string{req.BinaryPath}, args...),
		Trajectory:        trajectory,
		EventLog:          eventLog,
		TranscriptEvents:  transcriptEvents,
		Commands:          commands,
		RawAgentArtifacts: rawArtifacts,
	}, nil
}

// extractClnkuCommands parses the clnku event log and returns generic CommandRecords.
func extractClnkuCommands(eventLog string) ([]CommandRecord, error) {
	starts, dones, err := parseCommandLifecycleEvents(eventLog)
	if err != nil {
		return nil, err
	}
	commands := make([]CommandRecord, 0, len(dones))
	for i, done := range dones {
		rec := CommandRecord{
			Command:  done.Command,
			Stdout:   done.Stdout,
			Stderr:   done.Stderr,
			ExitCode: done.ExitCode,
		}
		if i < len(starts) {
			rec.Dir = starts[i].Dir
		}
		commands = append(commands, rec)
	}
	return commands, nil
}

// clnkuTrajectoryMessage mirrors the trajectory JSON structure for extraction.
type clnkuTrajectoryMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// extractClnkuTranscriptEvents parses the clnku trajectory JSON and returns
// generic TranscriptEvents with source-format details already extracted so
// normalization does not need to re-parse clnku-specific payloads.
func extractClnkuTranscriptEvents(trajectory string) ([]TranscriptEvent, error) {
	var messages []clnkuTrajectoryMessage
	if err := json.Unmarshal([]byte(trajectory), &messages); err != nil {
		return nil, fmt.Errorf("parse clnku trajectory: %w", err)
	}

	events := make([]TranscriptEvent, 0, len(messages))
	for _, msg := range messages {
		ev := TranscriptEvent{
			Index: len(events),
			Role:  msg.Role,
		}
		switch msg.Role {
		case "system":
			ev.Kind = "system_prompt"
			ev.Content = msg.Content
		case "assistant":
			turn, err := protocol.ParseTurn(msg.Content)
			if err != nil {
				ev.Kind = "assistant_turn"
				ev.Content = msg.Content
			} else {
				switch t := turn.(type) {
				case *protocol.ActTurn:
					ev.Kind = "assistant_turn"
					ev.TurnType = "act"
					ev.Content = msg.Content
				case *protocol.ClarifyTurn:
					ev.Kind = "clarification"
					ev.TurnType = "clarify"
					ev.Content = t.Question
				case *protocol.DoneTurn:
					ev.Kind = "completion"
					ev.TurnType = "done"
					ev.Content = t.Summary
				default:
					ev.Kind = "assistant_turn"
					ev.Content = msg.Content
				}
			}
		case "user":
			if isCommandResultMessage(msg.Content) {
				ev.Kind = "command_result"
				envelope, _ := parseCommandResultEnvelope(msg.Content)
				ev.Command = envelope.Command
				ev.Content = msg.Content
			} else if isStateUpdateMessage(msg.Content) {
				ev.Kind = "state_update"
				cwd, _ := transcript.ExtractStateCwd(msg.Content)
				ev.Cwd = cwd
				ev.Content = msg.Content
			} else {
				ev.Kind = "user_instruction"
				ev.Content = msg.Content
			}
		default:
			ev.Kind = "unknown"
			ev.Content = msg.Content
		}
		events = append(events, ev)
	}
	return events, nil
}

// clnku event-log and trajectory parsing types — used only by the clnku adapter.

type eventEnvelope struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

type commandStartEvent struct {
	Command string `json:"command"`
	Dir     string `json:"dir"`
}

type commandDoneEvent struct {
	Command  string `json:"command"`
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	ExitCode int    `json:"exit_code"`
	Err      string `json:"err,omitempty"`
}

type commandResultEnvelope struct {
	Command  string
	Stdout   string
	Stderr   string
	ExitCode int
}

func parseCommandLifecycleEvents(raw string) ([]commandStartEvent, []commandDoneEvent, error) {
	starts := []commandStartEvent{}
	dones := []commandDoneEvent{}

	scanner := bufio.NewScanner(strings.NewReader(raw))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		var envelope eventEnvelope
		if err := json.Unmarshal([]byte(line), &envelope); err != nil {
			return nil, nil, fmt.Errorf("parse event log line: %w", err)
		}

		switch envelope.Type {
		case "command_start":
			var start commandStartEvent
			if err := json.Unmarshal(envelope.Payload, &start); err != nil {
				return nil, nil, fmt.Errorf("parse command_start payload: %w", err)
			}
			starts = append(starts, start)
		case "command_done":
			var done commandDoneEvent
			if err := json.Unmarshal(envelope.Payload, &done); err != nil {
				return nil, nil, fmt.Errorf("parse command_done payload: %w", err)
			}
			dones = append(dones, done)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, nil, fmt.Errorf("scan event log: %w", err)
	}

	return starts, dones, nil
}

func isStateUpdateMessage(content string) bool {
	_, ok := transcript.ExtractStateCwd(content)
	return ok
}

func isCommandResultMessage(content string) bool {
	_, ok := parseCommandResultEnvelope(content)
	return ok
}

func parseCommandResultEnvelope(content string) (commandResultEnvelope, bool) {
	command, ok := extractTaggedSection(content, "[command]", "[/command]")
	if !ok {
		return commandResultEnvelope{}, false
	}
	exitCodeStr, ok := extractTaggedSection(content, "[exit_code]", "[/exit_code]")
	if !ok {
		return commandResultEnvelope{}, false
	}
	stdout, ok := extractTaggedSection(content, "[stdout]", "[/stdout]")
	if !ok {
		return commandResultEnvelope{}, false
	}
	stderr, ok := extractTaggedSection(content, "[stderr]", "[/stderr]")
	if !ok {
		return commandResultEnvelope{}, false
	}

	exitCode, err := strconv.Atoi(strings.TrimSpace(exitCodeStr))
	if err != nil {
		return commandResultEnvelope{}, false
	}

	return commandResultEnvelope{
		Command:  html.UnescapeString(command),
		Stdout:   html.UnescapeString(stdout),
		Stderr:   html.UnescapeString(stderr),
		ExitCode: exitCode,
	}, true
}

func extractTaggedSection(content, openTag, closeTag string) (string, bool) {
	start := strings.Index(content, openTag)
	if start < 0 {
		return "", false
	}
	start += len(openTag)
	end := strings.Index(content[start:], closeTag)
	if end < 0 {
		return "", false
	}
	return strings.Trim(strings.TrimSpace(content[start:start+end]), "\n"), true
}
