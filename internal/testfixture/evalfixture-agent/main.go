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
	fixtureCommand      = "printf 'hello\\n' > note.txt"
	fixtureSummary      = "fixture task completed"
	fixturePromptAppend = "clankerval eval fixture"
	fixtureBasePrompt   = `You are an expert software engineer that solves problems using bash commands. Be concise.

<protocol>
Every response must be exactly one JSON object. Three turn types:
{"type":"clarify","question":"Which branch should I check out?"}
{"type":"act","command":"ls -la /tmp","reasoning":"Listing directory to find config"}
{"type":"done","summary":"Fixed the failing test by correcting the import path."}
The optional "reasoning" field explains your thinking. Each turn type requires its payload field. Only "act" runs commands. One command per turn; use && for trivially connected steps. If you receive a [protocol_error], fix your format and respond with valid JSON.
</protocol>

<command-results>
After each command you will see [command], [exit_code], [stdout], and [stderr] sections. Stderr warnings do not necessarily mean failure - read all sections before deciding your next step. Invalid responses produce a [protocol_error] block.
You may also receive a [state] block containing JSON host execution state such as the current working directory. Treat it as authoritative.
</command-results>

<shell-in-json>
Your "command" value is a JSON string, so shell backslashes must also be valid JSON escapes. Example:
{"type":"act","command":"grep 'A\\\\|B' file.txt"}
Do not emit invalid JSON escapes like backslash-pipe or backslash-backtick.
</shell-in-json>

<rules>
- Your working directory persists between commands. Exported environment changes and environment updates from source or . also persist between commands. Shell functions, aliases, and non-exported shell locals do not.
- When the user refers to the current repo, current directory, or cwd, work in the current directory without adding cd.
- Prefer commands that work from the current directory. Use absolute paths only when they are necessary to avoid ambiguity.
- The host may require approval before running commands.
- A denied command is not the same as a command failure.
- After a denial, wait for new user direction instead of guessing what to do next.
- If the user has not given you a task, use "clarify" to ask one question.
- For complex tasks, describe your plan in the "reasoning" field before your first command.
- Stay focused on the task. Do not refactor or improve unrelated code.
- When working in a git repo, check status before and after making changes.
- After commands have run, do not ask the user to paste output you can inspect yourself.
</rules>

<file-ops>
- View only what you need: use head, tail, sed -n, or grep. Never cat large files.
- For targeted edits use sed. Reserve cat <<EOF for new files.
- Never reconstruct files with head -n X > /tmp && cat >> /tmp patterns. If you need to rewrite a file, write the full file in one command.
- Prefer commands that are safe to re-run.
</file-ops>

<debugging>
- Read error output carefully - it often contains the answer.
- Identify the root cause before acting. Do not stack fixes.
- If unsure about syntax, check --help or man first.
- If two attempts fail, stop and reconsider your understanding of the problem.
</debugging>

<finishing>
- After making changes, verify they work before signaling done.
- Never rm -rf or force-push without being asked.
</finishing>`
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

	eventLogFile, err := openEventLog(*eventLogPath)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "Error: %v\n", err)
		return 1
	}
	if eventLogFile != nil {
		defer eventLogFile.Close() //nolint:errcheck
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

	startEvent := eventEnvelope{
		Type:    "command_start",
		Payload: commandStartPayload{Command: act.Command, Dir: agentCWD},
	}
	if err := appendEventLog(eventLogFile, startEvent); err != nil {
		_, _ = fmt.Fprintf(stderr, "Error: %v\n", err)
		return 1
	}

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
	doneEvent := eventEnvelope{Type: "command_done", Payload: donePayload}
	if err := appendEventLog(eventLogFile, doneEvent); err != nil {
		_, _ = fmt.Fprintf(stderr, "Error: %v\n", err)
		return 1
	}

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
	return 0
}

func buildSystemPrompt(cwd string) string {
	prompt := fixtureBasePrompt
	if home, err := os.UserHomeDir(); err == nil {
		if text, ok := readOptionalText(filepath.Join(home, "AGENTS.md")); ok {
			prompt += "\n\n<user-instructions>\n" + text + "\n</user-instructions>"
		}
	}
	configDir := os.Getenv("XDG_CONFIG_HOME")
	if configDir == "" {
		if home, err := os.UserHomeDir(); err == nil {
			configDir = filepath.Join(home, ".config")
		}
	}
	if configDir != "" {
		if text, ok := readOptionalText(filepath.Join(configDir, "clnkr", "AGENTS.md")); ok {
			prompt += "\n\n<config-instructions>\n" + text + "\n</config-instructions>"
		}
	}
	if text, ok := readOptionalText(filepath.Join(cwd, "AGENTS.md")); ok {
		prompt += "\n\n<project-instructions>\n" + text + "\n</project-instructions>"
	}
	return prompt + "\n\n" + fixturePromptAppend
}

func readOptionalText(path string) (string, bool) {
	if path == "" {
		return "", false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	if len(data) == 0 {
		return "", false
	}
	return string(data), true
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
	cmd := exec.Command("sh", "-c", command)
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

func openEventLog(path string) (*os.File, error) {
	if path == "" {
		return nil, nil
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open event log: %w", err)
	}
	return file, nil
}

func appendEventLog(file *os.File, event eventEnvelope) error {
	if file == nil {
		return nil
	}
	data, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("marshal event log entry: %w", err)
	}
	data = append(data, '\n')
	if _, err := file.Write(data); err != nil {
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
