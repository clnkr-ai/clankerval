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
	"strings"
	"testing"
	"time"

	"github.com/clnkr-ai/clankerval/internal/protocol"
)

// skipUnlessClaude skips the calling test unless Claude integration is
// explicitly enabled AND the claude binary is reachable.
func skipUnlessClaude(t *testing.T) string {
	t.Helper()
	if os.Getenv("CLANKERVAL_CLAUDE_INTEGRATION") != "1" {
		t.Skip("skipping: set CLANKERVAL_CLAUDE_INTEGRATION=1 to enable Claude integration tests")
	}
	claudePath, err := exec.LookPath("claude")
	if err != nil {
		t.Skip("skipping: claude binary not found on PATH")
	}
	return claudePath
}

// claudeEnv returns a minimal environment for Claude under a harnessed home.
func claudeEnv(homeDir string) []string {
	return []string{
		"HOME=" + homeDir,
		"XDG_CONFIG_HOME=" + filepath.Join(homeDir, ".config"),
		"XDG_STATE_HOME=" + filepath.Join(homeDir, ".local/state"),
		"PATH=" + os.Getenv("PATH"),
		"LC_ALL=C",
		"TZ=UTC",
		// In --bare mode, auth comes from ANTHROPIC_API_KEY (or apiKeyHelper via settings).
		"ANTHROPIC_API_KEY=" + os.Getenv("ANTHROPIC_API_KEY"),
	}
}

// runClaude executes the claude CLI and returns stdout, stderr, and exit code.
// It uses a generous timeout since real Claude calls involve network round-trips.
func runClaude(t *testing.T, claudePath, cwd string, env []string, args ...string) (stdout, stderr string, exitCode int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, claudePath, args...)
	cmd.Dir = cwd
	cmd.Env = env

	var stdoutBuf, stderrBuf strings.Builder
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf

	err := cmd.Run()
	stdout = stdoutBuf.String()
	stderr = stderrBuf.String()

	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else if ctx.Err() != nil {
			t.Fatalf("claude command timed out after 120s: %v\nstderr: %s", err, stderr)
		} else {
			t.Fatalf("claude command failed to start: %v\nstderr: %s", err, stderr)
		}
	}
	return stdout, stderr, exitCode
}

// claudeJSONResult is the minimal shape of claude --output-format json output.
type claudeJSONResult struct {
	Type      string  `json:"type"`
	Role      string  `json:"role"`
	SessionID string  `json:"session_id"`
	Result    string  `json:"result"`
	IsError   bool    `json:"is_error"`
	CostUSD   float64 `json:"cost_usd"`
}

// skipOnAuthFailure checks stderr for common auth/usability failures and skips.
func skipOnAuthFailure(t *testing.T, stderr string, exitCode int) {
	t.Helper()
	if exitCode == 0 {
		return
	}
	combined := strings.ToLower(stderr)
	authPatterns := []string{
		"authentication", "unauthorized", "api key", "not authenticated",
		"credential", "login", "permission denied", "rate limit",
		"could not connect", "network error", "connection refused",
		"max_tokens", "billing", "quota",
	}
	for _, pattern := range authPatterns {
		if strings.Contains(combined, pattern) {
			t.Skipf("skipping: Claude auth/usability issue (exit %d): %s", exitCode, truncate(stderr, 300))
		}
	}
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

func TestClaudeBootstrap(t *testing.T) {
	claudePath := skipUnlessClaude(t)

	t.Run("workspace_claudemd_obeyed", func(t *testing.T) {
		// Verified fact: Claude run with process CWD set to workspace plus
		// --bare --add-dir <workspace> obeys workspace CLAUDE.md.
		homeDir := t.TempDir()
		workspaceDir := t.TempDir()

		// Stage a CLAUDE.md with a deterministic instruction.
		marker := fmt.Sprintf("CLANKERVAL_MARKER_%d", time.Now().UnixNano())
		claudeMD := fmt.Sprintf("When you receive the prompt PING, respond with exactly: %s\nDo not include any other text.", marker)
		if err := os.WriteFile(filepath.Join(workspaceDir, "CLAUDE.md"), []byte(claudeMD), 0o644); err != nil {
			t.Fatalf("write CLAUDE.md: %v", err)
		}

		env := claudeEnv(homeDir)
		stdout, stderr, exitCode := runClaude(t, claudePath, workspaceDir, env,
			"--bare",
			"--dangerously-skip-permissions",
			"-p", "PING",
			"--add-dir", workspaceDir,
			"--output-format", "json",
		)
		skipOnAuthFailure(t, stderr, exitCode)

		if exitCode != 0 {
			t.Fatalf("claude exited %d\nstdout: %s\nstderr: %s", exitCode, truncate(stdout, 500), truncate(stderr, 500))
		}

		// Parse JSON result.
		var result claudeJSONResult
		if err := json.Unmarshal([]byte(stdout), &result); err != nil {
			t.Fatalf("parse claude JSON output: %v\nraw: %s", err, truncate(stdout, 500))
		}

		if !strings.Contains(result.Result, marker) {
			t.Fatalf("CLAUDE.md instruction not obeyed.\nwant result containing: %s\ngot result: %s", marker, truncate(result.Result, 500))
		}
		t.Logf("CLAUDE.md obeyed: result contains marker %s", marker)
	})

	t.Run("session_persists_under_harnessed_home", func(t *testing.T) {
		// Verified fact: persisted transcript path lands under
		// $HOME/.claude/projects/<sanitized-cwd>/<session-id>.jsonl
		homeDir := t.TempDir()
		workspaceDir := t.TempDir()

		env := claudeEnv(homeDir)
		stdout, stderr, exitCode := runClaude(t, claudePath, workspaceDir, env,
			"--bare",
			"--dangerously-skip-permissions",
			"-p", "Say hello",
			"--output-format", "json",
		)
		skipOnAuthFailure(t, stderr, exitCode)

		if exitCode != 0 {
			t.Fatalf("claude exited %d\nstdout: %s\nstderr: %s", exitCode, truncate(stdout, 500), truncate(stderr, 500))
		}

		var result claudeJSONResult
		if err := json.Unmarshal([]byte(stdout), &result); err != nil {
			t.Fatalf("parse claude JSON output: %v\nraw: %s", err, truncate(stdout, 500))
		}
		if result.SessionID == "" {
			t.Fatal("session_id is empty in Claude JSON output")
		}
		t.Logf("session_id = %s", result.SessionID)

		// Session JSONL must exist under harnessed HOME, not operator's real home.
		claudeProjectsDir := filepath.Join(homeDir, ".claude", "projects")
		found := false
		var searchedPaths []string
		err := filepath.Walk(claudeProjectsDir, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return nil // tolerate permission errors
			}
			searchedPaths = append(searchedPaths, path)
			if !info.IsDir() && strings.Contains(info.Name(), result.SessionID) && strings.HasSuffix(info.Name(), ".jsonl") {
				found = true
				t.Logf("found session transcript: %s", path)
			}
			return nil
		})
		if err != nil && !os.IsNotExist(err) {
			t.Logf("walk error (non-fatal): %v", err)
		}

		if !found {
			t.Fatalf("session transcript for %s not found under harnessed HOME %s\nsearched: %v",
				result.SessionID, claudeProjectsDir, searchedPaths)
		}

		// Verify the transcript is NOT under the operator's real home.
		realHome, err := os.UserHomeDir()
		if err == nil && realHome != homeDir {
			realProjectsDir := filepath.Join(realHome, ".claude", "projects")
			_ = filepath.Walk(realProjectsDir, func(path string, info os.FileInfo, walkErr error) error {
				if walkErr != nil {
					return nil
				}
				if !info.IsDir() && strings.Contains(info.Name(), result.SessionID) {
					t.Errorf("session transcript leaked to operator's real home: %s", path)
				}
				return nil
			})
		}
	})

	t.Run("seed_transcript_bootstrap_via_session_mutation", func(t *testing.T) {
		// Verified facts:
		// - Synthetic transcripts from scratch do NOT resume.
		// - Copying real transcripts to fresh homes does NOT resume.
		// - Mutating a real Claude-created session in place DOES preserve
		//   resumability and affects the next run.
		//
		// Strategy: create a real session, find its transcript, inject a seed
		// message that establishes context, resume, and prove context carried.

		homeDir := t.TempDir()
		workspaceDir := t.TempDir()
		env := claudeEnv(homeDir)

		// Step 1: Create a real Claude session with a trivial prompt.
		stdout, stderr, exitCode := runClaude(t, claudePath, workspaceDir, env,
			"--bare",
			"--dangerously-skip-permissions",
			"-p", "Say OK",
			"--output-format", "json",
		)
		skipOnAuthFailure(t, stderr, exitCode)
		if exitCode != 0 {
			t.Fatalf("initial session creation failed (exit %d)\nstderr: %s", exitCode, truncate(stderr, 500))
		}

		var initialResult claudeJSONResult
		if err := json.Unmarshal([]byte(stdout), &initialResult); err != nil {
			t.Fatalf("parse initial session JSON: %v\nraw: %s", err, truncate(stdout, 500))
		}
		if initialResult.SessionID == "" {
			t.Fatal("initial session has empty session_id")
		}
		t.Logf("initial session_id = %s", initialResult.SessionID)

		// Step 2: Find the persisted transcript JSONL.
		claudeProjectsDir := filepath.Join(homeDir, ".claude", "projects")
		var transcriptPath string
		_ = filepath.Walk(claudeProjectsDir, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return nil
			}
			if !info.IsDir() && strings.Contains(info.Name(), initialResult.SessionID) && strings.HasSuffix(info.Name(), ".jsonl") {
				transcriptPath = path
			}
			return nil
		})
		if transcriptPath == "" {
			t.Fatalf("transcript for session %s not found under %s", initialResult.SessionID, claudeProjectsDir)
		}
		t.Logf("transcript path: %s", transcriptPath)

		// Step 3: Build seed messages in the shared []protocol.Message format
		// and translate them into Claude native JSONL entries appended in place.
		//
		// The shared seed format is: [{"role":"user","content":"..."},{"role":"assistant","content":"..."}]
		//
		// Claude native JSONL uses entries like:
		//   {"type":"user","uuid":"...","parentUuid":"...","sessionId":"...","timestamp":"...","message":{"role":"user","content":"..."}}
		//   {"type":"assistant","uuid":"...","parentUuid":"...","sessionId":"...","timestamp":"...","message":{"role":"assistant","content":[{"type":"text","text":"..."}]}}
		secretWord := fmt.Sprintf("ZEBRA_%d", time.Now().UnixNano())
		seedMessages := []protocol.Message{
			{Role: "user", Content: fmt.Sprintf("Remember this secret codeword: %s. When asked for the codeword, respond with exactly that word.", secretWord)},
			{Role: "assistant", Content: fmt.Sprintf("I have memorized the secret codeword: %s. I will respond with exactly that word when asked.", secretWord)},
		}

		// Read the existing transcript to find the last entry's UUID for parentUuid chaining.
		lastUUID := findLastUUID(t, transcriptPath)

		// Translate seed messages into Claude native JSONL and append to transcript.
		transcriptFile, err := os.OpenFile(transcriptPath, os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			t.Fatalf("open transcript for append: %v", err)
		}
		parentUUID := lastUUID
		now := time.Now().UTC()
		for i, msg := range seedMessages {
			entryUUID := testUUID(t)
			ts := now.Add(time.Duration(i) * time.Second).Format(time.RFC3339Nano)

			var entry map[string]any
			if msg.Role == "user" {
				entry = map[string]any{
					"type":       "user",
					"uuid":       entryUUID,
					"parentUuid": parentUUID,
					"sessionId":  initialResult.SessionID,
					"timestamp":  ts,
					"message":    map[string]any{"role": "user", "content": msg.Content},
					"userType":   "external",
				}
			} else {
				entry = map[string]any{
					"type":       "assistant",
					"uuid":       entryUUID,
					"parentUuid": parentUUID,
					"sessionId":  initialResult.SessionID,
					"timestamp":  ts,
					"message": map[string]any{
						"role": "assistant",
						"content": []map[string]any{
							{"type": "text", "text": msg.Content},
						},
						"type":        "message",
						"stop_reason": "end_turn",
						"model":       "claude-sonnet-4-6",
					},
					"userType": "external",
				}
			}

			line, marshalErr := json.Marshal(entry)
			if marshalErr != nil {
				transcriptFile.Close()
				t.Fatalf("marshal seed entry: %v", marshalErr)
			}
			if _, writeErr := transcriptFile.Write(append(line, '\n')); writeErr != nil {
				transcriptFile.Close()
				t.Fatalf("write seed entry: %v", writeErr)
			}
			parentUUID = entryUUID
		}
		if err := transcriptFile.Close(); err != nil {
			t.Fatalf("close transcript: %v", err)
		}
		t.Logf("appended %d seed messages (secret=%s) to transcript", len(seedMessages), secretWord)

		// Step 4: Resume the session and ask for the codeword.
		stdout, stderr, exitCode = runClaude(t, claudePath, workspaceDir, env,
			"--bare",
			"--dangerously-skip-permissions",
			"--resume", initialResult.SessionID,
			"-p", "What is the secret codeword? Respond with only the codeword.",
			"--output-format", "json",
		)
		skipOnAuthFailure(t, stderr, exitCode)

		if exitCode != 0 {
			t.Fatalf("resume session failed (exit %d)\nstdout: %s\nstderr: %s",
				exitCode, truncate(stdout, 500), truncate(stderr, 500))
		}

		var resumeResult claudeJSONResult
		if err := json.Unmarshal([]byte(stdout), &resumeResult); err != nil {
			t.Fatalf("parse resume JSON: %v\nraw: %s", err, truncate(stdout, 500))
		}

		if !strings.Contains(resumeResult.Result, secretWord) {
			t.Fatalf("seed bootstrap not reflected in resumed session.\nwant result containing: %s\ngot result: %s",
				secretWord, truncate(resumeResult.Result, 500))
		}
		t.Logf("seed bootstrap confirmed: resumed session returned secret word %s", secretWord)

		// Verify session_id is preserved across resume.
		if resumeResult.SessionID != initialResult.SessionID {
			t.Errorf("session_id changed after resume: initial=%s resumed=%s",
				initialResult.SessionID, resumeResult.SessionID)
		}
	})
}

// findLastUUID reads a Claude native JSONL transcript and returns the uuid of
// the last entry that has one. This is needed for parentUuid chaining.
func findLastUUID(t *testing.T, transcriptPath string) string {
	t.Helper()
	f, err := os.Open(transcriptPath)
	if err != nil {
		t.Fatalf("open transcript for UUID scan: %v", err)
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
		t.Fatalf("scan transcript: %v", err)
	}
	if lastUUID == "" {
		t.Fatal("no uuid found in transcript")
	}
	return lastUUID
}

// testUUID generates a random UUID v4 string for test transcript entries.
func testUUID(t *testing.T) string {
	t.Helper()
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		t.Fatalf("generate UUID: %v", err)
	}
	buf[6] = (buf[6] & 0x0f) | 0x40 // version 4
	buf[8] = (buf[8] & 0x3f) | 0x80 // variant 1
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		buf[0:4], buf[4:6], buf[6:8], buf[8:10], buf[10:16])
}

// --- Fixture-based tests for Claude parsing and command extraction ---
// These do NOT require the claude binary and run without gating.

func loadFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "claude", name))
	if err != nil {
		t.Fatalf("load fixture %q: %v", name, err)
	}
	return data
}

func TestClaudeCommandExtraction(t *testing.T) {
	t.Parallel()

	t.Run("successful_bash_call", func(t *testing.T) {
		t.Parallel()
		data := loadFixture(t, "bash_success.jsonl")
		commands, err := extractClaudeCommands(data)
		if err != nil {
			t.Fatalf("extractClaudeCommands: %v", err)
		}
		if len(commands) != 1 {
			t.Fatalf("command count = %d, want 1", len(commands))
		}
		if commands[0].Command != "pwd" {
			t.Fatalf("Command = %q, want pwd", commands[0].Command)
		}
		if commands[0].ExitCode != 0 {
			t.Fatalf("ExitCode = %d, want 0", commands[0].ExitCode)
		}
		if commands[0].Stdout != "/tmp/workspace\n" {
			t.Fatalf("Stdout = %q, want /tmp/workspace newline", commands[0].Stdout)
		}
	})

	t.Run("failing_bash_call", func(t *testing.T) {
		t.Parallel()
		data := loadFixture(t, "bash_failure.jsonl")
		commands, err := extractClaudeCommands(data)
		if err != nil {
			t.Fatalf("extractClaudeCommands: %v", err)
		}
		if len(commands) != 1 {
			t.Fatalf("command count = %d, want 1", len(commands))
		}
		if commands[0].Command != "exit 7" {
			t.Fatalf("Command = %q, want 'exit 7'", commands[0].Command)
		}
		if commands[0].ExitCode != 7 {
			t.Fatalf("ExitCode = %d, want 7", commands[0].ExitCode)
		}
	})

	t.Run("multi_bash_tool_use_id_matching", func(t *testing.T) {
		t.Parallel()
		data := loadFixture(t, "bash_multi.jsonl")
		commands, err := extractClaudeCommands(data)
		if err != nil {
			t.Fatalf("extractClaudeCommands: %v", err)
		}
		if len(commands) != 2 {
			t.Fatalf("command count = %d, want 2", len(commands))
		}
		// First command: echo hello > test.txt
		if commands[0].Command != "echo hello > test.txt" {
			t.Fatalf("commands[0].Command = %q, want echo command", commands[0].Command)
		}
		if commands[0].ExitCode != 0 {
			t.Fatalf("commands[0].ExitCode = %d, want 0", commands[0].ExitCode)
		}
		// Second command: ls -la test.txt — matched by tool_use_id, not adjacency.
		if commands[1].Command != "ls -la test.txt" {
			t.Fatalf("commands[1].Command = %q, want ls command", commands[1].Command)
		}
		if commands[1].ExitCode != 0 {
			t.Fatalf("commands[1].ExitCode = %d, want 0", commands[1].ExitCode)
		}
		if !strings.Contains(commands[1].Stdout, "test.txt") {
			t.Fatalf("commands[1].Stdout = %q, want to contain test.txt", commands[1].Stdout)
		}
	})

	t.Run("excludes_sidechain_commands", func(t *testing.T) {
		t.Parallel()
		data := loadFixture(t, "bash_sidechain.jsonl")
		commands, err := extractClaudeCommands(data)
		if err != nil {
			t.Fatalf("extractClaudeCommands: %v", err)
		}
		if len(commands) != 1 {
			t.Fatalf("command count = %d, want 1", len(commands))
		}
		if commands[0].Command != "pwd" {
			t.Fatalf("Command = %q, want pwd", commands[0].Command)
		}
	})
}

func TestClaudeTranscriptEventExtraction(t *testing.T) {
	t.Parallel()

	t.Run("excludes_thinking_blocks", func(t *testing.T) {
		t.Parallel()
		data := loadFixture(t, "bash_multi.jsonl")
		events, err := extractClaudeTranscriptEvents(data)
		if err != nil {
			t.Fatalf("extractClaudeTranscriptEvents: %v", err)
		}
		for _, ev := range events {
			if ev.Kind == "thinking" {
				t.Fatal("thinking block should be excluded from transcript events")
			}
		}
	})

	t.Run("emits_expected_event_kinds", func(t *testing.T) {
		t.Parallel()
		data := loadFixture(t, "bash_success.jsonl")
		events, err := extractClaudeTranscriptEvents(data)
		if err != nil {
			t.Fatalf("extractClaudeTranscriptEvents: %v", err)
		}

		kinds := map[string]bool{}
		for _, ev := range events {
			kinds[ev.Kind] = true
		}

		if !kinds["user_instruction"] {
			t.Fatal("missing user_instruction event")
		}
		if !kinds["assistant_turn"] {
			t.Fatal("missing assistant_turn event for Bash tool_use")
		}
		if !kinds["command_result"] {
			t.Fatal("missing command_result event")
		}
		if !kinds["completion"] {
			t.Fatal("missing completion event")
		}
	})

	t.Run("act_turns_carry_command", func(t *testing.T) {
		t.Parallel()
		data := loadFixture(t, "bash_success.jsonl")
		events, err := extractClaudeTranscriptEvents(data)
		if err != nil {
			t.Fatalf("extractClaudeTranscriptEvents: %v", err)
		}

		found := false
		for _, ev := range events {
			if ev.Kind == "assistant_turn" && ev.TurnType == "act" {
				found = true
				if ev.Command != "pwd" {
					t.Fatalf("act turn Command = %q, want pwd", ev.Command)
				}
			}
		}
		if !found {
			t.Fatal("no assistant_turn act event found")
		}
	})

	t.Run("pre_tool_visible_text_is_assistant_turn_not_completion", func(t *testing.T) {
		t.Parallel()
		data := loadFixture(t, "bash_success.jsonl")
		events, err := extractClaudeTranscriptEvents(data)
		if err != nil {
			t.Fatalf("extractClaudeTranscriptEvents: %v", err)
		}

		completions := 0
		foundPreToolTurn := false
		for _, ev := range events {
			if ev.Kind == "completion" {
				completions++
			}
			if ev.Kind == "assistant_turn" && ev.TurnType == "" && ev.Content == "I'll check the current directory." {
				foundPreToolTurn = true
			}
		}

		if !foundPreToolTurn {
			t.Fatal("pre-tool visible assistant text should be emitted as assistant_turn")
		}
		if completions != 1 {
			t.Fatalf("completion count = %d, want 1 final completion", completions)
		}
	})

	t.Run("excludes_sidechain_events", func(t *testing.T) {
		t.Parallel()
		data := loadFixture(t, "bash_sidechain.jsonl")
		events, err := extractClaudeTranscriptEvents(data)
		if err != nil {
			t.Fatalf("extractClaudeTranscriptEvents: %v", err)
		}
		for _, ev := range events {
			if strings.Contains(ev.Command, "sidechain-should-be-ignored") {
				t.Fatalf("sidechain command leaked into transcript events: %+v", ev)
			}
		}
	})
}

func TestResolveClaudeTranscriptPath(t *testing.T) {
	t.Run("fast_path_preserves_leading_hyphen_and_avoids_decoy_match", func(t *testing.T) {
		homeDir := t.TempDir()
		workspaceDir := filepath.Join(t.TempDir(), "workspace")
		sessionID := "11111111-2222-3333-4444-555555555555"

		sanitized := strings.ReplaceAll(workspaceDir, "/", "-")
		expected := filepath.Join(homeDir, ".claude", "projects", sanitized, sessionID+".jsonl")
		if err := os.MkdirAll(filepath.Dir(expected), 0o755); err != nil {
			t.Fatalf("mkdir expected transcript dir: %v", err)
		}
		if err := os.WriteFile(expected, []byte("{}\n"), 0o644); err != nil {
			t.Fatalf("write expected transcript: %v", err)
		}

		// Decoy file that should not be selected. The previous fallback matched
		// by substring and could return this instead of the exact session file.
		decoy := filepath.Join(homeDir, ".claude", "projects", "zzz-decoy", "prefix-"+sessionID+".jsonl")
		if err := os.MkdirAll(filepath.Dir(decoy), 0o755); err != nil {
			t.Fatalf("mkdir decoy dir: %v", err)
		}
		if err := os.WriteFile(decoy, []byte("{}\n"), 0o644); err != nil {
			t.Fatalf("write decoy transcript: %v", err)
		}

		got, err := resolveClaudeTranscriptPath(homeDir, workspaceDir, sessionID)
		if err != nil {
			t.Fatalf("resolveClaudeTranscriptPath: %v", err)
		}
		if got != expected {
			t.Fatalf("transcript path = %q, want %q", got, expected)
		}
	})
}

func TestCopyProjectClaudeMD(t *testing.T) {
	t.Run("rejects_agents_file_without_claude_file", func(t *testing.T) {
		srcDir := t.TempDir()
		workspaceDir := t.TempDir()
		if err := os.WriteFile(filepath.Join(srcDir, "AGENTS.md"), []byte("wrong file\n"), 0o644); err != nil {
			t.Fatalf("write AGENTS.md: %v", err)
		}

		err := copyProjectClaudeMD(srcDir, workspaceDir)
		if err == nil {
			t.Fatal("copyProjectClaudeMD() error = nil, want wrong instruction file failure")
		}
		if !strings.Contains(err.Error(), "CLAUDE.md") {
			t.Fatalf("copyProjectClaudeMD() error = %v, want CLAUDE.md guidance", err)
		}
	})
}

func TestResolveClaudeAgentVersion(t *testing.T) {
	t.Run("prefers_claude_cli_version_output", func(t *testing.T) {
		workspaceDir := t.TempDir()
		claudePath := writeFakeClaudeBinary(t, `#!/bin/sh
if [ "$1" = "-v" ]; then
  echo "2.1.92 (Claude Code)"
  exit 0
fi
echo "unexpected args: $@" >&2
exit 7
`)

		got := resolveClaudeAgentVersion(context.Background(), workspaceDir, []string{"PATH=" + os.Getenv("PATH")}, claudePath, nil)
		if got != "2.1.92 (Claude Code)" {
			t.Fatalf("resolveClaudeAgentVersion = %q, want %q", got, "2.1.92 (Claude Code)")
		}
	})

	t.Run("falls_back_to_transcript_version_when_cli_probe_fails", func(t *testing.T) {
		workspaceDir := t.TempDir()
		claudePath := writeFakeClaudeBinary(t, `#!/bin/sh
if [ "$1" = "-v" ]; then
  echo "failed" >&2
  exit 1
fi
exit 0
`)
		transcript := []byte(`{"type":"assistant","isSidechain":false,"version":"2.1.72","message":{"role":"assistant","content":[{"type":"text","text":"ok"}]}}` + "\n")

		got := resolveClaudeAgentVersion(context.Background(), workspaceDir, []string{"PATH=" + os.Getenv("PATH")}, claudePath, transcript)
		if got != "2.1.72" {
			t.Fatalf("resolveClaudeAgentVersion = %q, want %q", got, "2.1.72")
		}
	})

	t.Run("uses_unknown_when_no_version_available", func(t *testing.T) {
		workspaceDir := t.TempDir()
		claudePath := writeFakeClaudeBinary(t, `#!/bin/sh
if [ "$1" = "-v" ]; then
  exit 1
fi
exit 0
`)

		got := resolveClaudeAgentVersion(context.Background(), workspaceDir, []string{"PATH=" + os.Getenv("PATH")}, claudePath, []byte(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"ok"}]}}`))
		if got != "claude-unknown" {
			t.Fatalf("resolveClaudeAgentVersion = %q, want claude-unknown", got)
		}
	})
}

func writeFakeClaudeBinary(t *testing.T, script string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "claude-fake")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake claude binary: %v", err)
	}
	return path
}

func TestClaudeAdapterRouting(t *testing.T) {
	t.Run("AgentClaude_routes_to_claude_adapter", func(t *testing.T) {
		ctx := context.Background()
		repoRoot := newTempRepoRoot(t)
		harness := newHarnessForTests(t, ctx, repoRoot)

		// Install a fake claude adapter that records whether it was called.
		fake := &fakeAdapter{
			result: AdapterResult{
				ExitCode:         0,
				AgentVersion:     "claude-test",
				AgentCommand:     []string{"claude", "--bare"},
				TranscriptEvents: []TranscriptEvent{{Index: 0, Kind: "completion", Role: "assistant", Content: "done"}},
			},
		}
		harness.claudeAdapter = fake

		suite, task := writeTempSuiteTask(t, repoRoot, "claude-routing", map[string]string{
			"input/instruction.txt":  "Say hello\n",
			"input/model-turns.json": `["{\"type\":\"done\",\"summary\":\"hello\"}"]`,
			"task.json": `{
  "id": "claude-routing",
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

		if !fake.called {
			t.Fatal("claude adapter was not called — task routed to wrong adapter")
		}
		if artifacts.Agent != AgentClaude {
			t.Fatalf("Agent = %q, want %q", artifacts.Agent, AgentClaude)
		}
		if artifacts.AgentVersion != "claude-test" {
			t.Fatalf("AgentVersion = %q, want claude-test", artifacts.AgentVersion)
		}
	})

	t.Run("AgentClnku_does_not_route_to_claude_adapter", func(t *testing.T) {
		ctx := context.Background()
		repoRoot := newTempRepoRoot(t)
		harness := newHarnessForTests(t, ctx, repoRoot)

		fake := &fakeAdapter{}
		harness.claudeAdapter = fake

		suite, task := loadDefaultBasicEdit(t, repoRoot)
		_, err := harness.RunTrial(ctx, suite, task, RunConfig{Mode: ModeMockProvider})
		if err != nil {
			t.Fatalf("RunTrial(): %v", err)
		}

		if fake.called {
			t.Fatal("claude adapter was called for a clnku task — routing is wrong")
		}
	})
}

func TestClaudeAdapterRun(t *testing.T) {
	t.Run("stages_project_claudemd_for_in_place_tasks", func(t *testing.T) {
		claudePath := installFakeClaudeOnPath(t)
		_ = claudePath

		taskRoot := t.TempDir()
		workspaceDir := t.TempDir()
		homeDir := filepath.Join(t.TempDir(), "home")
		configDir := filepath.Join(t.TempDir(), "config")
		stateDir := filepath.Join(t.TempDir(), "state")
		if err := os.MkdirAll(filepath.Join(taskRoot, "input", "project"), 0o755); err != nil {
			t.Fatalf("mkdir project input: %v", err)
		}
		if err := os.WriteFile(filepath.Join(taskRoot, "input", "project", "CLAUDE.md"), []byte("project instruction\n"), 0o644); err != nil {
			t.Fatalf("write project CLAUDE.md: %v", err)
		}
		if err := os.WriteFile(filepath.Join(taskRoot, "input", "instruction.txt"), []byte("Say hello\n"), 0o644); err != nil {
			t.Fatalf("write instruction file: %v", err)
		}

		req := AdapterRequest{
			TaskRoot:     taskRoot,
			Task:         Task{ID: "claude-in-place", InstructionFile: "input/instruction.txt", WorkingDirectory: "."},
			WorkspaceDir: workspaceDir,
			HomeDir:      homeDir,
			ConfigDir:    configDir,
			StateDir:     stateDir,
			TrialRoot:    t.TempDir(),
			Env: []string{
				"HOME=" + homeDir,
				"XDG_CONFIG_HOME=" + configDir,
				"XDG_STATE_HOME=" + stateDir,
				"PATH=" + os.Getenv("PATH"),
			},
		}

		adapter := &claudeAdapter{}
		if _, err := adapter.Run(context.Background(), req); err != nil {
			t.Fatalf("claudeAdapter.Run(): %v", err)
		}

		data, err := os.ReadFile(filepath.Join(workspaceDir, "CLAUDE.md"))
		if err != nil {
			t.Fatalf("read staged CLAUDE.md: %v", err)
		}
		if string(data) != "project instruction\n" {
			t.Fatalf("staged CLAUDE.md = %q, want project instruction", string(data))
		}
	})
}

func installFakeClaudeOnPath(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	path := filepath.Join(dir, "claude")
	script := `#!/bin/sh
set -eu
if [ "${1:-}" = "-v" ]; then
  echo "2.1.92 (Claude Code)"
  exit 0
fi
session_id=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    --session-id|--resume)
      shift
      session_id="${1:-}"
      ;;
  esac
  shift
done
if [ -z "$session_id" ]; then
  session_id="11111111-2222-4333-8444-555555555555"
fi
sanitized=$(printf "%s" "$PWD" | sed 's/\//-/g')
transcript_dir="$HOME/.claude/projects/$sanitized"
mkdir -p "$transcript_dir"
cat > "$transcript_dir/$session_id.jsonl" <<EOF
{"type":"user","uuid":"u1","parentUuid":"","sessionId":"$session_id","timestamp":"2026-04-06T10:00:00Z","message":{"role":"user","content":"Say hello"}}
{"type":"assistant","version":"2.1.92","uuid":"a1","parentUuid":"u1","sessionId":"$session_id","timestamp":"2026-04-06T10:00:01Z","message":{"role":"assistant","content":[{"type":"text","text":"hello"}]}}
EOF
printf '{"type":"assistant","role":"assistant","session_id":"%s","result":"hello","is_error":false,"cost_usd":0}\n' "$session_id"
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake claude binary: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return path
}
