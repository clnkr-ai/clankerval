package evaluations

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

type normalizationRoots struct {
	Workdir string
	Home    string
	Config  string
	State   string
	Temp    string
}

type pathReplacement struct {
	base        string
	placeholder string
	priority    int
}

// NormalizedTranscriptRecord is one stable transcript record derived from raw trial data.
type NormalizedTranscriptRecord struct {
	Index    int    `json:"index"`
	Kind     string `json:"kind"`
	Role     string `json:"role,omitempty"`
	TurnType string `json:"turn_type,omitempty"`
	Content  string `json:"content,omitempty"`
	Command  string `json:"command,omitempty"`
	Cwd      string `json:"cwd,omitempty"`
	Stdout   string `json:"stdout,omitempty"`
	Stderr   string `json:"stderr,omitempty"`
	ExitCode int    `json:"exit_code"`
}

// NormalizedOutcome summarizes the final trial state for grading and export.
type NormalizedOutcome struct {
	FinalExitCode    int      `json:"final_exit_code"`
	FinalCwd         string   `json:"final_cwd"`
	DiffPresent      bool     `json:"diff_present"`
	ChangedPaths     []string `json:"changed_paths"`
	ChangedFileCount int      `json:"changed_file_count"`
}

// NormalizeTranscript projects a stable transcript record sequence from
// adapter-supplied TranscriptEvents and Commands. Path and text normalization
// is applied here; source-format parsing lives in the adapter.
func NormalizeTranscript(artifacts RunArtifacts) ([]NormalizedTranscriptRecord, error) {
	roots := artifacts.normalizationRoots()
	events := artifacts.TranscriptEvents
	commands := artifacts.Commands
	records := make([]NormalizedTranscriptRecord, 0, len(events)+len(commands))
	cmdIndex := 0

	for _, ev := range events {
		switch ev.Kind {
		case "system_prompt":
			records = append(records, NormalizedTranscriptRecord{
				Index:   len(records),
				Kind:    "system_prompt",
				Role:    "system",
				Content: normalizeText(ev.Content, roots),
			})
		case "user_instruction":
			records = append(records, NormalizedTranscriptRecord{
				Index:   len(records),
				Kind:    "user_instruction",
				Role:    "user",
				Content: normalizeText(ev.Content, roots),
			})
		case "assistant_turn":
			rec := NormalizedTranscriptRecord{
				Index:    len(records),
				Kind:     "assistant_turn",
				Role:     "assistant",
				TurnType: ev.TurnType,
				Content:  normalizeText(ev.Content, roots),
			}
			records = append(records, rec)
			// For act turns, synthesize a command_start from the next Command.
			if ev.TurnType == "act" && cmdIndex < len(commands) {
				records = append(records, NormalizedTranscriptRecord{
					Index:    len(records),
					Kind:     "command_start",
					Role:     "system",
					TurnType: "act",
					Command:  normalizeText(commands[cmdIndex].Command, roots),
					Cwd:      normalizePath(commands[cmdIndex].Dir, roots),
				})
			}
		case "command_result":
			rec := NormalizedTranscriptRecord{
				Index: len(records),
				Kind:  "command_result",
				Role:  "user",
			}
			if cmdIndex < len(commands) {
				rec.Command = normalizeText(commands[cmdIndex].Command, roots)
				rec.Stdout = normalizeText(commands[cmdIndex].Stdout, roots)
				rec.Stderr = normalizeText(commands[cmdIndex].Stderr, roots)
				rec.ExitCode = commands[cmdIndex].ExitCode
				cmdIndex++
			}
			records = append(records, rec)
		case "state_update":
			records = append(records, NormalizedTranscriptRecord{
				Index: len(records),
				Kind:  "state_update",
				Role:  "user",
				Cwd:   normalizePath(ev.Cwd, roots),
			})
		case "clarification":
			records = append(records, NormalizedTranscriptRecord{
				Index:    len(records),
				Kind:     "clarification",
				Role:     "assistant",
				TurnType: ev.TurnType,
				Content:  normalizeText(ev.Content, roots),
			})
		case "completion":
			records = append(records, NormalizedTranscriptRecord{
				Index:    len(records),
				Kind:     "completion",
				Role:     "assistant",
				TurnType: ev.TurnType,
				Content:  normalizeText(ev.Content, roots),
			})
		default:
			records = append(records, NormalizedTranscriptRecord{
				Index:   len(records),
				Kind:    ev.Kind,
				Role:    ev.Role,
				Content: normalizeText(ev.Content, roots),
			})
		}
	}

	return records, nil
}

// NormalizeOutcome derives a stable end-state summary from raw trial artifacts.
// Final cwd is derived from the last state_update TranscriptEvent rather than
// re-parsing agent-specific transcript payloads.
func NormalizeOutcome(artifacts RunArtifacts) (NormalizedOutcome, error) {
	finalCwd := ""
	for i := len(artifacts.TranscriptEvents) - 1; i >= 0; i-- {
		ev := artifacts.TranscriptEvents[i]
		if ev.Kind == "state_update" && ev.Cwd != "" {
			finalCwd = normalizePath(ev.Cwd, artifacts.normalizationRoots())
			break
		}
	}

	return NormalizedOutcome{
		FinalExitCode:    artifacts.ExitCode,
		FinalCwd:         finalCwd,
		DiffPresent:      strings.TrimSpace(artifacts.GitDiff) != "",
		ChangedPaths:     parseGitNameStatusChangedPaths(artifacts.GitNameStatus),
		ChangedFileCount: parseGitNumstatChangedFileCount(artifacts.GitNumstat),
	}, nil
}

func parseGitNameStatusChangedPaths(nameStatus string) []string {
	lines := splitNonEmptyLines(nameStatus)
	paths := make([]string, 0, len(lines))
	for _, line := range lines {
		fields := strings.Split(line, "\t")
		if len(fields) < 2 {
			continue
		}
		for _, field := range fields[1:] {
			path := parseGitPathToken(field)
			if path == "" {
				continue
			}
			paths = append(paths, path)
		}
	}
	return paths
}

func parseGitNumstatChangedFileCount(numstat string) int {
	return len(splitNonEmptyLines(numstat))
}

func splitNonEmptyLines(value string) []string {
	lines := strings.Split(value, "\n")
	trimmed := make([]string, 0, len(lines))
	for _, line := range lines {
		if line = strings.TrimSpace(line); line != "" {
			trimmed = append(trimmed, line)
		}
	}
	return trimmed
}

func parseGitPathToken(token string) string {
	token = strings.TrimSpace(token)
	if token == "" {
		return ""
	}
	if len(token) >= 2 && token[0] == '"' && token[len(token)-1] == '"' {
		if unquoted, err := strconv.Unquote(token); err == nil {
			return filepath.ToSlash(unquoted)
		}
	}
	return filepath.ToSlash(token)
}

func normalizeText(value string, roots normalizationRoots) string {
	if value == "" {
		return ""
	}

	replacements := buildPathReplacements(roots)
	normalized := value
	for _, replacement := range replacements {
		normalized = strings.ReplaceAll(normalized, replacement.base, replacement.placeholder)
	}

	resolved, err := filepath.EvalSymlinks(value)
	if err == nil && resolved != value {
		normalized = resolved
		for _, replacement := range replacements {
			normalized = strings.ReplaceAll(normalized, replacement.base, replacement.placeholder)
		}
	}

	return filepath.ToSlash(normalized)
}

func normalizePath(value string, roots normalizationRoots) string {
	if value == "" {
		return ""
	}

	cleaned := filepath.Clean(value)
	normalized := normalizeText(cleaned, roots)
	if normalized != filepath.ToSlash(cleaned) {
		return normalized
	}

	resolved, err := filepath.EvalSymlinks(cleaned)
	if err == nil {
		normalized = normalizeText(resolved, roots)
		if normalized != filepath.ToSlash(resolved) {
			return normalized
		}
	}

	return filepath.ToSlash(cleaned)
}

func buildPathReplacements(roots normalizationRoots) []pathReplacement {
	type namedRoot struct {
		value       string
		placeholder string
		priority    int
	}

	named := []namedRoot{
		{value: roots.Workdir, placeholder: "<WORKDIR>", priority: 0},
		{value: roots.Home, placeholder: "<HOME>", priority: 1},
		{value: filepath.Join(roots.Config, "clnkr"), placeholder: "<CONFIG>/clnkr", priority: 2},
		{value: roots.Config, placeholder: "<CONFIG>", priority: 3},
		{value: roots.State, placeholder: "<STATE>", priority: 4},
		{value: roots.Temp, placeholder: "<TMP>", priority: 5},
	}

	seen := map[string]pathReplacement{}
	for _, root := range named {
		if strings.TrimSpace(root.value) == "" {
			continue
		}
		candidates := []string{filepath.Clean(root.value)}
		if resolved, err := filepath.EvalSymlinks(root.value); err == nil {
			candidates = append(candidates, filepath.Clean(resolved))
		}
		for _, candidate := range candidates {
			if candidate == "." || candidate == "" {
				continue
			}
			if existing, ok := seen[candidate]; ok && existing.priority <= root.priority {
				continue
			}
			seen[candidate] = pathReplacement{
				base:        filepath.ToSlash(candidate),
				placeholder: root.placeholder,
				priority:    root.priority,
			}
		}
	}

	replacements := make([]pathReplacement, 0, len(seen))
	for _, replacement := range seen {
		replacements = append(replacements, replacement)
	}
	sort.SliceStable(replacements, func(i, j int) bool {
		if len(replacements[i].base) != len(replacements[j].base) {
			return len(replacements[i].base) > len(replacements[j].base)
		}
		return replacements[i].priority < replacements[j].priority
	})
	return replacements
}

func checksumSHA256Bytes(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}

func resolveExistingPath(path string) (string, error) {
	cleaned := filepath.Clean(path)
	current := cleaned
	missingParts := []string{}
	for {
		resolved, err := filepath.EvalSymlinks(current)
		if err == nil {
			for i := len(missingParts) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, missingParts[i])
			}
			return resolved, nil
		}
		if !os.IsNotExist(err) {
			return "", fmt.Errorf("resolve %q: %w", current, err)
		}
		parent := filepath.Dir(current)
		if parent == current {
			for i := len(missingParts) - 1; i >= 0; i-- {
				current = filepath.Join(current, missingParts[i])
			}
			return current, nil
		}
		missingParts = append(missingParts, filepath.Base(current))
		current = parent
	}
}
