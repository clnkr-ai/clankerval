package clnkusim

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// WriteSourceTree materializes a self-contained clnku test binary source tree.
func WriteSourceTree(root string) error {
	if err := os.MkdirAll(filepath.Join(root, "cmd", "clnku"), 0o755); err != nil {
		return fmt.Errorf("mkdir cmd/clnku: %w", err)
	}
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte(goModFile), 0o644); err != nil {
		return fmt.Errorf("write go.mod: %w", err)
	}
	if err := os.WriteFile(filepath.Join(root, "cmd", "clnku", "main.go"), []byte(strings.ReplaceAll(mainFile, "§", "`")), 0o644); err != nil {
		return fmt.Errorf("write cmd/clnku/main.go: %w", err)
	}
	return nil
}

// BuildBinary builds the self-contained clnku test binary at outputPath.
func BuildBinary(outputPath string) error {
	buildRoot, err := os.MkdirTemp("", "clankerval-clnkusim-src-*")
	if err != nil {
		return fmt.Errorf("create build root: %w", err)
	}
	defer func() {
		_ = os.RemoveAll(buildRoot)
	}()

	if err := WriteSourceTree(buildRoot); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(outputPath), 0o755); err != nil {
		return fmt.Errorf("mkdir output dir: %w", err)
	}

	cmd := exec.Command("go", "build", "-o", outputPath, "./cmd/clnku")
	cmd.Dir = buildRoot
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("build clnku test binary: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

const goModFile = `module example.com/clnkusim

go 1.22
`

const mainFile = `package main

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
)

type message struct {
	Role    string §json:"role"§
	Content string §json:"content"§
}

type chatCompletionRequest struct {
	Model    string    §json:"model"§
	Messages []message §json:"messages"§
}

type chatCompletionResponse struct {
	Choices []struct {
		Message struct {
			Content string §json:"content"§
		} §json:"message"§
	} §json:"choices"§
}

type jsonEvent struct {
	Type    string §json:"type"§
	Payload any    §json:"payload"§
}

type turnEnvelope struct {
	Type     string §json:"type"§
	Command  string §json:"command,omitempty"§
	Question string §json:"question,omitempty"§
	Summary  string §json:"summary,omitempty"§
}

type commandResult struct {
	Command  string
	Stdout   string
	Stderr   string
	ExitCode int
}

func main() {
	var (
		task          = flag.String("p", "", "task")
		trajectory    = flag.String("trajectory", "", "trajectory path")
		eventLog      = flag.String("event-log", "", "event log path")
		maxSteps      = flag.Int("max-steps", 10, "max steps")
		loadMessages  = flag.String("load-messages", "", "seed transcript")
		dumpPrompt    = flag.Bool("dump-system-prompt", false, "dump system prompt")
		_             = flag.Bool("full-send", false, "accepted for compatibility")
	)
	flag.Parse()

	cwd, err := os.Getwd()
	if err != nil {
		fail(err)
	}
	prompt := buildSystemPrompt(cwd)

	if *dumpPrompt {
		fmt.Print(prompt)
		return
	}

	messages, err := loadTranscript(*loadMessages)
	if err != nil {
		fail(err)
	}
	messages = append(messages, message{Role: "user", Content: *task})

	events := make([][]byte, 0, 4)
	flush := func() {
		if err := writeTrajectory(*trajectory, messages); err != nil {
			fail(err)
		}
		if err := writeEventLog(*eventLog, events); err != nil {
			fail(err)
		}
	}
	defer flush()

	for step := 0; step < *maxSteps; step++ {
		content, err := queryModel(prompt, messages)
		if err != nil {
			fail(err)
		}
		messages = append(messages, message{Role: "assistant", Content: content})

		turn, err := parseTurn(content)
		if err != nil {
			fail(err)
		}

		switch turn.Type {
		case "act":
			if turn.Command == "" {
				fail(errors.New("act turn missing command"))
			}
			start, err := json.Marshal(jsonEvent{
				Type: "command_start",
				Payload: struct {
					Command string §json:"command"§
					Dir     string §json:"dir"§
				}{Command: turn.Command, Dir: cwd},
			})
			if err != nil {
				fail(err)
			}
			events = append(events, start)

			result, err := runCommand(cwd, turn.Command)
			if err != nil {
				fail(err)
			}
			done, err := json.Marshal(jsonEvent{
				Type: "command_done",
				Payload: struct {
					Command  string §json:"command"§
					Stdout   string §json:"stdout"§
					Stderr   string §json:"stderr"§
					ExitCode int    §json:"exit_code"§
				}{
					Command:  result.Command,
					Stdout:   result.Stdout,
					Stderr:   result.Stderr,
					ExitCode: result.ExitCode,
				},
			})
			if err != nil {
				fail(err)
			}
			events = append(events, done)

			messages = append(messages, message{Role: "user", Content: formatCommandResult(result)})
			messages = append(messages, message{Role: "user", Content: formatStateMessage(cwd)})
		case "done", "clarify":
			return
		default:
			fail(fmt.Errorf("unsupported turn type %q", turn.Type))
		}
	}

	fail(errors.New("step limit exceeded"))
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}

func buildSystemPrompt(cwd string) string {
	parts := []string{"clnku test stub"}
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

func loadTranscript(path string) ([]message, error) {
	if path == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read seed transcript: %w", err)
	}
	var messages []message
	if err := json.Unmarshal(data, &messages); err != nil {
		return nil, fmt.Errorf("parse seed transcript: %w", err)
	}
	return messages, nil
}

func queryModel(prompt string, messages []message) (string, error) {
	reqBody, err := json.Marshal(chatCompletionRequest{
		Model:    getenvDefault("CLNKR_MODEL", "test-model"),
		Messages: append([]message{{Role: "system", Content: prompt}}, messages...),
	})
	if err != nil {
		return "", fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, strings.TrimRight(os.Getenv("CLNKR_BASE_URL"), "/")+"/chat/completions", bytes.NewReader(reqBody))
	if err != nil {
		return "", fmt.Errorf("new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if apiKey := os.Getenv("CLNKR_API_KEY"); apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("query model: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read response body: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("query model status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var decoded chatCompletionResponse
	if err := json.Unmarshal(body, &decoded); err != nil {
		return "", fmt.Errorf("decode response: %w", err)
	}
	if len(decoded.Choices) == 0 {
		return "", errors.New("decode response: no choices")
	}
	return decoded.Choices[0].Message.Content, nil
}

func parseTurn(content string) (turnEnvelope, error) {
	var turn turnEnvelope
	if err := json.Unmarshal([]byte(strings.TrimSpace(content)), &turn); err != nil {
		return turnEnvelope{}, fmt.Errorf("parse turn: %w", err)
	}
	return turn, nil
}

func runCommand(cwd, command string) (commandResult, error) {
	cmd := exec.Command("sh", "-c", command)
	cmd.Dir = cwd
	cmd.Env = os.Environ()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	exitCode := 0
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitCode()
		} else {
			return commandResult{}, fmt.Errorf("run command: %w", err)
		}
	}

	return commandResult{
		Command:  command,
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		ExitCode: exitCode,
	}, nil
}

func writeTrajectory(path string, messages []message) error {
	if path == "" {
		return nil
	}
	data, err := json.Marshal(messages)
	if err != nil {
		return fmt.Errorf("marshal trajectory: %w", err)
	}
	return os.WriteFile(path, data, 0o644)
}

func writeEventLog(path string, events [][]byte) error {
	if path == "" {
		return nil
	}
	var data []byte
	for _, event := range events {
		data = append(data, event...)
		data = append(data, '\n')
	}
	return os.WriteFile(path, data, 0o644)
}

func formatCommandResult(result commandResult) string {
	replacer := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", "[", "&#91;", "]", "&#93;")
	var b strings.Builder
	fmt.Fprintf(&b, "[command]\n%s\n[/command]\n", replacer.Replace(result.Command))
	fmt.Fprintf(&b, "[exit_code]\n%d\n[/exit_code]\n", result.ExitCode)
	fmt.Fprintf(&b, "[stdout]\n%s\n[/stdout]\n", replacer.Replace(result.Stdout))
	fmt.Fprintf(&b, "[stderr]\n%s\n[/stderr]", replacer.Replace(result.Stderr))
	return b.String()
}

func formatStateMessage(cwd string) string {
	body, err := json.Marshal(struct {
		Source string §json:"source"§
		Kind   string §json:"kind"§
		Cwd    string §json:"cwd"§
	}{
		Source: "clnkr",
		Kind:   "state",
		Cwd:    cwd,
	})
	if err != nil {
		fail(err)
	}
	return fmt.Sprintf("[state]\n%s\n[/state]", body)
}

func getenvDefault(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
`
