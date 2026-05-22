package evaluations

import (
	"strings"
	"testing"
)

func TestExtractClnkuCommandsHandlesLargeEventLogLines(t *testing.T) {
	largeStdout := strings.Repeat("x", 70*1024)
	eventLog := `{"type":"command_start","payload":{"command":"printf lots","dir":"/tmp/workspace"}}` + "\n" +
		`{"type":"command_done","payload":{"command":"printf lots","stdout":"` + largeStdout + `","stderr":"","exit_code":0}}` + "\n"

	commands, err := extractClnkuCommands(eventLog)
	if err != nil {
		t.Fatalf("extractClnkuCommands(): %v", err)
	}
	if len(commands) != 1 {
		t.Fatalf("command count = %d, want 1", len(commands))
	}
	if commands[0].Stdout != largeStdout {
		t.Fatalf("stdout length = %d, want %d", len(commands[0].Stdout), len(largeStdout))
	}
}
