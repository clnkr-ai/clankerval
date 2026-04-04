package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/clnkr-ai/clankerval/internal/protocol"
	"github.com/clnkr-ai/clankerval/internal/transcript"
)

const (
	fixtureCommand = "printf 'hello\\n' > note.txt"
	fixtureSummary = "fixture task completed"
)

type eventEnvelope struct {
	Type    string `json:"type"`
	Payload any    `json:"payload"`
}

type commandStartPayload struct {
	Command string `json:"command"`
	Dir     string `json:"dir"`
}

type commandDonePayload struct {
	Command  string `json:"command"`
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	ExitCode int    `json:"exit_code"`
	Err      string `json:"err,omitempty"`
}

type chatCompletionRequest struct {
	Model    string             `json:"model"`
	Messages []protocol.Message `json:"messages"`
}

type chatCompletionResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
}

type commandResult struct {
	Command  string
	Stdout   string
	Stderr   string
	ExitCode int
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("evalfixture-agent", flag.ContinueOnError)
	flags.SetOutput(stderr)

	var (
		taskPrompt     = flags.String("p", "", "task to run")
		eventLogPath   = flags.String("event-log", "", "write event log")
		trajectoryPath = flags.String("trajectory", "", "write trajectory")
		maxSteps       = flags.Int("max-steps", 10, "step limit")
		fullSend       = flags.Bool("full-send", false, "accepted for compatibility")
		loadMessages   = flags.String("load-messages", "", "seed messages")
		dumpPrompt     = flags.Bool("dump-system-prompt", false, "print system prompt")
	)
	if err := flags.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		_, _ = fmt.Fprintf(stderr, "Error: %v\n", err)
		return 1
	}
	if flags.NArg() != 0 {
		_, _ = fmt.Fprintf(stderr, "Error: unexpected arguments: %s\n", strings.Join(flags.Args(), " "))
		return 1
	}

	cwd, err := os.Getwd()
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "Error: get working directory: %v\n", err)
		return 1
	}
	systemPrompt := buildSystemPrompt(cwd)
	if *dumpPrompt {
		_, _ = fmt.Fprint(stdout, systemPrompt)
		return 0
	}
	if strings.TrimSpace(*taskPrompt) == "" {
		_, _ = fmt.Fprintln(stderr, "Error: -p is required")
		return 1
	}
	if *maxSteps < 1 {
		_, _ = fmt.Fprintf(stderr, "Error: --max-steps must be >= 1, got %d\n", *maxSteps)
		return 1
	}
	_ = fullSend

	messages, err := loadSeedMessages(*loadMessages)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "Error: %v\n", err)
		return 1
	}

	agentCWD := cwd
	if restored, ok := latestStateCwd(messages); ok {
		agentCWD = restored
	}
	if err := ensureDir(agentCWD); err != nil {
		_, _ = fmt.Fprintf(stderr, "Error: %v\n", err)
		return 1
	}

	messages = append(messages, protocol.Message{Role: "user", Content: *taskPrompt})
	messages = appendStateIfNeeded(messages, agentCWD)

	actContent, err := nextAssistantTurn(systemPrompt, messages)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "Error: %v\n", err)
		return 1
	}
	messages = append(messages, protocol.Message{Role: "assistant", Content: actContent})

	actTurn, err := protocol.ParseTurn(actContent)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "Error: parse fixture act turn: %v\n", err)
		return 1
	}
	act, ok := actTurn.(*protocol.ActTurn)
	if !ok {
		_, _ = fmt.Fprintf(stderr, "Error: fixture act turn type %T\n", actTurn)
		return 1
	}

	events := make([]json.RawMessage, 0, 2)
	startEvent, err := json.Marshal(eventEnvelope{
		Type:    "command_start",
		Payload: commandStartPayload{Command: act.Command, Dir: agentCWD},
	})
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "Error: marshal command_start: %v\n", err)
		return 1
	}
	events = append(events, startEvent)

	result, nextCWD, runErr := runCommand(agentCWD, act.Command)
	donePayload := commandDonePayload{
		Command:  result.Command,
		Stdout:   result.Stdout,
		Stderr:   result.Stderr,
		ExitCode: result.ExitCode,
	}
	if runErr != nil {
		donePayload.Err = runErr.Error()
	}
	doneEvent, err := json.Marshal(eventEnvelope{Type: "command_done", Payload: donePayload})
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "Error: marshal command_done: %v\n", err)
		return 1
	}
	events = append(events, doneEvent)

	messages = append(messages, protocol.Message{Role: "user", Content: transcript.FormatCommandResult(transcript.CommandResult{
		Command:  result.Command,
		Stdout:   result.Stdout,
		Stderr:   result.Stderr,
		ExitCode: result.ExitCode,
	})})
	if nextCWD != agentCWD {
		messages = append(messages, protocol.Message{Role: "user", Content: transcript.FormatStateMessage(nextCWD)})
		agentCWD = nextCWD
	}

	doneContent, err := nextAssistantTurn(systemPrompt, messages)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "Error: %v\n", err)
		return 1
	}
	messages = append(messages, protocol.Message{Role: "assistant", Content: doneContent})

	if err := writeTrajectory(*trajectoryPath, messages); err != nil {
		_, _ = fmt.Fprintf(stderr, "Error: %v\n", err)
		return 1
	}
	if err := writeEventLog(*eventLogPath, events); err != nil {
		_, _ = fmt.Fprintf(stderr, "Error: %v\n", err)
		return 1
	}
	return 0
}

func buildSystemPrompt(cwd string) string {
	parts := []string{"clankerval eval fixture"}
	for _, path := range []string{
		filepath.Join(os.Getenv("HOME"), "AGENTS.md"),
		filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "clnkr", "AGENTS.md"),
		filepath.Join(cwd, "AGENTS.md"),
	} {
		if text, ok := readOptionalText(path); ok {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "\n\n")
}

func readOptionalText(path string) (string, bool) {
	if path == "" {
		return "", false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	text := strings.TrimSpace(string(data))
	if text == "" {
		return "", false
	}
	return text, true
}

func loadSeedMessages(path string) ([]protocol.Message, error) {
	if path == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read seed transcript: %w", err)
	}
	var messages []protocol.Message
	if err := json.Unmarshal(data, &messages); err != nil {
		return nil, fmt.Errorf("parse seed transcript: %w", err)
	}
	return messages, nil
}

func latestStateCwd(messages []protocol.Message) (string, bool) {
	transcriptMessages := make([]transcript.Message, 0, len(messages))
	for _, message := range messages {
		transcriptMessages = append(transcriptMessages, transcript.Message{
			Role:    message.Role,
			Content: message.Content,
		})
	}
	return transcript.ExtractLatestCwd(transcriptMessages)
}

func appendStateIfNeeded(messages []protocol.Message, cwd string) []protocol.Message {
	if latest, ok := latestStateCwd(messages); ok && latest == cwd {
		return messages
	}
	return append(messages, protocol.Message{Role: "user", Content: transcript.FormatStateMessage(cwd)})
}

func nextAssistantTurn(systemPrompt string, messages []protocol.Message) (string, error) {
	baseURL := strings.TrimRight(os.Getenv("CLNKR_BASE_URL"), "/")
	if baseURL == "" {
		if len(messages) == 0 {
			return "", errors.New("fixture messages unexpectedly empty")
		}
		last := messages[len(messages)-1]
		if last.Role == "user" && strings.Contains(last.Content, "[command]") {
			return `{"type":"done","summary":"` + fixtureSummary + `"}`, nil
		}
		return `{"type":"act","command":"` + fixtureCommand + `"}`, nil
	}

	reqBody, err := json.Marshal(chatCompletionRequest{
		Model:    getenvDefault("CLNKR_MODEL", "test-model"),
		Messages: append([]protocol.Message{{Role: "system", Content: systemPrompt}}, messages...),
	})
	if err != nil {
		return "", fmt.Errorf("marshal mock-provider request: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, baseURL+"/chat/completions", bytes.NewReader(reqBody))
	if err != nil {
		return "", fmt.Errorf("build mock-provider request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if apiKey := os.Getenv("CLNKR_API_KEY"); apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("query mock-provider: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read mock-provider response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("query mock-provider status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var decoded chatCompletionResponse
	if err := json.Unmarshal(body, &decoded); err != nil {
		return "", fmt.Errorf("decode mock-provider response: %w", err)
	}
	if len(decoded.Choices) == 0 {
		return "", errors.New("decode mock-provider response: no choices")
	}
	return decoded.Choices[0].Message.Content, nil
}

func runCommand(cwd, command string) (commandResult, string, error) {
	cmd := exec.Command("sh", "-lc", command)
	cmd.Dir = cwd
	cmd.Env = os.Environ()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	runErr := cmd.Run()
	exitCode := 0
	if runErr != nil {
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			exitCode = exitErr.ExitCode()
		} else {
			return commandResult{}, cwd, fmt.Errorf("run command: %w", runErr)
		}
	}

	return commandResult{
		Command:  command,
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		ExitCode: exitCode,
	}, cwd, nil
}

func writeTrajectory(path string, messages []protocol.Message) error {
	if path == "" {
		return nil
	}
	data, err := json.MarshalIndent(messages, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal trajectory: %w", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write trajectory: %w", err)
	}
	return nil
}

func writeEventLog(path string, events []json.RawMessage) error {
	if path == "" {
		return nil
	}
	var data []byte
	for _, event := range events {
		data = append(data, event...)
		data = append(data, '\n')
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write event log: %w", err)
	}
	return nil
}

func ensureDir(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("stat working directory %q: %w", path, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("working directory %q is not a directory", path)
	}
	return nil
}

func getenvDefault(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
