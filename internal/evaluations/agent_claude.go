package evaluations

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/clnkr-ai/clankerval/internal/protocol"
)

// claudeAdapter implements AgentAdapter for the Claude Code CLI.
type claudeAdapter struct{}

func (a *claudeAdapter) Run(ctx context.Context, req AdapterRequest) (AdapterResult, error) {
	claudePath, err := exec.LookPath("claude")
	if err != nil {
		return AdapterResult{}, fmt.Errorf("claude adapter: claude binary not found on PATH: %w", err)
	}

	// Stage project CLAUDE.md into the effective workspace for every Claude task.
	if err := copyProjectClaudeMD(filepath.Join(req.TaskRoot, "input", "project"), req.WorkspaceDir); err != nil {
		return AdapterResult{}, fmt.Errorf("copy project CLAUDE.md: %w", err)
	}

	// Read instruction file.
	instructionBytes, err := os.ReadFile(filepath.Join(req.TaskRoot, req.Task.InstructionFile))
	if err != nil {
		return AdapterResult{}, fmt.Errorf("read instruction file: %w", err)
	}
	instruction := strings.TrimSpace(string(instructionBytes))

	// Determine session ID for seed bootstrap or fresh run.
	sessionID := generateUUID()
	var seedBootstrapped bool

	if req.Task.SeedTranscriptFile != "" {
		// Bootstrap seed via real session mutation (proven path from Task 6).
		bootstrapID, err := bootstrapClaudeSeed(ctx, claudePath, req)
		if err != nil {
			return AdapterResult{}, fmt.Errorf("claude seed bootstrap: %w", err)
		}
		sessionID = bootstrapID
		seedBootstrapped = true
	}

	// Build Claude args.
	args := []string{
		"--bare",
		"--dangerously-skip-permissions",
		"--output-format", "json",
		"--add-dir", req.WorkspaceDir,
		"--max-turns", strconv.Itoa(req.Task.StepLimit),
	}

	if seedBootstrapped {
		args = append(args, "--resume", sessionID, "-p", instruction)
	} else {
		args = append(args, "--session-id", sessionID, "-p", instruction)
	}

	// Launch Claude with CWD = workspace (required for CLAUDE.md to be obeyed).
	stdout, stderr, exitCode, err := runCommand(ctx, req.WorkspaceDir, req.Env, claudePath, args...)
	if err != nil {
		return AdapterResult{}, fmt.Errorf("run claude: %w", err)
	}

	// Parse the JSON result from stdout.
	var result claudeResultPayload
	if parseErr := json.Unmarshal([]byte(stdout), &result); parseErr != nil {
		return AdapterResult{}, fmt.Errorf("parse claude JSON output (exit %d): %w\nstdout: %s\nstderr: %s",
			exitCode, parseErr, truncateStr(stdout, 500), truncateStr(stderr, 500))
	}

	if result.SessionID == "" && exitCode == 0 {
		return AdapterResult{}, fmt.Errorf("claude returned no session_id despite exit 0")
	}

	// Resolve and read the transcript JSONL.
	transcriptPath, err := resolveClaudeTranscriptPath(req.HomeDir, req.WorkspaceDir, result.SessionID)
	if err != nil {
		return AdapterResult{}, fmt.Errorf("resolve claude transcript: %w", err)
	}

	transcriptBytes, err := os.ReadFile(transcriptPath)
	if err != nil {
		return AdapterResult{}, fmt.Errorf("read claude transcript %q: %w", transcriptPath, err)
	}

	// Extract commands and transcript events from the native JSONL.
	commands, err := extractClaudeCommands(transcriptBytes)
	if err != nil {
		return AdapterResult{}, fmt.Errorf("extract claude commands: %w", err)
	}

	transcriptEvents, err := extractClaudeTranscriptEvents(transcriptBytes)
	if err != nil {
		return AdapterResult{}, fmt.Errorf("extract claude transcript events: %w", err)
	}

	agentVersion := resolveClaudeAgentVersion(ctx, req.WorkspaceDir, req.Env, claudePath, transcriptBytes)

	rawArtifacts := []RawAgentArtifact{
		{Name: "result.json", Content: []byte(stdout)},
		{Name: "transcript.jsonl", Content: transcriptBytes},
	}

	return AdapterResult{
		ExitCode:          exitCode,
		AgentVersion:      agentVersion,
		AgentCommand:      append([]string{claudePath}, args...),
		TranscriptEvents:  transcriptEvents,
		Commands:          commands,
		RawAgentArtifacts: rawArtifacts,
	}, nil
}

// copyProjectClaudeMD stages input/project/CLAUDE.md into workspace CLAUDE.md.
func copyProjectClaudeMD(srcDir, workspaceDir string) error {
	src := filepath.Join(srcDir, "CLAUDE.md")
	if _, err := os.Stat(src); err != nil {
		if os.IsNotExist(err) {
			wrongSrc := filepath.Join(srcDir, "AGENTS.md")
			if _, wrongErr := os.Stat(wrongSrc); wrongErr == nil {
				return fmt.Errorf("found %q but Claude tasks require %q", wrongSrc, src)
			} else if wrongErr != nil && !os.IsNotExist(wrongErr) {
				return fmt.Errorf("stat %q: %w", wrongSrc, wrongErr)
			}
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
	if err := os.WriteFile(filepath.Join(workspaceDir, "CLAUDE.md"), data, 0o644); err != nil {
		return fmt.Errorf("write workspace CLAUDE.md: %w", err)
	}
	return nil
}

// bootstrapClaudeSeed creates a real Claude session, injects seed messages
// into its transcript, and returns the session ID for resumption.
func bootstrapClaudeSeed(ctx context.Context, claudePath string, req AdapterRequest) (string, error) {
	// Step 1: Create a real session with a trivial prompt.
	args := []string{
		"--bare",
		"--dangerously-skip-permissions",
		"-p", "Say OK",
		"--output-format", "json",
		"--add-dir", req.WorkspaceDir,
	}
	stdout, _, exitCode, err := runCommand(ctx, req.WorkspaceDir, req.Env, claudePath, args...)
	if err != nil {
		return "", fmt.Errorf("create initial session: %w", err)
	}
	if exitCode != 0 {
		return "", fmt.Errorf("create initial session exit %d: %s", exitCode, truncateStr(stdout, 300))
	}

	var initResult claudeResultPayload
	if err := json.Unmarshal([]byte(stdout), &initResult); err != nil {
		return "", fmt.Errorf("parse initial session: %w", err)
	}
	if initResult.SessionID == "" {
		return "", fmt.Errorf("initial session has empty session_id")
	}

	// Step 2: Find the transcript.
	transcriptPath, err := resolveClaudeTranscriptPath(req.HomeDir, req.WorkspaceDir, initResult.SessionID)
	if err != nil {
		return "", fmt.Errorf("find initial transcript: %w", err)
	}

	// Step 3: Read seed messages from the shared format.
	seedPath := filepath.Join(req.TaskRoot, req.Task.SeedTranscriptFile)
	seedData, err := os.ReadFile(seedPath)
	if err != nil {
		return "", fmt.Errorf("read seed transcript %q: %w", seedPath, err)
	}

	// Apply template replacements.
	replacer := strings.NewReplacer(
		"__TMP__", req.TrialRoot,
		"__WORKDIR__", req.WorkspaceDir,
		"__HOME__", req.HomeDir,
		"__CONFIG__", req.ConfigDir,
		"__STATE__", req.StateDir,
	)
	seedData = []byte(replacer.Replace(string(seedData)))

	var seedMessages []protocol.Message
	if err := json.Unmarshal(seedData, &seedMessages); err != nil {
		return "", fmt.Errorf("parse seed messages: %w", err)
	}

	// Step 4: Find last UUID in existing transcript for chaining.
	lastUUID, err := findLastTranscriptUUID(transcriptPath)
	if err != nil {
		return "", fmt.Errorf("find last transcript UUID: %w", err)
	}

	// Step 5: Translate seed messages into Claude native JSONL and append.
	f, err := os.OpenFile(transcriptPath, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return "", fmt.Errorf("open transcript for append: %w", err)
	}
	defer f.Close()

	parentUUID := lastUUID
	now := time.Now().UTC()
	for i, msg := range seedMessages {
		entryUUID := generateUUID()
		ts := now.Add(time.Duration(i) * time.Second).Format(time.RFC3339Nano)

		var entry map[string]any
		if msg.Role == "user" {
			entry = map[string]any{
				"type":       "user",
				"uuid":       entryUUID,
				"parentUuid": parentUUID,
				"sessionId":  initResult.SessionID,
				"timestamp":  ts,
				"message":    map[string]any{"role": "user", "content": msg.Content},
				"userType":   "external",
			}
		} else {
			entry = map[string]any{
				"type":       "assistant",
				"uuid":       entryUUID,
				"parentUuid": parentUUID,
				"sessionId":  initResult.SessionID,
				"timestamp":  ts,
				"message": map[string]any{
					"role": "assistant",
					"content": []map[string]any{
						{"type": "text", "text": msg.Content},
					},
					"type":        "message",
					"stop_reason": "end_turn",
				},
				"userType": "external",
			}
		}

		line, err := json.Marshal(entry)
		if err != nil {
			return "", fmt.Errorf("marshal seed entry: %w", err)
		}
		if _, err := f.Write(append(line, '\n')); err != nil {
			return "", fmt.Errorf("write seed entry: %w", err)
		}
		parentUUID = entryUUID
	}

	return initResult.SessionID, nil
}

// claudeResultPayload is the minimal shape of claude --output-format json output.
type claudeResultPayload struct {
	Type      string  `json:"type"`
	Role      string  `json:"role"`
	SessionID string  `json:"session_id"`
	Result    string  `json:"result"`
	IsError   bool    `json:"is_error"`
	CostUSD   float64 `json:"cost_usd"`
}

// claudeTranscriptEntry represents one JSONL line from a Claude session transcript.
type claudeTranscriptEntry struct {
	Type        string          `json:"type"`
	IsSidechain bool            `json:"isSidechain"`
	Version     string          `json:"version,omitempty"`
	UUID        string          `json:"uuid"`
	ParentUUID  string          `json:"parentUuid"`
	SessionID   string          `json:"sessionId"`
	Message     json.RawMessage `json:"message"`
}

// claudeMessage represents a message within a Claude transcript entry.
type claudeMessage struct {
	Role    string               `json:"role"`
	Content claudeMessageContent `json:"content"`
	Model   string               `json:"model,omitempty"`
}

// claudeMessageContent handles content that can be either a string or an array.
type claudeMessageContent struct {
	Text  string
	Items []claudeContentItem
}

func (c *claudeMessageContent) UnmarshalJSON(data []byte) error {
	// Try string first.
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		c.Text = s
		return nil
	}
	// Try array of content items.
	var items []claudeContentItem
	if err := json.Unmarshal(data, &items); err == nil {
		c.Items = items
		return nil
	}
	return fmt.Errorf("claude message content: expected string or array, got: %s", truncateStr(string(data), 100))
}

// claudeContentItem represents one item in a Claude message content array.
type claudeContentItem struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	Name      string          `json:"name,omitempty"`
	ID        string          `json:"id,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	Content   json.RawMessage `json:"content,omitempty"`
	IsError   bool            `json:"is_error,omitempty"`
}

// claudeToolUseInput holds the parsed input for a Bash tool_use.
type claudeToolUseInput struct {
	Command string `json:"command"`
}

// claudeToolResultPayload holds the top-level toolUseResult field from a tool_result entry.
type claudeToolUseResultPayload struct {
	Stdout      string `json:"stdout"`
	Stderr      string `json:"stderr"`
	Interrupted bool   `json:"interrupted"`
	ExitCode    int    `json:"exitCode"`
}

// resolveClaudeTranscriptPath finds the transcript JSONL for a session under
// the harnessed HOME. Path is HOME/.claude/projects/<sanitized-cwd>/<session-id>.jsonl
// where sanitized-cwd has path separators replaced by hyphens. Real Claude
// paths preserve the leading hyphen from absolute working directories.
func resolveClaudeTranscriptPath(homeDir, workspaceDir, sessionID string) (string, error) {
	if sessionID == "" {
		return "", fmt.Errorf("empty session ID")
	}

	// Try the known sanitized-cwd convention first.
	sanitized := strings.ReplaceAll(workspaceDir, "/", "-")
	candidate := filepath.Join(homeDir, ".claude", "projects", sanitized, sessionID+".jsonl")
	if _, err := os.Stat(candidate); err == nil {
		return candidate, nil
	}

	// Fallback: walk the projects directory looking for the session file.
	projectsDir := filepath.Join(homeDir, ".claude", "projects")
	var found string
	err := filepath.Walk(projectsDir, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if !info.IsDir() && info.Name() == sessionID+".jsonl" {
			found = path
		}
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		return "", fmt.Errorf("walk claude projects dir: %w", err)
	}
	if found == "" {
		return "", fmt.Errorf("transcript for session %s not found under %s", sessionID, projectsDir)
	}
	return found, nil
}

// findLastTranscriptUUID reads a Claude JSONL transcript and returns the uuid
// of the last entry for parentUuid chaining.
func findLastTranscriptUUID(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open transcript: %w", err)
	}
	defer f.Close()

	var lastUUID string
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 256*1024), 1024*1024)
	for scanner.Scan() {
		var entry struct {
			UUID string `json:"uuid"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &entry); err == nil && entry.UUID != "" {
			lastUUID = entry.UUID
		}
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("scan transcript: %w", err)
	}
	if lastUUID == "" {
		return "", fmt.Errorf("no uuid found in transcript")
	}
	return lastUUID, nil
}

// extractClaudeCommands parses a Claude native JSONL transcript and returns
// CommandRecords for each Bash tool_use / tool_result pair, matched by tool_use_id.
func extractClaudeCommands(transcriptBytes []byte) ([]CommandRecord, error) {
	// First pass: collect all Bash tool_use entries keyed by ID.
	type bashToolUse struct {
		ID      string
		Command string
	}
	toolUses := map[string]bashToolUse{}
	var toolUseOrder []string

	// Second pass: collect tool_result entries keyed by tool_use_id.
	type toolResult struct {
		ToolUseID string
		Stdout    string
		Stderr    string
		ExitCode  int
		IsError   bool
	}
	toolResults := map[string]toolResult{}

	scanner := bufio.NewScanner(strings.NewReader(string(transcriptBytes)))
	scanner.Buffer(make([]byte, 0, 256*1024), 2*1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		var entry claudeTranscriptEntry
		if err := json.Unmarshal(line, &entry); err != nil {
			continue
		}
		if entry.IsSidechain {
			continue
		}

		if len(entry.Message) == 0 {
			continue
		}

		var msg claudeMessage
		if err := json.Unmarshal(entry.Message, &msg); err != nil {
			continue
		}

		for _, item := range msg.Content.Items {
			switch item.Type {
			case "tool_use":
				if item.Name != "Bash" {
					continue
				}
				var input claudeToolUseInput
				if err := json.Unmarshal(item.Input, &input); err != nil {
					continue
				}
				toolUses[item.ID] = bashToolUse{ID: item.ID, Command: input.Command}
				toolUseOrder = append(toolUseOrder, item.ID)

			case "tool_result":
				tr := toolResult{ToolUseID: item.ToolUseID, IsError: item.IsError}

				// Try to parse the content for exit code and output.
				if len(item.Content) > 0 {
					var contentStr string
					if err := json.Unmarshal(item.Content, &contentStr); err == nil {
						tr.Stdout = contentStr
						// Parse "Exit code N\n..." pattern from error results.
						if item.IsError && strings.HasPrefix(contentStr, "Exit code ") {
							fmt.Sscanf(contentStr, "Exit code %d", &tr.ExitCode)
						}
					}
				}

				// Parse the top-level toolUseResult if present.
				var rawEntry map[string]json.RawMessage
				if err := json.Unmarshal(line, &rawEntry); err == nil {
					if toolUseResultRaw, ok := rawEntry["toolUseResult"]; ok {
						// toolUseResult can be a string or an object.
						var resultObj claudeToolUseResultPayload
						if err := json.Unmarshal(toolUseResultRaw, &resultObj); err == nil {
							tr.Stdout = resultObj.Stdout
							tr.Stderr = resultObj.Stderr
							tr.ExitCode = resultObj.ExitCode
						} else {
							// It's a string (error case).
							var resultStr string
							if err := json.Unmarshal(toolUseResultRaw, &resultStr); err == nil {
								// Parse "Error: Exit code N\nout\nerr" pattern.
								if strings.HasPrefix(resultStr, "Error: Exit code ") {
									fmt.Sscanf(resultStr, "Error: Exit code %d", &tr.ExitCode)
									parts := strings.SplitN(resultStr, "\n", 4)
									if len(parts) >= 3 {
										tr.Stdout = parts[1]
										tr.Stderr = parts[2]
									}
								}
							}
						}
					}
				}

				toolResults[item.ToolUseID] = tr
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan transcript: %w", err)
	}

	// Build commands in tool_use order, matching by ID.
	commands := make([]CommandRecord, 0, len(toolUseOrder))
	for _, id := range toolUseOrder {
		tu := toolUses[id]
		rec := CommandRecord{Command: tu.Command}
		if tr, ok := toolResults[id]; ok {
			rec.Stdout = tr.Stdout
			rec.Stderr = tr.Stderr
			rec.ExitCode = tr.ExitCode
		}
		commands = append(commands, rec)
	}
	return commands, nil
}

// extractClaudeTranscriptEvents parses a Claude native JSONL transcript and
// returns generic TranscriptEvents for normalization and grading. Thinking
// blocks and sidechain events are excluded.
func extractClaudeTranscriptEvents(transcriptBytes []byte) ([]TranscriptEvent, error) {
	var events []TranscriptEvent

	scanner := bufio.NewScanner(strings.NewReader(string(transcriptBytes)))
	scanner.Buffer(make([]byte, 0, 256*1024), 2*1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		var entry claudeTranscriptEntry
		if err := json.Unmarshal(line, &entry); err != nil {
			continue
		}
		if entry.IsSidechain {
			continue
		}

		// Skip non-message entry types (system, config, etc.).
		if entry.Type != "user" && entry.Type != "assistant" {
			continue
		}

		if len(entry.Message) == 0 {
			continue
		}

		var msg claudeMessage
		if err := json.Unmarshal(entry.Message, &msg); err != nil {
			continue
		}

		switch msg.Role {
		case "user":
			// User message: could be instruction or tool_result.
			if msg.Content.Text != "" {
				events = append(events, TranscriptEvent{
					Index:   len(events),
					Kind:    "user_instruction",
					Role:    "user",
					Content: msg.Content.Text,
				})
				continue
			}
			// Check for tool_result items.
			for _, item := range msg.Content.Items {
				if item.Type == "tool_result" {
					events = append(events, TranscriptEvent{
						Index: len(events),
						Kind:  "command_result",
						Role:  "user",
					})
				}
			}

		case "assistant":
			hasToolUse := false
			for _, item := range msg.Content.Items {
				if item.Type == "tool_use" {
					hasToolUse = true
					break
				}
			}

			// Process content items, skipping thinking blocks.
			for _, item := range msg.Content.Items {
				switch item.Type {
				case "thinking":
					// Exclude thinking blocks.
					continue
				case "text":
					event := TranscriptEvent{
						Index:   len(events),
						Kind:    "completion",
						Role:    "assistant",
						Content: item.Text,
					}
					if hasToolUse {
						event.Kind = "assistant_turn"
					}
					events = append(events, event)
				case "tool_use":
					if item.Name == "Bash" {
						var input claudeToolUseInput
						if err := json.Unmarshal(item.Input, &input); err == nil {
							events = append(events, TranscriptEvent{
								Index:    len(events),
								Kind:     "assistant_turn",
								Role:     "assistant",
								TurnType: "act",
								Command:  input.Command,
							})
						}
					}
				}
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan transcript: %w", err)
	}

	return events, nil
}

func resolveClaudeAgentVersion(ctx context.Context, workspaceDir string, env []string, claudePath string, transcriptBytes []byte) string {
	if version, ok := probeClaudeCLIVersion(ctx, workspaceDir, env, claudePath); ok {
		return version
	}
	if version := extractClaudeTranscriptVersion(transcriptBytes); version != "" {
		return version
	}
	return "claude-unknown"
}

func probeClaudeCLIVersion(ctx context.Context, workspaceDir string, env []string, claudePath string) (string, bool) {
	stdout, _, exitCode, err := runCommand(ctx, workspaceDir, env, claudePath, "-v")
	if err != nil || exitCode != 0 {
		return "", false
	}
	version := strings.TrimSpace(stdout)
	if version == "" {
		return "", false
	}
	return version, true
}

func extractClaudeTranscriptVersion(transcriptBytes []byte) string {
	scanner := bufio.NewScanner(strings.NewReader(string(transcriptBytes)))
	scanner.Buffer(make([]byte, 0, 256*1024), 2*1024*1024)
	for scanner.Scan() {
		var entry claudeTranscriptEntry
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			continue
		}
		if entry.IsSidechain {
			continue
		}
		if entry.Version != "" {
			return entry.Version
		}
	}
	return ""
}

// generateUUID creates a random UUID v4 string.
func generateUUID() string {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		panic(fmt.Sprintf("generate UUID: %v", err))
	}
	buf[6] = (buf[6] & 0x0f) | 0x40 // version 4
	buf[8] = (buf[8] & 0x3f) | 0x80 // variant 1
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		buf[0:4], buf[4:6], buf[6:8], buf[8:10], buf[10:16])
}

func truncateStr(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
